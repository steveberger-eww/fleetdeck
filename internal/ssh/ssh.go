// Package ssh: one pooled SSH connection per server, host keys pinned on first use, private keys decrypted only
// in memory. Scripts are piped over stdin to `sh -s`, so the remote login shell (bash, zsh, fish...) and quoting never matter.
package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

// Mode picks how a script runs: as the login user, as root (sudo -n, or the stored sudo password),
// or as root when available and the login user otherwise.
type Mode string

const (
	ModeRoot Mode = "root" // default
	ModeUser Mode = "user"
	ModeAuto Mode = "auto"
)

// NoExit is Result.Code when the remote command ended without an exit status.
const NoExit = -1

type ExecOpts struct {
	Mode      Mode               // "" = ModeRoot
	OnData    func(chunk string) // called with stdout and stderr as UTF-8 text, in arrival order
	Timeout   time.Duration      // 0 = none; on expiry appends "\n[timed out after Ns]\n" and kills the channel
	MaxOutput int                // cap for Result.Out in bytes; 0 = 2_000_000 (OnData still sees everything)
}

type Result struct {
	Code int    // exit status, or NoExit
	Out  string // stdout and stderr interleaved
}

// Notify is called when a pinned host key changes. main wires it to alerts.Notify (avoids an import cycle).
var Notify = func(title, message, level string) {}

const (
	idleTimeout      = 5 * time.Minute
	maxChannels      = 6 // OpenSSH's default MaxSessions is 10
	handshakeTimeout = 15 * time.Second
	keepaliveEvery   = 30 * time.Second
	keepaliveMax     = 3
)

var wrap = map[Mode]string{
	ModeUser: "sh -s",
	// Non-root users need passwordless sudo; `sudo -n` fails fast instead of hanging on a prompt.
	ModeRoot: `sh -c 'if [ "$(id -u)" = 0 ]; then exec sh -s; else exec sudo -n sh -s; fi'`,
	// Use root when available, otherwise fall back to the login user (probe: more info, never fails on sudo).
	ModeAuto: `sh -c 'if [ "$(id -u)" != 0 ] && sudo -n true 2>/dev/null; then exec sudo -n sh -s; else exec sh -s; fi'`,
}

// ---- sudo with a stored password ----
// The password is the first stdin line, read into a shell variable (never argv, never a file), and piped
// to `sudo -S` for exactly one attempt. The script goes to a 0600 temp file so a wrong password can't
// make sudo consume script lines as further attempts. NOPASSWD rules still win via `sudo -n`.
func sudoPW(onReject string) string {
	return `sh -c 'IFS= read -r p; if [ "$(id -u)" = 0 ]; then unset p; exec sh -s; fi; ` +
		`f=$(mktemp) || exit 1; trap "rm -f \"\$f\"" EXIT; cat > "$f"; ` +
		`if sudo -n true 2>/dev/null; then unset p; sudo -n sh "$f"; ` +
		`else printf "%s\n" "$p" | sudo -k -S -p "" sh "$f" ` + onReject + `; fi'`
}

// WrapPW are the wrappers used when a stored sudo password is sent as the first stdin line.
var WrapPW = map[Mode]string{ModeRoot: sudoPW(""), ModeAuto: sudoPW(`|| sh "$f"`)}

type mismatch struct{ fp, raw string }

type entry struct {
	ready  chan struct{} // closed once client or err is set
	client *ssh.Client
	err    error
	active int
	queue  []chan struct{}
	idle   *time.Timer
}

var (
	mu         sync.Mutex
	pool       = map[string]*entry{}   // serverId -> pooled connection
	mismatches = map[string]mismatch{} // serverId -> key the server offered instead of the pinned one
	rejected   = map[string]string{}   // serverId -> sudo_enc that the server refused; skip it until it changes
)

