package alerts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/probe"
	"fleetdeck/internal/sec"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.OpenTest(dir); err != nil {
		panic(err)
	}
	if err := sec.Init(); err != nil {
		panic(err)
	}
	code := m.Run()
	db.DropTest()
	os.RemoveAll(dir)
	os.Exit(code)
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	must(t, err)
}

func srv(t *testing.T, id string) *db.Server {
	t.Helper()
	s, err := db.GetServer(id)
	must(t, err)
	return s
}

func message(t *testing.T, rule string) string {
	t.Helper()
	m, err := db.Get[string](`SELECT message FROM alert_state WHERE rule_id = ?`, rule)
	must(t, err)
	if m == nil {
		t.Fatalf("rule %s is not firing", rule)
	}
	return *m
}

func TestTagScopedRules(t *testing.T) {
	exec(t, `INSERT INTO servers (id, name, host, tags, created_at) VALUES ('s1', 'web1', 'h', 'prod,web', 0), ('s2', 'db1', 'h', 'db', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, tag, created_at) VALUES ('r1', 'updates', 0, 'web', 0)`)
	firing := func() []string {
		ids, err := db.Select[string](`SELECT server_id FROM alert_state`)
		must(t, err)
		return ids
	}
	three := 3.0
	snap := &probe.Snapshot{Updates: &three} // JS passed the partial snapshot { updates: 3 }
	Evaluate(srv(t, "s1"), snap, nil)
	Evaluate(srv(t, "s2"), snap, nil)
	eq(t, firing(), []string{"s1"})
	exec(t, `UPDATE servers SET tags = 'prod' WHERE id = 's1'`) // tag removed -> alert dropped
	Evaluate(srv(t, "s1"), snap, nil)
	eq(t, firing(), []string{})
	if _, err := ValidateRule("default", map[string]any{"metric": "cpu", "tag": "web", "server_id": "s1"}); err == nil {
		t.Fatal("server and tag together must be rejected")
	}
}

