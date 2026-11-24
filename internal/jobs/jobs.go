// Package jobs: Semaphore-style task runner: script library, jobs on many hosts with live output, cron schedules.
package jobs

import (
	"cmp"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/repos"
	"fleetdeck/internal/sec"
	"fleetdeck/internal/ssh"
)

const (
	outputCap = 1_000_000
	parallel  = 8
)

// Ansible is the pseudo server id of the run holding the full ansible-playbook log.
const Ansible = "__ansible__"

// NewID returns a random UUID for new rows.
func NewID() string { return db.NewID() }

// ---------- JSON columns ----------

// scanJSON decodes a TEXT column holding JSON.
func scanJSON(src, dst any) error {
	switch v := src.(type) {
	case string:
		return json.Unmarshal([]byte(v), dst)
	case []byte:
		return json.Unmarshal(v, dst)
	case nil:
		return nil
	}
	return fmt.Errorf("unexpected JSON column type %T", src)
}

func jsonValue(v any) (driver.Value, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// Param is a script parameter definition.
type Param struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Default  string   `json:"default"`
	Required bool     `json:"required"`
	Type     string   `json:"type,omitempty"`    // "" (text) | checkbox | choice
	Options  []string `json:"options,omitempty"` // choice only
}

// Resolve checks values for a script's parameters, filling in defaults for missing ones. Only declared
// parameters are returned. A checkbox is "true" or "false"; a choice must be one of its options.
func (sc *Script) Resolve(given Values) (Values, error) {
	out := Values{}
	for _, p := range sc.Params {
		v, ok := given[p.Name]
		if !ok {
			v = p.Default
		}
		switch p.Type {
		case "checkbox":
			if v != "true" && v != "false" {
				return nil, httperr.Bad(fmt.Sprintf(`"%s" must be true or false`, p.Label))
			}
		case "choice":
			if !slices.Contains(p.Options, v) {
				return nil, httperr.Bad(fmt.Sprintf(`"%s" must be one of: %s`, p.Label, strings.Join(p.Options, ", ")))
			}
		default:
			if p.Required && strings.TrimSpace(v) == "" {
				return nil, httperr.Bad(fmt.Sprintf(`"%s" is required`, p.Label))
			}
		}
		out[p.Name] = v
	}
	return out, nil
}

// Params is the scripts.params column.
type Params []Param

func (p *Params) Scan(src any) error { return scanJSON(src, p) }
func (p Params) Value() (driver.Value, error) {
	if p == nil {
		p = Params{}
	}
	return jsonValue([]Param(p))
}

// Names is a JSON array of strings column (scripts.vars).
type Names []string

func (n *Names) Scan(src any) error { return scanJSON(src, n) }
func (n Names) Value() (driver.Value, error) {
	if n == nil {
		n = Names{}
	}
	return jsonValue([]string(n))
}

// Values are parameter values (jobs/schedules/triggers.params). Non-string JSON values are accepted and
// stringified, as the JS did with String(v).
type Values map[string]string

func (v *Values) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*v = Values{}
	for k, r := range raw {
		var s string
		if json.Unmarshal(r, &s) != nil {
			s = string(r)
		}
		(*v)[k] = s
	}
	return nil
}
func (v *Values) Scan(src any) error { return scanJSON(src, v) }
func (v Values) Value() (driver.Value, error) {
	if v == nil {
		return "{}", nil
	}
	return jsonValue(map[string]string(v))
}

// Options of a job: {"check": bool} for ansible, {} for shell.
type Options struct {
	Check *bool `json:"check,omitempty"`
}

func (o *Options) Scan(src any) error          { return scanJSON(src, o) }
func (o Options) Value() (driver.Value, error) { return jsonValue(o) }

// Targets select servers: all, by id, or by tag.
type Targets struct {
	All     bool     `json:"all"`
	Servers []string `json:"servers"`
	Tags    []string `json:"tags"`
}

func (t *Targets) Scan(src any) error          { return scanJSON(src, t) }
func (t Targets) Value() (driver.Value, error) { return jsonValue(t) }

// ---------- scripts ----------

// Seed inserts the built-in scripts that are missing.
func Seed() error {
	now := db.Now()
	for _, s := range Builtin {
		if _, err := db.Exec(`INSERT INTO scripts (id, name, description, body, as_root, params, builtin, created_at) VALUES (?, ?, ?, ?, 1, ?, 1, ?) ON CONFLICT DO NOTHING`,
			s.ID, s.Name, s.Description, s.Body, s.Params, now); err != nil {
			return err
		}
	}
	return nil
}

// Script is a scripts row with params/vars decoded and the flags as booleans (the JS scriptRow).
type Script struct {
	ID          string  `db:"id" json:"id"`
	TenantID    *string `db:"tenant_id" json:"-"` // nil = the shared library
	Name        string  `db:"name" json:"name"`
	Description string  `db:"description" json:"description"`
	Body        string  `db:"body" json:"body"`
	AsRoot      bool    `db:"as_root" json:"as_root"`
	Params      Params  `db:"params" json:"params"`
	Builtin     bool    `db:"builtin" json:"builtin"`
	CreatedAt   int64   `db:"created_at" json:"created_at"`
	Kind        string  `db:"kind" json:"kind"`
	RepoID      *string `db:"repo_id" json:"repo_id"`
	Playbook    *string `db:"playbook" json:"playbook"`
	Vars        Names   `db:"vars" json:"vars"`
	NextScript  *string `db:"next_script_id" json:"next_script_id"` // chain: run this script next
	NextWhen    string  `db:"next_when" json:"next_when"`           // success | failure | always
	NextOKOnly  bool    `db:"next_ok_only" json:"next_ok_only"`     // only on the servers where this one succeeded
}

// GetScript loads a script (nil, nil if missing).
func GetScript(id string) (*Script, error) {
	return db.Get[Script](`SELECT * FROM scripts WHERE id = ?`, id)
}

// ScriptFor loads a script the organization can run: its own or one from the shared library (nil, nil if neither).
func ScriptFor(tenant, id string) (*Script, error) {
	return db.Get[Script](`SELECT * FROM scripts WHERE id = ? AND (tenant_id = ? OR tenant_id IS NULL)`, id, tenant)
}

// ScriptInput is the request body of script create/update.
type ScriptInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Kind        string   `json:"kind"`
	RepoID      string   `json:"repo_id"`
	Playbook    string   `json:"playbook"`
	AsRoot      *bool    `json:"as_root"` // nil = true
	Params      []Param  `json:"params"`
	Vars        []string `json:"vars"`
	NextScript  string   `json:"next_script_id"`
	NextWhen    string   `json:"next_when"`
	NextOKOnly  bool     `json:"next_ok_only"`
	Self        string   `json:"-"` // the script's own id when editing: it can't chain to itself
}