// Shq single-quotes s for sh.
func Shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Fingerprint of a wire-format public key: "SHA256:" + unpadded base64 of its SHA-256.
func Fingerprint(wireKey []byte) string {
	h := sha256.Sum256(wireKey)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

func isRejected(id, enc string) bool {
	mu.Lock()
	defer mu.Unlock()
	return rejected[id] == enc
}

func sudoPassword(s *db.Server, mode Mode) (string, error) {
	enc := db.Str(s.SudoEnc)
	if mode == ModeUser || enc == "" || isRejected(s.ID, enc) {
		return "", nil
	}
	pw, err := sec.Decrypt(enc)
	if err != nil || strings.ContainsAny(pw, "\r\n") {
		return "", err
	}
	return pw, nil
}

func verifyHost(id string, key ssh.PublicKey) bool {
	fp := Fingerprint(key.Marshal())
	// "ssh-ed25519 AAAA..." from the wire-format key, for known_hosts files (Ansible).
	raw := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
	row, err := db.Get[struct {
		Name       string  `db:"name"`
		HostKey    *string `db:"host_key"`
		HostKeyRaw *string `db:"host_key_raw"`
	}](`SELECT name, host_key, host_key_raw FROM servers WHERE id = ?`, id)
	if err != nil || row == nil {
		return false
	}
	if db.Str(row.HostKey) == "" {
		if _, err := db.Exec(`UPDATE servers SET host_key = ?, host_key_raw = ? WHERE id = ?`, fp, raw, id); err != nil {
			log.Printf("pin host key: %v", err)
			return false
		}
		mu.Lock()
		delete(mismatches, id) // address changed: an old mismatch must not linger (or be "accepted" over the new pin)
		mu.Unlock()
		db.Event("security", fmt.Sprintf("Pinned host key %s for %s", fp, row.Name), db.EventOpts{ServerID: id})
		return true
	}
	if *row.HostKey == fp {
		mu.Lock()
		delete(mismatches, id)
		mu.Unlock()
		if db.Str(row.HostKeyRaw) == "" {
			_, _ = db.Exec(`UPDATE servers SET host_key_raw = ? WHERE id = ?`, raw, id)
		}
		return true
	}
	mu.Lock()
	prev := mismatches[id].fp
	mismatches[id] = mismatch{fp, raw}
	mu.Unlock()
	if prev != fp {
		db.Event("security", fmt.Sprintf("Host key CHANGED for %s: pinned %s, offered %s. Connection refused.", row.Name, *row.HostKey, fp),
			db.EventOpts{Level: "error", ServerID: id})
		goSafe(func() {
			Notify(fmt.Sprintf("🔴 %s: host key changed", row.Name),
				fmt.Sprintf("Pinned %s, offered %s. Fleetdeck refuses to connect until you trust the new key in the server settings.", *row.HostKey, fp), "error")
		})
	}
	return false
}

func goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("ssh: panic: %v", r)
			}
		}()
		fn()
	}()
}

// parseSigner accepts a passphrase for unencrypted keys too (it is ignored then), like ssh2 did.
func parseSigner(pemText, passphrase string) (ssh.Signer, error) {
	s, err := ssh.ParsePrivateKey([]byte(pemText))
	var pm *ssh.PassphraseMissingError
	if errors.As(err, &pm) && passphrase != "" {
		s, err = ssh.ParsePrivateKeyWithPassphrase([]byte(pemText), []byte(passphrase))
	}
	return s, err
}

func defaultKey() ssh.Signer {
	home, _ := os.UserHomeDir()
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", n))
		if err != nil {
			continue // missing
		}
		if s, err := ssh.ParsePrivateKey(data); err == nil { // skips passphrase-protected keys
			return s
		}
	}
	return nil
}

// credentials returns the signers to offer, and the agent connection to close after the handshake (or nil).
func credentials(s *db.Server) ([]ssh.Signer, io.Closer, error) {
	if id := db.Str(s.KeyID); id != "" {
		k, err := db.Get[struct {
			Priv string  `db:"private_enc"`
			Pass *string `db:"passphrase_enc"`
		}](`SELECT private_enc, passphrase_enc FROM keys WHERE id = ?`, id)
		if err != nil {
			return nil, nil, err
		}
		if k == nil {
			return nil, nil, errors.New("assigned key no longer exists")
		}
		priv, err := sec.Decrypt(k.Priv)
		if err != nil {
			return nil, nil, err
		}
		pass := ""
		if db.Str(k.Pass) != "" {
			if pass, err = sec.Decrypt(*k.Pass); err != nil {
				return nil, nil, err
			}
		}
		signer, err := parseSigner(priv, pass)
		if err != nil {
			return nil, nil, err
		}
		return []ssh.Signer{signer}, nil, nil
	}
	var signers []ssh.Signer
	if k := defaultKey(); k != nil {
		signers = append(signers, k) // ssh2 tried the private key before the agent
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" && signers == nil {
		return nil, nil, errors.New("no key assigned and no ssh-agent or ~/.ssh key available")
	}
	if sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			if as, err := agent.NewClient(c).Signers(); err == nil {
				signers = append(signers, as...)
			}
			return signers, c, nil
		}
	}
	return signers, nil, nil
}

