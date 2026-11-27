package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fleetdeck/internal/admin"
	"fleetdeck/internal/db"
	"fleetdeck/internal/jobs"
	"fleetdeck/internal/sec"
)

func TestServerValidationBlocksSSHOptionInjection(t *testing.T) {
	for _, b := range []map[string]any{
		{"name": "x", "host": "-oProxyCommand=touch /tmp/pwn"},
		{"name": "x", "host": "a", "user": "-oFoo"},
		{"name": "x", "host": "a b"},
		{"name": "x", "host": "a", "tags": "ok, no spaces allowed"},
	} {
		if _, err := ValidateServer("default", b); err == nil {
			t.Errorf("accepted %v", b)
		}
	}
	got, err := ValidateServer("default", map[string]any{"name": "x", "host": "web1.example.com", "tags": "Prod, web"})
	want := &ServerFields{Name: "x", Host: "web1.example.com", User: "root", Port: 22, KeyID: nil, Tags: "prod,web", Notes: "", Paused: 0}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// One pass through the API the way the browser uses it. Nothing here needs SSH: the server is created paused.
func TestAPISmoke(t *testing.T) {
	if err := db.OpenTest(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.DropTest)
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
	code, m, _ = call("POST", "/api/auth/setup", `{"token":"`+token+`","email":"ops@example.com","name":"Ops","password":"correct horse"}`)
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
	code, m, _ = call("POST", "/api/custom-metrics", `{"name":"Backups","command":"","tag":"web"}`)
	expect(code, 400, m, "custom metric without a command")
	code, cm, _ := call("POST", "/api/custom-metrics", `{"name":"Backups","unit":"files","command":"ls /var/backups | wc -l","tag":"web"}`)
	expect(code, 200, cm, "create custom metric")
	_, _, list := call("GET", "/api/servers/"+server["id"].(string)+"/custom?range=6h", "")
	if l := list.([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "Backups" || l[0].(map[string]any)["series"].(map[string]any)["points"] == nil {
		t.Fatalf("server custom metrics: %v", list)
	}
	code, m, _ = call("PUT", "/api/custom-metrics/"+cm["id"].(string), `{"command":"ls /srv/backups | wc -l"}`)
	if code != 200 || m["tag"] != "web" || m["name"] != "Backups" || m["command"] != "ls /srv/backups | wc -l" {
		t.Fatalf("custom metric merge: %d %v", code, m)
	}
	code, crule, _ := call("POST", "/api/alerts/rules", `{"metric":"custom","custom_id":"`+cm["id"].(string)+`","threshold":5}`)
	if code != 200 || crule["custom_name"] != "Backups" || crule["custom_unit"] != "files" {
		t.Fatalf("custom rule: %d %v", code, crule)
	}
	code, m, _ = call("DELETE", "/api/custom-metrics/"+cm["id"].(string), "")
	expect(code, 200, m, "delete custom metric")
	_, alertsPage, _ := call("GET", "/api/alerts", "")
	if len(alertsPage["rules"].([]any)) != 4 || len(alertsPage["custom"].([]any)) != 0 { // 3 default rules + cpu
		t.Fatalf("custom rule not removed with its metric: %v", alertsPage)
	}
	code, variable, _ := call("POST", "/api/variables", `{"name":"REGION","value":"eu","secret":false}`)
	expect(code, 200, variable, "create variable")
	if variable["value"] != "eu" || variable["secret"] != false {
		t.Fatalf("variable: %v", variable)
	}

	_, _, list = call("GET", "/api/servers", "")
	if s := list.([]any)[0].(map[string]any); s["name"] != "web1" || s["key_id"] != key["id"] || s["spark"] == nil {
		t.Fatalf("servers: %v", list)
	}
	_, _, list = call("GET", "/api/keys", "")
	if used := list.([]any)[0].(map[string]any)["used_by"].([]any); len(used) != 1 || used[0].(map[string]any)["name"] != "web1" {
		t.Fatalf("keys: %v", list)
	}
	_, alertsPage, _ = call("GET", "/api/alerts", "")
	if alertsPage["metrics"].(map[string]any)["cpu"] == nil || len(alertsPage["rules"].([]any)) != 4 {
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

	// Prometheus: no token, then a generated one; paused servers are left out.
	raw := func(path string, hdr ...string) (int, string, string) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w.Code, w.Header().Get("Content-Type"), w.Body.String()
	}
	if code, _, _ := raw("/api/metrics"); code != 401 {
		t.Fatalf("metrics without a token: %d", code)
	}
	code, m, _ = call("POST", "/api/tenant/metrics-token", "")
	expect(code, 200, m, "metrics token")
	bearer := "Bearer " + m["token"].(string)
	if code, _, _ := raw("/api/metrics", "Authorization", "Bearer wrong"); code != 401 {
		t.Fatalf("metrics with a wrong token: %d", code)
	}
	if code, _, body := raw("/api/metrics", "Authorization", bearer); code != 200 || strings.Contains(body, "web1") {
		t.Fatalf("paused server in metrics: %d %s", code, body)
	}
	if _, err := db.Exec(`UPDATE servers SET paused = 0, last_json = ? WHERE id = ?`, `{"cpu":0.25,"disks":[{"mount":"/","size":100,"used":40,"pct":0.4}],"reboot":true,"t":1700000000000}`, server["id"]); err != nil {
		t.Fatal(err)
	}
	code, ctype, body := raw("/api/metrics", "Authorization", bearer)
	sid := server["id"].(string)
	for _, want := range []string{"# TYPE fleetdeck_up gauge\nfleetdeck_up{server=\"web1\",server_id=\"" + sid + "\"} 1\n",
		"fleetdeck_cpu_usage_ratio{server=\"web1\",server_id=\"" + sid + "\"} 0.25\n",
		"fleetdeck_filesystem_used_bytes{server=\"web1\",server_id=\"" + sid + "\",mount=\"/\"} 40\n",
		"fleetdeck_reboot_required{server=\"web1\",server_id=\"" + sid + "\"} 1\n"} {
		if code != 200 || !strings.HasPrefix(ctype, "text/plain; version=0.0.4") || !strings.Contains(body, want) {
			t.Fatalf("metrics: %d %s, missing %q in:\n%s", code, ctype, want, body)
		}
	}
	code, m, _ = call("DELETE", "/api/tenant/metrics-token", "")
	expect(code, 200, m, "turn off scraping")
	if code, _, _ := raw("/api/metrics", "Authorization", bearer); code != 401 {
		t.Fatalf("metrics after turning it off: %d", code)
	}
	if _, err := db.Exec(`UPDATE servers SET paused = 1 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metrics (server_id, t, cpu, mem) VALUES (?, ?, 0.5, NULL)`, sid, db.Now()-60e3); err != nil {
		t.Fatal(err)
	}
	if code, ctype, body := raw("/api/servers/"+sid+"/metrics.csv?range=1h", "Cookie", cookie); code != 200 || ctype != "text/csv; charset=utf-8" ||
		!strings.HasPrefix(body, "time,cpu_pct,mem_pct,") || !strings.Contains(body, "Z,50,,") {
		t.Fatalf("csv: %d %s %s", code, ctype, body)
	}

	// Lockout: the bad setup token above plus 4 wrong passwords make 5 failures; then 429 even for the right one.
	for i := 0; i < 4; i++ {
		code, m, _ = call("POST", "/api/auth/login", `{"email":"ops@example.com","password":"wrong password"}`)
		expect(code, 401, m, "wrong password")
	}
	code, m, _ = call("POST", "/api/auth/login", `{"email":"ops@example.com","password":"correct horse"}`)
	if code != 429 || m["error"] != "Too many failed attempts. Try again in 15 min." {
		t.Fatalf("lockout: %d %v", code, m)
	}

	code, m, _ = call("POST", "/api/hooks/not-a-token", "anything at all", "Content-Type", "text/plain")
	if code != 404 || m["error"] != "Not found" {
		t.Fatalf("webhook: %d %v", code, m)
	}
}

// Organizations don't see each other; inside one, roles and server assignments decide what people see and do.
func TestTenancy(t *testing.T) {
	if err := db.OpenTest(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.DropTest)
	t.Setenv("FLEETDECK_SECRET", "")
	if err := sec.Init(); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Seed(); err != nil { // the shared library
		t.Fatal(err)
	}
	srv := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, false)
	token := srv.SetupToken()
	call := func(cookie *string, method, path, body string) (int, map[string]any, any) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Cookie", *cookie)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var v any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		if sc := w.Header().Get("Set-Cookie"); strings.HasPrefix(sc, "fd_session=") {
			*cookie = strings.SplitN(sc, ";", 2)[0]
		}
		m, _ := v.(map[string]any)
		return w.Code, m, v
	}
	must := func(code, want int, m map[string]any, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: %d %v, want %d", what, code, m, want)
		}
	}
	// accept follows an invite link as a new person; returns their session.
	accept := func(link, name string) string {
		t.Helper()
		tok := link[strings.LastIndex(link, "/")+1:]
		s := ""
		code, m, _ := call(&s, "POST", "/api/invites/"+tok+"/accept", `{"name":"`+name+`","password":"long enough pw"}`)
		must(code, 200, m, "accept invite")
		if code, _, _ := call(&s, "POST", "/api/invites/"+tok+"/accept", `{"name":"x","password":"long enough pw"}`); code != 404 {
			t.Fatalf("invite used twice: %d", code)
		}
		return s
	}

	boss := ""
	code, m, _ := call(&boss, "POST", "/api/auth/setup", `{"token":"`+token+`","email":"boss@msp.example","name":"Boss","organization":"MSP"}`)
	must(code, 400, m, "setup without password")
	code, m, _ = call(&boss, "POST", "/api/auth/setup", `{"token":"`+token+`","email":"boss@msp.example","name":"Boss","organization":"MSP","password":"correct horse"}`)
	must(code, 200, m, "setup")
	code, a, _ := call(&boss, "POST", "/api/servers", `{"name":"msp-web","host":"192.0.2.1","paused":true}`)
	must(code, 200, a, "add server in MSP")

	// A customer organization with its own admin.
	code, m, _ = call(&boss, "POST", "/api/instance/tenants", `{"name":"Acme GmbH","admin_email":"it@acme.example"}`)
	must(code, 200, m, "create organization")
	if m["tenant"].(map[string]any)["slug"] != "acme-gmbh" {
		t.Fatalf("slug: %v", m)
	}
	acme := accept(m["invite_url"].(string), "Acme IT")
	_, _, list := call(&acme, "GET", "/api/servers", "")
	if len(list.([]any)) != 0 {
		t.Fatalf("Acme sees MSP's servers: %v", list)
	}
	code, m, _ = call(&acme, "GET", "/api/servers/"+a["id"].(string), "")
	must(code, 404, m, "Acme opens MSP's server")
	code, m, _ = call(&acme, "POST", "/api/alerts/rules", `{"metric":"cpu","threshold":90,"server_id":"`+a["id"].(string)+`"}`)
	must(code, 400, m, "Acme alert rule on MSP's server")
	code, m, _ = call(&acme, "GET", "/api/instance", "")
	must(code, 403, m, "Acme admin is no super admin")
	_, _, list = call(&acme, "GET", "/api/scripts", "")
	if sc := list.([]any); len(sc) == 0 || sc[0].(map[string]any)["shared"] != true {
		t.Fatalf("shared library missing: %v", sc)
	}
	code, m, _ = call(&acme, "PUT", "/api/scripts/update", `{"name":"mine now","body":"true"}`)
	must(code, 403, m, "Acme edits a shared script")
	code, m, _ = call(&acme, "POST", "/api/scripts/update/copy", "")
	if code != 200 || m["shared"] != false || m["name"] != "Update packages (copy)" {
		t.Fatalf("copy shared script: %d %v", code, m)
	}

	// Inside MSP: an operator sees only the servers assigned to them.
	code, m, _ = call(&boss, "POST", "/api/members/invites", `{"email":"op@msp.example","role":"operator"}`)
	must(code, 200, m, "invite operator")
	op := accept(m["url"].(string), "Op")
	_, _, list = call(&op, "GET", "/api/servers", "")
	if len(list.([]any)) != 0 {
		t.Fatalf("operator sees unassigned servers: %v", list)
	}
	_, mem, _ := call(&boss, "GET", "/api/members", "")
	var opID string
	for _, x := range mem["members"].([]any) {
		if u := x.(map[string]any); u["email"] == "op@msp.example" {
			opID = u["id"].(string)
		}
	}
	code, m, _ = call(&boss, "PUT", "/api/servers/"+a["id"].(string)+"/members", `{"user_ids":["`+opID+`"]}`)
	must(code, 200, m, "assign server")
	_, _, list = call(&op, "GET", "/api/servers", "")
	if len(list.([]any)) != 1 {
		t.Fatalf("operator doesn't see the assigned server: %v", list)
	}
	code, m, _ = call(&op, "PUT", "/api/servers/"+a["id"].(string), `{"name":"renamed"}`)
	must(code, 403, m, "operator edits a server")
	code, m, _ = call(&op, "GET", "/api/variables", "")
	must(code, 403, m, "operator opens variables")
	code, m, _ = call(&op, "POST", "/api/servers", `{"name":"x","host":"192.0.2.9","paused":true}`)
	must(code, 403, m, "operator adds a server")

	code, m, _ = call(&boss, "PUT", "/api/members/"+opID, `{"role":"viewer"}`)
	must(code, 200, m, "make viewer")
	code, m, _ = call(&op, "POST", "/api/jobs", `{"body":"id","targets":{"all":true}}`)
	must(code, 403, m, "viewer starts a job")

	// The last admin stays; the super admin can step into any organization.
	_, me, _ := call(&boss, "GET", "/api/me", "")
	bossID := me["user"].(map[string]any)["id"].(string)
	code, m, _ = call(&boss, "PUT", "/api/members/"+bossID, `{"role":"viewer"}`)
	must(code, 409, m, "demote the last admin")
	var acmeID string
	for _, x := range me["tenants"].([]any) {
		if o := x.(map[string]any); o["slug"] == "acme-gmbh" {
			acmeID = o["id"].(string)
		}
	}
	code, m, _ = call(&boss, "POST", "/api/me/tenant", `{"id":"`+acmeID+`"}`)
	must(code, 200, m, "super admin enters Acme")
	_, me, _ = call(&boss, "GET", "/api/me", "")
	if me["role"] != "admin" || me["tenant"].(map[string]any)["id"] != acmeID {
		t.Fatalf("after switch: %v", me)
	}
	code, m, _ = call(&acme, "POST", "/api/me/tenant", `{"id":"default"}`)
	must(code, 404, m, "Acme admin enters MSP")

	// A personal organization: the viewer's own servers, which nobody else sees, super admins included.
	code, m, _ = call(&op, "POST", "/api/me/personal", "")
	must(code, 200, m, "create personal organization")
	code, m, _ = call(&op, "POST", "/api/me/personal", "")
	must(code, 409, m, "second personal organization")
	_, me, _ = call(&op, "GET", "/api/me", "")
	mine := me["tenant"].(map[string]any)
	if me["role"] != "admin" || mine["personal"] != true || len(me["tenants"].([]any)) != 2 {
		t.Fatalf("personal organization: %v", me)
	}
	code, p, _ := call(&op, "POST", "/api/servers", `{"name":"home-nas","host":"192.0.2.50","paused":true}`)
	must(code, 200, p, "add server in personal organization")
	code, m, _ = call(&op, "POST", "/api/members/invites", `{"email":"friend@example.com","role":"viewer"}`)
	must(code, 409, m, "invite into personal organization")
	code, m, _ = call(&op, "PUT", "/api/tenant/sso", `{"issuer":"https://idp.example.com","client_id":"x","secret":"y"}`)
	must(code, 409, m, "single sign-on for personal organization")
	_, me, _ = call(&boss, "GET", "/api/me", "")
	for _, x := range me["tenants"].([]any) {
		if x.(map[string]any)["id"] == mine["id"] {
			t.Fatalf("super admin lists the personal organization: %v", me["tenants"])
		}
	}
	code, m, _ = call(&boss, "POST", "/api/me/tenant", `{"id":"`+mine["id"].(string)+`"}`)
	must(code, 404, m, "super admin enters a personal organization")
	code, m, _ = call(&boss, "GET", "/api/servers/"+p["id"].(string), "")
	must(code, 404, m, "super admin opens a personal server")
	code, m, _ = call(&op, "POST", "/api/me/tenant", `{"id":"default"}`)
	must(code, 200, m, "back to MSP")
	_, me, _ = call(&op, "GET", "/api/me", "")
	if me["role"] != "viewer" {
		t.Fatalf("role in MSP after going personal: %v", me)
	}
	code, m, _ = call(&boss, "DELETE", "/api/instance/users/"+opID, "")
	must(code, 200, m, "delete an account with a personal organization")
	if n, err := db.Get[int](`SELECT COUNT(*) FROM tenants WHERE id = ?`, mine["id"]); err != nil || *n != 0 {
		t.Fatalf("personal organization outlived its person: %v %v", n, err)
	}
}

