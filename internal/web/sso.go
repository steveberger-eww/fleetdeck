package web

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

// Single sign-on with any OpenID Connect provider (authentik, Keycloak, …), at two levels:
//   - the instance provider (super admins set it up, e.g. the company's authentik): signs in any account,
//     matched by connected identity or email, and creates accounts for invites accepted through it.
//   - an organization's own provider (its admins set it up; a licensed feature): signs in that organization's
//     members only, optionally creating them with a default role. Such sessions are locked to the organization
//     and have no super-admin powers, because the organization runs that provider.
//
// Keys (settings for the instance, tenant_settings for an organization): oidc_issuer, oidc_client_id,
// oidc_secret (encrypted); organizations also oidc_role (role for new people; "" = members only).
// All providers use the same redirect URI.

const ssoCallback = "/api/auth/oidc/callback"

type provider struct {
	tenant string // "" = the instance provider
	key    string // identities.provider
}

func instanceSSO() provider       { return provider{key: "instance"} }
func tenantSSO(t string) provider { return provider{tenant: t, key: "tenant:" + t} }
func (p provider) get(k string) string {
	if p.tenant == "" {
		return db.Setting(k, "")
	}
	return db.TenantSetting(p.tenant, k, "")
}

func (p provider) set(k string, v any) error {
	if p.tenant == "" {
		return db.SetSetting(k, v)
	}
	return db.SetTenantSetting(p.tenant, k, v)
}

func (p provider) configured() bool {
	return p.get("oidc_issuer") != "" && p.get("oidc_client_id") != ""
}

// ssoLabel is the provider's host for "Sign in with …", or "" when it isn't set up.
func ssoLabel(p provider) string {
	if !p.configured() {
		return ""
	}
	if u, err := url.Parse(p.get("oidc_issuer")); err == nil {
		return u.Hostname()
	}
	return "single sign-on"
}

type oidcFlow struct {
	verifier, nonce string
	p               provider
	link            string // session hash when connecting an account from Settings
	invite          string // invite token when accepting an invite through the instance provider
	expires         int64
}

func (s *Server) redirectURL(r *http.Request) string { return s.publicBase(r) + ssoCallback }

// discover loads the provider's metadata. It accepts what people tend to paste: the issuer with or without
// the trailing slash, or the full .well-known URL. Returns the provider and the issuer that worked.
func discover(ctx context.Context, issuer string) (*oidc.Provider, string, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/.well-known/openid-configuration")
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname()))) {
		return nil, "", httperr.Bad("Issuer URL must start with https://")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil && !strings.HasSuffix(issuer, "/") {
		if p2, err2 := oidc.NewProvider(ctx, issuer+"/"); err2 == nil {
			return p2, issuer + "/", nil
		}
	}
	if err != nil {
		return nil, "", httperr.Bad("Couldn't load the provider's OpenID configuration: " + err.Error())
	}
	return p, issuer, nil
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func (s *Server) oauthConfig(ctx context.Context, r *http.Request, p provider) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	op, _, err := discover(ctx, p.get("oidc_issuer"))
	if err != nil {
		return nil, nil, err
	}
	secret, err := sec.Decrypt(p.get("oidc_secret"))
	if err != nil {
		return nil, nil, err
	}
	id := p.get("oidc_client_id")
	return &oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: op.Endpoint(), RedirectURL: s.redirectURL(r),
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, op.Verifier(&oidc.Config{ClientID: id}), nil
}

func (s *Server) flowCookie(r *http.Request, value string, maxAge int) string {
	c := fmt.Sprintf("fd_oidc=%s; Path=/api/auth/oidc/; HttpOnly; SameSite=Lax; Max-Age=%d", value, maxAge)
	if s.isHTTPS(r) {
		c += "; Secure"
	}
	return c
}

// ssoPage answers the browser navigation (not the SPA's fetch), so it is HTML. On success it forwards at once:
// a meta refresh is a same-site navigation, so the SameSite=Strict session cookie is sent with it.
func ssoPage(w http.ResponseWriter, next, problem string) {
	body := `<meta http-equiv="refresh" content="0;url=` + html.EscapeString(next) + `"><p>Signed in. Continuing…</p>`
	if problem != "" {
		body = `<h1>Single sign-on didn't work</h1><p>` + html.EscapeString(problem) + `</p><p><a href="` + html.EscapeString(next) + `">Back to Fleetdeck</a></p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Fleetdeck</title>
<style>:root{color-scheme:light dark}body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 16px}</style>%s`, body)
}