var passphraseRe = regexp.MustCompile(`(?i)passphrase`)

// Explain turns raw network/SSH errors into what to check; the original text stays in brackets
// (and stays wrapped, so errors.Is still matches it).
func Explain(err error, s *db.Server) error {
	if err == nil {
		return nil
	}
	at := fmt.Sprintf("%s:%d", s.Host, s.Port)
	msg := err.Error()
	var dns *net.DNSError
	var ne net.Error
	hint := ""
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		hint = fmt.Sprintf("Connection refused by %s: is sshd running, and is the port right?", at)
	case errors.Is(err, syscall.ETIMEDOUT):
		hint = fmt.Sprintf("No answer from %s: check the address, the firewall, and that the server is up.", at)
	case errors.Is(err, syscall.EHOSTUNREACH):
		hint = fmt.Sprintf("%s is unreachable from Fleetdeck: check the address and routing.", at)
	case errors.Is(err, syscall.ENETUNREACH):
		hint = fmt.Sprintf("No network route to %s.", at)
	case errors.As(err, &dns) && dns.IsNotFound:
		hint = fmt.Sprintf(`Cannot resolve the hostname "%s".`, s.Host)
	case errors.As(err, &dns) && (dns.IsTemporary || dns.IsTimeout):
		hint = fmt.Sprintf(`DNS lookup for "%s" failed (temporary); try again.`, s.Host)
	case errors.Is(err, syscall.ECONNRESET):
		hint = fmt.Sprintf("%s dropped the connection: sshd may be restarting, or fail2ban/MaxStartups is blocking Fleetdeck.", at)
	case errors.As(err, &ne) && ne.Timeout(): // Explain only sees dial/handshake errors
		hint = fmt.Sprintf("No SSH handshake from %s within 15s: the host or a firewall is silently dropping traffic.", at)
	case strings.Contains(msg, "unable to authenticate"):
		hint = fmt.Sprintf(`SSH login as "%s" was rejected: check that the key is in ~%s/.ssh/authorized_keys.`, s.User, s.User)
	case passphraseRe.MatchString(msg):
		hint = "The private key is encrypted: add its passphrase in Keys."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%s [%w]", hint, err)
}

var errHostDenied = errors.New("host key verification failed")

func connect(s *db.Server) (*ssh.Client, error) {
	signers, agentConn, err := credentials(s)
	if err != nil {
		return nil, Explain(err, s)
	}
	if agentConn != nil {
		defer agentConn.Close()
	}
	addr := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))
	deadline := time.Now().Add(handshakeTimeout)
	c, err := net.DialTimeout("tcp", addr, handshakeTimeout)
	if err != nil {
		return nil, Explain(err, s)
	}
	_ = c.SetDeadline(deadline)
	cc, chans, reqs, err := ssh.NewClientConn(c, addr, &ssh.ClientConfig{
		User: s.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if verifyHost(s.ID, key) {
				return nil
			}
			return errHostDenied
		},
	})
	if err != nil {
		c.Close()
		mu.Lock()
		fp := mismatches[s.ID].fp
		mu.Unlock()
		if fp != "" && errors.Is(err, errHostDenied) {
			return nil, fmt.Errorf("HOST KEY CHANGED (offered %s). Verify it, then accept it in server settings.", fp)
		}
		return nil, Explain(err, s)
	}
	_ = c.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