func TestLogSearch(t *testing.T) {
	if err := db.OpenTest(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.DropTest)
	t.Setenv("FLEETDECK_SECRET", "")
	if err := sec.Init(); err != nil {
		t.Fatal(err)
	}
	srv := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, false)
	cookie := ""
	call := func(path string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		if strings.HasPrefix(path, "/api/auth/setup") {
			r = httptest.NewRequest("POST", "/api/auth/setup", strings.NewReader(`{"token":"`+srv.SetupToken()+`","email":"a@b.example","name":"A","password":"correct horse"}`))
			r.Header.Set("Content-Type", "application/json")
		}
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
	if code, m := call("/api/auth/setup"); code != 200 {
		t.Fatal(code, m)
	}
	if _, err := db.Exec(`INSERT INTO servers (id, tenant_id, name, host, tags, created_at)
    SELECT id, (SELECT tenant_id FROM memberships LIMIT 1), id, 'h', tags, 0 FROM (VALUES ('web1', 'web'), ('web2', 'web'), ('db1', 'db')) v(id, tags)`); err != nil {
		t.Fatal(err)
	}
	journal = func(_ context.Context, s *db.Server, o admin.LogsOpts) ([]admin.LogEntry, error) {
		switch s.Name {
		case "web2":
			return nil, errors.New("connection refused")
		case "web1":
			return []admin.LogEntry{{T: 10, Msg: "a"}, {T: 30, Msg: "c"}}, nil
		}
		return []admin.LogEntry{{T: 20, Msg: "b"}}, nil
	}
	t.Cleanup(func() { journal = admin.Logs })

	code, m := call("/api/logs?scope=all&since=1h")
	if code != 200 {
		t.Fatal(code, m)
	}
	var got []string
	for _, e := range m["entries"].([]any) {
		e := e.(map[string]any)
		got = append(got, e["server"].(string)+":"+e["msg"].(string))
	}
	errs := m["errors"].([]any)
	if strings.Join(got, ",") != "web1:a,db1:b,web1:c" || len(errs) != 1 || errs[0].(map[string]any)["server"] != "web2" {
		t.Fatalf("%v %v", got, errs)
	}
	if _, m := call("/api/logs?scope=tag:db"); len(m["entries"].([]any)) != 1 {
		t.Fatalf("tag scope: %v", m)
	}
	if code, m := call("/api/logs?scope=all&priority=9"); code != 400 {
		t.Fatalf("bad filter: %d %v", code, m)
	}
}
