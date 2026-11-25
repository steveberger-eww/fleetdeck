package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"fleetdeck/internal/db"
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

func at(y int, mon time.Month, d, h, m int) time.Time {
	return time.Date(y, mon, d, h, m, 0, 0, time.Local)
}

func TestCron(t *testing.T) {
	m, err := ParseCron("*/15 9-17 * * 1-5")
	if err != nil || !m(at(2026, 11, 24, 9, 45)) || m(at(2026, 11, 24, 9, 46)) || m(at(2026, 11, 29, 9, 45)) { // Tuesday; Sunday
		t.Fatal("*/15 9-17 * * 1-5", err)
	}
	either, _ := ParseCron("0 0 1 * 1") // day-of-month OR Monday
	if !either(at(2026, 11, 1, 0, 0)) || !either(at(2026, 11, 23, 0, 0)) || either(at(2026, 11, 24, 0, 0)) {
		t.Fatal("dom/dow OR rule")
	}
	if sun, _ := ParseCron("0 0 * * 7"); !sun(at(2026, 11, 29, 0, 0)) { // 7 = Sunday
		t.Fatal("7 = Sunday")
	}
	if runs, err := NextRuns("@daily", 3, at(2026, 1, 1, 12, 0)); err != nil || len(runs) != 3 {
		t.Fatal(runs, err)
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Error(bad)
		}
	}
}

func TestRebootDetection(t *testing.T) {
	now := int64(1_000_000_000)
	if !BootedAfter("42.10 80.00\n", now-300_000, now) { // up 42s, run started 5 min ago
		t.Error("rebooted")
	}
	if BootedAfter("9000.5 1.0", now-300_000, now) || BootedAfter("", now-300_000, now) {
		t.Error("not rebooted")
	}
}

func TestSharedVariables(t *testing.T) {
	str := func(s string) *string { return &s }
	if _, err := ValidateVariable("default", VariableInput{Name: "1BAD", Value: str("x")}, nil); err == nil {
		t.Error("1BAD")
	}
	if _, err := ValidateVariable("default", VariableInput{Name: "TOKEN", Value: str("")}, nil); err == nil {
		t.Error("empty value")
	}
	v, err := ValidateVariable("default", VariableInput{Name: "TOKEN", Value: str("s3cr3t-value")}, nil)
	if err != nil || v.Secret != 1 || strings.Contains(v.ValueEnc, "s3cr3t") {
		t.Fatal(v, err)
	}
	if _, err := db.Exec(`INSERT INTO variables (id, name, value_enc, secret, created_at) VALUES (?, ?, ?, ?, 0)`, "v1", v.Name, v.ValueEnc, v.Secret); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateVariable("default", VariableInput{Name: "TOKEN", Value: str("y")}, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Error(err)
	}
	old, _ := db.Get[Variable](`SELECT * FROM variables WHERE id = 'v1'`)
	kept, err := ValidateVariable("default", VariableInput{Value: str(""), Name: "RENAMED"}, old)
	if err != nil || kept.Name != "TOKEN" { // can't rename
		t.Fatal(kept, err)
	}
	if plain, _ := sec.Decrypt(kept.ValueEnc); plain != "s3cr3t-value" { // blank = keep
		t.Error(plain)
	}
	script := ScriptInput{Name: "s", Body: "echo", Vars: []string{"TOKEN"}}
	if s, err := ValidateScript("default", script); err != nil || !slices.Equal(s.Vars, Names{"TOKEN"}) {
		t.Fatal(s, err)
	}
	nope := script
	nope.Vars = []string{"NOPE"}
	if _, err := ValidateScript("default", nope); err == nil || !strings.Contains(err.Error(), "unknown variable") {
		t.Error(err)
	}
	clash := script
	clash.Params = []Param{{Name: "TOKEN"}}
	if _, err := ValidateScript("default", clash); err == nil || !strings.Contains(err.Error(), "both a parameter") {
		t.Error(err)
	}
	if got := Masker([]string{"s3cr3t-value"})("token=s3cr3t-value; again s3cr3t-value"); got != "token=••••••; again ••••••" {
		t.Error(got)
	}
}

// Not in test.js: JSON columns keep the JS shapes ([] not null, {} for shell options, lenient param values).
func TestJSONShapes(t *testing.T) {
	if err := Seed(); err != nil {
		t.Fatal(err)
	}
	sc, err := GetScript("update")
	if err != nil || sc == nil || sc.Params == nil || sc.Vars == nil || !sc.AsRoot || !sc.Builtin {
		t.Fatal(sc, err)
	}
	b, _ := json.Marshal(sc)
	if !strings.Contains(string(b), `"params":[]`) || !strings.Contains(string(b), `"vars":[]`) {
		t.Error(string(b))
	}
	var v Values
	if err := json.Unmarshal([]byte(`{"a":"x","n":5,"b":true}`), &v); err != nil || v["a"] != "x" || v["n"] != "5" || v["b"] != "true" {
		t.Error(v, err)
	}
	if o, _ := (Options{}).Value(); o != "{}" {
		t.Error(o)
	}
}