// keepalive closes the connection after keepaliveMax unanswered keepalives (like ssh2's keepaliveCountMax).
func keepalive(c *ssh.Client, done <-chan struct{}) {
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	var missed atomic.Int32
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		if missed.Add(1) > keepaliveMax {
			c.Close()
			return
		}
		go func() {
			if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err == nil {
				missed.Store(0) // any reply (OpenSSH answers "failure") proves the peer is alive
			}
		}()
	}
}

func getEntry(s *db.Server) *entry {
	mu.Lock()
	defer mu.Unlock()
	if e := pool[s.ID]; e != nil {
		return e
	}
	e := &entry{ready: make(chan struct{})}
	pool[s.ID] = e
	srv := *s
	go func() {
		c, err := connect(&srv)
		mu.Lock()
		e.client, e.err = c, err
		if err != nil && pool[srv.ID] == e {
			delete(pool, srv.ID)
		}
		mu.Unlock()
		close(e.ready)
		if c == nil {
			return
		}
		done := make(chan struct{})
		go keepalive(c, done)
		_ = c.Wait()
		close(done)
		mu.Lock()
		if pool[srv.ID] == e {
			delete(pool, srv.ID)
		}
		mu.Unlock()
	}()
	return e
}

// acquire waits for the connection and a channel slot (FIFO). Every successful acquire needs a release.
func acquire(ctx context.Context, s *db.Server) (*entry, error) {
	e := getEntry(s)
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if e.err != nil {
		return nil, e.err
	}
	mu.Lock()
	if e.idle != nil {
		e.idle.Stop()
	}
	if e.active < maxChannels {
		e.active++
		mu.Unlock()
		return e, nil
	}
	ch := make(chan struct{})
	e.queue = append(e.queue, ch)
	mu.Unlock()
	select {
	case <-ch: // slot is handed over, not re-counted
		return e, nil
	case <-ctx.Done():
		mu.Lock()
		if i := slices.Index(e.queue, ch); i >= 0 {
			e.queue = slices.Delete(e.queue, i, i+1)
			mu.Unlock()
		} else {
			mu.Unlock()
			release(s.ID, e) // the slot arrived meanwhile: pass it on
		}
		return nil, ctx.Err()
	}
}

func release(id string, e *entry) {
	mu.Lock()
	defer mu.Unlock()
	if len(e.queue) > 0 {
		close(e.queue[0])
		e.queue = e.queue[1:]
		return
	}
	if e.active--; e.active == 0 {
		e.idle = time.AfterFunc(idleTimeout, func() {
			mu.Lock()
			busy := e.active > 0
			mu.Unlock()
			if !busy {
				closeEntry(id, e)
			}
		})
	}
}

func closeEntry(id string, e *entry) {
	mu.Lock()
	if pool[id] == e {
		delete(pool, id)
	}
	if e.idle != nil {
		e.idle.Stop()
	}
	mu.Unlock()
	go func() {
		<-e.ready
		if e.client != nil {
			e.client.Close()
		}
	}()
}

// Drop closes the pooled connection (running commands and terminals on it end).
func Drop(id string) {
	mu.Lock()
	e := pool[id]
	mu.Unlock()
	if e != nil {
		closeEntry(id, e)
	}
}

var sudoRejectRe = regexp.MustCompile(`incorrect password attempt|Sorry, try again`)

// Exec runs script on the server and waits. Cancelling ctx kills the channel (output stops; a remote process
// without a PTY may keep running). Retries once when a pooled connection died silently between uses.
func Exec(ctx context.Context, s *db.Server, script string, o ExecOpts) (Result, error) {
	r, err := execOnce(ctx, s, script, o)
	if err != nil && ctx.Err() == nil && diedSilently(err) {
		Drop(s.ID)
		return execOnce(ctx, s, script, o)
	}
	return r, err
}

// Go's forms of ssh2's /Not connected|No response|ECONNRESET|channel/.
func diedSilently(err error) bool {
	var oc *ssh.OpenChannelError
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.As(err, &oc)
}