// ssoView is the settings page's view of a provider.
func (s *Server) ssoView(c *ctx, p provider) (J, error) {
	out := J{"issuer": p.get("oidc_issuer"), "client_id": p.get("oidc_client_id"), "has_secret": p.get("oidc_secret") != "",
		"redirect_url": s.redirectURL(c.r)}
	if p.tenant != "" {
		out["role"] = p.get("oidc_role")
		out["licensed"] = licensed(p.tenant, "tenant-sso")
		t, err := getTenant(p.tenant)
		if err != nil || t == nil {
			return nil, cmpErr(err, notFound("Organization"))
		}
		out["login_url"] = s.publicBase(c.r) + "/?org=" + url.QueryEscape(t.Slug)
	}
	return out, nil
}

// saveSSO stores a provider's settings from a request body, after checking the provider answers.
func saveSSO(c *ctx, p provider) error {
	id := strings.TrimSpace(c.body.str("client_id"))
	secret := strings.TrimSpace(c.body.str("client_secret"))
	if id == "" {
		return httperr.Bad("Client ID is missing")
	}
	if secret == "" && p.get("oidc_secret") == "" {
		return httperr.Bad("Client secret is missing")
	}
	_, issuer, err := discover(c.r.Context(), c.body.str("issuer"))
	if err != nil {
		return err
	}
	// Another provider or client means another account namespace: connected identities no longer apply.
	if issuer != p.get("oidc_issuer") || id != p.get("oidc_client_id") {
		if err := exec(`DELETE FROM identities WHERE provider = ?`, p.key); err != nil {
			return err
		}
	}
	if secret != "" {
		if err := p.set("oidc_secret", sec.Encrypt(secret)); err != nil {
			return err
		}
	}
	if err := p.set("oidc_issuer", issuer); err != nil {
		return err
	}
	return p.set("oidc_client_id", id)
}

func removeSSO(p provider) error {
	for _, k := range []string{"oidc_issuer", "oidc_client_id", "oidc_secret", "oidc_role"} {
		if err := p.set(k, nil); err != nil {
			return err
		}
	}
	return exec(`DELETE FROM identities WHERE provider = ?`, p.key)
}

