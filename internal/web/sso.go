package web

import (
	"cmp"
	"context"
	"errors"
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

// Single sign-on with any OpenID Connect provider (authentik, Keycloak, …).
// Fleetdeck has one owner, so SSO is bound to one account: the provider's `sub` saved by "Connect your account".
// Settings: oidc_issuer, oidc_client_id, oidc_secret (encrypted), oidc_sub, oidc_name, password_login ("0" = off).

const ssoCallback = "/api/auth/oidc/callback"

type oidcFlow struct {
	verifier, nonce string
	link            string // session hash when connecting an account from Settings; "" for sign-in
	expires         int64
}

func ssoConfigured() bool { return db.Setting("oidc_issuer", "") != "" && db.Setting("oidc_client_id", "") != "" }
func ssoLinked() bool     { return ssoConfigured() && db.Setting("oidc_sub", "") != "" }

// passwordLogin is false only when SSO is connected and the owner turned passwords off.
// FLEETDECK_PASSWORD_LOGIN=1 turns it back on, for when the provider is down.
func passwordLogin() bool {
	return os.Getenv("FLEETDECK_PASSWORD_LOGIN") == "1" || !ssoLinked() || db.Setting("password_login", "1") != "0"
}

func (s *Server) redirectURL(r *http.Request) string {
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + s.hostOf(r) + ssoCallback
}

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

func (s *Server) oauthConfig(ctx context.Context, r *http.Request) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p, _, err := discover(ctx, db.Setting("oidc_issuer", ""))
	if err != nil {
		return nil, nil, err
	}
	secret, err := sec.Decrypt(db.Setting("oidc_secret", ""))
	if err != nil {
		return nil, nil, err
	}
	id := db.Setting("oidc_client_id", "")
	return &oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: p.Endpoint(), RedirectURL: s.redirectURL(r),
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, p.Verifier(&oidc.Config{ClientID: id}), nil
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

func (s *Server) ssoRoutes() {
	s.route("GET /api/sso", func(c *ctx) (any, error) {
		return J{
			"issuer": db.Setting("oidc_issuer", ""), "client_id": db.Setting("oidc_client_id", ""), "has_secret": db.Setting("oidc_secret", "") != "",
			"redirect_url": s.redirectURL(c.r), "linked": ssoLinked(), "account": db.Setting("oidc_name", ""),
			"password_login": passwordLogin(), "password_forced": os.Getenv("FLEETDECK_PASSWORD_LOGIN") == "1",
		}, nil
	})

	s.route("PUT /api/sso", func(c *ctx) (any, error) {
		id := strings.TrimSpace(c.body.str("client_id"))
		secret := strings.TrimSpace(c.body.str("client_secret"))
		if id == "" {
			return nil, httperr.Bad("Client ID is missing")
		}
		if secret == "" && db.Setting("oidc_secret", "") == "" {
			return nil, httperr.Bad("Client secret is missing")
		}
		_, issuer, err := discover(c.r.Context(), c.body.str("issuer"))
		if err != nil {
			return nil, err
		}
		// Another provider or client means another account namespace: the connected account no longer applies.
		if issuer != db.Setting("oidc_issuer", "") || id != db.Setting("oidc_client_id", "") {
			for _, k := range []string{"oidc_sub", "oidc_name", "password_login"} {
				if err := db.SetSetting(k, nil); err != nil {
					return nil, err
				}
			}
		}
		if secret != "" {
			if err := db.SetSetting("oidc_secret", sec.Encrypt(secret)); err != nil {
				return nil, err
			}
		}
		if err := db.SetSetting("oidc_issuer", issuer); err != nil {
			return nil, err
		}
		if err := db.SetSetting("oidc_client_id", id); err != nil {
			return nil, err
		}
		c.audit("Single sign-on settings saved ("+issuer+")", "")
		return okResp, nil
	})

	s.route("DELETE /api/sso", func(c *ctx) (any, error) {
		for _, k := range []string{"oidc_issuer", "oidc_client_id", "oidc_secret", "oidc_sub", "oidc_name", "password_login"} {
			if err := db.SetSetting(k, nil); err != nil {
				return nil, err
			}
		}
		c.audit("Single sign-on removed; password sign-in is on", "")
		return okResp, nil
	})

	s.route("PUT /api/sso/password-login", func(c *ctx) (any, error) {
		on := c.body.truthy("enabled")
		if !on && !ssoLinked() {
			return nil, httperr.Bad("Connect your single sign-on account first")
		}
		v := any(nil)
		if !on {
			v = "0"
		}
		if err := db.SetSetting("password_login", v); err != nil {
			return nil, err
		}
		c.audit(map[bool]string{true: "Password sign-in turned on", false: "Password sign-in turned off"}[on], "")
		return okResp, nil
	})

	// Browser navigations from here on, not fetches.
	s.route("GET /api/auth/oidc/start", func(c *ctx) (any, error) {
		f := oidcFlow{verifier: oauth2.GenerateVerifier(), nonce: sec.Token(), expires: db.Now() + 10*60e3}
		back := "/"
		if c.query("link") != "" {
			if c.sess == nil {
				ssoPage(c.w, "/", "Sign in to Fleetdeck first, then connect your account from Settings.")
				return nil, nil
			}
			f.link, back = c.sess.Hash, "/#/settings/sso"
		}
		if !ssoConfigured() || (f.link == "" && !ssoLinked()) {
			ssoPage(c.w, back, "Single sign-on isn't set up yet.")
			return nil, nil
		}
		cfg, _, err := s.oauthConfig(c.r.Context(), c.r)
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
		if f.link != "" {
			back = "/#/settings/sso"
		}
		problem := func(msg string) (any, error) {
			db.Event("security", "Single sign-on failed: "+msg, db.EventOpts{Level: "warn", IP: c.ip})
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
		cfg, verifier, err := s.oauthConfig(c.r.Context(), c.r)
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
		}
		_ = idt.Claims(&claims)
		name := cmp.Or(claims.Username, claims.Email, idt.Subject)

		if f.link != "" {
			if live, err := db.Get[session](`SELECT * FROM sessions WHERE hash = ?`, f.link); err != nil || live == nil {
				return problem("Your Fleetdeck session ended. Sign in and try again.")
			}
			if err := errors.Join(db.SetSetting("oidc_sub", idt.Subject), db.SetSetting("oidc_name", name)); err != nil {
				return nil, err
			}
			db.Event("audit", "Single sign-on account connected: "+name, db.EventOpts{Actor: "admin", IP: c.ip})
			ssoPage(c.w, back, "")
			return nil, nil
		}
		if idt.Subject != db.Setting("oidc_sub", "") {
			return problem(fmt.Sprintf("%q is not the account connected to Fleetdeck.", name))
		}
		if err := c.createSession(); err != nil {
			return nil, err
		}
		c.w.Header().Add("Set-Cookie", s.flowCookie(c.r, "", 0)) // createSession replaced the header
		db.Event("security", "Logged in with single sign-on ("+name+")", db.EventOpts{Actor: "admin", IP: c.ip})
		ssoPage(c.w, back, "")
		return nil, nil
	}, open)
}