func execOnce(ctx context.Context, s *db.Server, script string, o ExecOpts) (Result, error) {
	mode := o.Mode
	if mode == "" {
		mode = ModeRoot
	}
	maxOut := o.MaxOutput
	if maxOut == 0 {
		maxOut = 2_000_000
	}
	enc := db.Str(s.SudoEnc)
	if mode == ModeRoot && enc != "" && isRejected(s.ID, enc) {
		msg := "The stored sudo password was rejected by this server. Update it in the server settings.\n"
		if o.OnData != nil {
			o.OnData(msg)
		}
		return Result{1, msg}, nil
	}
	pw, err := sudoPassword(s, mode)
	if err != nil {
		return Result{}, err
	}
	e, err := acquire(ctx, s)
	if err != nil {
		return Result{}, err
	}
	defer release(s.ID, e)
	sess, err := e.client.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	stdin, _ := sess.StdinPipe()
	cmd := wrap[mode]
	if pw != "" {
		cmd = WrapPW[mode]
	}
	if err := sess.Start(cmd); err != nil {
		return Result{}, err
	}

	var (
		omu       sync.Mutex
		out, seen strings.Builder
		closed    bool
	)
	add := func(chunk string) {
		omu.Lock()
		defer omu.Unlock()
		if chunk == "" || closed {
			return
		}
		if pw != "" && seen.Len() < 4096 {
			seen.WriteString(chunk)
		}
		if out.Len() < maxOut {
			out.WriteString(chunk)
		}
		if o.OnData != nil {
			o.OnData(chunk)
		}
	}
	var wg sync.WaitGroup
	for _, r := range []io.Reader{stdout, stderr} {
		wg.Add(1)
		go func() { defer wg.Done(); pump(r, add) }()
	}
	// ponytail: closing the channel stops output, but a remote process without a PTY may keep running.
	var once sync.Once
	cancel := func() { once.Do(func() { _ = sess.Signal(ssh.SIGKILL); _ = sess.Close() }) }
	if o.Timeout > 0 {
		t := time.AfterFunc(o.Timeout, func() {
			add(fmt.Sprintf("\n[timed out after %ds]\n", int(math.Round(o.Timeout.Seconds()))))
			cancel()
		})
		defer t.Stop()
	}
	defer context.AfterFunc(ctx, cancel)()
	go func() {
		in := script
		if !strings.HasSuffix(in, "\n") {
			in += "\n"
		}
		if pw != "" {
			in = pw + "\n" + in
		}
		_, _ = io.WriteString(stdin, in)
		_ = stdin.Close()
	}()
	wg.Wait()
	werr := sess.Wait()
	omu.Lock()
	closed = true
	res := Result{Code: NoExit, Out: out.String()}
	matched := pw != "" && sudoRejectRe.MatchString(seen.String())
	omu.Unlock()
	var ee *ssh.ExitError
	switch {
	case werr == nil:
		res.Code = 0
	case errors.As(werr, &ee) && ee.Signal() != "":
		res.Code = 128
	case errors.As(werr, &ee):
		res.Code = ee.ExitStatus()
	}
	if matched {
		mu.Lock()
		rejected[s.ID] = enc
		mu.Unlock()
		db.Event("security", s.Name+" rejected the stored sudo password; Fleetdeck stops using it until it is changed",
			db.EventOpts{Level: "warn", ServerID: s.ID})
	}
	return res, nil
}

// pump reads r to EOF and passes text on without ever splitting a UTF-8 sequence across chunks.
func pump(r io.Reader, add func(string)) {
	buf := make([]byte, 32*1024)
	var rest []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b := append(rest, buf[:n]...)
			cut := len(b)
			for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
				if utf8.RuneStart(b[i]) {
					if !utf8.FullRune(b[i:]) {
						cut = i
					}
					break
				}
			}
			add(strings.ToValidUTF8(string(b[:cut]), "�"))
			rest = append([]byte(nil), b[cut:]...)
		}
		if err != nil {
			break
		}
	}
	if len(rest) > 0 {
		add("�") // like StringDecoder.end() on a truncated sequence
	}
}

// Shell is an interactive PTY session; it holds one channel slot of the pooled connection until closed.
type Shell interface {
	io.ReadWriter // Read: terminal output; Write: keystrokes
	Resize(cols, rows int) error
	Close() error
	Wait() error // returns when the remote shell exits
}

type shell struct {
	sess  *ssh.Session
	stdin io.WriteCloser
	r     *io.PipeReader
	done  chan struct{}
	err   error
}