func TestSmartAlert(t *testing.T) {
	// Same probe output as the probe package's S.M.A.R.T. test.
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	type M = map[string]any
	sata := M{"smart_status": M{"passed": true}, "ata_smart_attributes": M{"table": []M{{"id": 5, "raw": M{"value": 8}}}}}
	nvme := M{"smart_status": M{"passed": true}, "nvme_smart_health_information_log": M{"percentage_used": 3}}
	pending := M{"smart_status": M{"passed": true}, "ata_smart_attributes": M{"table": []M{{"id": 197, "raw": M{"value": 2}}}}}
	s := probe.Parse(strings.Join([]string{"auto_updates=on", "smart=ok", "smartdev=/dev/sda " + j(sata), "smartdev=/dev/nvme0 " + j(nvme),
		"smartdev=/dev/sdb " + j(pending), `smartdev=/dev/sdc {"smartctl":{"exit_status":2}}`, "smartdev=/dev/sdd {broken"}, "\n"))

	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('s9', 'nas', 'h', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('r9', 'smart', 0, 's9', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('r9r', 'smart_realloc', 5, 's9', 0), ('r9w', 'smart_wear', 2, 's9', 0), ('r9t', 'smart_temp', 55, 's9', 0)`)
	Evaluate(srv(t, "s9"), s, nil)
	if m := message(t, "r9"); !strings.Contains(m, "S.M.A.R.T. problems is 1") {
		t.Fatal(m)
	}
	eq(t, message(t, "r9r"), "nas: Reallocated sectors (worst disk) is 8 (threshold 5)")
	eq(t, message(t, "r9w"), "nas: SSD wear (most worn disk) is 3% (threshold 2%)")
	n, err := db.Get[int](`SELECT COUNT(*) FROM alert_state WHERE rule_id = 'r9t'`) // no disk reports a temperature
	must(t, err)
	eq(t, *n, 0)
}

func TestContainerAndNetworkAlerts(t *testing.T) {
	ps := func(name, state, status string) string {
		b, _ := json.Marshal(map[string]string{"Names": name, "Image": "img", "State": state, "Status": status})
		return "ctr=" + string(b)
	}
	s := probe.Parse(strings.Join([]string{"docker=2 3", ps("web", "running", "Up 2 hours (healthy)"), ps("db", "running", "Up 1 hour (unhealthy)"),
		ps("job", "restarting", "Restarting (1) 3 seconds ago"), "if=eth0 5000 7000", "net=5000 7000"}, "\n"))
	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('n1', 'edge', 'h', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('rn', 'net', 50, 'n1', 0), ('ru', 'unhealthy', 0, 'n1', 0)`)
	rx, tx := 40e6, 20e6
	s.Rx, s.Tx = &rx, &tx
	Evaluate(srv(t, "n1"), s, nil)
	if m := message(t, "rn"); !strings.Contains(m, "is 60 MB/s") {
		t.Fatal(m)
	}
	message(t, "ru")
}

func TestQuietHours(t *testing.T) {
	night, err := ValidateMute("default", map[string]any{"kind": "daily", "start": "22:00", "end": "06:30"})
	must(t, err)
	eq(t, []int64{night.StartAt, night.EndAt}, []int64{1320, 390})
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	eq(t, InWindow(night, at(23, 0)), true)
	eq(t, InWindow(night, at(6, 29)), true)
	eq(t, InWindow(night, at(6, 30)), false)
	day, err := ValidateMute("default", map[string]any{"kind": "daily", "start": "09:00", "end": "17:00"})
	must(t, err)
	eq(t, InWindow(day, at(18, 0)), false)
	now := float64(db.Now())
	for _, b := range []map[string]any{{"start": "25:00", "end": "01:00"}, {"start": "10:00", "end": "10:00"}, {"kind": "once", "start": 1.0, "end": 2.0},
		{"kind": "once", "start": now + 1e6, "end": now}} {
		if _, err := ValidateMute("default", b); err == nil {
			t.Fatalf("accepted %v", b)
		}
	}

	exec(t, `INSERT INTO servers (id, name, host, tags, created_at) VALUES ('q1', 'quiet', 'h', 'lab', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('rq', 'failed', 0, 'q1', 0)`)
	exec(t, `INSERT INTO mute_windows (id, tag, kind, start_at, end_at, created_at) VALUES ('m1', 'lab', 'once', ?, ?, 0)`, db.Now()-1000, db.Now()+3600e3)
	s := srv(t, "q1")
	w, err := QuietFor(s, time.Now())
	must(t, err)
	if w == nil || w.ID != "m1" {
		t.Fatalf("quietFor = %v", w)
	}
	notified := func() int {
		n, err := db.Get[int](`SELECT notified FROM alert_state WHERE rule_id = 'rq'`)
		must(t, err)
		return *n
	}
	snap := &probe.Snapshot{Failed: 2}
	Evaluate(s, snap, nil)
	eq(t, notified(), 0)                                // fired, not sent
	exec(t, `DELETE FROM mute_windows WHERE id = 'm1'`) // window over, still firing -> sent now
	Evaluate(s, snap, nil)
	eq(t, notified(), 1)
}

// Not in test.js: the UI iterates these objects for its pickers, so order and shape must match the JS.
func TestMetricsAndTypesJSON(t *testing.T) {
	b, err := json.Marshal(Metrics)
	must(t, err)
	eq(t, string(b), `{"down":{"label":"Unreachable","unit":"min","hint":"Fires when the server cannot be reached for N minutes"},"cpu":{"label":"CPU usage","unit":"%","col":"cpu","scale":100,"def":90},"mem":{"label":"Memory usage","unit":"%","col":"mem","scale":100,"def":90},"swap":{"label":"Swap usage","unit":"%","col":"swap","scale":100,"def":50},"disk":{"label":"Disk usage (fullest mount)","unit":"%","def":90},"inodes":{"label":"Inode usage (fullest mount)","unit":"%","def":90},"load":{"label":"Load per CPU core","unit":"","col":"load1","scale":1,"def":1.5},"temp":{"label":"Temperature","unit":"°C","col":"temp","scale":1,"def":80},"updates":{"label":"Pending updates","unit":"","def":0},"security":{"label":"Pending security updates","unit":"","def":0},"failed":{"label":"Failed systemd units","unit":"","def":0},"reboot":{"label":"Reboot required","unit":""},"smart":{"label":"Disks with S.M.A.R.T. problems","unit":"","def":0},"smart_realloc":{"label":"Reallocated sectors (worst disk)","unit":"","hint":"A rising count means the disk is wearing out; a few on an old disk can be stable","def":10},"smart_wear":{"label":"SSD wear (most worn disk)","unit":"%","hint":"NVMe: share of the rated write endurance used","def":80},"smart_temp":{"label":"Disk temperature (hottest disk)","unit":"°C","def":55},"unhealthy":{"label":"Unhealthy or restarting containers","unit":"","def":0},"net":{"label":"Network traffic (in + out)","unit":" MB/s","col":"rx + tx","scale":0.000001,"def":50},"gpu":{"label":"GPU usage (busiest GPU)","unit":"%","col":"gpu","scale":100,"def":90},"custom":{"label":"Custom metric","unit":"","hint":"One of your custom metrics, from a server's Metrics panel"},"check":{"label":"Endpoint check failing","unit":"min","hint":"Fires when one of the server's endpoint checks (Network tab) fails for N minutes"},"process":{"label":"Process not running","unit":"","hint":"Fires when no process with this name runs on the server"},"tls_days":{"label":"TLS certificate days left","unit":" days","hint":"The soonest-expiring certificate of the server's HTTPS checks","def":14,"below":true}}`)
	b, err = json.Marshal(Types)
	must(t, err)
	eq(t, string(b), `{"webhook":{"label":"Webhook (JSON POST)","fields":[{"key":"url","label":"URL","secret":true}]},"ntfy":{"label":"ntfy","fields":[{"key":"url","label":"Topic URL (https://ntfy.sh/my-topic)"},{"key":"token","label":"Access token (optional)","secret":true,"optional":true}]},"discord":{"label":"Discord","fields":[{"key":"url","label":"Webhook URL","secret":true}]},"slack":{"label":"Slack","fields":[{"key":"url","label":"Incoming webhook URL","secret":true}]},"telegram":{"label":"Telegram","fields":[{"key":"token","label":"Bot token","secret":true},{"key":"chat_id","label":"Chat ID"}]},"pushover":{"label":"Pushover","fields":[{"key":"token","label":"Application API token","secret":true},{"key":"user","label":"User or group key","secret":true},{"key":"device","label":"Device (optional, all devices if empty)","optional":true}]},"gotify":{"label":"Gotify","fields":[{"key":"url","label":"Server URL"},{"key":"token","label":"App token","secret":true}]}}`)
	eq(t, []string{fmtNum(60), fmtNum(12.25), fmtNum(0.123), fmtNum(1.5)}, []string{"60", "12.3", "0.1", "1.5"})
}

// Not in test.js: secrets masked in the view and kept on edit when blank; ntfy payload and failure text.
func TestNotifiers(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		if got.body["title"] == "boom" {
			http.Error(w, "nope", 500)
		}
	}))
	defer hs.Close()

	in, err := ValidateNotifier(map[string]any{"type": "ntfy", "name": " phone ", "config": map[string]any{"url": hs.URL + "/alerts/my-topic", "token": "tk"}}, nil)
	must(t, err)
	n := &Notifier{ID: "x", Name: in.Name, Type: in.Type, ConfigEnc: EncryptConfig(in.Config), Enabled: in.Enabled}
	v, err := NotifierView(n)
	must(t, err)
	eq(t, v.Config, map[string]string{"url": hs.URL + "/alerts/my-topic", "token": "••••••"})
	kept, err := ValidateNotifier(map[string]any{"type": "ntfy", "name": "phone", "config": map[string]any{"url": hs.URL + "/t", "token": ""}}, n)
	must(t, err)
	eq(t, kept.Config["token"], "tk")
	_, err = ValidateNotifier(map[string]any{"type": "gotify", "name": "g", "config": map[string]any{"url": "ftp://x", "token": "t"}}, nil)
	eq(t, err.Error(), "Server URL must start with http:// or https://")

	must(t, SendOne(n, "hi", "msg", "error"))
	eq(t, []any{got.path, got.auth, got.body["topic"], got.body["priority"]}, []any{"/alerts/", "Bearer tk", "my-topic", 4.0})
	eq(t, SendOne(n, "boom", "msg", "info").Error(), "HTTP 500 nope\n")
}