func TestRunAt(t *testing.T) {
	if err := Seed(); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(3 * time.Hour)
	in := ScheduleInput{Name: "later", ScriptID: "update", Targets: &Targets{All: true}, RunAt: new(when.UnixMilli())}
	v, err := ValidateSchedule("default", in, true)
	if err != nil || v.Once != 1 {
		t.Fatal(v, err)
	}
	if match, _ := ParseCron(*v.Cron); !match(when.Truncate(time.Minute)) || match(when.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatal("cron", *v.Cron)
	}
	in.RunAt = new(time.Now().UnixMilli())
	if _, err := ValidateSchedule("default", in, true); err == nil {
		t.Fatal("current minute must be rejected")
	}
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTypedParams(t *testing.T) {
	sc := &Script{Params: Params{{Name: "ENV", Label: "Env", Type: "choice", Options: []string{"dev", "prod"}, Default: "dev"},
		{Name: "DRY", Label: "Dry", Type: "checkbox", Default: "true"}, {Name: "WHO", Label: "Who", Required: true}}}
	v, err := sc.Resolve(Values{"WHO": "me", "DRY": "false", "OTHER": "x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, v, Values{"ENV": "dev", "DRY": "false", "WHO": "me"}) // defaults filled, undeclared dropped
	for _, bad := range []Values{{"WHO": "me", "ENV": "qa"}, {"WHO": "me", "DRY": "1"}, {"WHO": " "}} {
		if _, err := sc.Resolve(bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}

	if _, err := db.Exec(`INSERT INTO scripts (id, name, body, params, created_at) VALUES ('typed', 't', '', ?, 0)`, sc.Params); err != nil {
		t.Fatal(err)
	}
	vars, err := ansibleVars("typed", map[string]string{"DRY": "true", "ENV": "prod", "REGION": "eu"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, vars, map[string]any{"DRY": true, "ENV": "prod", "REGION": "eu"})
}

// Chains run on a server that refuses connections, so every step fails right away.
func TestChain(t *testing.T) {
	OnJobDone(chain)
	if _, err := db.Exec(`INSERT INTO servers (id, name, host, port, created_at) VALUES ('dead', 'dead', '127.0.0.1', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	var script func(id, next, when string)
	script = func(id, next, when string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO scripts (id, tenant_id, name, body, created_at) VALUES (?, 'default', ?, 'true', 0) ON CONFLICT (id) DO NOTHING`, id, id); err != nil {
			t.Fatal(err)
		}
		if next == "" {
			return
		}
		script(next, "", "")
		if _, err := db.Exec(`UPDATE scripts SET next_script_id = ?, next_when = ? WHERE id = ?`, next, when, id); err != nil {
			t.Fatal(err)
		}
	}
	jobCount := func() int { n, _ := db.Get[int](`SELECT COUNT(*) FROM jobs`); return *n }
	settle := func(want int) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			mu.Lock()
			idle := len(live) == 0
			mu.Unlock()
			if idle && jobCount() >= want {
				time.Sleep(300 * time.Millisecond) // a step that shouldn't start would by now
				break
			}
		}
		if got := jobCount(); got != want {
			ev, _ := db.Rows(`SELECT message FROM events ORDER BY id`)
			js, _ := db.Rows(`SELECT id, name, status, actor, parent_job FROM jobs ORDER BY id`)
			t.Fatalf("%d jobs, want %d\n%v\n%v", got, want, ev, js)
		}
	}
	start := func(id string) int64 {
		t.Helper()
		sc, _ := GetScript(id)
		sp := SpecFromScript(sc, Values{}, false)
		sp.Targets, sp.TenantID, sp.Actor = Targets{Servers: []string{"dead"}}, "default", "test"
		jid, err := StartJob(sp)
		if err != nil {
			t.Fatal(err)
		}
		return jid
	}
	base := jobCount()

	// step1 -always-> step2 (fails) -failure-> step3 (fails) -success-> step4: skipped.
	script("step3", "step4", "success")
	script("step2", "step3", "failure")
	script("step1", "step2", "always")
	first := start("step1")
	settle(base + 3)
	j, _ := GetJob(first)
	for _, name := range []string{"step2", "step3"} {
		next, _ := GetJob(*j.NextJob)
		if next.Name != name || *next.ParentJob != j.ID || *next.Actor != fmt.Sprintf("chain from #%d", j.ID) {
			t.Fatalf("%s: %+v", name, next)
		}
		j = next
	}
	if j.NextJob != nil {
		t.Fatal("step4 ran after a failure")
	}

	// Cancelled jobs and dry runs never continue.
	chain("default", first, "", "cancelled", "", nil)
	if _, err := db.Exec(`UPDATE jobs SET options = '{"check":true}' WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	chain("default", first, "", "failed", "", nil)
	settle(base + 3)

	// A loop stops after MaxChain jobs.
	script("loop-b", "loop-a", "failure")
	script("loop-a", "loop-b", "failure")
	start("loop-a")
	settle(base + 3 + MaxChain)

	if _, err := ValidateScript("default", ScriptInput{Name: "x", Body: "true", NextScript: "step1", NextWhen: "always", Self: "step1"}); err == nil {
		t.Error("chained to itself")
	}
	if _, err := ValidateScript("default", ScriptInput{Name: "x", Body: "true", NextScript: "step1", NextWhen: "sometimes"}); err == nil {
		t.Error("bad next_when")
	}
}
