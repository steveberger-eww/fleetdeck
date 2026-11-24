package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/ssh"
)

var terminalPath = regexp.MustCompile(`^/api/servers/([\w-]+)/terminal$`)

func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" && strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// terminal serves the browser terminal: a WebSocket bridged to an SSH PTY.
// Every upgrade request lands here; anything but a valid terminal request is refused before upgrading.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	deny := func(code int) {
		w.Header().Set("Connection", "close")
		w.WriteHeader(code)
	}
	m := terminalPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		deny(404)
		return
	}
	if !s.sameOrigin(r, true) { // blocks cross-site WebSocket hijacking
		deny(403)
		return
	}
	c := &ctx{s: s, r: r, ip: s.clientIP(r)}
	var err error
	if c.sess, err = getSession(r); c.sess == nil || err != nil {
		deny(401)
		return
	}
	if err := c.load(); err != nil || c.user == nil || c.tenant == "" {
		deny(401)
		return
	}
	srv, err := c.server(m[1], roleOperator)
	if err != nil {
		deny(httperr.Status(err))
		return
	}
	// The Origin check above is ours (proxy-aware), so the library's own is skipped.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	db.Event("audit", "Opened terminal on "+srv.Name, db.EventOpts{Actor: c.actor(), IP: c.ip, ServerID: srv.ID})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu    sync.Mutex
		shell ssh.Shell
		left  bool // the browser is gone
	)
	q := r.URL.Query()
	go func() {
		sh, err := ssh.OpenShell(srv, clampSize(q.Get("cols"), 80, 500), clampSize(q.Get("rows"), 24, 200))
		mu.Lock()
		gone := left
		if err == nil && !gone {
			shell = sh
		}
		mu.Unlock()
		switch {
		case err != nil:
			if !gone {
				_ = conn.Write(ctx, websocket.MessageText, []byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n"))
				_ = conn.Close(websocket.StatusInternalError, "ssh error")
			}
			return
		case gone: // browser left before the shell opened: don't leak the channel
			_ = sh.Close()
			return
		}
		buf := make([]byte, 32<<10)
		for {
			n, err := sh.Read(buf)
			if n > 0 && conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				break
			}
			if err != nil {
				break
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "session ended")
	}()

	// Protocol: JSON text frames. {t:'i', d:string} = keystrokes, {t:'r', c, r} = resize.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		mu.Lock()
		sh := shell
		mu.Unlock()
		if sh == nil {
			continue
		}
		switch msg["t"] {
		case "i":
			if d, ok := msg["d"].(string); ok {
				_, _ = sh.Write([]byte(d))
			}
		case "r":
			_ = sh.Resize(clampSize(msg["c"], 80, 500), clampSize(msg["r"], 24, 200))
		}
	}
	mu.Lock()
	left = true
	sh := shell
	mu.Unlock()
	if sh != nil {
		_ = sh.Close()
	}
}

// clampSize is Math.min(Math.max(Number(v) || def, 2), max).
func clampSize(v any, def, max float64) int {
	n := jsNumber(v)
	if n == 0 || math.IsNaN(n) {
		n = def
	}
	return int(math.Min(math.Max(n, 2), max))
}