func TestMountRules(t *testing.T) {
	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('m1', 'nas', 'h', 0)`)
	exec(t, `INSERT INTO alert_rules (id, server_id, metric, threshold, mount, created_at) VALUES
	  ('rd', 'm1', 'disk', 80, '/data', 0), ('ri', 'm1', 'inodes', 90, NULL, 0), ('rg', 'm1', 'disk', 80, '/gone', 0)`)
	f := func(v float64) *float64 { return &v }
	// "/" is fuller than /data but under its own threshold; the /data rule must look at /data only.
	snap := &probe.Snapshot{Disks: []probe.Disk{{Mount: "/", Pct: 0.85, InodePct: f(0.95)}, {Mount: "/data", Pct: 0.82}}}
	Evaluate(srv(t, "m1"), snap, nil)
	eq(t, message(t, "rd"), "nas: Disk usage on /data is 82% (threshold 80%)")
	eq(t, message(t, "ri"), "nas: Inode usage (fullest mount) is 95% (threshold 90%)")
	gone, err := db.Get[int](`SELECT 1 FROM alert_state WHERE rule_id = 'rg'`)
	must(t, err)
	eq(t, gone, (*int)(nil)) // missing mount never fires

	snap.Disks[1].Pct = 0.5
	Evaluate(srv(t, "m1"), snap, nil)
	n, err := db.Get[int](`SELECT COUNT(*) FROM alert_state WHERE rule_id = 'rd'`)
	must(t, err)
	eq(t, *n, 0)

	r, err := ValidateRule("default", map[string]any{"metric": "disk", "threshold": 80.0, "mount": " /data "})
	must(t, err)
	eq(t, *r.Mount, "/data")
	r, err = ValidateRule("default", map[string]any{"metric": "cpu", "threshold": 80.0, "mount": "/data"})
	must(t, err)
	eq(t, r.Mount, (*string)(nil)) // ignored for other metrics
	if _, err := ValidateRule("default", map[string]any{"metric": "disk", "threshold": 80.0, "mount": "data"}); err == nil {
		t.Fatal("relative mount accepted")
	}
}

func TestGPUAlert(t *testing.T) {
	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('g1', 'ml', 'h', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('rg1', 'gpu', 90, 'g1', 0)`)
	Evaluate(srv(t, "g1"), probe.Parse("nv=20, 1, 2, 40, 50, A\nnv=95, 1, 2, 40, 50, B"), nil)
	eq(t, message(t, "rg1"), "ml: GPU usage (busiest GPU) is 95% (threshold 90%)")
	Evaluate(srv(t, "g1"), probe.Parse("hostname=ml"), nil) // GPU gone: no value, resolves
	n, err := db.Get[int](`SELECT COUNT(*) FROM alert_state WHERE rule_id = 'rg1'`)
	must(t, err)
	eq(t, *n, 0)
}

