package web

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-jose/go-jose/v4"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

// A fake provider signs ID tokens for whichever subject the test picks; Fleetdeck must only let the connected one in.
func TestSSOFlow(t *testing.T) {
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEETDECK_SECRET", "")
	t.Setenv("FLEETDECK_PASSWORD_LOGIN", "")
	if err := sec.Init(); err != nil {
		t.Fatal(err)
	}

	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k"))
	var sub, nonce string
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/o/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": idp.URL + "/o/", "authorization_endpoint": idp.URL + "/auth",
				"token_endpoint": idp.URL + "/token", "jwks_uri": idp.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			claims, _ := json.Marshal(map[string]any{"iss": idp.URL + "/o/", "aud": "fd", "sub": sub, "nonce": nonce, "preferred_username": sub,
				"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
			jws, _ := signer.Sign(claims)
			raw, _ := jws.CompactSerialize()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()

	srv := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, false)
	token := srv.SetupToken()
	do := func(method, path, body, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	cookieOf := func(w *httptest.ResponseRecorder, name string) string {
		for _, c := range w.Result().Cookies() {
			if c.Name == name {
				return name + "=" + c.Value
			}
		}
		return ""
	}
	// start -> provider -> callback, as a browser would. Returns the callback's response.
	flow := func(start, session, as string, tamper bool) *httptest.ResponseRecorder {
		t.Helper()
		w := do("GET", start, "", session)
		loc, err := url.Parse(w.Header().Get("Location"))
		if w.Code != 302 || err != nil {
			t.Fatalf("start: %d %s", w.Code, w.Body)
		}
		sub, nonce = as, loc.Query().Get("nonce")
		flowCookie := cookieOf(w, "fd_oidc")
		if tamper {
			flowCookie = "fd_oidc=someone-elses"
		}
		return do("GET", ssoCallback+"?code=c&state="+url.QueryEscape(loc.Query().Get("state")), "", flowCookie)
	}

	session := cookieOf(do("POST", "/api/auth/setup", `{"token":"`+token+`","password":"correct horse battery"}`, ""), "fd_session")
	if w := do("PUT", "/api/sso", `{"issuer":"`+idp.URL+`/o","client_id":"fd","client_secret":"s"}`, session); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := db.Setting("oidc_issuer", ""); got != idp.URL+"/o/" {
		t.Fatalf("issuer not normalised: %q", got)
	}
	if w := do("PUT", "/api/sso/password-login", `{"enabled":false}`, session); w.Code != 400 {
		t.Fatalf("passwords turned off before an account was connected: %d", w.Code)
	}

	if w := flow("/api/auth/oidc/start?link=1", session, "owner", true); db.Setting("oidc_sub", "") != "" || !strings.Contains(w.Body.String(), "another browser") {
		t.Fatalf("link accepted without the flow cookie: %s", w.Body)
	}
	if w := flow("/api/auth/oidc/start?link=1", session, "owner", false); db.Setting("oidc_sub", "") != "owner" {
		t.Fatalf("link failed: %s", w.Body)
	}

	if w := flow("/api/auth/oidc/start", "", "intruder", false); cookieOf(w, "fd_session") != "" {
		t.Fatalf("another account signed in: %s", w.Body)
	}
	w := flow("/api/auth/oidc/start", "", "owner", false)
	if cookieOf(w, "fd_session") == "" || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Fatalf("owner sign-in failed: %s", w.Body)
	}

	if w := do("PUT", "/api/sso/password-login", `{"enabled":false}`, session); w.Code != 200 {
		t.Fatalf("password off: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/auth/login", `{"password":"correct horse battery"}`, ""); w.Code != 403 {
		t.Fatalf("password sign-in still works when off: %d", w.Code)
	}
	t.Setenv("FLEETDECK_PASSWORD_LOGIN", "1")
	if w := do("POST", "/api/auth/login", `{"password":"correct horse battery"}`, ""); w.Code != 200 {
		t.Fatalf("recovery env var ignored: %d %s", w.Code, w.Body)
	}
}
