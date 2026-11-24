package ssh

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.Open(dir); err != nil {
		panic(err)
	}
	if err := sec.Init(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestShellQuoting(t *testing.T) {
	v := "it's $HOME `id`"
	out, err := exec.Command("sh", "-c", "printf %s "+Shq(v)).Output()
	if err != nil || string(out) != v {
		t.Fatalf("%q %v", out, err)
	}
}

func TestExplain(t *testing.T) {
	srv := &db.Server{Host: "web1", Port: 22, User: "deploy"}
	// Adapted: Go errors carry the errno, not the "ECONNRESET" text; the retry matches with errors.Is instead.
	e := Explain(fmt.Errorf("read tcp: %w", syscall.ECONNRESET), srv)
	if !strings.Contains(e.Error(), "dropped the connection") || !errors.Is(e, syscall.ECONNRESET) || !diedSilently(e) {
		t.Fatal(e)
	}
	if e := Explain(errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]"), srv); !strings.Contains(e.Error(), "~deploy/.ssh/authorized_keys") {
		t.Fatal(e)
	}
	if e := Explain(errors.New("weird"), srv); e.Error() != "weird" {
		t.Fatal(e)
	}
}

// Golden values printed by the Node version (JSON.stringify(WRAP_PW)).
func TestWrapVerbatim(t *testing.T) {
	var want map[Mode]string
	_ = json.Unmarshal([]byte(`{"root":"sh -c 'IFS= read -r p; if [ \"$(id -u)\" = 0 ]; then unset p; exec sh -s; fi; f=$(mktemp) || exit 1; trap \"rm -f \\\"\\$f\\\"\" EXIT; cat > \"$f\"; if sudo -n true 2>/dev/null; then unset p; sudo -n sh \"$f\"; else printf \"%s\\n\" \"$p\" | sudo -k -S -p \"\" sh \"$f\" ; fi'","auto":"sh -c 'IFS= read -r p; if [ \"$(id -u)\" = 0 ]; then unset p; exec sh -s; fi; f=$(mktemp) || exit 1; trap \"rm -f \\\"\\$f\\\"\" EXIT; cat > \"$f\"; if sudo -n true 2>/dev/null; then unset p; sudo -n sh \"$f\"; else printf \"%s\\n\" \"$p\" | sudo -k -S -p \"\" sh \"$f\" || sh \"$f\"; fi'"}`), &want)
	for m, w := range want {
		if WrapPW[m] != w {
			t.Fatalf("%s:\n%s\n%s", m, WrapPW[m], w)
		}
	}
}

func TestKeys(t *testing.T) {
	priv, pub, err := GenerateKey("fleetdeck-x")
	if err != nil || !strings.HasPrefix(pub, "ssh-ed25519 AAAA") || !strings.HasSuffix(pub, " fleetdeck-x") {
		t.Fatal(pub, err)
	}
	if got, err := ParsePrivateKey(priv, "ignored"); err != nil || got+" fleetdeck-x" != pub {
		t.Fatal(got, err)
	}
	if _, err := ParsePrivateKey(pub, ""); err == nil || err.Error() != "That is a public key; paste the private key" {
		t.Fatal(err)
	}
	if _, err := ParsePrivateKey("junk", ""); err == nil || !strings.HasPrefix(err.Error(), "Invalid key: ") {
		t.Fatal(err)
	}
}

// ---- in-process SSH server: runs exec requests with the local sh ----

type testServer struct {
	addr         string
	active, peak atomic.Int32
	listener     net.Listener
}

func startServer(t *testing.T, opts ...func(*ssh.ServerConfig)) *testServer {
	_, hk, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hk)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	for _, o := range opts {
		o(cfg)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{addr: l.Addr().String(), listener: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go ts.session(ch, creqs)
				}
			}()
		}
	}()
	return ts
}

func (ts *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	n := ts.active.Add(1)
	defer ts.active.Add(-1)
	for p := ts.peak.Load(); n > p && !ts.peak.CompareAndSwap(p, n); p = ts.peak.Load() {
	}
	defer ch.Close()
	var cmd *exec.Cmd
	done := make(chan struct{})
	for {
		select {
		case <-done:
			code := cmd.ProcessState.ExitCode()
			_, _ = ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(code)))
			return
		case r, ok := <-reqs:
			if !ok {
				if cmd != nil && cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			}
			switch r.Type {
			case "exec":
				cmd = exec.Command("sh", "-c", string(r.Payload[4:]))
				in, _ := cmd.StdinPipe()
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				_ = cmd.Start()
				go func() { io.Copy(in, ch); in.Close() }()
				go func() { cmd.Wait(); close(done) }()
				r.Reply(true, nil)
			case "signal":
				cmd.Process.Kill()
			default:
				r.Reply(false, nil)
			}
		}
	}
}

