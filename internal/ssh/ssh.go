// Package ssh: one pooled SSH connection per server, host keys pinned on first use, private keys decrypted only
// in memory. Scripts are piped over stdin to `sh -s`, so the remote login shell and quoting never matter.
//
// CONTRACT STUB: signatures are final; bodies are filled in by the ssh port.
package ssh

import (
	"context"
	"io"
	"strings"
	"time"

	"fleetdeck/internal/db"
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

// Exec runs script on the server and waits. Cancelling ctx kills the channel (output stops; a remote process
// without a PTY may keep running). Retries once when a pooled connection died silently between uses.
func Exec(ctx context.Context, s *db.Server, script string, o ExecOpts) (Result, error) {
	panic("not implemented")
}

// Shell is an interactive PTY session; it holds one channel slot of the pooled connection until closed.
type Shell interface {
	io.ReadWriter // Read: terminal output; Write: keystrokes
	Resize(cols, rows int) error
	Close() error
	Wait() error // returns when the remote shell exits
}

// OpenShell starts a login shell with an xterm-256color PTY.
func OpenShell(s *db.Server, cols, rows int) (Shell, error) { panic("not implemented") }

// Drop closes the pooled connection (running commands and terminals on it end).
func Drop(id string) { panic("not implemented") }

// Shq single-quotes s for sh.
func Shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Explain turns raw network/SSH errors into what to check; the original text stays in brackets.
func Explain(err error, s *db.Server) error { panic("not implemented") }

// ParsePrivateKey validates a private key (OpenSSH, PKCS#1/#8, EC PEM) and returns its "type base64" public key.
// Errors are httperr 400s ("Invalid key: ..." / "That is a public key; paste the private key").
func ParsePrivateKey(pem, passphrase string) (string, error) { panic("not implemented") }

// GenerateKey makes an ed25519 key pair: OpenSSH PEM private key and "ssh-ed25519 AAAA... comment" public key.
func GenerateKey(comment string) (priv, pub string, err error) { panic("not implemented") }

// Status is the live connection state shown on the server view.
type Status struct {
	Connected       bool    `json:"connected"`
	HostKeyMismatch *string `json:"hostKeyMismatch"` // fingerprint the server offered instead of the pinned one
	SudoRejected    bool    `json:"sudoRejected"`
}

func StatusOf(s *db.Server) Status { panic("not implemented") }

// AcceptHostKey pins the pending mismatched key and reconnects. ok=false when there is none.
func AcceptHostKey(id string) (fp string, ok bool) { panic("not implemented") }

// Fingerprint of a wire-format public key: "SHA256:" + unpadded base64 of its SHA-256.
func Fingerprint(wireKey []byte) string { panic("not implemented") }

// SudoUsable reports whether the server has a stored sudo password that hasn't been rejected.
func SudoUsable(s *db.Server) bool { panic("not implemented") }
