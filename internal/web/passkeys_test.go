package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

// authenticator is a software passkey: one P-256 key, a credential id and a signature counter.
type authenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	userID string
}

var b64 = base64.RawURLEncoding

const origin = "https://example.com" // httptest's host, with X-Forwarded-Proto: https

func authData(flags byte, count uint32, extra []byte) []byte {
	h := sha256.Sum256([]byte("example.com"))
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, count)
	return append(out, extra...)
}

func clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func (a *authenticator) register(t *testing.T, opts map[string]any) map[string]any {
	pk := opts["publicKey"].(map[string]any)
	a.userID = string(must1(b64.DecodeString(pk["user"].(map[string]any)["id"].(string))))
	x, y := a.key.PublicKey.X.FillBytes(make([]byte, 32)), a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose := must1(webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y}))
	cred := append(make([]byte, 16), byte(len(a.id)>>8), byte(len(a.id))) // zero AAGUID, id length
	cred = append(append(cred, a.id...), cose...)
	att := must1(webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData(0x45, 0, cred)})) // UP, UV, AT
	return map[string]any{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key", "clientExtensionResults": map[string]any{},
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", pk["challenge"].(string))), "attestationObject": b64.EncodeToString(att)}}
}

func (a *authenticator) assert(t *testing.T, opts map[string]any, count uint32) map[string]any {
	cd := clientData("webauthn.get", opts["publicKey"].(map[string]any)["challenge"].(string))
	ad := authData(0x05, count, nil) // UP, UV
	h := sha256.Sum256(cd)
	sig := must1(ecdsa.SignASN1(rand.Reader, a.key, sha256Sum(append(ad, h[:]...))))
	return map[string]any{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key", "clientExtensionResults": map[string]any{},
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig),
			"userHandle": b64.EncodeToString([]byte(a.userID))}}
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func must1[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestPasskeys(t *testing.T) {
	if err := db.OpenTest(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.DropTest)
	t.Setenv("FLEETDECK_SECRET", "")
	if err := sec.Init(); err != nil {
		t.Fatal(err)
	}
	srv := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, true)
	cookie := ""
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if sc := w.Header().Get("Set-Cookie"); strings.HasPrefix(sc, "fd_session=") {
			cookie = strings.SplitN(sc, ";", 2)[0]
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	expect := func(code, want int, m map[string]any, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: %d %v, want %d", what, code, m, want)
		}
	}
	code, m := call("POST", "/api/auth/setup", map[string]any{"token": srv.SetupToken(), "email": "ops@example.com", "name": "Ops", "password": "correct horse"})
	expect(code, 200, m, "setup")

	a := &authenticator{key: must1(ecdsa.GenerateKey(elliptic.P256(), rand.Reader)), id: []byte("credential-1")}
	code, opts := call("POST", "/api/account/passkeys/begin", map[string]any{"name": "Laptop"})
	expect(code, 200, opts, "begin registration")
	code, m = call("POST", "/api/account/passkeys/finish", map[string]any{"credential": a.register(t, opts)})
	expect(code, 200, m, "finish registration")
	_, acct := call("GET", "/api/account", nil)
	keys := acct["passkeys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["name"] != "Laptop" {
		t.Fatalf("passkeys: %v", keys)
	}

	login := func(count uint32) (int, map[string]any) {
		t.Helper()
		code, b := call("POST", "/api/auth/passkey/begin", nil)
		expect(code, 200, b, "begin login")
		return call("POST", "/api/auth/passkey/finish", map[string]any{"token": b["token"], "credential": a.assert(t, b["options"].(map[string]any), count)})
	}
	call("POST", "/api/auth/logout", nil)
	cookie = ""
	code, m = login(5)
	expect(code, 200, m, "passkey sign-in")
	code, m = call("GET", "/api/me", nil)
	expect(code, 200, m, "signed in")

	code, m = login(3) // the counter went backwards: a cloned authenticator?
	expect(code, 401, m, "cloned passkey")
	if n, _ := db.Get[int](`SELECT COUNT(*) FROM events WHERE message LIKE '%counter went backwards%'`); *n != 1 {
		t.Fatal("no audit entry for the clone warning")
	}

	code, m = call("DELETE", "/api/account/passkeys/"+keys[0].(map[string]any)["id"].(string), nil)
	expect(code, 200, m, "remove passkey")
	code, m = login(10)
	expect(code, 401, m, "removed passkey")
}