func (s *shell) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *shell) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *shell) Resize(cols, rows int) error { return s.sess.WindowChange(rows, cols) }
func (s *shell) Wait() error                 { <-s.done; return s.err }
func (s *shell) Close() error {
	err := s.sess.Close()
	s.r.Close() // unblocks output copying when nobody reads anymore
	return err
}

// OpenShell starts a login shell with an xterm-256color PTY.
func OpenShell(s *db.Server, cols, rows int) (Shell, error) {
	e, err := acquire(context.Background(), s)
	if err != nil {
		return nil, err
	}
	sh, err := startShell(e.client, cols, rows)
	if err != nil {
		release(s.ID, e)
		return nil, err
	}
	go func() { <-sh.done; release(s.ID, e) }()
	return sh, nil
}

func startShell(c *ssh.Client, cols, rows int) (*shell, error) {
	sess, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, _ := sess.StdinPipe()
	r, w := io.Pipe()
	sess.Stdout, sess.Stderr = w, w
	if err = sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{}); err == nil {
		err = sess.Shell()
	}
	if err != nil {
		sess.Close()
		return nil, err
	}
	sh := &shell{sess: sess, stdin: stdin, r: r, done: make(chan struct{})}
	go func() {
		sh.err = sess.Wait()
		w.Close()
		close(sh.done)
	}()
	return sh, nil
}

// ParsePrivateKey validates a private key (OpenSSH, PKCS#1/#8, EC PEM) and returns its "type base64" public key.
// Errors are httperr 400s ("Invalid key: ..." / "That is a public key; paste the private key").
func ParsePrivateKey(pemText, passphrase string) (string, error) {
	signer, err := parseSigner(pemText, passphrase)
	if err != nil {
		if _, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(pemText)); perr == nil {
			return "", httperr.Bad("That is a public key; paste the private key")
		}
		return "", httperr.Bad("Invalid key: " + strings.TrimPrefix(err.Error(), "ssh: "))
	}
	pub := signer.PublicKey()
	return pub.Type() + " " + base64.StdEncoding.EncodeToString(pub.Marshal()), nil
}

// GenerateKey makes an ed25519 key pair: OpenSSH PEM private key and "ssh-ed25519 AAAA... comment" public key.
func GenerateKey(comment string) (priv, pub string, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(sk, comment)
	if err != nil {
		return "", "", err
	}
	sp, err := ssh.NewPublicKey(pk)
	if err != nil {
		return "", "", err
	}
	pubLine := strings.TrimSpace(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment)
	return string(pem.EncodeToMemory(block)), pubLine, nil
}

// Status is the live connection state shown on the server view.
type Status struct {
	Connected       bool    `json:"connected"`
	HostKeyMismatch *string `json:"hostKeyMismatch"` // fingerprint the server offered instead of the pinned one
	SudoRejected    bool    `json:"sudoRejected"`
}

func StatusOf(s *db.Server) Status {
	mu.Lock()
	defer mu.Unlock()
	var st Status
	if e := pool[s.ID]; e != nil && e.client != nil {
		st.Connected = true
	}
	if m, ok := mismatches[s.ID]; ok {
		st.HostKeyMismatch = &m.fp
	}
	enc := db.Str(s.SudoEnc)
	st.SudoRejected = enc != "" && rejected[s.ID] == enc
	return st
}

// AcceptHostKey pins the pending mismatched key and reconnects. ok=false when there is none.
func AcceptHostKey(id string) (fp string, ok bool) {
	mu.Lock()
	m, ok := mismatches[id]
	mu.Unlock()
	if !ok {
		return "", false
	}
	if _, err := db.Exec(`UPDATE servers SET host_key = ?, host_key_raw = ? WHERE id = ?`, m.fp, m.raw, id); err != nil {
		log.Printf("accept host key: %v", err)
	}
	mu.Lock()
	delete(mismatches, id)
	mu.Unlock()
	Drop(id)
	return m.fp, true
}

// SudoUsable reports whether the server has a stored sudo password that hasn't been rejected.
func SudoUsable(s *db.Server) bool {
	enc := db.Str(s.SudoEnc)
	return enc != "" && !isRejected(s.ID, enc)
}