func (s *Server) ssoRoutes() {
	// Your view of the instance provider: is it there, is your account connected. Super admins also configure it.
	s.route("GET /api/sso", func(c *ctx) (any, error) {
		p := instanceSSO()
		out, err := s.ssoView(c, p)
		if err != nil {
			return nil, err
		}
		id, err := db.Get[string](`SELECT name FROM identities WHERE provider = 'instance' AND user_id = ?`, c.user.ID)
		if err != nil {
			return nil, err
		}
		out["configured"], out["can_configure"], out["linked"], out["account"] = p.configured(), c.super, id != nil, db.Str(id)
		out["password_login"] = c.user.PasswordLogin != 0 || os.Getenv("FLEETDECK_PASSWORD_LOGIN") == "1"
		out["password_forced"] = os.Getenv("FLEETDECK_PASSWORD_LOGIN") == "1"
		out["has_password"] = c.user.PasswordHash != nil
		return out, nil
	}, account)

	s.route("PUT /api/sso", func(c *ctx) (any, error) {
		if err := saveSSO(c, instanceSSO()); err != nil {
			return nil, err
		}
		db.Event("audit", "Single sign-on settings saved ("+db.Setting("oidc_issuer", "")+")", db.EventOpts{Actor: c.actor(), IP: c.ip})
		return okResp, nil
	}, supers)

	s.route("DELETE /api/sso", func(c *ctx) (any, error) {
		if err := removeSSO(instanceSSO()); err != nil {
			return nil, err
		}
		db.Event("audit", "Single sign-on removed", db.EventOpts{Actor: c.actor(), IP: c.ip})
		return okResp, nil
	}, supers)

	// Your own account: single sign-on only, or password too.
	s.route("PUT /api/sso/password-login", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		on := c.body.truthy("enabled")
		if !on {
			if id, err := db.Get[int](`SELECT 1 FROM identities WHERE user_id = ?`, c.user.ID); err != nil || id == nil {
				return nil, cmpErr(err, httperr.Bad("Connect your single sign-on account first"))
			}
		}
		if err := exec(`UPDATE users SET password_login = ? WHERE id = ?`, b01(on), c.user.ID); err != nil {
			return nil, err
		}
		db.Event("security", map[bool]string{true: "Password sign-in turned on", false: "Password sign-in turned off"}[on], db.EventOpts{Actor: c.actor(), IP: c.ip})
		return okResp, nil
	}, account)

	// The organization's own provider.
	s.route("GET /api/tenant/sso", func(c *ctx) (any, error) { return s.ssoView(c, tenantSSO(c.tenant)) }, admins)
	s.route("PUT /api/tenant/sso", func(c *ctx) (any, error) {
		if !licensed(c.tenant, "tenant-sso") {
			return nil, httperr.New(402, "Your organization's own single sign-on needs a business license")
		}
		if personal(c.tenant) { // its provider could create accounts on this instance
			return nil, httperr.New(409, "A personal organization can't have its own single sign-on")
		}
		p := tenantSSO(c.tenant)
		if role := c.body.str("role"); role != "" {
			if err := validRole(role); err != nil || role == "admin" {
				return nil, cmpErr(err, httperr.Bad("New people can join as owner, operator or viewer"))
			}
		}
		if c.body.has("issuer") {
			if err := saveSSO(c, p); err != nil {
				return nil, err
			}
		}
		if c.body.has("role") {
			if err := p.set("oidc_role", nullIfEmpty(c.body.str("role"))); err != nil {
				return nil, err
			}
		}
		c.audit("Organization single sign-on saved ("+p.get("oidc_issuer")+")", "")
		return s.ssoView(c, p)
	}, admins)
	s.route("DELETE /api/tenant/sso", func(c *ctx) (any, error) {
		if err := removeSSO(tenantSSO(c.tenant)); err != nil {
			return nil, err
		}
		c.audit("Organization single sign-on removed", "")
		return okResp, nil
	}, admins)

	// Browser navigations from here on, not fetches.
	// ?org=<slug>: the organization's provider. ?invite=<token>: accept an invite with the instance provider.
	// ?link=1: connect your account to the instance provider.
	s.route("GET /api/auth/oidc/start", func(c *ctx) (any, error) {
		f := oidcFlow{verifier: oauth2.GenerateVerifier(), nonce: sec.Token(), expires: db.Now() + 10*60e3, p: instanceSSO()}
		back := "/"
		switch {
		case c.query("org") != "":
			t, err := db.Get[tenant](`SELECT * FROM tenants WHERE slug = ?`, strings.ToLower(c.query("org")))
			if err != nil {
				return nil, err
			}
			if t == nil || !licensed(t.ID, "tenant-sso") {
				ssoPage(c.w, back, "This organization has no single sign-on.")
				return nil, nil
			}
			f.p, back = tenantSSO(t.ID), "/?org="+url.QueryEscape(t.Slug)
		case c.query("invite") != "":
			f.invite, back = c.query("invite"), "/#/invite/"+url.PathEscape(c.query("invite"))
		case c.query("link") != "":
			if c.user == nil || c.unlocked() != nil {
				ssoPage(c.w, "/", "Sign in to Fleetdeck first, then connect your account from Settings.")
				return nil, nil
			}
			f.link, back = c.sess.Hash, "/#/settings/sso"
		}
		if !f.p.configured() {
			ssoPage(c.w, back, "Single sign-on isn't set up yet.")
			return nil, nil
		}
		cfg, _, err := s.oauthConfig(c.r.Context(), c.r, f.p)
		if err != nil {
			ssoPage(c.w, back, err.Error())
			return nil, nil
		}
		state := sec.Token()
		s.mu.Lock()
		for k, v := range s.oidcPending {
			if v.expires < db.Now() {
				delete(s.oidcPending, k)
			}
		}
		s.oidcPending[state] = f
		s.mu.Unlock()
		c.w.Header().Add("Set-Cookie", s.flowCookie(c.r, state, 600))
		http.Redirect(c.w, c.r, cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(f.verifier), oidc.Nonce(f.nonce)), http.StatusFound)
		return nil, nil
	}, open)

	s.route("GET "+ssoCallback, func(c *ctx) (any, error) {
		state := c.query("state")
		s.mu.Lock()
		f, ok := s.oidcPending[state]
		delete(s.oidcPending, state)
		s.mu.Unlock()
		c.w.Header().Add("Set-Cookie", s.flowCookie(c.r, "", 0))
		back := "/"
		switch {
		case f.link != "":
			back = "/#/settings/sso"
		case f.invite != "":
			back = "/#/invite/" + url.PathEscape(f.invite)
		}
		problem := func(msg string) (any, error) {
			db.Event("security", "Single sign-on failed: "+msg, db.EventOpts{Level: "warn", IP: c.ip, TenantID: f.p.tenant})
			ssoPage(c.w, back, msg)
			return nil, nil
		}
		// The cookie ties the answer to the browser that started; without it a link could plant someone else's account.
		if ck, err := c.r.Cookie("fd_oidc"); !ok || err != nil || ck.Value != state || f.expires < db.Now() {
			return problem("The sign-in took too long or was started in another browser. Please try again.")
		}
		if e := c.query("error"); e != "" {
			return problem("The provider answered: " + strings.TrimSpace(e+" "+c.query("error_description")))
		}
		cfg, verifier, err := s.oauthConfig(c.r.Context(), c.r, f.p)
		if err != nil {
			return problem(err.Error())
		}
		ctx, cancel := context.WithTimeout(c.r.Context(), 15*time.Second)
		defer cancel()
		tok, err := cfg.Exchange(ctx, c.query("code"), oauth2.VerifierOption(f.verifier))
		if err != nil {
			return problem("Exchanging the code failed (check the client secret): " + err.Error())
		}
		raw, _ := tok.Extra("id_token").(string)
		idt, err := verifier.Verify(ctx, raw)
		if err != nil {
			return problem("The ID token was rejected (in authentik: pick a Signing Key on the provider): " + err.Error())
		}
		if idt.Nonce != f.nonce {
			return problem("The ID token doesn't belong to this sign-in. Please try again.")
		}
		var claims struct {
			Username string `json:"preferred_username"`
			Email    string `json:"email"`
			Name     string `json:"name"`
		}
		_ = idt.Claims(&claims)
		label := cmp.Or(claims.Username, claims.Email, idt.Subject)

		u, problemMsg, err := s.ssoUser(c, f, idt.Subject, claims.Email, cmp.Or(claims.Name, claims.Username, claims.Email), label)
		if err != nil {
			return nil, err
		}
		if problemMsg != "" {
			return problem(problemMsg)
		}
		if f.link != "" {
			db.Event("security", "Single sign-on account connected: "+label, db.EventOpts{Actor: u.Email, IP: c.ip})
			ssoPage(c.w, back, "")
			return nil, nil
		}
		t, locked := "", f.p.tenant != ""
		if locked {
			t = f.p.tenant
		} else if f.invite != "" {
			if inv, err := getInvite(f.invite); err == nil && inv != nil {
				if err := join(u, inv); err != nil {
					return nil, err
				}
				t = inv.TenantID
				db.Event("audit", fmt.Sprintf("%s joined as %s", u.Email, inv.Role), db.EventOpts{Actor: u.Email, IP: c.ip, TenantID: t})
			}
			back = "/"
		}
		if err := c.createSession(u, t, locked); err != nil {
			return nil, err
		}
		c.w.Header().Add("Set-Cookie", s.flowCookie(c.r, "", 0)) // createSession replaced the header
		db.Event("security", "Logged in with single sign-on ("+label+")", db.EventOpts{Actor: u.Email, IP: c.ip, TenantID: t})
		if locked {
			back = "/"
		}
		ssoPage(c.w, back, "")
		return nil, nil
	}, open)
}

