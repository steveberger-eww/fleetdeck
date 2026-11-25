package web

// Passkeys (WebAuthn): passwordless sign-in. A passkey verifies the user (PIN, fingerprint), so it counts as both
// factors. Registered from Settings → Account; used from the login page.

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

// passkeyRow is a row of passkeys; Credential is webauthn.Credential as JSON (public key, sign count, flags).
type passkeyRow struct {
	ID         string `db:"id" json:"id"` // base64url credential id
	UserID     string `db:"user_id" json:"-"`
	Name       string `db:"name" json:"name"`
	Credential string `db:"credential" json:"-"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	LastUsed   *int64 `db:"last_used" json:"last_used"`
}

// waUser adapts a user to webauthn.User. The user handle is the user's id (a random UUID).
type waUser struct {
	u     *user
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return []byte(w.u.ID) }
func (w waUser) WebAuthnName() string                       { return w.u.Email }
func (w waUser) WebAuthnDisplayName() string                { return cmp.Or(w.u.Name, w.u.Email) }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func loadWAUser(u *user) (waUser, error) {
	rows, err := db.Select[passkeyRow](`SELECT * FROM passkeys WHERE user_id = ?`, u.ID)
	w := waUser{u: u}
	for _, r := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(r.Credential), &c); err != nil {
			return w, err
		}
		w.creds = append(w.creds, c)
	}
	return w, err
}

// relyingParty is WebAuthn for the host the browser used. Behind a proxy, that needs FLEETDECK_TRUST_PROXY=1.
func (s *Server) relyingParty(c *ctx) (*webauthn.WebAuthn, error) {
	host := s.hostOf(c.r)
	rpID := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		rpID = h
	}
	scheme := "http"
	if s.isHTTPS(c.r) {
		scheme = "https"
	}
	if scheme == "http" && rpID != "localhost" {
		return nil, httperr.Bad("Passkeys need HTTPS (or localhost)")
	}
	return webauthn.New(&webauthn.Config{RPID: strings.Trim(rpID, "[]"), RPDisplayName: "Fleetdeck", RPOrigins: []string{scheme + "://" + host}})
}

type passkeyFlow struct {
	data  webauthn.SessionData
	name  string // registration: the passkey's name
	until time.Time
}

// pending ceremonies live 5 minutes; key "reg:<session hash>" or "login:<token>". Capped, because anyone can start
// a sign-in.
func (s *Server) putFlow(key string, f passkeyFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, x := range s.passkeyPending {
		if time.Now().After(x.until) {
			delete(s.passkeyPending, k)
		}
	}
	if len(s.passkeyPending) >= 10_000 {
		return httperr.New(429, "Too many passkey requests. Try again in a few minutes.")
	}
	f.until = time.Now().Add(5 * time.Minute)
	s.passkeyPending[key] = f
	return nil
}

func (s *Server) takeFlow(key string) (passkeyFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.passkeyPending[key]
	delete(s.passkeyPending, key)
	return f, ok && time.Now().Before(f.until)
}

// credentialJSON is the browser's PublicKeyCredential, passed through as the "credential" field.
func credentialJSON(b obj) []byte {
	data, _ := json.Marshal(b["credential"])
	return data
}

// passkeyAllowed: like a password, a passkey doesn't get around "single sign-on only".
func passkeyAllowed(u *user) error {
	if u.PasswordLogin == 0 && os.Getenv("FLEETDECK_PASSWORD_LOGIN") != "1" {
		return httperr.New(403, "Sign-in without single sign-on is turned off for this account. Use single sign-on.")
	}
	return nil
}

func (s *Server) passkeyRoutes() {
	s.route("POST /api/account/passkeys/begin", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(c.body.str("name"))
		if name == "" || jsLen(name) > 60 {
			return nil, httperr.Bad("Give the passkey a name (max 60 characters), e.g. the device")
		}
		rp, err := s.relyingParty(c)
		if err != nil {
			return nil, err
		}
		w, err := loadWAUser(c.user)
		if err != nil {
			return nil, err
		}
		var exclude []protocol.CredentialDescriptor
		for _, cr := range w.creds {
			exclude = append(exclude, cr.Descriptor())
		}
		opts, data, err := rp.BeginRegistration(w, webauthn.WithExclusions(exclude),
			webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired,
				RequireResidentKey: new(true), UserVerification: protocol.VerificationRequired}))
		if err != nil {
			return nil, err
		}
		return opts, s.putFlow("reg:"+c.sess.Hash, passkeyFlow{data: *data, name: name})
	}, account)

	s.route("POST /api/account/passkeys/finish", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		f, ok := s.takeFlow("reg:" + c.sess.Hash)
		if !ok {
			return nil, httperr.Bad("The passkey request expired. Try again.")
		}
		rp, err := s.relyingParty(c)
		if err != nil {
			return nil, err
		}
		parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON(c.body))
		if err != nil {
			return nil, httperr.Bad("Invalid passkey response: " + err.Error())
		}
		w, err := loadWAUser(c.user)
		if err != nil {
			return nil, err
		}
		cred, err := rp.CreateCredential(w, f.data, parsed)
		if err != nil {
			return nil, httperr.Bad("The passkey could not be verified: " + err.Error())
		}
		js, err := json.Marshal(cred)
		if err != nil {
			return nil, err
		}
		if err := exec(`INSERT INTO passkeys (id, user_id, name, credential, created_at) VALUES (?, ?, ?, ?, ?)`,
			base64.RawURLEncoding.EncodeToString(cred.ID), c.user.ID, f.name, string(js), db.Now()); err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf("Added passkey %q", f.name), db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	s.route("DELETE /api/account/passkeys/{id}", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		pk, err := db.Get[passkeyRow](`SELECT * FROM passkeys WHERE id = ? AND user_id = ?`, c.param("id"), c.user.ID)
		if pk == nil || err != nil {
			return nil, cmpErr(err, notFound("Passkey"))
		}
		if err := exec(`DELETE FROM passkeys WHERE id = ?`, pk.ID); err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf("Removed passkey %q", pk.Name), db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	// Sign-in: the browser picks the passkey (discoverable credential), so no email is asked for.
	s.route("POST /api/auth/passkey/begin", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		rp, err := s.relyingParty(c)
		if err != nil {
			return nil, err
		}
		opts, data, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return nil, err
		}
		tok := sec.Token()
		return J{"token": tok, "options": opts}, s.putFlow("login:"+tok, passkeyFlow{data: *data})
	}, open)

	s.route("POST /api/auth/passkey/finish", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		fail := func(why string) error {
			s.recordFailure(c.ip)
			db.Event("security", "Failed passkey sign-in: "+why, db.EventOpts{Level: "warn", IP: c.ip})
			return httperr.New(401, "Passkey sign-in failed")
		}
		f, ok := s.takeFlow("login:" + c.body.str("token"))
		if !ok {
			return nil, httperr.Bad("The sign-in request expired. Try again.")
		}
		rp, err := s.relyingParty(c)
		if err != nil {
			return nil, err
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON(c.body))
		if err != nil {
			return nil, fail("invalid response")
		}
		var u *user
		_, cred, err := rp.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
			if u, err = getUser(string(handle)); err != nil || u == nil {
				return nil, cmpErr(err, errors.New("unknown user"))
			}
			return loadWAUser(u)
		}, f.data, parsed)
		if err != nil {
			return nil, fail("unknown or removed passkey, or a bad signature")
		}
		id := base64.RawURLEncoding.EncodeToString(cred.ID)
		pk, err := db.Get[passkeyRow](`SELECT * FROM passkeys WHERE id = ? AND user_id = ?`, id, u.ID)
		if pk == nil || err != nil {
			return nil, cmpErr(err, fail("unknown passkey"))
		}
		if cred.Authenticator.CloneWarning {
			s.recordFailure(c.ip)
			db.Event("security", fmt.Sprintf("Refused passkey %q of %s: its signature counter went backwards, so the authenticator may have been cloned. Remove the passkey if you don't trust the device.", pk.Name, u.Email),
				db.EventOpts{Level: "error", Actor: u.Email, IP: c.ip})
			return nil, httperr.New(401, "This passkey looks cloned and was refused. See the audit log.")
		}
		if err := passkeyAllowed(u); err != nil {
			return nil, err
		}
		js, err := json.Marshal(cred) // the new sign count
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE passkeys SET credential = ?, last_used = ? WHERE id = ?`, string(js), db.Now(), pk.ID); err != nil {
			return nil, err
		}
		s.mu.Lock()
		delete(s.failures, c.ip)
		s.mu.Unlock()
		if err := c.createSession(u, "", false); err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf("Logged in with passkey %q", pk.Name), db.EventOpts{Actor: u.Email, IP: c.ip})
		return okResp, nil
	}, open)
}
