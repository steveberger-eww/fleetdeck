// Package web is Fleetdeck's HTTP layer: auth and sessions, the JSON API, static files,
// the live job stream and the terminal WebSocket.
package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

// SessionTTL is how long a session lives without being used.
const SessionTTL = 7 * 24 * time.Hour

const sessionMS = int64(SessionTTL / time.Millisecond)

var mimeTypes = map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".png": "image/png", ".woff2": "font/woff2"}

// Server is the HTTP handler.
type Server struct {
	trustProxy bool
	static     fs.FS
	mux        *http.ServeMux

	mu             sync.Mutex // guards the fields below
	setupToken     string
	failures       map[string]*failure    // per-IP login failures
	pendingTOTP    map[string]string      // session hash -> secret awaiting confirmation
	oidcPending    map[string]oidcFlow    // OIDC state -> flow in progress
	passkeyPending map[string]passkeyFlow // WebAuthn ceremonies in progress

	totpMu sync.Mutex // makes checkTOTP's read-compare-write of totp_last atomic
}

// The roles of an organization, weakest first. Admins see every server; the others only those assigned to them.
const (
	roleViewer   = 1 // looks at assigned servers
	roleOperator = 2 // works on them: jobs, services, terminal
	roleOwner    = 3 // adds servers, edits and shares the ones they have
	roleAdmin    = 4 // everything in the organization
)

var roleLevel = map[string]int{"viewer": roleViewer, "operator": roleOperator, "owner": roleOwner, "admin": roleAdmin}

// New returns the handler. static holds the UI (index.html at its root); trustProxy honours X-Forwarded-*.
func New(static fs.FS, trustProxy bool) *Server {
	s := &Server{trustProxy: trustProxy, static: static, mux: http.NewServeMux(), failures: map[string]*failure{}, pendingTOTP: map[string]string{}, oidcPending: map[string]oidcFlow{},
		passkeyPending: map[string]passkeyFlow{}}
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 404, errBody("Not found")) })
	s.routes()
	return s
}

// SetupToken creates the one-time setup token when no password is set yet; "" otherwise.
func (s *Server) SetupToken() string {
	if !needsSetup() {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupToken = sec.Token()[:24]
	return s.setupToken
}

func needsSetup() bool {
	n, err := db.Get[bool](`SELECT EXISTS (SELECT 1 FROM users)`)
	return err == nil && !*n
}

// ---------- request context ----------

type ctx struct {
	s    *Server
	w    *resp
	r    *http.Request
	ip   string
	sess *session
	body obj

	user   *user  // signed-in user (nil when not signed in)
	tenant string // the organization the session works in ("" = none)
	role   string // the user's role there; super admins are "admin" everywhere
	super  bool   // super admin, and not signed in with an organization's own single sign-on
}

func (c *ctx) level() int { return roleLevel[c.role] }

func (c *ctx) param(k string) string { return c.r.PathValue(k) }

// query is like Object.fromEntries(url.searchParams): the last value wins.
func (c *ctx) query(k string) string {
	v := c.r.URL.Query()[k]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// actor names the signed-in user in the event log.
func (c *ctx) actor() string {
	if c.user == nil {
		return ""
	}
	return c.user.Email
}

func (c *ctx) audit(message, serverID string) {
	db.Event("audit", message, db.EventOpts{Actor: c.actor(), IP: c.ip, ServerID: serverID, TenantID: c.tenant})
}

type handler func(c *ctx) (any, error)

type flag int

// Without a flag a route needs a signed-in member of an organization; viewers may only read (GET).
const (
	open    flag = 1 << iota // no session needed
	raw                      // the body is not read (webhooks post whatever their sender likes)
	account                  // a signed-in user, with or without an organization (their own account)
	admins                   // the organization's admins
	supers                   // super admins
)

// adminOnly are the parts of an organization only its admins see, by path prefix.
var adminOnly = []string{"/api/variables", "/api/repos", "/api/tools", "/api/schedules", "/api/triggers", "/api/alerts", "/api/custom-metrics",
	"/api/mutes", "/api/notifiers", "/api/events", "/api/settings", "/api/members", "/api/tenant", "/api/accounts"}

func (s *Server) route(pattern string, h handler, flags ...flag) {
	var f flag
	for _, x := range flags {
		f |= x
	}
	_, p, _ := strings.Cut(pattern, " ")
	if f&(open|account|supers) == 0 && slices.ContainsFunc(adminOnly, func(a string) bool { return strings.HasPrefix(p, a) }) {
		f |= admins
	}
	s.mux.HandleFunc(pattern, func(w0 http.ResponseWriter, r *http.Request) {
		w := w0.(*resp)
		c := &ctx{s: s, w: w, r: r, ip: s.clientIP(r)}
		out, err := func() (any, error) {
			var err error
			if c.sess, err = getSession(r); err != nil {
				return nil, err
			}
			if c.sess != nil {
				if err := c.load(); err != nil {
					return nil, err
				}
			}
			if err := c.allowed(f); err != nil {
				return nil, err
			}
			// Sliding cookie, so "7 idle days" holds; otherwise the browser drops it 7 days after sign-in.
			if c.sess != nil && db.Now()-c.sess.SeenAt > 3600e3 {
				w.Header().Set("Set-Cookie", s.sessionCookie(r, sessionToken(r), sessionMS/1000))
			}
			if f&raw != 0 {
				_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)) // drain and ignore
			} else if c.body, err = readBody(r); err != nil {
				return nil, err
			}
			return h(c)
		}()
		if err != nil {
			fail(w, r, err)
		} else if out != nil {
			writeJSON(w, 200, out)
		}
	})
}

