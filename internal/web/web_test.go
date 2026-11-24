package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

func TestServerValidationBlocksSSHOptionInjection(t *testing.T) {
	for _, b := range []map[string]any{
		{"name": "x", "host": "-oProxyCommand=touch /tmp/pwn"},
		{"name": "x", "host": "a", "user": "-oFoo"},
		{"name": "x", "host": "a b"},
		{"name": "x", "host": "a", "tags": "ok, no spaces allowed"},
	} {
		if _, err := ValidateServer(b); err == nil {
			t.Errorf("accepted %v", b)
		}
	}
	got, err := ValidateServer(map[string]any{"name": "x", "host": "web1.example.com", "tags": "Prod, web"})
	want := &ServerFields{Name: "x", Host: "web1.example.com", User: "root", Port: 22, KeyID: nil, Tags: "prod,web", Notes: "", Paused: 0}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// One pass through the API the way the browser uses it. Nothing here needs SSH: the server is created paused.
func TestAPISmoke(t *testing.T) {
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEETDECK_SECRET", "")
	if err := sec.Init(); err != nil {
		t.Fatal(err)
	}
	srv := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, false)
	token := srv.SetupToken()
	cookie := ""
	call := func(method, path, body string, hdr ...string) (int, map[string]any, any) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var v any
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatalf("%s %s: %v: %s", method, path, err, w.Body)
			}
		}
		if sc := w.Header().Get("Set-Cookie"); sc != "" {
			cookie = strings.SplitN(sc, ";", 2)[0]
		}
		m, _ := v.(map[string]any)
		return w.Code, m, v
	}
	expect := func(code, want int, m map[string]any, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: %d %v, want %d", what, code, m, want)
		}
	}

	code, m, _ := call("GET", "/", "")
	expect(code, 200, m, "index.html")
	code, m, _ = call("GET", "/api/auth/state", "")
	if code != 200 || m["setup"] != true || m["authed"] != false {
		t.Fatalf("state: %v", m)
	}
	code, m, _ = call("POST", "/api/auth/setup", `{"token":"nope","password":"correct horse"}`)
	expect(code, 401, m, "bad setup token")
	code, m, _ = call("POST", "/api/auth/setup", `{"token":"`+token+`","password":"correct horse"}`)
	expect(code, 200, m, "setup")
	if !strings.HasPrefix(cookie, "fd_session=") {
		t.Fatal("no session cookie")
	}
	code, m, _ = call("GET", "/api/auth/state", "")
	if m["authed"] != true || m["setup"] != false {
		t.Fatalf("state after setup: %v", m)
	}

	// Without the cookie, cross-origin, non-JSON.
	saved := cookie
	cookie = ""
	code, m, _ = call("GET", "/api/servers", "")
	expect(code, 401, m, "no cookie")
	cookie = saved
	code, m, _ = call("POST", "/api/keys", `{"name":"k"}`, "Origin", "http://evil.example")
	expect(code, 403, m, "cross-origin")
	code, m, _ = call("POST", "/api/keys", `name=k`, "Content-Type", "application/x-www-form-urlencoded")
	expect(code, 415, m, "form post")
	code, m, _ = call("POST", "/api/keys", `{"name":`, "Origin", "http://example.com")
	expect(code, 400, m, "invalid JSON")

	code, key, _ := call("POST", "/api/keys", `{"name":"deploy","generate":true}`)
	expect(code, 200, key, "generate key")
	if !strings.HasPrefix(key["public_key"].(string), "ssh-ed25519 ") || key["has_passphrase"] != false {
		t.Fatalf("key: %v", key)
	}
	code, server, _ := call("POST", "/api/servers", `{"name":"web1","host":"192.0.2.10","port":"2222","tags":"Prod, web","paused":true,"key_id":"`+key["id"].(string)+`"}`)
	expect(code, 200, server, "add server")
	if server["status"] != "paused" || server["port"] != 2222.0 || !reflect.DeepEqual(server["tags"], []any{"prod", "web"}) || server["has_sudo_password"] != false {
		t.Fatalf("server: %v", server)
	}
	code, script, _ := call("POST", "/api/scripts", `{"name":"Hello","body":"echo $WHO","as_root":0,"params":[{"name":"WHO","default":"world"}]}`)
	expect(code, 200, script, "create script")
	if script["as_root"] != false || script["kind"] != "shell" || script["params"].([]any)[0].(map[string]any)["label"] != "WHO" {
		t.Fatalf("script: %v", script)
	}
	code, sched, _ := call("POST", "/api/schedules", `{"name":"nightly","script_id":"`+script["id"].(string)+`","cron":"0 3 * * *","targets":{"tags":["prod"]}}`)
	expect(code, 200, sched, "create schedule")
	if sched["script_name"] != "Hello" || sched["enabled"] != true || sched["next"] == nil {
		t.Fatalf("schedule: %v", sched)
	}
	code, m, _ = call("PUT", "/api/schedules/"+sched["id"].(string), `{"enabled":false}`)
	if code != 200 || m["enabled"] != false || m["cron"] != "0 3 * * *" {
		t.Fatalf("schedule merge: %d %v", code, m)
	}
	runAt := time.Now().Add(3 * time.Hour).Truncate(time.Minute)
	code, once, _ := call("POST", "/api/schedules", fmt.Sprintf(`{"name":"later","script_id":"%s","run_at":%d,"targets":{"servers":["%s"]}}`, script["id"], runAt.UnixMilli(), server["id"]))
	if code != 200 || once["once"] != true || once["next"] != float64(runAt.UnixMilli()) {
		t.Fatalf("one-off schedule: %d %v", code, once)
	}
	code, m, _ = call("PUT", "/api/schedules/"+once["id"].(string), `{"enabled":false}`)
	if code != 200 || m["once"] != true {
		t.Fatalf("one-off merge: %d %v", code, m)
	}
	code, typed, _ := call("POST", "/api/scripts", `{"name":"Typed","body":"echo","params":[{"name":"ENV","type":"choice","options":["dev"," prod ","dev"]},{"name":"DRY","type":"checkbox","default":"yes"}]}`)
	expect(code, 200, typed, "create typed script")
	if p := typed["params"].([]any); p[0].(map[string]any)["default"] != "dev" || len(p[0].(map[string]any)["options"].([]any)) != 2 || p[1].(map[string]any)["default"] != "false" {
		t.Fatalf("typed params: %v", p)
	}
	code, m, _ = call("POST", "/api/jobs", `{"script_id":"`+typed["id"].(string)+`","params":{"ENV":"staging"},"targets":{"all":true}}`)
	if code != 400 || m["error"] != `"ENV" must be one of: dev, prod` {
		t.Fatalf("bad choice: %d %v", code, m)
	}
	code, m, _ = call("POST", "/api/schedules", `{"name":"x","script_id":"`+typed["id"].(string)+`","cron":"0 3 * * *","params":{"DRY":"maybe"},"targets":{"all":true}}`)
	expect(code, 400, m, "bad checkbox on a schedule")
	code, rule, _ := call("POST", "/api/alerts/rules", `{"metric":"cpu","threshold":"85","minutes":5,"server_id":"`+server["id"].(string)+`"}`)
	expect(code, 200, rule, "create rule")
	if rule["server_name"] != "web1" || rule["enabled"] != true || rule["threshold"] != 85.0 {
		t.Fatalf("rule: %v", rule)
	}
	code, variable, _ := call("POST", "/api/variables", `{"name":"REGION","value":"eu","secret":false}`)
	expect(code, 200, variable, "create variable")
	if variable["value"] != "eu" || variable["secret"] != false {
		t.Fatalf("variable: %v", variable)
	}

	_, _, list := call("GET", "/api/servers", "")
	if s := list.([]any)[0].(map[string]any); s["name"] != "web1" || s["key_id"] != key["id"] || s["spark"] == nil {
		t.Fatalf("servers: %v", list)
	}
	_, _, list = call("GET", "/api/keys", "")
	if used := list.([]any)[0].(map[string]any)["used_by"].([]any); len(used) != 1 || used[0].(map[string]any)["name"] != "web1" {
		t.Fatalf("keys: %v", list)
	}
	_, alertsPage, _ := call("GET", "/api/alerts", "")
	if alertsPage["metrics"].(map[string]any)["cpu"] == nil || len(alertsPage["rules"].([]any)) != 1 {
		t.Fatalf("alerts: %v", alertsPage)
	}
	_, _, list = call("GET", "/api/variables", "")
	if len(list.([]any)) != 1 {
		t.Fatalf("variables: %v", list)
	}
	code, m, _ = call("DELETE", "/api/keys/"+key["id"].(string), "")
	if code != 409 || m["error"] != "Key is used by: web1" {
		t.Fatalf("delete used key: %d %v", code, m)
	}

	// Lockout: the bad setup token above plus 4 wrong passwords make 5 failures; then 429 even for the right one.
	for i := 0; i < 4; i++ {
		code, m, _ = call("POST", "/api/auth/login", `{"password":"wrong password"}`)
		expect(code, 401, m, "wrong password")
	}
	code, m, _ = call("POST", "/api/auth/login", `{"password":"correct horse"}`)
	if code != 429 || m["error"] != "Too many failed attempts. Try again in 15 min." {
		t.Fatalf("lockout: %d %v", code, m)
	}

	code, m, _ = call("POST", "/api/hooks/not-a-token", "anything at all", "Content-Type", "text/plain")
	if code != 404 || m["error"] != "Not found" {
		t.Fatalf("webhook: %d %v", code, m)
	}
}