func TestCustomAlert(t *testing.T) {
	exec(t, `INSERT INTO servers (id, name, host, tags, created_at) VALUES ('c1', 'nas', 'h', 'backup', 0)`)
	exec(t, `INSERT INTO custom_metrics (id, name, unit, command, tag, created_at) VALUES ('cb', 'Hours since backup', 'h', 'x', 'backup', 0)`)
	if _, err := ValidateRule("default", map[string]any{"metric": "custom", "threshold": 26.0}); err == nil {
		t.Fatal("custom rule without a metric accepted")
	}
	r, err := ValidateRule("default", map[string]any{"metric": "custom", "threshold": 26.0, "custom_id": "cb", "tag": "backup"})
	must(t, err)
	eq(t, *r.CustomID, "cb")
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, tag, custom_id, created_at) VALUES ('rc', 'custom', 26, 'backup', 'cb', 0)`)
	Evaluate(srv(t, "c1"), probe.Parse("custom.cb=30"), nil)
	eq(t, message(t, "rc"), "nas: Hours since backup is 30 h (threshold 26 h)")

	// Averaged over minutes: from the stored values, not the snapshot.
	exec(t, `INSERT INTO custom_values (server_id, metric_id, t, v) VALUES ('c1', 'cb', ?, 10)`, db.Now())
	exec(t, `UPDATE alert_rules SET minutes = 5 WHERE id = 'rc'`)
	Evaluate(srv(t, "c1"), probe.Parse("custom.cb=30"), nil)
	n, err := db.Get[int](`SELECT COUNT(*) FROM alert_state WHERE rule_id = 'rc'`)
	must(t, err)
	eq(t, *n, 0)

	exec(t, `DELETE FROM custom_metrics WHERE id = 'cb'`) // the rule goes with it
	n, err = db.Get[int](`SELECT COUNT(*) FROM alert_rules WHERE id = 'rc'`)
	must(t, err)
	eq(t, *n, 0)

	c, err := ValidateCustom("default", map[string]any{"name": " Backups ", "unit": "files", "command": " ls /var/backups | wc -l ", "tag": "Backup"})
	must(t, err)
	eq(t, []any{c.Name, c.Unit, c.Command, *c.Tag, c.ServerID}, []any{"Backups", "files", "ls /var/backups | wc -l", "backup", (*string)(nil)})
	if _, err := ValidateCustom("default", map[string]any{"name": "x"}); err == nil {
		t.Fatal("empty command accepted")
	}
	eq(t, []string{UnitSuffix("%"), UnitSuffix("°C"), UnitSuffix("GB"), UnitSuffix("")}, []string{"%", "°C", " GB", ""})
}

func TestEndpointCheckAlerts(t *testing.T) {
	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('e1', 'site', 'h', 0)`)
	exec(t, `INSERT INTO checks (id, server_id, name, kind, target, created_at) VALUES ('c1', 'e1', 'shop', 'http', 'https://shop.example', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, threshold, minutes, server_id, created_at) VALUES ('rc', 'check', 0, 3, 'e1', 0), ('rt', 'tls_days', 14, 0, 'e1', 0)`)
	firing := func(rule string) bool {
		n, err := db.Get[int](`SELECT 1 FROM alert_state WHERE rule_id = ?`, rule)
		must(t, err)
		return n != nil
	}
	set := func(failingFor time.Duration, days float64) {
		var since any
		if failingFor > 0 {
			since = db.Now() - failingFor.Milliseconds()
		}
		exec(t, `UPDATE checks SET failing_since = ?, last_error = 'connection refused', tls_days = ? WHERE id = 'c1'`, since, days)
	}
	set(time.Minute, 30)
	Evaluate(srv(t, "e1"), &probe.Snapshot{}, nil)
	if firing("rc") || firing("rt") {
		t.Fatal("fired too early")
	}
	set(4*time.Minute, 10)
	Evaluate(srv(t, "e1"), nil, errors.New("ssh down")) // checks don't need the server's SSH
	if m := message(t, "rc"); !strings.Contains(m, "shop (connection refused, for 4 min)") {
		t.Fatal(m)
	}
	if m := message(t, "rt"); !strings.Contains(m, "expires in 10 days (alert below 14 days)") {
		t.Fatal(m)
	}
	set(0, 60) // the service is back and the certificate renewed
	Evaluate(srv(t, "e1"), &probe.Snapshot{}, nil)
	if firing("rc") || firing("rt") {
		t.Fatal("did not resolve")
	}
}