func readBody(r *http.Request) (obj, error) {
	if r.Method == "GET" || r.Method == "HEAD" {
		return obj{}, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1_000_001))
	if err != nil {
		return nil, err
	}
	if len(data) > 1_000_000 {
		return nil, httperr.New(413, "Request too large")
	}
	if len(data) == 0 {
		return obj{}, nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, httperr.Bad("Invalid JSON")
	}
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case []any:
		return obj{}, nil // an array has no named properties, like in JS
	}
	return nil, nil // null or a primitive: reads see nothing; validateServer answers "invalid body"
}

// ---------- responses ----------

// resp remembers whether anything was written, so an error after the headers went out (SSE) isn't sent as JSON.
type resp struct {
	http.ResponseWriter
	wrote bool
}

func (w *resp) WriteHeader(code int)        { w.wrote = true; w.ResponseWriter.WriteHeader(code) }
func (w *resp) Write(b []byte) (int, error) { w.wrote = true; return w.ResponseWriter.Write(b) }
func (w *resp) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	if rw, ok := w.(*resp); ok && rw.wrote {
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("json: %v", err)
		status, buf = 500, *bytes.NewBufferString(`{"error":"Internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	status := httperr.Status(err)
	if status >= 500 {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	}
	writeJSON(w, status, errBody(err.Error()))
}

// ---------- plumbing ----------

func (s *Server) ServeHTTP(w0 http.ResponseWriter, r *http.Request) {
	if isUpgrade(r) {
		s.terminal(w0, r)
		return
	}
	w := &resp{ResponseWriter: w0}
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			log.Printf("panic: %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
			writeJSON(w, 500, errBody(fmt.Sprint(v)))
		}
	}()
	s.securityHeaders(w, r)
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method == "GET" && s.serveStatic(w, r) {
			return
		}
		writeJSON(w, 404, errBody("Not found"))
		return
	}
	mutating := r.Method != "GET" && r.Method != "HEAD"
	if mutating && !s.sameOrigin(r, false) {
		writeJSON(w, 403, errBody("Cross-origin request blocked"))
		return
	}
	hook := strings.HasPrefix(r.URL.Path, "/api/hooks/")
	if mutating && !hook && r.ContentLength > 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, 415, errBody("JSON only"))
		return
	}
	if r.Method == "HEAD" { // the mux would route HEAD to GET handlers; the JS router didn't
		writeJSON(w, 404, errBody("Not found"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

var saneHost = regexp.MustCompile(`^[\w.:\[\]-]+$`)

func (s *Server) securityHeaders(w http.ResponseWriter, r *http.Request) {
	connect := "connect-src 'self'"
	if h := s.hostOf(r); saneHost.MatchString(h) {
		connect += " ws://" + h + " wss://" + h
	}
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data:",
		connect, "font-src 'self'", "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'",
	}, "; "))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if s.isHTTPS(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		return false
	}
	data, err := fs.ReadFile(s.static, name)
	if err != nil {
		return false
	}
	ct := mimeTypes[path.Ext(name)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	_, _ = w.Write(data)
	return true
}

// ---------- auth ----------

// Rightmost entry: added by our proxy. The leftmost is client-controlled and would let anyone dodge the login lockout.
func (s *Server) clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); s.trustProxy && xff != "" {
		return strings.TrimSpace(xff[strings.LastIndex(xff, ",")+1:])
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (s.trustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

func (s *Server) hostOf(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); s.trustProxy && h != "" {
		return h
	}
	return r.Host
}

func (s *Server) sameOrigin(r *http.Request, required bool) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return !required
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host) // like new URL(o).host: lowercased, default port dropped
	if p := u.Port(); (p == "80" && (u.Scheme == "http" || u.Scheme == "ws")) || (p == "443" && (u.Scheme == "https" || u.Scheme == "wss")) {
		host = strings.TrimSuffix(host, ":"+p)
	}
	return host == s.hostOf(r)
}

// sessionToken reads the fd_session cookie. Malformed cookies are skipped, never an error.
func sessionToken(r *http.Request) string {
	tok := ""
	for _, h := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(h, ";") {
			i := strings.Index(part, "=")
			if i <= 0 || strings.TrimSpace(part[:i]) != "fd_session" {
				continue
			}
			if v, err := url.PathUnescape(strings.TrimSpace(part[i+1:])); err == nil && utf8.ValidString(v) {
				tok = v
			}
		}
	}
	return tok
}

type session struct {
	Hash      string  `db:"hash"`
	CreatedAt int64   `db:"created_at"`
	SeenAt    int64   `db:"seen_at"`
	IP        *string `db:"ip"`
	UA        *string `db:"ua"`
	UserID    string  `db:"user_id"`
	TenantID  *string `db:"tenant_id"`
	Locked    int     `db:"locked"`
}

func getSession(r *http.Request) (*session, error) {
	tok := sessionToken(r)
	if tok == "" {
		return nil, nil
	}
	hash := sec.SHA256(tok)
	ss, err := db.Get[session](`SELECT * FROM sessions WHERE hash = ?`, hash)
	if ss == nil || err != nil {
		return nil, err
	}
	now := db.Now()
	if now-ss.SeenAt > sessionMS {
		_, err := db.Exec(`DELETE FROM sessions WHERE hash = ?`, hash)
		return nil, err
	}
	if now-ss.SeenAt > 60e3 {
		if _, err := db.Exec(`UPDATE sessions SET seen_at = ? WHERE hash = ?`, now, hash); err != nil {
			return nil, err
		}
	}
	return ss, nil
}

func (s *Server) sessionCookie(r *http.Request, value string, maxAge int64) string {
	c := fmt.Sprintf("fd_session=%s; Path=/; HttpOnly; SameSite=Strict; Max-Age=%d", value, maxAge)
	if s.isHTTPS(r) {
		c += "; Secure"
	}
	return c
}

// createSession signs u in. tenant "" = their last or first organization; locked = signed in with that
// organization's own single sign-on (the session can't leave it and has no super-admin powers).
func (c *ctx) createSession(u *user, tenant string, locked bool) error {
	tok := sec.Token()
	now := db.Now()
	if tenant == "" {
		tenant = defaultTenant(u)
	}
	if _, err := db.Exec(`INSERT INTO sessions (hash, created_at, seen_at, ip, ua, user_id, tenant_id, locked) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sec.SHA256(tok), now, now, c.ip, jsSlice(c.r.Header.Get("User-Agent"), 300), u.ID, nullIfEmpty(tenant), b01(locked)); err != nil {
		return err
	}
	c.w.Header().Set("Set-Cookie", c.s.sessionCookie(c.r, tok, sessionMS/1000))
	return nil
}