// ScriptFields are the validated columns to store.
type ScriptFields struct {
	Vars        Names
	Name, Body  string
	Kind        string
	RepoID      *string
	Playbook    *string
	Description string
	AsRoot      int
	Params      Params
	NextScript  *string
	NextWhen    string
	NextOKOnly  int
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidateScript checks a script of the organization tenant; tenant "" is the shared library, whose scripts run in
// every organization: no playbooks (repositories belong to one organization), variables resolve where they run.
func ValidateScript(tenant string, b ScriptInput) (*ScriptFields, error) {
	name := strings.TrimSpace(b.Name)
	kind := "shell"
	if b.Kind == "ansible" {
		kind = "ansible"
	}
	body := ""
	if kind == "shell" {
		body = b.Body
	}
	if name == "" || jsLen(name) > 100 {
		return nil, httperr.Bad("name required (max 100 chars)")
	}
	if kind == "shell" && (strings.TrimSpace(body) == "" || jsLen(body) > 100_000) {
		return nil, httperr.Bad("script body required (max 100 KB)")
	}
	if kind == "ansible" && tenant == "" {
		return nil, httperr.Bad("Shared scripts can't be playbooks: repositories belong to one organization")
	}
	if kind == "ansible" {
		if ok, err := exists(`SELECT 1 FROM repos WHERE id = ? AND tenant_id = ?`, b.RepoID, tenant); err != nil || !ok {
			return nil, cmp.Or(err, httperr.Bad("Pick a repository"))
		}
		if !repos.ValidPlaybook(b.Playbook) {
			return nil, httperr.Bad("Playbook must be a relative path to a .yml file, e.g. site.yml")
		}
	}
	if len(b.Params) > 20 {
		return nil, httperr.Bad("max 20 parameters")
	}
	vars := Names{}
	for _, n := range b.Vars {
		if !slices.Contains(vars, n) {
			vars = append(vars, n)
		}
	}
	for _, n := range vars {
		if !identRE.MatchString(n) {
			return nil, httperr.Bad(fmt.Sprintf("invalid variable name \"%s\"", n))
		}
		if tenant == "" {
			continue
		}
		if ok, err := exists(`SELECT 1 FROM variables WHERE name = ? AND tenant_id = ?`, n, tenant); err != nil || !ok {
			return nil, cmp.Or(err, httperr.Bad(fmt.Sprintf("unknown variable \"%s\"", n)))
		}
	}
	for _, p := range b.Params {
		if slices.Contains(vars, strings.TrimSpace(p.Name)) {
			return nil, httperr.Bad(fmt.Sprintf("\"%s\" is both a parameter and a shared variable", p.Name))
		}
	}
	out := &ScriptFields{Vars: vars, Name: name, Body: body, Kind: kind, Description: truncate(b.Description, 500), AsRoot: 1, Params: Params{}, NextWhen: "success"}
	if b.NextScript != "" {
		if b.NextScript == b.Self {
			return nil, httperr.Bad("A script can't run itself next")
		}
		// A shared script (tenant "") may only chain to another shared one: ScriptFor then matches the library only.
		if next, err := ScriptFor(tenant, b.NextScript); next == nil || err != nil {
			return nil, cmp.Or(err, httperr.Bad("Unknown next script"))
		}
		if !slices.Contains([]string{"success", "failure", "always"}, b.NextWhen) {
			return nil, httperr.Bad("Run the next script on success, failure or always")
		}
		out.NextScript, out.NextWhen = &b.NextScript, b.NextWhen
		if b.NextOKOnly {
			out.NextOKOnly = 1
		}
	}
	if kind == "ansible" {
		out.RepoID, out.Playbook = &b.RepoID, &b.Playbook
	}
	if b.AsRoot != nil && !*b.AsRoot {
		out.AsRoot = 0
	}
	for _, p := range b.Params {
		n := strings.TrimSpace(p.Name)
		if !identRE.MatchString(n) {
			return nil, httperr.Bad(fmt.Sprintf("invalid parameter name \"%s\" (use letters, digits, underscore)", n))
		}
		q := Param{Name: n, Label: truncate(cmp.Or(p.Label, n), 100), Default: truncate(p.Default, 10_000), Required: p.Required}
		switch p.Type {
		case "", "text":
		case "checkbox":
			q.Type, q.Required = p.Type, false
			if q.Default != "true" {
				q.Default = "false"
			}
		case "choice":
			q.Type = p.Type
			for _, o := range p.Options {
				if o = truncate(strings.TrimSpace(o), 100); o != "" && !slices.Contains(q.Options, o) {
					q.Options = append(q.Options, o)
				}
			}
			if len(q.Options) == 0 || len(q.Options) > 50 {
				return nil, httperr.Bad(fmt.Sprintf(`"%s" needs 1-50 choices`, q.Label))
			}
			if !slices.Contains(q.Options, q.Default) {
				q.Default = q.Options[0]
			}
		default:
			return nil, httperr.Bad("unknown parameter type " + p.Type)
		}
		out.Params = append(out.Params, q)
	}
	return out, nil
}

func exists(q string, args ...any) (bool, error) {
	r, err := db.Get[int](q, args...)
	return r != nil, err
}

// jsLen approximates JS string length (UTF-16 units) closely enough for input limits.
func jsLen(s string) int { return utf8.RuneCountInString(s) }

func truncate(s string, n int) string {
	if jsLen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---------- shared variables ----------

// Variable is a row of the variables table.
type Variable struct {
	ID          string `db:"id"`
	TenantID    string `db:"tenant_id" json:"-"`
	Name        string `db:"name"`
	ValueEnc    string `db:"value_enc"`
	Secret      bool   `db:"secret"`
	Description string `db:"description"`
	CreatedAt   int64  `db:"created_at"`
}

// VariableInput is the request body of variable create/update.
type VariableInput struct {
	Name        string  `json:"name"`
	Value       *string `json:"value"`
	Secret      *bool   `json:"secret"` // nil = keep (or true for a new one)
	Description string  `json:"description"`
}

// VariableFields are the validated columns to store.
type VariableFields struct {
	Name        string
	Secret      int
	ValueEnc    string
	Description string
}

func ValidateVariable(tenant string, b VariableInput, existing *Variable) (*VariableFields, error) {
	name := strings.TrimSpace(b.Name)
	if existing != nil {
		name = existing.Name // name is the reference scripts use: fixed once created
	}
	if !identRE.MatchString(name) {
		return nil, httperr.Bad("Name: letters, digits and underscore, not starting with a digit")
	}
	if existing == nil {
		if ok, err := exists(`SELECT 1 FROM variables WHERE name = ? AND tenant_id = ?`, name, tenant); err != nil || ok {
			return nil, cmp.Or(err, httperr.New(409, fmt.Sprintf("A variable named %s already exists", name)))
		}
	}
	secret := existing == nil || existing.Secret
	if b.Secret != nil {
		secret = *b.Secret
	}
	value := ""
	if b.Value != nil {
		value = *b.Value
	}
	if existing != nil && value == "" && existing.Secret { // blank secret on edit = keep
		v, err := sec.Decrypt(existing.ValueEnc)
		if err != nil {
			return nil, err
		}
		value = v
	}
	if value == "" {
		return nil, httperr.Bad("Value is required")
	}
	if jsLen(value) > 64_000 {
		return nil, httperr.Bad("Value too long (max 64 KB)")
	}
	out := &VariableFields{Name: name, ValueEnc: sec.Encrypt(value), Description: truncate(b.Description, 300)}
	if secret {
		out.Secret = 1
	}
	return out, nil
}

// Decrypted only while a job starts; never written to the job row. Secret values are masked in output.
func sharedVars(tenant, scriptID string) (vars map[string]string, secrets []string, err error) {
	vars = map[string]string{}
	if scriptID == "" {
		return vars, nil, nil
	}
	names, err := db.Get[Names](`SELECT vars FROM scripts WHERE id = ?`, scriptID)
	if err != nil || names == nil {
		return vars, nil, err
	}
	for _, n := range *names {
		v, err := db.Get[Variable](`SELECT * FROM variables WHERE name = ? AND tenant_id = ?`, n, tenant)
		if err != nil {
			return nil, nil, err
		}
		if v == nil {
			continue
		}
		if vars[n], err = sec.Decrypt(v.ValueEnc); err != nil {
			return nil, nil, err
		}
		if v.Secret && jsLen(vars[n]) >= 3 {
			secrets = append(secrets, vars[n])
		}
	}
	return vars, secrets, nil
}

// Masker replaces each secret with ••••••.
// ponytail: masks per output chunk; a secret split across two SSH packets slips through. Buffer by line if that matters.
func Masker(secrets []string) func(string) string {
	return func(s string) string {
		for _, x := range secrets {
			s = strings.ReplaceAll(s, x, "••••••")
		}
		return s
	}
}

// Spec is everything needed to start a job.
type Spec struct {
	ScriptID string // "" = ad-hoc command
	Name     string
	Body     string // shell script, or the playbook path for ansible
	Kind     string // "shell" (default) or "ansible"
	RepoID   string
	Options  Options
	AsRoot   bool // set explicitly: the zero value runs as the login user
	Params   Values
	Targets  Targets
	Actor    string
	TenantID string // the organization the job runs in; targets resolve within it
	Member   string // a user who sees only their assigned servers; "" = every server of the organization
	Parent   int64  // the job whose chain started this one (0 = none)
}

// SpecFromScript is everything needed to start a job from a saved script (Targets and Actor still to set).
func SpecFromScript(sc *Script, params Values, check bool) Spec {
	s := Spec{ScriptID: sc.ID, Name: sc.Name, Kind: sc.Kind, Body: sc.Body, RepoID: db.Str(sc.RepoID), AsRoot: sc.AsRoot, Params: params}
	if sc.Kind == "ansible" {
		s.Body, s.Options = db.Str(sc.Playbook), Options{Check: &check}
	}
	return s
}

// ---------- targets ----------

func tagsOf(s *db.Server) []string {
	var out []string
	for _, t := range strings.Split(s.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ResolveTargets lists the organization's servers the targets match; with member set, only those assigned to them.
func ResolveTargets(tenant, member string, t Targets) ([]db.Server, error) {
	all, err := db.Select[db.Server](`SELECT * FROM servers WHERE tenant_id = ?
    AND (? = '' OR id IN (SELECT server_id FROM server_members WHERE user_id = ?)) ORDER BY name`, tenant, member, member)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(s db.Server) bool {
		return !(t.All || slices.Contains(t.Servers, s.ID) || slices.ContainsFunc(tagsOf(&s), func(x string) bool { return slices.Contains(t.Tags, x) }))
	}), nil
}

// ---------- jobs ----------

// Msg is a live job event, sent to subscribers as JSON:
// {type:"data", server, data} | {type:"run", server, status, code} | {type:"job", status} | {type:"snapshot", job}.
type Msg map[string]any

type liveRun struct{ status, output string }

type liveJob struct {
	ctx       context.Context
	cancel    context.CancelFunc
	order     []string // run ids in insertion order (the ansible pseudo-run first)
	runs      map[string]*liveRun
	subs      map[int]func(Msg)
	nextSub   int
	cancelled bool
	mask      func(string) string
}

// mu guards live and everything inside the live jobs. Subscribers are called with it held.
var (
	mu            sync.Mutex
	live          = map[int64]*liveJob{}
	doneListeners []func(tenant string, id int64, name, status, actor string, runStatuses []string)
)

// OnJobDone registers a listener called after every job finishes. runStatuses include the ansible pseudo-run.
func OnJobDone(fn func(tenant string, id int64, name, status, actor string, runStatuses []string)) {
	mu.Lock()
	defer mu.Unlock()
	doneListeners = append(doneListeners, fn)
}

func (j *liveJob) emit(m Msg) { // mu held
	for _, fn := range j.subs {
		fn(m)
	}
}

func appendCapped(out, d string) string {
	out += d
	if len(out) > outputCap {
		cut := len(out) - outputCap*9/10
		for cut < len(out) && !utf8.RuneStart(out[cut]) {
			cut++
		}
		out = "[…output truncated…]\n" + out[cut:]
	}
	return out
}

func (j *liveJob) data(server, d string) {
	mu.Lock()
	defer mu.Unlock()
	r := j.runs[server]
	r.output = appendCapped(r.output, d)
	j.emit(Msg{"type": "data", "server": server, "data": d})
}

func (j *liveJob) isCancelled() bool {
	mu.Lock()
	defer mu.Unlock()
	return j.cancelled
}

func saveRun(jobID int64, server, status, output string, code *int) {
	now := db.Now()
	var fin any = now
	if status == "running" {
		fin = nil
	}
	if _, err := db.Exec(`UPDATE job_runs SET status = ?, exit_code = ?, output = ?, started_at = COALESCE(started_at, ?), finished_at = ? WHERE job_id = ? AND server_id = ?`,
		status, code, db.PgText(output), now, fin, jobID, server); err != nil {
		log.Printf("job #%d: save run %s: %v", jobID, server, err)
	}
}

// set updates a run's status, tells subscribers and stores it.
func (j *liveJob) set(jobID int64, server, status string, code *int) {
	mu.Lock()
	r := j.runs[server]
	r.status = status
	out := r.output
	j.emit(Msg{"type": "run", "server": server, "status": status, "code": code})
	mu.Unlock()
	saveRun(jobID, server, status, out, code)
}

func guard(what string) {
	if r := recover(); r != nil {
		log.Printf("%s: panic: %v\n%s", what, r, debug.Stack())
	}
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// StartJob creates the job and runs it in the background; it returns the job id.
func StartJob(sp Spec) (int64, error) {
	if sp.TenantID == "" {
		return 0, errors.New("job without an organization")
	}
	servers, err := ResolveTargets(sp.TenantID, sp.Member, sp.Targets)
	if err != nil {
		return 0, err
	}
	if len(servers) == 0 {
		return 0, httperr.Bad("No servers match the selected targets")
	}
	vars, secrets, err := sharedVars(sp.TenantID, sp.ScriptID)
	if err != nil {
		return 0, err
	}
	mask := Masker(secrets)
	merged := maps.Clone(vars)
	maps.Copy(merged, sp.Params)
	if sp.Kind == "ansible" {
		return startAnsibleJob(sp, servers, merged, mask)
	}
	return startShellJob(sp, servers, merged, mask)
}

// ansibleVars types checkbox parameters as booleans; everything else stays a string.
func ansibleVars(scriptID string, vars map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range vars {
		out[k] = v
	}
	sc, err := GetScript(scriptID) // also for re-runs, which only have the stored string values
	if sc == nil || err != nil {
		return out, err
	}
	for _, p := range sc.Params {
		if v, ok := vars[p.Name]; ok && p.Type == "checkbox" {
			out[p.Name] = v == "true"
		}
	}
	return out, nil
}

func startShellJob(sp Spec, servers []db.Server, merged Values, mask func(string) string) (int64, error) {
	var prelude strings.Builder
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		if !envName.MatchString(k) {
			return 0, httperr.Bad("invalid parameter name " + k)
		}
		prelude.WriteString("export " + k + "=" + ssh.Shq(merged[k]) + "\n")
	}
	jobID, err := insertJob(sp, servers, false)
	if err != nil {
		return 0, err
	}
	j := newLive(jobID, servers, false, mask)
	db.Event("job", fmt.Sprintf("Job #%d \"%s\" started on %s", jobID, sp.Name, serverNames(servers)), db.EventOpts{Actor: sp.Actor, TenantID: sp.TenantID})

	go func() {
		defer guard(fmt.Sprintf("job #%d", jobID))
		defer finishJob(jobID, j, sp)
		queue := make(chan db.Server, len(servers))
		for _, s := range servers {
			queue <- s
		}
		close(queue)
		var wg sync.WaitGroup
		for range min(parallel, len(servers)) {
			wg.Go(func() {
				for s := range queue {
					func() {
						defer guard(fmt.Sprintf("job #%d on %s", jobID, s.Name))
						runOne(jobID, j, &s, prelude.String()+sp.Body, sp.AsRoot)
					}()
				}
			})
		}
		wg.Wait()
	}()
	return jobID, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertJob(sp Spec, servers []db.Server, ansible bool) (int64, error) {
	var id int64
	err := db.Tx(func(tx db.Txn) error {
		var err error
		var parent any
		if sp.Parent != 0 {
			parent = sp.Parent
		}
		asRoot := 0
		if sp.AsRoot {
			asRoot = 1
		}
		if ansible {
			err = tx.Get(&id, `INSERT INTO jobs (tenant_id, script_id, name, body, as_root, params, status, actor, created_at, kind, repo_id, options, parent_job) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				sp.TenantID, nullable(sp.ScriptID), sp.Name, sp.Body, asRoot, sp.Params, "running", nullable(sp.Actor), db.Now(), "ansible", nullable(sp.RepoID), sp.Options, parent)
		} else {
			err = tx.Get(&id, `INSERT INTO jobs (tenant_id, script_id, name, body, as_root, params, status, actor, created_at, parent_job) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				sp.TenantID, nullable(sp.ScriptID), sp.Name, sp.Body, asRoot, sp.Params, "running", nullable(sp.Actor), db.Now(), parent)
		}
		if err != nil {
			return err
		}
		if ansible {
			if _, err := tx.Exec(`INSERT INTO job_runs (job_id, server_id, server_name, status) VALUES (?, ?, ?, ?)`, id, Ansible, "ansible-playbook", "queued"); err != nil {
				return err
			}
		}
		for _, s := range servers {
			if _, err := tx.Exec(`INSERT INTO job_runs (job_id, server_id, server_name, status) VALUES (?, ?, ?, ?)`, id, s.ID, s.Name, "queued"); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func newLive(jobID int64, servers []db.Server, ansible bool, mask func(string) string) *liveJob {
	ctx, cancel := context.WithCancel(context.Background())
	j := &liveJob{ctx: ctx, cancel: cancel, runs: map[string]*liveRun{}, subs: map[int]func(Msg){}, mask: mask}
	if ansible {
		j.order = append(j.order, Ansible)
	}
	for _, s := range servers {
		j.order = append(j.order, s.ID)
	}
	for _, id := range j.order {
		j.runs[id] = &liveRun{status: "queued"}
	}
	mu.Lock()
	live[jobID] = j
	mu.Unlock()
	return j
}

func serverNames(servers []db.Server) string {
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

func finishJob(jobID int64, j *liveJob, sp Spec) {
	mu.Lock()
	statuses := make([]string, len(j.order))
	status := "success"
	for i, id := range j.order {
		statuses[i] = j.runs[id].status
		if statuses[i] != "success" && statuses[i] != "skipped" {
			status = "failed"
		}
	}
	if j.cancelled {
		status = "cancelled"
	}
	// Under mu, so a subscriber sees either the live job or the finished row, never neither.
	if _, err := db.Exec(`UPDATE jobs SET status = ?, finished_at = ? WHERE id = ?`, status, db.Now(), jobID); err != nil {
		log.Printf("job #%d: finish: %v", jobID, err)
	}
	j.emit(Msg{"type": "job", "status": status})
	delete(live, jobID)
	fns := slices.Clone(doneListeners)
	mu.Unlock()
	j.cancel()
	for _, fn := range fns {
		func() {
			defer guard("job done listener")
			fn(sp.TenantID, jobID, sp.Name, status, sp.Actor, statuses)
		}()
	}
}

func startAnsibleJob(sp Spec, servers []db.Server, plain map[string]string, mask func(string) string) (int64, error) {
	vars, err := ansibleVars(sp.ScriptID, plain)
	if err != nil {
		return 0, err
	}
	jobID, err := insertJob(sp, servers, true)
	if err != nil {
		return 0, err
	}
	j := newLive(jobID, servers, true, mask)
	check := sp.Options.Check != nil && *sp.Options.Check
	dry := ""
	if check {
		dry = ", dry run"
	}
	db.Event("job", fmt.Sprintf("Job #%d \"%s\" (ansible %s%s) started on %s", jobID, sp.Name, sp.Body, dry, serverNames(servers)), db.EventOpts{Actor: sp.Actor, TenantID: sp.TenantID})

	go func() {
		defer guard(fmt.Sprintf("job #%d", jobID))
		defer finishJob(jobID, j, sp) // also after a panic, so the job never stays live
		out := func(d string) { j.data(Ansible, mask(d)) }
		for _, id := range j.order {
			j.set(jobID, id, "running", nil)
		}
		res, err := func() (*repos.PlaybookResult, error) {
			// Connect once to servers that were never reached, so their host keys are pinned for known_hosts.
			var wg sync.WaitGroup
			for i := range servers {
				if servers[i].HostKeyRaw == nil || *servers[i].HostKeyRaw == "" {
					wg.Go(func() {
						defer guard("pin host key")
						_, _ = ssh.Exec(j.ctx, &servers[i], "true", ssh.ExecOpts{Mode: ssh.ModeUser, Timeout: 20 * time.Second})
					})
				}
			}
			wg.Wait()
			fresh := make([]db.Server, len(servers))
			for i, s := range servers {
				f, err := db.GetServer(s.ID)
				if err != nil {
					return nil, err
				}
				fresh[i] = *cmp.Or(f, &s)
			}
			// Cancelled while waiting for the repo lock or syncing: galaxy/ansible-playbook are killed via ctx.
			res, err := repos.RunPlaybook(j.ctx, repos.PlaybookOpts{RepoID: sp.RepoID, Playbook: sp.Body, Servers: fresh, Become: sp.AsRoot, Vars: vars, Check: check, OnData: out})
			if err == nil && res.Commit != "" {
				_, err = db.Exec(`UPDATE jobs SET commit_sha = ? WHERE id = ?`, res.Commit, jobID)
			}
			return res, err
		}()
		if err != nil {
			out("\n[error: " + err.Error() + "]\n")
		}
		cancelled := j.isCancelled()
		for _, s := range servers {
			a, st, hasSt := "", repos.Recap{}, false
			if res != nil {
				a = res.Alias[s.ID]
				st, hasSt = res.Recap[a]
			}
			o := "(no output for this server: see the ansible-playbook log)"
			if a != "" && res.PerHost[a] != "" {
				o = res.PerHost[a]
			}
			o = mask(o)
			if hasSt {
				o += fmt.Sprintf("\nok=%d changed=%d unreachable=%d failed=%d\n", st.OK, st.Changed, st.Unreachable, st.Failed)
			}
			mu.Lock()
			j.runs[s.ID].output = o
			mu.Unlock()
			status := "failed"
			switch {
			case cancelled:
				status = "cancelled"
			case hasSt:
				if st.Failed == 0 && st.Unreachable == 0 {
					status = "success"
				}
			case res != nil && res.Code == 0:
				status = "skipped"
			}
			j.set(jobID, s.ID, status, nil)
		}
		status, code := "failed", (*int)(nil)
		if res != nil {
			code = &res.Code
			if res.Code == 0 {
				status = "success"
			}
		}
		if cancelled {
			status = "cancelled"
		}
		j.set(jobID, Ansible, status, code)
	}()
	return jobID, nil
}

// A run whose connection drops mid-script (no exit code) may just be the server rebooting, e.g. after a kernel
// update. Wait for it to come back: an uptime shorter than the run means it rebooted, and the run counts as done.
const rebootWait = 10 * time.Minute

var leadingFloat = regexp.MustCompile(`^\s*[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?`)

// BootedAfter reports whether /proc/uptime output (seconds) says the host booted after since (epoch ms).
func BootedAfter(uptimeOut string, since, now int64) bool {
	up, err := strconv.ParseFloat(strings.TrimSpace(leadingFloat.FindString(uptimeOut)), 64) // like JS parseFloat
	return err == nil && up*1000 < float64(now-since)
}

func rebootedSince(ctx context.Context, server *db.Server, since int64) bool {
	end := time.Now().Add(rebootWait)
	for time.Now().Before(end) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Second):
		}
		res, err := ssh.Exec(ctx, server, "cat /proc/uptime", ssh.ExecOpts{Mode: ssh.ModeUser, Timeout: 15 * time.Second})
		if err == nil && res.Code == 0 {
			return BootedAfter(res.Out, since, db.Now())
		} // else: still down
	}
	return false
}

var sudoNeeded = regexp.MustCompile(`sudo: (a password is required|a terminal is required)`)

func runOne(jobID int64, j *liveJob, server *db.Server, script string, asRoot bool) {
	mu.Lock()
	r := j.runs[server.ID]
	if j.cancelled {
		r.status = "cancelled"
		out := r.output
		j.emit(Msg{"type": "run", "server": server.ID, "status": r.status})
		mu.Unlock()
		saveRun(jobID, server.ID, "cancelled", out, nil)
		return
	}
	mu.Unlock()
	j.set(jobID, server.ID, "running", nil)
	note := func(msg string) { j.data(server.ID, msg) }
	started := db.Now()
	var code *int
	rebooted := false
	mode := ssh.ModeUser
	if asRoot {
		mode = ssh.ModeRoot
	}
	res, err := ssh.Exec(j.ctx, server, script, ssh.ExecOpts{
		Mode:      mode,
		MaxOutput: 1, // output is collected through OnData; JS passed maxOutput 0
		OnData:    func(d string) { j.data(server.ID, j.mask(d)) },
	})
	switch {
	case err != nil:
		if j.ctx.Err() == nil { // a cancelled run just stops, like the JS
			note("[connection error: " + err.Error() + "]\n")
		}
	case res.Code != ssh.NoExit:
		code = &res.Code
	case j.ctx.Err() == nil:
		note(fmt.Sprintf("\n[connection lost before the script finished; waiting up to %d min in case %s is rebooting…]\n", int(rebootWait.Minutes()), server.Name))
		rebooted = rebootedSince(j.ctx, server, started)
		if rebooted {
			note("[" + server.Name + " rebooted and is back up: counting this run as successful. Check the output above to be sure.]\n")
		} else {
			note("[" + server.Name + " did not come back with a fresh boot: the script may not have finished.]\n")
		}
	}
	mu.Lock()
	out := r.output
	cancelled := j.cancelled
	mu.Unlock()
	if (code == nil || *code != 0) && sudoNeeded.MatchString(out) {
		note("[hint: the SSH user needs passwordless sudo, or store a sudo password in the server settings.]\n")
	}
	status := "failed"
	if cancelled {
		status = "cancelled"
	} else if (code != nil && *code == 0) || rebooted {
		status = "success"
	}
	j.set(jobID, server.ID, status, code)
}

// Run is a job_runs row.
type Run struct {
	JobID      int64   `db:"job_id" json:"job_id"`
	ServerID   string  `db:"server_id" json:"server_id"`
	ServerName *string `db:"server_name" json:"server_name"`
	Status     string  `db:"status" json:"status"`
	ExitCode   *int64  `db:"exit_code" json:"exit_code"`
	Output     string  `db:"output" json:"output"`
	StartedAt  *int64  `db:"started_at" json:"started_at"`
	FinishedAt *int64  `db:"finished_at" json:"finished_at"`
}

// Job is a jobs row with its runs; live runs carry their current status and output.
type Job struct {
	ID         int64   `db:"id" json:"id"`
	TenantID   string  `db:"tenant_id" json:"-"`
	ScriptID   *string `db:"script_id" json:"script_id"`
	Name       string  `db:"name" json:"name"`
	Body       string  `db:"body" json:"body"`
	AsRoot     int     `db:"as_root" json:"as_root"`
	Params     Values  `db:"params" json:"params"`
	Status     string  `db:"status" json:"status"`
	Actor      *string `db:"actor" json:"actor"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	FinishedAt *int64  `db:"finished_at" json:"finished_at"`
	Kind       string  `db:"kind" json:"kind"`
	RepoID     *string `db:"repo_id" json:"repo_id"`
	Options    Options `db:"options" json:"options"`
	CommitSHA  *string `db:"commit_sha" json:"commit_sha"`
	ParentJob  *int64  `db:"parent_job" json:"parent_job"`
	NextJob    *int64  `db:"-" json:"next_job"` // the job this one's chain started
	Runs       []Run   `db:"-" json:"runs"`
}

func loadJob(id int64) (*Job, error) {
	j, err := db.Get[Job](`SELECT * FROM jobs WHERE id = ?`, id)
	if err != nil || j == nil {
		return nil, err
	}
	if j.Runs, err = db.Select[Run](`SELECT * FROM job_runs WHERE job_id = ? ORDER BY server_name`, id); err != nil {
		return nil, err
	}
	if j.NextJob, err = db.Get[int64](`SELECT id FROM jobs WHERE parent_job = ? ORDER BY id LIMIT 1`, id); err != nil {
		return nil, err
	}
	if j.Params == nil {
		j.Params = Values{}
	}
	return j, nil
}

func overlay(j *Job, l *liveJob) { // mu held
	for i := range j.Runs {
		if r := l.runs[j.Runs[i].ServerID]; r != nil {
			j.Runs[i].Status, j.Runs[i].Output = r.status, r.output
		}
	}
}

// GetJob loads a job with its runs (nil, nil if missing).
func GetJob(id int64) (*Job, error) {
	mu.Lock()
	defer mu.Unlock()
	j, err := loadJob(id)
	if j != nil && live[id] != nil {
		overlay(j, live[id])
	}
	return j, err
}

// ListOpts are the query parameters of the job list, as strings (coerced like the JS did).
type ListOpts struct {
	Server, Limit, Before, Status string
	Tenant                        string
	Member                        string // a user who sees only jobs and runs on their assigned servers; "" = all
}

// JobSummary is a row of the job list.
type JobSummary struct {
	ID         int64        `db:"id" json:"id"`
	ScriptID   *string      `db:"script_id" json:"script_id"`
	Name       string       `db:"name" json:"name"`
	Kind       string       `db:"kind" json:"kind"`
	Status     string       `db:"status" json:"status"`
	Actor      *string      `db:"actor" json:"actor"`
	CreatedAt  int64        `db:"created_at" json:"created_at"`
	FinishedAt *int64       `db:"finished_at" json:"finished_at"`
	Runs       []RunSummary `db:"-" json:"runs"`
}

type RunSummary struct {
	ServerID   string  `db:"server_id" json:"server_id"`
	ServerName *string `db:"server_name" json:"server_name"`
	Status     string  `db:"status" json:"status"`
	ExitCode   *int64  `db:"exit_code" json:"exit_code"`
}

func ListJobs(o ListOpts) ([]JobSummary, error) {
	lim := 50
	if f, err := strconv.ParseFloat(o.Limit, 64); err == nil && math.Floor(f) != 0 && !math.IsInf(f, 0) {
		lim = int(min(max(math.Floor(f), 1), 200))
	}
	where, args := []string{"tenant_id = ?"}, []any{o.Tenant}
	if o.Member != "" {
		where, args = append(where, "id IN (SELECT job_id FROM job_runs WHERE server_id IN (SELECT server_id FROM server_members WHERE user_id = ?))"), append(args, o.Member)
	}
	if o.Status != "" {
		where, args = append(where, "status = ?"), append(args, o.Status)
	}
	if o.Server != "" {
		where, args = append(where, "id IN (SELECT job_id FROM job_runs WHERE server_id = ?)"), append(args, o.Server)
	}
	if o.Before != "" {
		b, _ := strconv.ParseInt(o.Before, 10, 64) // invalid -> 0 -> nothing, like JS's NaN
		where, args = append(where, "id < ?"), append(args, b)
	}
	q := `SELECT id, script_id, name, kind, status, actor, created_at, finished_at FROM jobs `
	if len(where) > 0 {
		q += "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := db.Select[JobSummary](q+fmt.Sprintf(" ORDER BY id DESC LIMIT %d", lim), args...)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Runs, err = db.Select[RunSummary](`SELECT server_id, server_name, status, exit_code FROM job_runs WHERE job_id = ? AND server_id != '__ansible__'
      AND (? = '' OR server_id IN (SELECT server_id FROM server_members WHERE user_id = ?))`, rows[i].ID, o.Member, o.Member); err != nil {
			return nil, err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range rows {
		if l := live[rows[i].ID]; l != nil {
			for k := range rows[i].Runs {
				if r := l.runs[rows[i].Runs[k].ServerID]; r != nil {
					rows[i].Runs[k].Status = r.status
				}
			}
		}
	}
	return rows, nil
}

// Subscribe streams a live job's events to fn. When the job is live, fn first gets {type:"snapshot", job}
// (taken atomically with subscribing, so nothing is lost in between), then every event; the last one is
// {type:"job"}. ok=false when the job isn't running: send GetJob as the snapshot and end the stream.
// fn is called with the package lock held: it must not block or call back into this package.
func Subscribe(id int64, fn func(Msg)) (unsub func(), ok bool) {
	mu.Lock()
	defer mu.Unlock()
	l := live[id]
	if l == nil {
		return nil, false
	}
	j, err := loadJob(id)
	if err != nil || j == nil {
		return nil, false
	}
	overlay(j, l)
	fn(Msg{"type": "snapshot", "job": j})
	n := l.nextSub
	l.nextSub++
	l.subs[n] = fn
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(l.subs, n)
	}, true
}

// CancelJob stops a running job; false when it isn't running.
func CancelJob(id int64) bool {
	mu.Lock()
	defer mu.Unlock()
	l := live[id]
	if l == nil {
		return false
	}
	l.cancelled = true
	l.cancel()
	return true
}

func IsRunning(id int64) bool {
	mu.Lock()
	defer mu.Unlock()
	return live[id] != nil
}

// ---------- chains ----------

// MaxChain is how many jobs one chain runs at most, the first one included.
const MaxChain = 10

// chain is a job-done listener: it starts the script the finished job's script runs next, if any.
// Cancelled jobs and dry runs never continue. The next step gets its own parameter defaults.
func chain(tenant string, id int64, _, status, _ string, _ []string) {
	if status != "success" && status != "failed" {
		return
	}
	fail := func(err error) {
		db.Event("job", fmt.Sprintf("Job #%d: next script in the chain not started: %s", id, err), db.EventOpts{Level: "error", TenantID: tenant})
	}
	j, err := loadJob(id)
	if err != nil || j == nil || j.ScriptID == nil || (j.Options.Check != nil && *j.Options.Check) {
		return
	}
	sc, err := ScriptFor(tenant, *j.ScriptID) // as it is now: an edit since the start counts
	if err != nil || sc == nil || sc.NextScript == nil {
		return
	}
	if sc.NextWhen != "always" && sc.NextWhen != map[string]string{"success": "success", "failed": "failure"}[status] {
		return
	}
	depth := 1
	for p := j.ParentJob; p != nil && depth < MaxChain; depth++ {
		up, err := db.Get[*int64](`SELECT parent_job FROM jobs WHERE id = ?`, *p)
		if err != nil {
			fail(err)
			return
		}
		if p = nil; up != nil {
			p = *up
		}
	}
	if depth >= MaxChain {
		fail(fmt.Errorf("stopped after %d steps", MaxChain))
		return
	}
	next, err := ScriptFor(tenant, *sc.NextScript)
	if err != nil || next == nil {
		fail(cmp.Or(err, errors.New("the script no longer exists")))
		return
	}
	params, err := next.Resolve(Values{})
	if err != nil {
		fail(err)
		return
	}
	servers := []string{}
	for _, r := range j.Runs {
		if r.ServerID != Ansible && (!sc.NextOKOnly || r.Status == "success") {
			servers = append(servers, r.ServerID)
		}
	}
	if len(servers) == 0 {
		fail(errors.New("no server succeeded"))
		return
	}
	sp := SpecFromScript(next, params, false)
	sp.Targets, sp.Actor, sp.TenantID, sp.Parent = Targets{Servers: servers, Tags: []string{}}, fmt.Sprintf("chain from #%d", id), tenant, id
	if _, err := StartJob(sp); err != nil {
		fail(err)
	}
}

// ---------- cron ----------

var macros = map[string]string{"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *"}
var limits = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
var cronItem = regexp.MustCompile(`^(\*|(\d+)(?:-(\d+))?)(?:/(\d+))?$`)

// ParseCron parses a 5-field cron expression or @macro into a matcher (local time, minute resolution).
// Day-of-month and day-of-week are ORed when both are restricted; 7 is Sunday too.
func ParseCron(expr string) (func(time.Time) bool, error) {
	src := strings.TrimSpace(expr)
	parts := strings.Fields(cmp.Or(macros[src], src))
	if len(parts) != 5 {
		return nil, httperr.Bad("Cron needs 5 fields: minute hour day-of-month month day-of-week")
	}
	var sets [5]uint64
	for i, p := range parts {
		lo, hi := limits[i][0], limits[i][1]
		for _, item := range strings.Split(p, ",") {
			m := cronItem.FindStringSubmatch(item)
			if m == nil {
				return nil, httperr.Bad(fmt.Sprintf("Invalid cron field \"%s\"", item))
			}
			num := func(s string) int { // huge numbers end up out of range, as in JS
				n, err := strconv.Atoi(s)
				if err != nil {
					return math.MaxInt32
				}
				return n
			}
			a, b, step := lo, hi, 1
			if m[1] != "*" {
				a = num(m[2])
				switch {
				case m[3] != "":
					b = num(m[3])
				case m[4] == "":
					b = a
				}
			}
			if m[4] != "" {
				step = num(m[4])
			}
			if a < lo || b > hi || a > b || step < 1 {
				return nil, httperr.Bad(fmt.Sprintf("Cron field \"%s\" is out of range %d-%d", item, lo, hi))
			}
			for v := a; v <= b; v += step {
				if i == 4 && v == 7 {
					sets[i] |= 1 // 7 = Sunday = 0
				} else {
					sets[i] |= 1 << v
				}
			}
		}
	}
	has := func(i, v int) bool { return sets[i]&(1<<v) != 0 }
	domAny, dowAny := parts[2] == "*", parts[4] == "*"
	return func(d time.Time) bool {
		if !has(0, d.Minute()) || !has(1, d.Hour()) || !has(3, int(d.Month())) {
			return false
		}
		if domAny || dowAny {
			return has(2, d.Day()) && has(4, int(d.Weekday()))
		}
		return has(2, d.Day()) || has(4, int(d.Weekday()))
	}, nil
}

// NextRuns returns the next n matching minutes after from, as epoch ms (searching at most a year ahead).
func NextRuns(expr string, n int, from time.Time) ([]int64, error) {
	match, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	d := from.Truncate(time.Minute)
	out := []int64{}
	for i := 0; i < 366*1440 && len(out) < n; i++ {
		d = d.Add(time.Minute)
		if match(d) {
			out = append(out, d.UnixMilli())
		}
	}
	return out, nil
}

// ScheduleInput is the request body of schedules and webhook triggers. For an update, prefill it from the
// existing row, then decode the body over it (the JS merged {...view(old), ...body}).
type ScheduleInput struct {
	Name     string   `json:"name"`
	ScriptID string   `json:"script_id"`
	Cron     string   `json:"cron"`
	Targets  *Targets `json:"targets"`
	Params   Values   `json:"params"`
	Enabled  *bool    `json:"enabled"` // nil = true
	Once     bool     `json:"once"`
	RunAt    *int64   `json:"run_at"` // one-off at this time (ms): sets Cron and Once
}

// ScheduleFields are the validated columns; Cron is nil for triggers.
type ScheduleFields struct {
	Name     string
	ScriptID string
	Cron     *string
	Targets  Targets
	Params   Values
	Enabled  int
	Once     int
}

// ValidateSchedule validates a schedule; withCron=false for webhook triggers, which share the shape but have no cron.
func ValidateSchedule(tenant string, b ScheduleInput, withCron bool) (*ScheduleFields, error) {
	name := strings.TrimSpace(b.Name)
	if name == "" || jsLen(name) > 100 {
		return nil, httperr.Bad("name required")
	}
	sc, err := ScriptFor(tenant, b.ScriptID)
	if sc == nil || err != nil {
		return nil, cmp.Or(err, httperr.Bad("unknown script"))
	}
	out := &ScheduleFields{Name: name, ScriptID: b.ScriptID, Params: Values{}, Enabled: 1}
	for k, v := range b.Params { // empty means "use the default", now and when the script's default changes
		if v != "" && slices.ContainsFunc(sc.Params, func(p Param) bool { return p.Name == k }) {
			out.Params[k] = v
		}
	}
	if _, err := sc.Resolve(out.Params); err != nil {
		return nil, err
	}
	if b.RunAt != nil && withCron {
		// Cron in the Fleetdeck host's time zone, so the browser's zone doesn't matter.
		t := time.UnixMilli(*b.RunAt).Local().Truncate(time.Minute)
		if !t.After(time.Now()) {
			return nil, httperr.Bad("pick a time in the future")
		}
		b.Cron, b.Once = fmt.Sprintf("%d %d %d %d *", t.Minute(), t.Hour(), t.Day(), int(t.Month())), true
	}
	if b.Once {
		out.Once = 1
	}
	if withCron {
		if _, err := ParseCron(b.Cron); err != nil {
			return nil, err
		}
		c := strings.TrimSpace(b.Cron)
		out.Cron = &c
	}
	if b.Targets != nil {
		out.Targets = Targets{All: b.Targets.All, Servers: b.Targets.Servers, Tags: b.Targets.Tags}
	}
	if out.Targets.Servers == nil {
		out.Targets.Servers = []string{}
	}
	if out.Targets.Tags == nil {
		out.Targets.Tags = []string{}
	}
	if !out.Targets.All && len(out.Targets.Servers) == 0 && len(out.Targets.Tags) == 0 {
		return nil, httperr.Bad("pick at least one target")
	}
	if b.Enabled != nil && !*b.Enabled {
		out.Enabled = 0
	}
	return out, nil
}

// Schedule is a schedules row (JSON: targets/params decoded, enabled as bool).
type Schedule struct {
	ID        string  `db:"id" json:"id"`
	TenantID  string  `db:"tenant_id" json:"-"`
	Name      string  `db:"name" json:"name"`
	ScriptID  string  `db:"script_id" json:"script_id"`
	Cron      string  `db:"cron" json:"cron"`
	Targets   Targets `db:"targets" json:"targets"`
	Params    Values  `db:"params" json:"params"`
	Enabled   bool    `db:"enabled" json:"enabled"`
	Once      bool    `db:"once" json:"once"`
	LastRun   *int64  `db:"last_run" json:"last_run"`
	LastJob   *int64  `db:"last_job" json:"last_job"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
}

// Trigger is a triggers (webhook) row; the token hash never leaves the server.
type Trigger struct {
	ID        string  `db:"id" json:"id"`
	TenantID  string  `db:"tenant_id" json:"-"`
	Name      string  `db:"name" json:"name"`
	ScriptID  string  `db:"script_id" json:"script_id"`
	Targets   Targets `db:"targets" json:"targets"`
	Params    Values  `db:"params" json:"params"`
	TokenHash string  `db:"token_hash" json:"-"`
	Enabled   bool    `db:"enabled" json:"enabled"`
	LastRun   *int64  `db:"last_run" json:"last_run"`
	LastJob   *int64  `db:"last_job" json:"last_job"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
}

// Start a saved schedule or trigger row and record it as its last run.
func runSaved(tenant, table, id, name, scriptID string, params Values, targets Targets, actor string) (int64, error) {
	script, err := ScriptFor(tenant, scriptID)
	if err != nil {
		return 0, err
	}
	if script == nil {
		return 0, errors.New("unknown script")
	}
	given := Values{}
	for k, v := range params {
		if v != "" { // saved before empty values were dropped: fall back to the default
			given[k] = v
		}
	}
	merged, err := script.Resolve(given)
	if err != nil {
		return 0, err
	}
	sp := SpecFromScript(script, merged, false)
	sp.Name, sp.Targets, sp.Actor, sp.TenantID = script.Name+" · "+name, targets, actor, tenant
	jobID, err := StartJob(sp)
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(`UPDATE `+table+` SET last_run = ?, last_job = ? WHERE id = ?`, db.Now(), jobID, id)
	return jobID, err
}

// RunSchedule starts a schedule now; actor "" = "schedule: <name>".
func RunSchedule(s *Schedule, actor string) (int64, error) {
	return runSaved(s.TenantID, "schedules", s.ID, s.Name, s.ScriptID, s.Params, s.Targets, cmp.Or(actor, "schedule: "+s.Name))
}

// RunTrigger starts a webhook's script.
func RunTrigger(t *Trigger) (int64, error) {
	return runSaved(t.TenantID, "triggers", t.ID, t.Name, t.ScriptID, t.Params, t.Targets, "webhook: "+t.Name)
}

func tick() {
	defer guard("schedules")
	now := time.Now().Truncate(time.Minute)
	schedules, err := db.Select[Schedule](`SELECT * FROM schedules WHERE enabled = 1`)
	if err != nil {
		log.Printf("schedules: %v", err)
		return
	}
	for i := range schedules {
		s := &schedules[i]
		match, err := ParseCron(s.Cron)
		if err == nil && !match(now) {
			continue
		}
		if err == nil {
			// No pile-ups: a slow or hung run blocks the next one instead of stacking more jobs on the same hosts.
			if s.LastJob != nil && IsRunning(*s.LastJob) {
				db.Event("schedule", fmt.Sprintf("Schedule \"%s\" skipped: job #%d from its previous run is still running", s.Name, *s.LastJob), db.EventOpts{Level: "warn", TenantID: s.TenantID})
				continue
			}
			_, err = RunSchedule(s, "")
			if err == nil && s.Once {
				// ponytail: a one-off missed while Fleetdeck was down waits for the same date next year; delete it then.
				_, err = db.Exec(`DELETE FROM schedules WHERE id = ?`, s.ID)
			}
		}
		if err != nil {
			db.Event("schedule", fmt.Sprintf("Schedule \"%s\" failed to start: %s", s.Name, err), db.EventOpts{Level: "error", TenantID: s.TenantID})
		}
	}
}

func untilNextMinute() time.Duration {
	ms := time.Now().UnixMilli()
	return time.Duration(60_000-ms%60_000+100) * time.Millisecond
}

func retentionDays(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(db.Setting(key, ""), 64); err == nil {
		return v
	}
	return fallback
}

func cleanup() {
	defer guard("retention")
	now := float64(db.Now())
	if _, err := db.Exec(`DELETE FROM jobs WHERE created_at < ?`, now-retentionDays("retention_jobs_days", 90)*864e5); err != nil {
		log.Printf("retention: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM events WHERE t < ?`, now-retentionDays("retention_events_days", 180)*864e5); err != nil {
		log.Printf("retention: %v", err)
	}
}

// Start seeds the built-in scripts, marks jobs a restart interrupted, and starts the cron loop and the hourly
// job/event retention cleanup.
func Start() error {
	if err := Seed(); err != nil {
		return err
	}
	OnJobDone(chain)
	if _, err := db.Exec(`UPDATE job_runs SET status = 'interrupted' WHERE status IN ('running', 'queued')`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE jobs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, db.Now()); err != nil {
		return err
	}
	go func() {
		for {
			time.Sleep(untilNextMinute())
			tick()
		}
	}()
	go func() {
		for range time.Tick(time.Hour) {
			cleanup()
		}
	}()
	return nil
}