func addServer(t *testing.T, addr string) *db.Server {
	priv, pub, _ := GenerateKey("test")
	kid, sid := db.NewID(), db.NewID()
	host, port, _ := net.SplitHostPort(addr)
	if _, err := db.Exec(`INSERT INTO keys (id, name, public_key, private_enc, created_at) VALUES (?, 'k', ?, ?, 0)`, kid, pub, sec.Encrypt(priv)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO servers (id, name, host, port, user, key_id, created_at) VALUES (?, 'srv', ?, ?, 'me', ?, 0)`, sid, host, port, kid); err != nil {
		t.Fatal(err)
	}
	s, _ := db.GetServer(sid)
	t.Cleanup(func() { Drop(sid) })
	return s
}

func run(t *testing.T, s *db.Server, script string, o ExecOpts) Result {
	t.Helper()
	o.Mode = ModeUser
	r, err := Exec(context.Background(), s, script, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExec(t *testing.T) {
	ts := startServer(t)
	s := addServer(t, ts.addr)

	r := run(t, s, "echo out; echo err >&2; exit 3", ExecOpts{})
	if r.Code != 3 || !strings.Contains(r.Out, "out\n") || !strings.Contains(r.Out, "err\n") {
		t.Fatalf("%+v", r)
	}
	if !StatusOf(s).Connected {
		t.Fatal("pooled connection expected")
	}
	// A multibyte character split across writes is never split across OnData chunks.
	var chunks []string
	r = run(t, s, `printf '\303'; sleep 0.2; printf '\244\n'`, ExecOpts{OnData: func(c string) { chunks = append(chunks, c) }})
	for _, c := range chunks {
		if !utf8.ValidString(c) || strings.Contains(c, "\uFFFD") {
			t.Fatalf("split rune: %q", chunks)
		}
	}
	if r.Out != "ä\n" || r.Code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(t, s, "head -c 5000 /dev/zero | tr '\\0' x", ExecOpts{MaxOutput: 100}); len(r.Out) > 5000 || len(r.Out) < 100 {
		t.Fatal(len(r.Out))
	}

	start := time.Now()
	r = run(t, s, "echo begin; sleep 5", ExecOpts{Timeout: 600 * time.Millisecond})
	if !strings.HasSuffix(r.Out, "\n[timed out after 1s]\n") || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start = time.Now()
	if _, err := Exec(ctx, s, "sleep 5", ExecOpts{Mode: ModeUser}); err != nil || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}

	// At most 6 channels at once; the rest wait their turn.
	var wg sync.WaitGroup
	for range 9 {
		wg.Add(1)
		go func() { defer wg.Done(); run(t, s, "sleep 0.3", ExecOpts{}) }()
	}
	wg.Wait()
	if p := ts.peak.Load(); p != maxChannels {
		t.Fatalf("peak channels %d", p)
	}

	// A connection that died between uses is replaced transparently.
	mu.Lock()
	pool[s.ID].client.Close()
	mu.Unlock()
	if r = run(t, s, "echo again", ExecOpts{}); r.Out != "again\n" {
		t.Fatalf("%+v", r)
	}
}

func TestHostKeyPinning(t *testing.T) {
	a, b := startServer(t), startServer(t)
	s := addServer(t, a.addr)
	run(t, s, "true", ExecOpts{})
	s, _ = db.GetServer(s.ID)
	if !strings.HasPrefix(db.Str(s.HostKey), "SHA256:") || !strings.HasPrefix(db.Str(s.HostKeyRaw), "ssh-ed25519 ") {
		t.Fatalf("not pinned: %v", s.HostKey)
	}
	pinned := *s.HostKey

	var notes atomic.Int32
	Notify = func(_, _, _ string) { notes.Add(1) }
	defer func() { Notify = func(_, _, _ string) {} }()
	Drop(s.ID)
	_, port, _ := net.SplitHostPort(b.addr)
	db.Exec(`UPDATE servers SET port = ? WHERE id = ?`, port, s.ID) // same server id, different host key
	s, _ = db.GetServer(s.ID)
	for range 2 {
		_, err := Exec(context.Background(), s, "true", ExecOpts{Mode: ModeUser})
		if err == nil || !strings.HasPrefix(err.Error(), "HOST KEY CHANGED (offered SHA256:") {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	fp := StatusOf(s).HostKeyMismatch
	if fp == nil || *fp == pinned || notes.Load() != 1 {
		t.Fatalf("mismatch %v, notified %d", fp, notes.Load())
	}
	if got, ok := AcceptHostKey(s.ID); !ok || got != *fp {
		t.Fatal(got, ok)
	}
	if r := run(t, s, "echo ok", ExecOpts{}); r.Out != "ok\n" || StatusOf(s).HostKeyMismatch != nil {
		t.Fatalf("%+v", r)
	}
	if _, ok := AcceptHostKey(s.ID); ok {
		t.Fatal("nothing pending")
	}
}

// Node (ssh2) pinned the ed25519 key of hosts that offer several; x/crypto's default order would see a "changed" key.
func TestHostKeyAlgos(t *testing.T) {
	for raw, want := range map[string][]string{
		"":                         {ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256},
		"ssh-ed25519 AAAA":         {ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256},
		"ecdsa-sha2-nistp256 AAAA": {ssh.KeyAlgoECDSA256, ssh.KeyAlgoED25519},
		"ssh-rsa AAAA":             {ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA, ssh.KeyAlgoED25519},
	} {
		got := hostKeyAlgos(raw)
		if len(got) != len(hostKeyOrder) || !slices.Equal(got[:len(want)], want) {
			t.Errorf("hostKeyAlgos(%q) = %v", raw, got)
		}
	}
}

// A hardened sshd: ECDSA host key offered too, and only key exchanges outside x/crypto's defaults.
func TestNodeCompatibleAlgorithms(t *testing.T) {
	ts := startServer(t, func(c *ssh.ServerConfig) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		signer, _ := ssh.NewSignerFromKey(k)
		c.AddHostKey(signer)
		c.KeyExchanges = []string{ssh.KeyExchangeDHGEXSHA256, ssh.KeyExchangeDH16SHA512}
	})
	s := addServer(t, ts.addr)
	run(t, s, "true", ExecOpts{})
	if s, _ = db.GetServer(s.ID); !strings.HasPrefix(db.Str(s.HostKeyRaw), "ssh-ed25519 ") {
		t.Fatalf("pinned %v, want the ed25519 key like ssh2", db.Str(s.HostKeyRaw))
	}
}