// Per-IP lockout: 5 failures -> 15 minutes.
// ponytail: entries are never pruned (as in the JS); prune expired ones if many distinct IPs ever matter.
type failure struct {
	n     int
	until int64
}

func (s *Server) checkRate(ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.failures[ip]; f != nil && f.until > db.Now() {
		return httperr.New(429, fmt.Sprintf("Too many failed attempts. Try again in %d min.", int(math.Ceil(float64(f.until-db.Now())/60e3))))
	}
	return nil
}

func (s *Server) recordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[ip]
	if f == nil {
		f = &failure{}
		s.failures[ip] = f
	}
	if f.n++; f.n >= 5 {
		f.until, f.n = db.Now()+15*60e3, 0
	}
}

func totpSecret(u *user) (string, error) {
	if e := db.Str(u.TOTPSecret); e != "" {
		return sec.Decrypt(e)
	}
	return "", nil
}

func (s *Server) checkTOTP(u *user, code any) (bool, error) {
	s.totpMu.Lock()
	defer s.totpMu.Unlock()
	secret, err := totpSecret(u)
	if secret == "" || err != nil {
		return err == nil, err
	}
	counter, ok := sec.VerifyTOTP(secret, strings.Join(strings.Fields(orStr(code)), ""), time.Now())
	last, err := db.Get[int64](`SELECT totp_last FROM users WHERE id = ?`, u.ID)
	if err != nil || last == nil {
		return false, err
	}
	if !ok || counter <= *last {
		return false, nil // no replay within the window
	}
	_, err = db.Exec(`UPDATE users SET totp_last = ? WHERE id = ?`, counter, u.ID)
	return err == nil, err
}

func orStr(v any) string { // String(v || '')
	if truthy(v) {
		return jsString(v)
	}
	return ""
}