func TestProcessAlert(t *testing.T) {
	snap := probe.Parse("proc=nginx\nproc=systemd-resolve\nproc=sshd")
	eq(t, snap.Processes, []string{"nginx", "systemd-resolve", "sshd"})
	eq(t, ProcessRunning("systemd-resolved", snap.Processes), true) // comm is cut to 15 characters
	eq(t, ProcessRunning("ngin", snap.Processes), false)

	exec(t, `INSERT INTO servers (id, name, host, created_at) VALUES ('p1', 'web9', 'h', 0)`)
	exec(t, `INSERT INTO alert_rules (id, metric, server_id, process, created_at) VALUES ('rp1', 'process', 'p1', 'nginx', 0), ('rp2', 'process', 'p1', 'postgres', 0)`)
	Evaluate(srv(t, "p1"), snap, nil)
	eq(t, message(t, "rp2"), "web9: postgres is not running")
	n, err := db.Get[int](`SELECT COUNT(*) FROM alert_state WHERE rule_id = 'rp1'`)
	must(t, err)
	eq(t, *n, 0)
	Evaluate(srv(t, "p1"), &probe.Snapshot{}, nil) // no process list (ps missing): keep the state, don't resolve
	message(t, "rp2")

	if _, err := ValidateRule("default", map[string]any{"metric": "process", "process": "nginx; rm -rf /"}); err == nil {
		t.Fatal("a process name with shell characters must be rejected")
	}
	r, err := ValidateRule("default", map[string]any{"metric": "process", "process": " php-fpm8.2 "})
	must(t, err)
	eq(t, *r.Process, "php-fpm8.2")
}