// ssoUser finds (or for invites and organizations that allow it, creates) the account a provider's answer signs
// in. A non-empty message is a problem to show instead.
func (s *Server) ssoUser(c *ctx, f oidcFlow, sub, email, name, label string) (*user, string, error) {
	var u *user
	if uid, err := db.Get[string](`SELECT user_id FROM identities WHERE provider = ? AND subject = ?`, f.p.key, sub); err != nil {
		return nil, "", err
	} else if uid != nil {
		if u, err = getUser(*uid); err != nil {
			return nil, "", err
		}
	}
	connect := func(u *user) error {
		_, err := db.Exec(`INSERT INTO identities (provider, subject, user_id, name, created_at) VALUES (?, ?, ?, ?, ?)
      ON CONFLICT (provider, subject) DO UPDATE SET user_id = excluded.user_id, name = excluded.name`, f.p.key, sub, u.ID, label, db.Now())
		return err
	}
	create := func(email, name string) (*user, error) {
		u := &user{ID: db.NewID(), Email: email, Name: cmp.Or(name, email), PasswordLogin: 1}
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created_at) VALUES (?, ?, ?, ?)`, u.ID, u.Email, u.Name, db.Now()); err != nil {
			return nil, err
		}
		return u, connect(u)
	}

	if f.link != "" { // connecting the signed-in account
		live, err := db.Get[session](`SELECT * FROM sessions WHERE hash = ?`, f.link)
		if err != nil || live == nil {
			return nil, "Your Fleetdeck session ended. Sign in and try again.", err
		}
		if u != nil && u.ID != live.UserID {
			return nil, fmt.Sprintf("%q is already connected to another Fleetdeck account.", label), nil
		}
		me, err := getUser(live.UserID)
		if err != nil || me == nil {
			return nil, "Your Fleetdeck session ended. Sign in and try again.", err
		}
		if err := exec(`DELETE FROM identities WHERE provider = ? AND user_id = ?`, f.p.key, me.ID); err != nil {
			return nil, "", err
		}
		return me, "", connect(me)
	}

	if f.p.tenant == "" { // the instance provider: trusted by the super admins, so email matches any account
		if u != nil {
			return u, "", nil
		}
		if email != "" {
			if u, err := userByEmail(email); err != nil || u != nil {
				if u != nil {
					err = connect(u)
				}
				return u, "", err
			}
		}
		if f.invite != "" {
			inv, err := getInvite(f.invite)
			if err != nil || inv == nil {
				return nil, "This invite link is invalid or has expired. Ask for a new one.", err
			}
			if other, err := userByEmail(inv.Email); err != nil || other != nil {
				return nil, fmt.Sprintf("There is already an account for %s. Sign in, then open the invite link again.", inv.Email), err
			}
			u, err := create(inv.Email, name)
			return u, "", err
		}
		return nil, fmt.Sprintf("There is no Fleetdeck account for %q. Ask an admin for an invite.", label), nil
	}

	// An organization's provider: only that organization's members, never super admins (it doesn't vouch for them).
	t, err := getTenant(f.p.tenant)
	if err != nil || t == nil {
		return nil, "This organization no longer exists.", err
	}
	isMember := func(u *user) (bool, error) {
		m, err := db.Get[int](`SELECT 1 FROM memberships WHERE user_id = ? AND tenant_id = ?`, u.ID, t.ID)
		return m != nil, err
	}
	if u != nil {
		if ok, err := isMember(u); err != nil || !ok {
			return nil, fmt.Sprintf("%s is no longer a member of %s.", label, t.Name), err
		}
		return u, "", nil
	}
	if email == "" {
		return nil, "The provider sent no email address, so the account can't be matched. Add the email scope.", nil
	}
	if existing, err := userByEmail(email); err != nil {
		return nil, "", err
	} else if existing != nil {
		ok, err := isMember(existing)
		if err != nil || !ok || existing.SuperAdmin != 0 {
			return nil, fmt.Sprintf("%s can't sign in with %s's single sign-on. Use your password or ask an admin of %s.", email, t.Name, t.Name), err
		}
		return existing, "", connect(existing)
	}
	role := f.p.get("oidc_role")
	if role == "" {
		return nil, fmt.Sprintf("There is no account for %s in %s. Ask an admin for an invite.", email, t.Name), nil
	}
	u, err = create(email, name)
	if err != nil {
		return nil, "", err
	}
	if _, err := db.Exec(`INSERT INTO memberships (user_id, tenant_id, role, created_at) VALUES (?, ?, ?, ?)`, u.ID, t.ID, role, db.Now()); err != nil {
		return nil, "", err
	}
	db.Event("audit", fmt.Sprintf("%s joined as %s through single sign-on", email, role), db.EventOpts{Actor: email, IP: c.ip, TenantID: t.ID})
	return u, "", nil
}
