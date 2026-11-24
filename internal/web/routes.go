package web

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"

	"fleetdeck/internal/admin"
	"fleetdeck/internal/alerts"
	"fleetdeck/internal/checks"
	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/jobs"
	"fleetdeck/internal/probe"
	"fleetdeck/internal/repos"
	"fleetdeck/internal/sec"
	"fleetdeck/internal/ssh"
)

// J is a JSON object built inline.
type J = map[string]any

var okResp = J{"ok": true}

func exec(q string, args ...any) error { _, err := db.Exec(q, args...); return err }

func notFound(what string) error { return httperr.New(404, what+" not found") }

// ---------- validation ----------

// ServerFields are the validated columns of a server.
type ServerFields struct {
	Name   string  `json:"name"`
	Host   string  `json:"host"`
	User   string  `json:"user"`
	Port   int     `json:"port"`
	KeyID  *string `json:"key_id"`
	Tags   string  `json:"tags"`
	Notes  string  `json:"notes"`
	Paused int     `json:"paused"`
}

var (
	hostRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]*$`)
	userRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]*$`)
	tagRE  = regexp.MustCompile(`^[a-z0-9_.-]{1,32}$`)
)

// ValidateServer checks a server from a request body (decoded JSON, coerced like the JS did).
// Host and user are allowlisted: they end up on the ssh command line of Ansible runs.
func ValidateServer(tenant string, body map[string]any) (*ServerFields, error) {
	if body == nil {
		return nil, httperr.Bad("invalid body")
	}
	b := obj(body)
	name, host := strings.TrimSpace(b.str("name")), strings.TrimSpace(b.str("host"))
	user := strings.TrimSpace(b.strOr("user", "root"))
	port := float64(22)
	if b.truthy("port") {
		port = jsNumber(b["port"])
	}
	if name == "" || jsLen(name) > 64 {
		return nil, httperr.Bad("Name is required (max 64 characters)")
	}
	if !hostRE.MatchString(host) || len(host) > 253 {
		return nil, httperr.Bad("Host must be a hostname or IP address")
	}
	if !userRE.MatchString(user) || len(user) > 32 {
		return nil, httperr.Bad("Invalid user name")
	}
	if !isInteger(port) || port < 1 || port > 65535 {
		return nil, httperr.Bad("Port must be 1-65535")
	}
	var keyID *string
	if b.truthy("key_id") {
		k := jsString(b["key_id"])
		if ok, err := db.Get[int](`SELECT 1 FROM keys WHERE id = ? AND tenant_id = ?`, k, tenant); err != nil || ok == nil {
			return nil, cmpErr(err, httperr.Bad("Selected key does not exist"))
		}
		keyID = &k
	}
	var raw []string
	if arr, ok := b["tags"].([]any); ok {
		for _, t := range arr {
			raw = append(raw, jsString(t))
		}
	} else {
		raw = strings.Split(b.str("tags"), ",")
	}
	var tags []string
	for _, t := range raw {
		if t = strings.ToLower(strings.TrimSpace(t)); t == "" {
			continue
		}
		if !tagRE.MatchString(t) {
			return nil, httperr.Bad(fmt.Sprintf(`Invalid tag "%s" (letters, digits, - _ .)`, t))
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	paused := 0
	if b.truthy("paused") {
		paused = 1
	}
	return &ServerFields{Name: name, Host: host, User: user, Port: int(port), KeyID: keyID, Tags: strings.Join(tags, ","),
		Notes: jsSlice(b.str("notes"), 4000), Paused: paused}, nil
}

func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// sudoField: keep = leave it as is; otherwise enc is the new encrypted value (nil = remove).
func sudoField(b obj) (enc *string, keep bool, err error) {
	if b.truthy("clear_sudo_password") {
		return nil, false, nil
	}
	pw, _ := b["sudo_password"].(string)
	if pw == "" {
		return nil, true, nil
	}
	if jsLen(pw) > 1024 || strings.ContainsAny(pw, "\r\n") {
		return nil, false, httperr.Bad("Invalid sudo password")
	}
	e := sec.Encrypt(pw)
	return &e, false, nil
}

func scriptInput(b obj) jobs.ScriptInput {
	in := jobs.ScriptInput{Name: b.str("name"), Description: b.str("description"), Body: b.str("body"), RepoID: b.str("repo_id"), Playbook: b.str("playbook"),
		NextScript: b.str("next_script_id"), NextWhen: b.strOr("next_when", "success"), NextOKOnly: b.truthy("next_ok_only")}
	if b["kind"] == "ansible" {
		in.Kind = "ansible"
	}
	if v, ok := b["as_root"]; ok && (v == false || v == 0.0) {
		in.AsRoot = new(false)
	}
	ps, _ := b["params"].([]any)
	for _, x := range ps {
		p, _ := x.(map[string]any)
		def := ""
		if v := p["default"]; v != nil {
			def = jsString(v)
		}
		q := jobs.Param{Name: obj(p).str("name"), Label: obj(p).str("label"), Default: def, Required: truthy(p["required"]), Type: obj(p).str("type")}
		opts, _ := p["options"].([]any)
		for _, o := range opts {
			q.Options = append(q.Options, jsString(o))
		}
		in.Params = append(in.Params, q)
	}
	vs, _ := b["vars"].([]any)
	for _, v := range vs {
		in.Vars = append(in.Vars, jsString(v))
	}
	return in
}

func targetsOf(v any) jobs.Targets {
	t, _ := v.(map[string]any)
	strs := func(x any) []string {
		out := []string{}
		arr, _ := x.([]any)
		for _, e := range arr {
			out = append(out, jsString(e))
		}
		return out
	}
	return jobs.Targets{All: truthy(t["all"]), Servers: strs(t["servers"]), Tags: strs(t["tags"])}
}

// valuesOf stringifies parameter values like jobs.Values does when it decodes JSON.
func valuesOf(v any) jobs.Values {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	out := jobs.Values{}
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		} else {
			data, _ := json.Marshal(x)
			out[k] = string(data)
		}
	}
	return out
}

func scheduleInput(b obj) jobs.ScheduleInput {
	t := targetsOf(b["targets"])
	in := jobs.ScheduleInput{Name: b.str("name"), ScriptID: b.str("script_id"), Cron: b.str("cron"), Targets: &t, Params: valuesOf(b["params"])}
	if b["enabled"] == false {
		in.Enabled = new(false)
	}
	in.Once = b["once"] == true
	if v, ok := b["run_at"].(float64); ok {
		in.RunAt = new(int64(v))
	}
	return in
}

func variableInput(b obj) jobs.VariableInput {
	in := jobs.VariableInput{Name: b.str("name"), Description: b.str("description")}
	if v := b["value"]; v != nil {
		in.Value = new(jsString(v))
	}
	if v, ok := b["secret"]; ok {
		in.Secret = new(truthy(v))
	}
	return in
}

func repoInput(b obj) repos.RepoInput {
	return repos.RepoInput{Name: b.str("name"), URL: b.str("url"), Branch: b.str("branch"), Auth: b.str("auth"),
		KeyID: b.str("key_id"), Username: b.str("username"), Token: b.str("token")}
}

// ---------- views ----------

type idName struct {
	ID   string `db:"id" json:"id"`
	Name string `db:"name" json:"name"`
}

type firing struct {
	Message string `db:"message" json:"message"`
	Since   int64  `db:"since" json:"since"`
	Metric  string `db:"metric" json:"metric"`
}

type serverOut struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Host            string            `json:"host"`
	Port            int               `json:"port"`
	User            string            `json:"user"`
	KeyID           *string           `json:"key_id"`
	Tags            []string          `json:"tags"`
	Notes           string            `json:"notes"`
	Paused          bool              `json:"paused"`
	HostKey         *string           `json:"host_key"`
	CreatedAt       int64             `json:"created_at"`
	Status          string            `json:"status"`
	Snapshot        json.RawMessage   `json:"snapshot"`
	Error           *probe.ProbeError `json:"error"`
	SSH             ssh.Status        `json:"ssh"`
	HasSudoPassword bool              `json:"has_sudo_password"`
	Alerts          int               `json:"alerts"`
	Firing          []firing          `json:"firing"`
	Spark           any               `json:"spark,omitempty"`
	Traffic         *probe.Totals     `json:"traffic,omitempty"`
}

func serverView(s *db.Server, spark any) (*serverOut, error) {
	out := &serverOut{ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port, User: s.User, KeyID: s.KeyID, Tags: []string{}, Notes: s.Notes,
		Paused: s.Paused != 0, HostKey: s.HostKey, CreatedAt: s.CreatedAt, Snapshot: json.RawMessage("null"), Error: probe.LastError(s.ID),
		SSH: ssh.StatusOf(s), HasSudoPassword: db.Str(s.SudoEnc) != "", Spark: spark}
	fresh := false
	if js := db.Str(s.LastJSON); js != "" {
		var snap struct{ T *float64 }
		if err := json.Unmarshal([]byte(js), &snap); err != nil {
			return nil, err
		}
		out.Snapshot = json.RawMessage(js)
		fresh = snap.T != nil && float64(db.Now())-*snap.T < 3*60e3
	}
	if s.Tags != "" {
		out.Tags = strings.Split(s.Tags, ",")
	}
	switch {
	case out.Paused:
		out.Status = "paused"
	case out.Error != nil:
		out.Status = "down"
	case fresh:
		out.Status = "up"
	default:
		out.Status = "unknown"
	}
	var err error
	out.Firing, err = db.Select[firing](`SELECT a.message, a.since, r.metric FROM alert_state a JOIN alert_rules r ON r.id = a.rule_id WHERE a.server_id = ? ORDER BY a.since`, s.ID)
	out.Alerts = len(out.Firing)
	return out, err
}

// getServer reloads a server the request already checked with c.server.
func getServer(id string) (*db.Server, error) {
	s, err := db.GetServer(id)
	if s == nil && err == nil {
		err = notFound("Server")
	}
	return s, err
}

type keyRow struct {
	ID            string  `db:"id"`
	TenantID      string  `db:"tenant_id" json:"-"`
	Name          string  `db:"name"`
	PublicKey     string  `db:"public_key"`
	PrivateEnc    string  `db:"private_enc"`
	PassphraseEnc *string `db:"passphrase_enc"`
	CreatedAt     int64   `db:"created_at"`
}

func keyView(k keyRow) (any, error) {
	used, err := db.Select[idName](`SELECT id, name FROM servers WHERE key_id = ?`, k.ID)
	return J{"id": k.ID, "name": k.Name, "public_key": k.PublicKey, "created_at": k.CreatedAt, "has_passphrase": db.Str(k.PassphraseEnc) != "", "used_by": used}, err
}

func keyViewByID(tenant, id string) (any, error) {
	k, err := db.Get[keyRow](`SELECT * FROM keys WHERE id = ? AND tenant_id = ?`, id, tenant)
	if k == nil || err != nil {
		return nil, cmpErr(err, notFound("Key"))
	}
	return keyView(*k)
}

// scriptOut is a script with whether it is from the shared library (read-only for organizations).
type scriptOut struct {
	*jobs.Script
	Shared bool `json:"shared"`
}

func scriptView(tenant, id string) (*scriptOut, error) {
	sc, err := jobs.ScriptFor(tenant, id)
	if sc == nil || err != nil {
		return nil, cmpErr(err, notFound("Script"))
	}
	return &scriptOut{sc, sc.TenantID == nil}, nil
}

type scheduleOut struct {
	jobs.Schedule
	ScriptName *string `db:"script_name" json:"script_name,omitempty"`
	Next       *int64  `db:"-" json:"next"`
}

// schedules lists the organization's schedules; rest goes after "WHERE x.tenant_id = ?" (AND …, ORDER BY …).
func schedules(tenant, rest string, args ...any) ([]scheduleOut, error) {
	list, err := db.Select[scheduleOut](`SELECT x.*, s.name AS script_name FROM schedules x LEFT JOIN scripts s ON s.id = x.script_id WHERE x.tenant_id = ? `+rest,
		append([]any{tenant}, args...)...)
	for i := range list {
		if next, err := jobs.NextRuns(list[i].Cron, 1, time.Now()); err == nil && len(next) > 0 {
			list[i].Next = &next[0]
		}
	}
	return list, err
}

func scheduleView(tenant, id string) (any, error) {
	list, err := schedules(tenant, `AND x.id = ?`, id)
	if len(list) == 0 || err != nil {
		return nil, cmpErr(err, notFound("Schedule"))
	}
	return list[0], nil
}

type triggerOut struct {
	jobs.Trigger
	ScriptName *string `db:"script_name" json:"script_name,omitempty"`
}

func triggers(tenant, rest string, args ...any) ([]triggerOut, error) {
	return db.Select[triggerOut](`SELECT x.*, s.name AS script_name FROM triggers x LEFT JOIN scripts s ON s.id = x.script_id WHERE x.tenant_id = ? `+rest,
		append([]any{tenant}, args...)...)
}

func getTrigger(tenant, id string) (*jobs.Trigger, error) {
	t, err := db.Get[jobs.Trigger](`SELECT * FROM triggers WHERE id = ? AND tenant_id = ?`, id, tenant)
	if t == nil && err == nil {
		err = notFound("Webhook")
	}
	return t, err
}

type ruleOut struct {
	alerts.Rule
	Enabled    bool    `db:"-" json:"enabled"`
	ServerName *string `db:"server_name" json:"server_name"`
	CustomName *string `db:"custom_name" json:"custom_name"`
	CustomUnit *string `db:"custom_unit" json:"custom_unit"`
}

func rules(tenant, rest string, args ...any) ([]ruleOut, error) {
	list, err := db.Select[ruleOut](`SELECT r.*, s.name AS server_name, c.name AS custom_name, c.unit AS custom_unit FROM alert_rules r
    LEFT JOIN servers s ON s.id = r.server_id LEFT JOIN custom_metrics c ON c.id = r.custom_id WHERE r.tenant_id = ? `+rest, append([]any{tenant}, args...)...)
	for i := range list {
		list[i].Enabled = list[i].Rule.Enabled != 0
	}
	return list, err
}

func ruleView(tenant, id string) (any, error) {
	list, err := rules(tenant, `AND r.id = ?`, id)
	if len(list) == 0 || err != nil {
		return nil, cmpErr(err, notFound("Rule"))
	}
	return list[0], nil
}

type muteOut struct {
	alerts.Mute
	Active     bool    `db:"-" json:"active"`
	ServerName *string `db:"server_name" json:"server_name"`
}

func mutes(tenant, rest string, args ...any) ([]muteOut, error) {
	list, err := db.Select[muteOut](`SELECT w.*, s.name AS server_name FROM mute_windows w LEFT JOIN servers s ON s.id = w.server_id WHERE w.tenant_id = ? `+rest,
		append([]any{tenant}, args...)...)
	for i := range list {
		list[i].Active = alerts.InWindow(&list[i].Mute, time.Now())
	}
	return list, err
}

// Secret values never leave the server; plain ones are shown so they can be edited.
func variableView(v *jobs.Variable) (any, error) {
	used, err := varUsers(v.TenantID, v.Name)
	if err != nil {
		return nil, err
	}
	var value *string
	if !v.Secret {
		plain, err := sec.Decrypt(v.ValueEnc)
		if err != nil {
			return nil, err
		}
		value = &plain
	}
	return J{"id": v.ID, "name": v.Name, "secret": v.Secret, "description": v.Description, "created_at": v.CreatedAt, "value": value, "used_by": used}, nil
}

func varUsers(tenant, name string) ([]idName, error) {
	return db.Select[idName](`SELECT id, name FROM scripts WHERE tenant_id = ? AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(scripts.vars::jsonb) v WHERE v = ?) ORDER BY name`, tenant, name)
}

func getVariable(tenant, id string) (*jobs.Variable, error) {
	v, err := db.Get[jobs.Variable](`SELECT * FROM variables WHERE id = ? AND tenant_id = ?`, id, tenant)
	if v == nil && err == nil {
		err = notFound("Variable")
	}
	return v, err
}

func getRepo(tenant, id string) (*repos.Repo, error) {
	r, err := repos.GetRepo(id)
	if (r == nil || r.TenantID != tenant) && err == nil {
		return nil, notFound("Repository")
	}
	return r, err
}

func names(list []idName) string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.Name
	}
	return strings.Join(out, ", ")
}

func jobID(c *ctx) (int64, bool) {
	id, err := strconv.ParseInt(c.param("id"), 10, 64)
	return id, err == nil
}

// journal reads a server's journal; a variable so tests can fake the servers.
var journal = admin.Logs

// localTZ is the IANA name of the host's time zone (daily quiet hours use local time).
var localTZ = sync.OnceValue(func() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if i := strings.Index(tz, "zoneinfo/"); i >= 0 {
			tz = tz[i+len("zoneinfo/"):]
		}
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	return "UTC"
})

// qrDataURI draws text as an SVG QR code (2-module border), like uqr's renderSVG.
func qrDataURI(text string) (string, error) {
	c, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", err
	}
	var d strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.Black(x, y) {
				continue
			}
			n := 1
			for x+n < c.Size && c.Black(x+n, y) {
				n++
			}
			fmt.Fprintf(&d, "M%d %dh%dv1h-%dz", x+2, y+2, n, n)
			x += n - 1
		}
	}
	w := c.Size + 4
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d"><rect fill="white" width="%d" height="%d"/><path fill="black" d="%s"/></svg>`, w, w, w, w, d.String())
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)), nil
}

var nonWord = regexp.MustCompile(`[^\w.-]`)

// Remote actions keep running when the browser goes away, like in the JS server.
func detached(c *ctx) context.Context { return context.WithoutCancel(c.r.Context()) }

// ---------- routes ----------

func (s *Server) routes() {
	s.tenancyRoutes()
	s.ssoRoutes()
	s.passkeyRoutes()
	s.serverRoutes()
	s.libraryRoutes()
	s.jobRoutes()
	s.alertRoutes()
}

func (s *Server) serverRoutes() {
	s.route("GET /api/servers", func(c *ctx) (any, error) {
		sp, err := probe.Sparks()
		if err != nil {
			return nil, err
		}
		tr, err := probe.Traffic()
		if err != nil {
			return nil, err
		}
		list, err := c.visibleServers("")
		if err != nil {
			return nil, err
		}
		out := make([]*serverOut, len(list))
		for i := range list {
			spark := sp[list[i].ID]
			if spark == nil {
				spark = [][3]any{}
			}
			if out[i], err = serverView(&list[i], spark); err != nil {
				return nil, err
			}
			if t, ok := tr[list[i].ID]; ok {
				out[i].Traffic = &t
			}
		}
		return out, nil
	})

	s.route("POST /api/servers", func(c *ctx) (any, error) {
		if c.level() < roleOwner {
			return nil, httperr.New(403, "Only owners and admins can add servers")
		}
		v, err := ValidateServer(c.tenant, c.body)
		if err != nil {
			return nil, err
		}
		sudo, _, err := sudoField(c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO servers (id, tenant_id, name, host, port, "user", key_id, tags, notes, paused, created_at, sudo_enc) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.Host, v.Port, v.User, v.KeyID, v.Tags, v.Notes, v.Paused, db.Now(), sudo); err != nil {
			return nil, err
		}
		if c.level() < roleAdmin { // owners own what they add
			if err := exec(`INSERT INTO server_members (server_id, user_id) VALUES (?, ?)`, id, c.user.ID); err != nil {
				return nil, err
			}
		}
		c.audit(fmt.Sprintf("Added server %s (%s@%s:%d)", v.Name, v.User, v.Host, v.Port), id)
		srv, err := getServer(id)
		if err != nil {
			return nil, err
		}
		if srv.Paused == 0 {
			go probe.Probe(srv)
		}
		return serverView(srv, nil)
	})

	s.route("GET /api/servers/{id}", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleViewer)
		if err != nil {
			return nil, err
		}
		return serverView(srv, nil)
	})

	s.route("PUT /api/servers/{id}", func(c *ctx) (any, error) {
		old, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		v, err := ValidateServer(c.tenant, merge(toObj(old), c.body))
		if err != nil {
			return nil, err
		}
		// Checked before anything is saved (the JS saved the server first, then rejected a bad sudo password).
		sudo, keepSudo, err := sudoField(c.body)
		if err != nil {
			return nil, err
		}
		moved := old.Host != v.Host || old.Port != v.Port
		hostKey, hostKeyRaw := old.HostKey, old.HostKeyRaw
		if moved {
			hostKey, hostKeyRaw = nil, nil
		}
		if err := exec(`UPDATE servers SET name = ?, host = ?, port = ?, "user" = ?, key_id = ?, tags = ?, notes = ?, paused = ?, host_key = ?, host_key_raw = ? WHERE id = ?`,
			v.Name, v.Host, v.Port, v.User, v.KeyID, v.Tags, v.Notes, v.Paused, hostKey, hostKeyRaw, old.ID); err != nil {
			return nil, err
		}
		if !keepSudo {
			if err := exec(`UPDATE servers SET sudo_enc = ? WHERE id = ?`, sudo, old.ID); err != nil {
				return nil, err
			}
			verb := "Removed"
			if sudo != nil {
				verb = "Set"
			}
			c.audit(fmt.Sprintf("%s the sudo password of %s", verb, v.Name), old.ID)
		}
		if v.Paused != 0 {
			if err := exec(`DELETE FROM alert_state WHERE server_id = ?`, old.ID); err != nil {
				return nil, err
			}
			probe.ClearError(old.ID)
		}
		// Reconnect only when the login changes: dropping the connection kills running jobs and open terminals.
		if moved || old.User != v.User || db.Str(old.KeyID) != db.Str(v.KeyID) {
			ssh.Drop(old.ID)
		}
		note := ""
		if moved {
			note = " (address changed, host key will be re-pinned)"
		}
		c.audit(fmt.Sprintf("Updated server %s%s", v.Name, note), old.ID)
		srv, err := getServer(old.ID)
		if err != nil {
			return nil, err
		}
		if srv.Paused == 0 {
			go probe.Probe(srv)
		}
		return serverView(srv, nil)
	})

	s.route("DELETE /api/servers/{id}", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		ssh.Drop(srv.ID)
		if err := db.Tx(func(tx db.Txn) error {
			for _, q := range []string{`DELETE FROM metrics WHERE server_id = ?`, `DELETE FROM container_metrics WHERE server_id = ?`, `DELETE FROM custom_values WHERE server_id = ?`, `DELETE FROM servers WHERE id = ?`} {
				if _, err := tx.Exec(q, srv.ID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		probe.ClearError(srv.ID)
		c.audit("Removed server "+srv.Name, "")
		return okResp, nil
	})

	s.route("POST /api/servers/{id}/probe", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOperator)
		if err != nil {
			return nil, err
		}
		probe.Probe(srv)
		if srv, err = getServer(srv.ID); err != nil {
			return nil, err
		}
		return serverView(srv, nil)
	})

	s.route("POST /api/servers/{id}/test", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOperator)
		if err != nil {
			return nil, err
		}
		res, err := ssh.Exec(c.r.Context(), srv, `id -un; if [ "$(id -u)" = 0 ]; then echo root; elif sudo -n true 2>/dev/null; then echo sudo; else echo none; fi`,
			ssh.ExecOpts{Mode: ssh.ModeUser, Timeout: 20 * time.Second})
		if err != nil {
			return J{"ok": false, "error": err.Error()}, nil
		}
		lines := strings.Split(strings.TrimSpace(res.Out), "\n")
		out := J{"ok": true, "user": lines[0]}
		if len(lines) > 1 {
			priv := lines[1]
			if priv == "none" && db.Str(srv.SudoEnc) != "" {
				fresh, err := getServer(srv.ID)
				if err != nil {
					return J{"ok": false, "error": err.Error()}, nil
				}
				r, err := ssh.Exec(c.r.Context(), fresh, "id -u", ssh.ExecOpts{Mode: ssh.ModeRoot, Timeout: 20 * time.Second})
				if err != nil {
					return J{"ok": false, "error": err.Error()}, nil
				}
				rl := strings.Split(strings.TrimSpace(r.Out), "\n")
				priv = "wrong-password"
				if r.Code == 0 && rl[len(rl)-1] == "0" {
					priv = "password"
				}
			}
			out["privilege"] = priv
		}
		fresh, err := getServer(srv.ID)
		if err != nil {
			return J{"ok": false, "error": err.Error()}, nil
		}
		out["host_key"] = fresh.HostKey
		return out, nil
	})

	s.route("POST /api/servers/{id}/hostkey/accept", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		fp, ok := ssh.AcceptHostKey(srv.ID)
		if !ok {
			return nil, httperr.Bad("No pending host key change for this server")
		}
		db.Event("security", fmt.Sprintf("Accepted new host key %s for %s", fp, srv.Name), db.EventOpts{Level: "warn", Actor: c.actor(), IP: c.ip, ServerID: srv.ID})
		go probe.Probe(srv)
		return J{"ok": true, "host_key": fp}, nil
	})

	// Admin views read with the request's context (they stop when the browser leaves);
	// actions use detached(c), so they finish like they did in the JS server.
	// Viewers read, operators act.
	onServer := func(pattern string, fn func(c *ctx, srv *db.Server) (any, error)) {
		need := roleOperator
		if strings.HasPrefix(pattern, "GET ") {
			need = roleViewer
		}
		s.route(pattern, func(c *ctx) (any, error) {
			srv, err := c.server(c.param("id"), need)
			if err != nil {
				return nil, err
			}
			return fn(c, srv)
		})
	}
	onServer("GET /api/servers/{id}/metrics", func(c *ctx, srv *db.Server) (any, error) { return probe.Series(srv.ID, c.query("range")) })
	onServer("GET /api/servers/{id}/custom", func(c *ctx, srv *db.Server) (any, error) {
		list, err := probe.CustomFor(srv)
		if err != nil {
			return nil, err
		}
		type out struct {
			probe.Custom
			Series *probe.SeriesOf[probe.CustomPoint] `json:"series"`
		}
		res := make([]out, len(list))
		for i, m := range list {
			res[i].Custom = m
			if res[i].Series, err = probe.CustomSeries(srv.ID, m.ID, c.query("range")); err != nil {
				return nil, err
			}
		}
		return res, nil
	})
	onServer("GET /api/servers/{id}/services", func(c *ctx, srv *db.Server) (any, error) { return admin.Services(c.r.Context(), srv) })
	onServer("GET /api/servers/{id}/services/status", func(c *ctx, srv *db.Server) (any, error) {
		text, err := admin.ServiceStatus(c.r.Context(), srv, c.query("unit"))
		return J{"text": text}, err
	})
	onServer("POST /api/servers/{id}/services", func(c *ctx, srv *db.Server) (any, error) {
		out, err := admin.ServiceAction(detached(c), srv, c.body.String("unit"), c.body.String("action"))
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("systemctl %s %s on %s", c.body.String("action"), c.body.String("unit"), srv.Name), srv.ID)
		return J{"ok": true, "output": out}, nil
	})
	onServer("GET /api/servers/{id}/network", func(c *ctx, srv *db.Server) (any, error) { return admin.Network(c.r.Context(), srv) })
	onServer("POST /api/servers/{id}/network/allow", func(c *ctx, srv *db.Server) (any, error) {
		o := admin.UfwAllowOpts{Port: jsInt(c.body.num("port")), Proto: c.body.String("proto"), Route: c.body.truthy("route")}
		out, err := admin.UfwAllow(detached(c), srv, o)
		if err != nil {
			return nil, err
		}
		verb := "allow"
		if o.Route {
			verb = "route allow"
		}
		c.audit(fmt.Sprintf("ufw %s %d/%s on %s", verb, o.Port, o.Proto, srv.Name), srv.ID)
		return J{"ok": true, "output": out}, nil
	})
	// Plain TCP connects from the Fleetdeck host: proves what a firewall really lets through (from where Fleetdeck sits).
	onServer("POST /api/servers/{id}/network/reach", func(c *ctx, srv *db.Server) (any, error) {
		var ports []int
		seen := map[float64]bool{}
		arr, _ := c.body["ports"].([]any)
		for _, x := range arr {
			if n := jsNumber(x); !seen[n] && isInteger(n) && n > 0 && n < 65536 {
				seen[n] = true
				ports = append(ports, int(n))
			}
		}
		ports = ports[:min(len(ports), 100)]
		out := map[string]bool{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range ports {
			wg.Go(func() {
				conn, err := net.DialTimeout("tcp", net.JoinHostPort(srv.Host, strconv.Itoa(p)), 3*time.Second)
				if err == nil {
					conn.Close()
				}
				mu.Lock()
				out[strconv.Itoa(p)] = err == nil
				mu.Unlock()
			})
		}
		wg.Wait()
		return out, nil
	})
	onServer("GET /api/servers/{id}/timers", func(c *ctx, srv *db.Server) (any, error) { return admin.Timers(c.r.Context(), srv) })
	onServer("GET /api/servers/{id}/restarts", func(c *ctx, srv *db.Server) (any, error) { return admin.StaleServices(c.r.Context(), srv) })
	onServer("GET /api/servers/{id}/logs", func(c *ctx, srv *db.Server) (any, error) {
		return admin.Logs(c.r.Context(), srv, admin.LogsOpts{Unit: c.query("unit"), Priority: c.query("priority"), Since: c.query("since"), Lines: c.query("lines")})
	})
	onServer("GET /api/servers/{id}/packages", func(c *ctx, srv *db.Server) (any, error) {
		return admin.Packages(c.r.Context(), srv, c.query("refresh") == "1")
	})
	onServer("GET /api/servers/{id}/packages/history", func(c *ctx, srv *db.Server) (any, error) { return admin.UpdateHistory(c.r.Context(), srv) })
	onServer("GET /api/servers/{id}/processes", func(c *ctx, srv *db.Server) (any, error) { return admin.Processes(c.r.Context(), srv) })
	onServer("POST /api/servers/{id}/processes/kill", func(c *ctx, srv *db.Server) (any, error) {
		signal := c.body.strOr("signal", "TERM")
		if _, err := admin.Kill(detached(c), srv, jsInt(c.body.num("pid")), signal); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("kill -%s %s on %s", signal, c.body.String("pid"), srv.Name), srv.ID)
		return okResp, nil
	})
	onServer("GET /api/servers/{id}/containers", func(c *ctx, srv *db.Server) (any, error) { return admin.Containers(c.r.Context(), srv) })
	onServer("GET /api/servers/{id}/containers/metrics", func(c *ctx, srv *db.Server) (any, error) {
		return probe.ContainerSeries(srv.ID, c.query("name"), c.query("range"))
	})
	onServer("GET /api/servers/{id}/containers/logs", func(c *ctx, srv *db.Server) (any, error) {
		text, err := admin.ContainerLogs(c.r.Context(), srv, c.query("id"), c.query("lines"))
		return J{"text": text}, err
	})
	onServer("POST /api/servers/{id}/containers", func(c *ctx, srv *db.Server) (any, error) {
		id := c.body.String("id")
		out, err := admin.ContainerAction(detached(c), srv, id, c.body.String("action"))
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("docker %s %s on %s", c.body.String("action"), jsSlice(id, 12), srv.Name), srv.ID)
		return J{"ok": true, "output": out}, nil
	})

	// ---- endpoint checks (Network tab): owners set them up, operators run them now, everyone sees them ----
	type checkOut struct {
		checks.Check
		Uptime *float64 `json:"uptime"` // last 24 hours
	}
	checkView := func(id string) (*checkOut, error) {
		ch, err := db.Get[checks.Check](`SELECT * FROM checks WHERE id = ?`, id)
		if ch == nil || err != nil {
			return nil, cmpErr(err, notFound("Check"))
		}
		up, err := checks.Uptime(id, db.Now()-864e5)
		return &checkOut{*ch, up}, err
	}
	// serverCheck loads a check of a server the user may work on with role need.
	serverCheck := func(c *ctx, need int) (*db.Server, *checks.Check, error) {
		srv, err := c.server(c.param("id"), need)
		if err != nil {
			return nil, nil, err
		}
		ch, err := db.Get[checks.Check](`SELECT * FROM checks WHERE id = ? AND server_id = ?`, c.param("check"), srv.ID)
		if ch == nil || err != nil {
			return nil, nil, cmpErr(err, notFound("Check"))
		}
		return srv, ch, nil
	}
	onServer("GET /api/servers/{id}/checks", func(c *ctx, srv *db.Server) (any, error) {
		ids, err := db.Select[string](`SELECT id FROM checks WHERE server_id = ? ORDER BY lower(name)`, srv.ID)
		out := make([]*checkOut, len(ids))
		for i := range ids {
			if out[i], err = checkView(ids[i]); err != nil {
				return nil, err
			}
		}
		return out, err
	})
	s.route("POST /api/servers/{id}/checks", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		v, err := checks.Validate(c.body)
		if err != nil {
			return nil, err
		}
		v.ID, v.TenantID, v.ServerID = db.NewID(), srv.TenantID, srv.ID
		if err := exec(`INSERT INTO checks (id, tenant_id, server_id, name, kind, target, expect_status, contains, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, v.TenantID, v.ServerID, v.Name, v.Kind, v.Target, v.ExpectStatus, v.Contains, b01(v.Enabled), db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Added %s check %s on %s", v.Kind, v.Target, srv.Name), srv.ID)
		if err := checks.RunAndSave(detached(c), v); err != nil {
			return nil, err
		}
		return checkView(v.ID)
	})
	s.route("PUT /api/servers/{id}/checks/{check}", func(c *ctx) (any, error) {
		srv, old, err := serverCheck(c, roleOwner)
		if err != nil {
			return nil, err
		}
		base := obj{"kind": old.Kind, "target": old.Target, "name": old.Name, "contains": old.Contains, "enabled": old.Enabled}
		if old.ExpectStatus != nil {
			base["expect_status"] = float64(*old.ExpectStatus)
		}
		v, err := checks.Validate(merge(base, c.body))
		if err != nil {
			return nil, err
		}
		v.ID = old.ID
		if err := exec(`UPDATE checks SET name = ?, kind = ?, target = ?, expect_status = ?, contains = ?, enabled = ?, failing_since = NULL WHERE id = ?`,
			v.Name, v.Kind, v.Target, v.ExpectStatus, v.Contains, b01(v.Enabled), v.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Changed %s check %s on %s", v.Kind, v.Target, srv.Name), srv.ID)
		if v.Enabled {
			if err := checks.RunAndSave(detached(c), v); err != nil {
				return nil, err
			}
		}
		return checkView(v.ID)
	})
	s.route("DELETE /api/servers/{id}/checks/{check}", func(c *ctx) (any, error) {
		srv, ch, err := serverCheck(c, roleOwner)
		if err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM checks WHERE id = ?`, ch.ID); err != nil { // its results go with it
			return nil, err
		}
		c.audit(fmt.Sprintf("Removed check %s on %s", ch.Target, srv.Name), srv.ID)
		return okResp, nil
	})
	s.route("POST /api/servers/{id}/checks/{check}/run", func(c *ctx) (any, error) {
		_, ch, err := serverCheck(c, roleOperator)
		if err != nil {
			return nil, err
		}
		if err := checks.RunAndSave(detached(c), ch); err != nil {
			return nil, err
		}
		return checkView(ch.ID)
	})

	// ---- journal search across servers (the Logs tab's scope selector) ----
	// scope: "all" or "tag:<name>". A server that fails shows as one error row; the rest still answer.
	s.route("GET /api/logs", func(c *ctx) (any, error) {
		list, err := c.visibleServers("AND paused = 0")
		if err != nil {
			return nil, err
		}
		if tag, ok := strings.CutPrefix(c.query("scope"), "tag:"); ok {
			list = slices.DeleteFunc(list, func(s db.Server) bool { return !slices.Contains(strings.Split(s.Tags, ","), tag) })
		} else if c.query("scope") != "all" {
			return nil, httperr.Bad("scope must be all or tag:<name>")
		}
		lines := c.query("lines")
		if n, err := strconv.Atoi(lines); err != nil || n > 500 {
			lines = "500"
		}
		o := admin.LogsOpts{Unit: c.query("unit"), Priority: c.query("priority"), Since: c.query("since"), Lines: lines}
		if _, err := admin.LogsCommand(o); err != nil { // a bad filter would fail on every server the same way
			return nil, err
		}
		type entry struct {
			admin.LogEntry
			ServerID string `json:"server_id"`
			Server   string `json:"server"`
		}
		type failed struct {
			ServerID string `json:"server_id"`
			Server   string `json:"server"`
			Error    string `json:"error"`
		}
		entries, errs := []entry{}, []failed{}
		var mu sync.Mutex
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i := range list {
			srv := &list[i]
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(c.r.Context(), 20*time.Second)
				defer cancel()
				got, err := journal(ctx, srv, o)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, failed{srv.ID, srv.Name, err.Error()})
					return
				}
				for _, e := range got {
					entries = append(entries, entry{e, srv.ID, srv.Name})
				}
			})
		}
		wg.Wait()
		slices.SortStableFunc(entries, func(a, b entry) int { return cmp.Compare(a.T, b.T) })
		slices.SortFunc(errs, func(a, b failed) int { return strings.Compare(a.Server, b.Server) })
		return J{"entries": entries, "errors": errs, "servers": len(list)}, nil
	})

	// ---- authorized_keys across the fleet ----
	s.route("GET /api/accounts", func(c *ctx) (any, error) {
		list, err := c.visibleServers("AND paused = 0")
		if err != nil {
			return nil, err
		}
		out := make([]J, len(list))
		// ponytail: 8 servers at a time, on demand; cache it if the fleet gets large.
		for i := 0; i < len(list); i += 8 {
			var wg sync.WaitGroup
			for j := i; j < min(i+8, len(list)); j++ {
				wg.Go(func() {
					srv := &list[j]
					users, err := admin.Accounts(c.r.Context(), srv)
					if err != nil {
						out[j] = J{"server_id": srv.ID, "server_name": srv.Name, "error": err.Error()}
						return
					}
					if users == nil {
						users = []admin.Account{}
					}
					out[j] = J{"server_id": srv.ID, "server_name": srv.Name, "login_user": srv.User, "users": users}
				})
			}
			wg.Wait()
		}
		keys, err := db.Select[struct {
			idName
			PublicKey string `db:"public_key" json:"-"`
			FP        string `db:"-" json:"fp"`
		}](`SELECT id, name, public_key FROM keys WHERE tenant_id = ?`, c.tenant)
		if err != nil {
			return nil, err
		}
		fps := map[string]string{}
		for i := range keys {
			keys[i].FP = admin.BlobFingerprint(blobOf(keys[i].PublicKey))
			fps[keys[i].ID] = keys[i].FP
		}
		// Which key Fleetdeck logs in with per server, so the UI can mark it and not offer to remove it.
		own := map[string]string{}
		for _, srv := range list {
			if fp, ok := fps[db.Str(srv.KeyID)]; ok {
				own[srv.ID] = fp
			}
		}
		return J{"servers": out, "keys": keys, "own": own}, nil
	}, admins)

	s.route("POST /api/servers/{id}/authorized-keys/revoke", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		user, blob := c.body.str("user"), c.body.str("blob")
		if srv.KeyID != nil {
			pub, err := db.Get[string](`SELECT public_key FROM keys WHERE id = ?`, *srv.KeyID)
			if err != nil {
				return nil, err
			}
			if pub != nil && blobOf(*pub) != "" && blobOf(*pub) == blob && user == srv.User {
				return nil, httperr.New(409, fmt.Sprintf("Fleetdeck logs in to %s with this key. Removing it would lock Fleetdeck out. Assign another key first.", srv.Name))
			}
		}
		out, err := admin.RevokeKey(detached(c), srv, user, blob)
		if err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf("Removed SSH key %s from %s@%s", admin.BlobFingerprint(blob), user, srv.Name),
			db.EventOpts{Level: "warn", Actor: c.actor(), IP: c.ip, ServerID: srv.ID})
		return J{"ok": true, "output": out}, nil
	})
}

func blobOf(pub string) string {
	if f := strings.Fields(pub); len(f) > 1 {
		return f[1]
	}
	return ""
}

func (s *Server) libraryRoutes() {
	// ---- keys ----
	// Owners see and add keys (they pick one when adding a server); renaming and deleting is for admins.
	s.route("GET /api/keys", func(c *ctx) (any, error) {
		if c.level() < roleOwner {
			return nil, httperr.New(403, "Only owners and admins manage SSH keys")
		}
		list, err := db.Select[keyRow](`SELECT * FROM keys WHERE tenant_id = ? ORDER BY lower(name)`, c.tenant)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(list))
		for i, k := range list {
			if out[i], err = keyView(k); err != nil {
				return nil, err
			}
		}
		return out, nil
	})

	s.route("POST /api/keys", func(c *ctx) (any, error) {
		if c.level() < roleOwner {
			return nil, httperr.New(403, "Only owners and admins manage SSH keys")
		}
		name := strings.TrimSpace(c.body.str("name"))
		if name == "" || jsLen(name) > 64 {
			return nil, httperr.Bad("Name is required")
		}
		passphrase := c.body.str("passphrase")
		gen := c.body.truthy("generate")
		var priv, pub string
		var err error
		if gen {
			priv, pub, err = ssh.GenerateKey("fleetdeck-" + nonWord.ReplaceAllString(name, "_"))
		} else {
			priv = strings.TrimSpace(c.body.str("private_key")) + "\n"
			pub, err = ssh.ParsePrivateKey(priv, passphrase)
		}
		if err != nil {
			return nil, err
		}
		var passEnc *string
		if passphrase != "" && !gen {
			passEnc = new(sec.Encrypt(passphrase))
		}
		id := db.NewID()
		if err := exec(`INSERT INTO keys (id, tenant_id, name, public_key, private_enc, passphrase_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, name, pub, sec.Encrypt(priv), passEnc, db.Now()); err != nil {
			return nil, err
		}
		verb := "Imported"
		if gen {
			verb = "Generated"
		}
		c.audit(fmt.Sprintf(`%s SSH key "%s"`, verb, name), "")
		return keyViewByID(c.tenant, id)
	})

	s.route("PUT /api/keys/{id}", func(c *ctx) (any, error) {
		name := strings.TrimSpace(c.body.str("name"))
		if name == "" || jsLen(name) > 64 {
			return nil, httperr.Bad("Name is required")
		}
		old, err := db.Get[string](`SELECT name FROM keys WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Key"))
		}
		if err := exec(`UPDATE keys SET name = ? WHERE id = ?`, name, c.param("id")); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Renamed SSH key "%s" to "%s"`, *old, name), "")
		return keyViewByID(c.tenant, c.param("id"))
	}, admins)

	s.route("DELETE /api/keys/{id}", func(c *ctx) (any, error) {
		k, err := db.Get[keyRow](`SELECT * FROM keys WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if k == nil || err != nil {
			return nil, cmpErr(err, notFound("Key"))
		}
		used, err := db.Select[idName](`SELECT id, name FROM servers WHERE key_id = ? UNION ALL SELECT id, 'repository ' || name FROM repos WHERE key_id = ?`, k.ID, k.ID)
		if err != nil {
			return nil, err
		}
		if len(used) > 0 {
			return nil, httperr.New(409, "Key is used by: "+names(used))
		}
		if err := exec(`DELETE FROM keys WHERE id = ?`, k.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Deleted SSH key "%s"`, k.Name), "")
		return okResp, nil
	}, admins)

	// ---- scripts: the organization's own, plus the shared library (built-ins) that only super admins edit ----
	s.route("GET /api/scripts", func(c *ctx) (any, error) {
		list, err := db.Select[jobs.Script](`SELECT * FROM scripts WHERE tenant_id = ? OR tenant_id IS NULL ORDER BY builtin DESC, (tenant_id IS NULL) DESC, lower(name)`, c.tenant)
		out := make([]scriptOut, len(list))
		for i := range list {
			out[i] = scriptOut{&list[i], list[i].TenantID == nil}
		}
		return out, err
	})
	// editable loads a script the user may change: the organization's own (admins), or a shared one (super admins).
	editable := func(c *ctx) (*jobs.Script, error) {
		sc, err := jobs.ScriptFor(c.tenant, c.param("id"))
		if sc == nil || err != nil {
			return nil, cmpErr(err, notFound("Script"))
		}
		if sc.TenantID == nil && !c.super {
			return nil, httperr.New(403, "This script is from the shared library. Make a copy to change it.")
		}
		return sc, nil
	}
	s.route("POST /api/scripts", func(c *ctx) (any, error) {
		scope := &c.tenant // nil = the shared library
		if c.body.truthy("shared") {
			if !c.super {
				return nil, httperr.New(403, "Only super admins add to the shared library")
			}
			scope = nil
		}
		v, err := jobs.ValidateScript(db.Str(scope), scriptInput(c.body))
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO scripts (id, tenant_id, name, description, body, as_root, params, builtin, created_at, kind, repo_id, playbook, vars, next_script_id, next_when, next_ok_only)
      VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, scope, v.Name, v.Description, v.Body, v.AsRoot, v.Params, db.Now(), v.Kind, v.RepoID, v.Playbook, v.Vars, v.NextScript, v.NextWhen, v.NextOKOnly); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created script "%s"`, v.Name), "")
		return scriptView(c.tenant, id)
	}, admins)
	s.route("PUT /api/scripts/{id}", func(c *ctx) (any, error) {
		sc, err := editable(c)
		if err != nil {
			return nil, err
		}
		in := scriptInput(c.body)
		in.Self = sc.ID
		v, err := jobs.ValidateScript(db.Str(sc.TenantID), in)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE scripts SET name = ?, description = ?, body = ?, as_root = ?, params = ?, kind = ?, repo_id = ?, playbook = ?, vars = ?,
      next_script_id = ?, next_when = ?, next_ok_only = ? WHERE id = ?`,
			v.Name, v.Description, v.Body, v.AsRoot, v.Params, v.Kind, v.RepoID, v.Playbook, v.Vars, v.NextScript, v.NextWhen, v.NextOKOnly, sc.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Edited script "%s"`, v.Name), "")
		return scriptView(c.tenant, sc.ID)
	}, admins)
	s.route("POST /api/scripts/{id}/copy", func(c *ctx) (any, error) {
		sc, err := jobs.ScriptFor(c.tenant, c.param("id"))
		if sc == nil || err != nil {
			return nil, cmpErr(err, notFound("Script"))
		}
		id := db.NewID()
		if err := exec(`INSERT INTO scripts (id, tenant_id, name, description, body, as_root, params, builtin, created_at, kind, repo_id, playbook, vars, next_script_id, next_when, next_ok_only)
      SELECT ?, ?, left(name || ' (copy)', 100), description, body, as_root, params, 0, ?, kind, repo_id, playbook, vars, next_script_id, next_when, next_ok_only FROM scripts WHERE id = ?`,
			id, c.tenant, db.Now(), sc.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Copied script "%s"`, sc.Name), "")
		return scriptView(c.tenant, id)
	}, admins)
	s.route("DELETE /api/scripts/{id}", func(c *ctx) (any, error) {
		sc, err := editable(c)
		if err != nil {
			return nil, err
		}
		if sc.Builtin {
			return nil, httperr.New(409, "Built-in scripts can't be deleted (the server pages use them). Edit it instead.")
		}
		if err := exec(`DELETE FROM scripts WHERE id = ?`, sc.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Deleted script "%s"`, sc.Name), "")
		return okResp, nil
	}, admins)

	// ---- shared variables ----
	s.route("GET /api/variables", func(c *ctx) (any, error) {
		list, err := db.Select[jobs.Variable](`SELECT * FROM variables WHERE tenant_id = ? ORDER BY name`, c.tenant)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(list))
		for i := range list {
			if out[i], err = variableView(&list[i]); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	s.route("POST /api/variables", func(c *ctx) (any, error) {
		v, err := jobs.ValidateVariable(c.tenant, variableInput(c.body), nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO variables (id, tenant_id, name, value_enc, secret, description, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.ValueEnc, v.Secret, v.Description, db.Now()); err != nil {
			return nil, err
		}
		secret := ""
		if v.Secret != 0 {
			secret = "secret "
		}
		c.audit(fmt.Sprintf("Created %svariable %s", secret, v.Name), "")
		nv, err := getVariable(c.tenant, id)
		if err != nil {
			return nil, err
		}
		return variableView(nv)
	})
	s.route("PUT /api/variables/{id}", func(c *ctx) (any, error) {
		old, err := getVariable(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := jobs.ValidateVariable(c.tenant, variableInput(c.body), old)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE variables SET value_enc = ?, secret = ?, description = ? WHERE id = ?`, v.ValueEnc, v.Secret, v.Description, old.ID); err != nil {
			return nil, err
		}
		c.audit("Updated variable "+v.Name, "")
		nv, err := getVariable(c.tenant, old.ID)
		if err != nil {
			return nil, err
		}
		return variableView(nv)
	})
	s.route("DELETE /api/variables/{id}", func(c *ctx) (any, error) {
		v, err := getVariable(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		used, err := varUsers(c.tenant, v.Name)
		if err != nil {
			return nil, err
		}
		if len(used) > 0 {
			return nil, httperr.New(409, "Used by scripts: "+names(used))
		}
		if err := exec(`DELETE FROM variables WHERE id = ?`, v.ID); err != nil {
			return nil, err
		}
		c.audit("Deleted variable "+v.Name, "")
		return okResp, nil
	})

	// ---- git repositories ----
	s.route("GET /api/repos", func(c *ctx) (any, error) {
		list, err := db.Select[repos.Repo](`SELECT * FROM repos WHERE tenant_id = ? ORDER BY lower(name)`, c.tenant)
		if err != nil {
			return nil, err
		}
		type repoOut struct {
			repos.View
			UsedBy []idName `json:"used_by"`
		}
		out := make([]repoOut, len(list))
		for i := range list {
			out[i].View = repos.RepoView(&list[i])
			if out[i].UsedBy, err = db.Select[idName](`SELECT id, name FROM scripts WHERE repo_id = ?`, list[i].ID); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	s.route("POST /api/repos", func(c *ctx) (any, error) {
		v, err := repos.ValidateRepo(c.tenant, repoInput(c.body), nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO repos (id, tenant_id, name, url, branch, auth, key_id, username, token_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.URL, v.Branch, v.Auth, v.KeyID, v.Username, v.TokenEnc, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Added repository "%s" (%s)`, v.Name, v.URL), "")
		r, err := getRepo(c.tenant, id)
		if err != nil {
			return nil, err
		}
		go repos.Sync(r, nil) // first clone in the background; errors show on the repo
		return repos.RepoView(r), nil
	})
	s.route("PUT /api/repos/{id}", func(c *ctx) (any, error) {
		old, err := getRepo(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		base := obj{"name": old.Name, "url": old.URL, "branch": old.Branch, "auth": old.Auth, "key_id": db.Str(old.KeyID), "username": db.Str(old.Username)}
		v, err := repos.ValidateRepo(c.tenant, repoInput(merge(base, c.body)), old)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE repos SET name = ?, url = ?, branch = ?, auth = ?, key_id = ?, username = ?, token_enc = ?, last_error = NULL WHERE id = ?`,
			v.Name, v.URL, v.Branch, v.Auth, v.KeyID, v.Username, v.TokenEnc, old.ID); err != nil {
			return nil, err
		}
		if old.URL != v.URL {
			repos.RemoveCheckout(old.ID) // fresh clone for a different remote
		}
		c.audit(fmt.Sprintf(`Updated repository "%s"`, v.Name), "")
		r, err := getRepo(c.tenant, old.ID)
		if err != nil {
			return nil, err
		}
		return repos.RepoView(r), nil
	})
	s.route("DELETE /api/repos/{id}", func(c *ctx) (any, error) {
		r, err := getRepo(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		used, err := db.Select[idName](`SELECT id, name FROM scripts WHERE repo_id = ?`, r.ID)
		if err != nil {
			return nil, err
		}
		if len(used) > 0 {
			return nil, httperr.New(409, "Used by scripts: "+names(used))
		}
		if err := exec(`DELETE FROM repos WHERE id = ?`, r.ID); err != nil {
			return nil, err
		}
		repos.RemoveCheckout(r.ID)
		c.audit(fmt.Sprintf(`Deleted repository "%s"`, r.Name), "")
		return okResp, nil
	})
	syncView := func(r *repos.Repo) (any, error) {
		if _, err := repos.Sync(r, nil); err != nil {
			return nil, httperr.New(502, err.Error())
		}
		r, err := getRepo(r.TenantID, r.ID)
		if err != nil {
			return nil, err
		}
		return repos.RepoView(r), nil
	}
	s.route("POST /api/repos/{id}/sync", func(c *ctx) (any, error) {
		r, err := getRepo(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		return syncView(r)
	})
	s.route("POST /api/repos/{id}/forget-hostkey", func(c *ctx) (any, error) {
		r, err := getRepo(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		host, err := repos.ForgetHostKey(r)
		if err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf(`Forgot the pinned Git host key of %s (repository "%s"); the next sync pins the new one`, host, r.Name),
			db.EventOpts{Level: "warn", Actor: c.actor(), IP: c.ip, TenantID: c.tenant})
		return syncView(r)
	})
	s.route("GET /api/repos/{id}/files", func(c *ctx) (any, error) {
		r, err := getRepo(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		files, err := repos.Files(r)
		if err != nil {
			return nil, httperr.New(502, err.Error())
		}
		if files == nil {
			files = []string{}
		}
		return files, nil
	})
	s.route("GET /api/tools", func(c *ctx) (any, error) { return repos.Tools(), nil })
}

func (s *Server) jobRoutes() {
	s.route("GET /api/jobs", func(c *ctx) (any, error) {
		return jobs.ListJobs(jobs.ListOpts{Server: c.query("server"), Limit: c.query("limit"), Before: c.query("before"), Status: c.query("status"),
			Tenant: c.tenant, Member: c.member()})
	})

	s.route("POST /api/jobs", func(c *ctx) (any, error) {
		b := c.body
		var spec jobs.Spec
		if b.truthy("script_id") {
			sc, err := jobs.ScriptFor(c.tenant, jsString(b["script_id"]))
			if sc == nil || err != nil {
				return nil, cmpErr(err, notFound("Script"))
			}
			given := jobs.Values{}
			for k, x := range b.obj("params") {
				if x != nil {
					given[k] = jsString(x)
				}
			}
			params, err := sc.Resolve(given)
			if err != nil {
				return nil, err
			}
			spec = jobs.SpecFromScript(sc, params, b.obj("options").truthy("check"))
		} else {
			body := b.str("body")
			if strings.TrimSpace(body) == "" {
				return nil, httperr.Bad("Command is required")
			}
			if jsLen(body) > 100_000 {
				return nil, httperr.Bad("Command too long")
			}
			name := b.strOr("name", strings.Split(strings.TrimSpace(body), "\n")[0])
			spec = jobs.Spec{Name: jsSlice(name, 100), Body: body, AsRoot: b["as_root"] != false, Params: jobs.Values{}}
		}
		spec.Targets, spec.Actor, spec.TenantID, spec.Member = targetsOf(b["targets"]), c.actor(), c.tenant, c.member()
		id, err := jobs.StartJob(spec)
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Started job #%d "%s"`, id, spec.Name), "")
		return J{"id": id}, nil
	})

	// getJob loads a job of the organization. Members see only the runs on their servers, and a job with none of
	// those not at all; all = every run of the job is on their servers (needed to cancel or re-run it).
	getJob := func(c *ctx) (j *jobs.Job, all bool, err error) {
		id, ok := jobID(c)
		if !ok {
			return nil, false, notFound("Job")
		}
		if j, err = jobs.GetJob(id); err != nil {
			return nil, false, err
		}
		if j == nil || j.TenantID != c.tenant {
			return nil, false, notFound("Job")
		}
		vis, err := c.sees()
		if err != nil || vis == nil {
			return j, true, err
		}
		runs := j.Runs[:0]
		for _, r := range j.Runs {
			if vis[r.ServerID] {
				runs = append(runs, r)
			}
		}
		all = len(runs) == len(j.Runs)
		if j.Runs = runs; len(runs) == 0 {
			return nil, false, notFound("Job")
		}
		return j, all, nil
	}
	s.route("GET /api/jobs/{id}", func(c *ctx) (any, error) {
		j, _, err := getJob(c)
		return j, err
	})

	s.route("POST /api/jobs/{id}/cancel", func(c *ctx) (any, error) {
		j, all, err := getJob(c)
		if err != nil {
			return nil, err
		}
		if !all {
			return nil, httperr.New(403, "This job also runs on servers you can't see")
		}
		if !jobs.CancelJob(j.ID) {
			return nil, httperr.New(409, "Job is not running")
		}
		c.audit("Cancelled job #"+c.param("id"), "")
		return okResp, nil
	})

	s.route("POST /api/jobs/{id}/rerun", func(c *ctx) (any, error) {
		j, all, err := getJob(c)
		if err != nil {
			return nil, err
		}
		if !all {
			return nil, httperr.New(403, "This job also ran on servers you can't see")
		}
		servers := []string{}
		for _, r := range j.Runs {
			if r.ServerID != jobs.Ansible && (!c.body.truthy("failed_only") || (r.Status != "success" && r.Status != "skipped")) {
				servers = append(servers, r.ServerID)
			}
		}
		id, err := jobs.StartJob(jobs.Spec{ScriptID: db.Str(j.ScriptID), Name: j.Name, Body: j.Body, Kind: j.Kind, RepoID: db.Str(j.RepoID), Options: j.Options,
			AsRoot: j.AsRoot != 0, Params: j.Params, Targets: jobs.Targets{Servers: servers, Tags: []string{}}, Actor: c.actor(), TenantID: c.tenant, Member: c.member()})
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Re-ran job #%d as #%d", j.ID, id), "")
		return J{"id": id}, nil
	})

	s.route("GET /api/jobs/{id}/stream", func(c *ctx) (any, error) {
		job, _, err := getJob(c)
		if err != nil {
			return nil, err
		}
		vis, err := c.sees()
		if err != nil {
			return nil, err
		}
		h := c.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		c.w.WriteHeader(200)
		rc := http.NewResponseController(c.w)
		write := func(s string) bool {
			_, err := io.WriteString(c.w, s)
			return err == nil && rc.Flush() == nil
		}
		send := func(m any) bool {
			data, err := json.Marshal(m)
			return err == nil && write("data: "+string(data)+"\n\n")
		}
		// The callback runs under the jobs lock: it only hands messages over, never blocks.
		// ponytail: a client 4096 events behind is cut off; the UI then reloads the job once.
		msgs, lost := make(chan jobs.Msg, 4096), make(chan struct{})
		var once sync.Once
		unsub, live := jobs.Subscribe(job.ID, func(m jobs.Msg) {
			if srv, ok := m["server"].(string); ok && vis != nil && !vis[srv] {
				return // a run on a server this user doesn't see
			}
			select {
			case msgs <- m:
			default:
				once.Do(func() { close(lost) })
			}
		})
		if !live {
			if j, _, err := getJob(c); err == nil && j != nil {
				job = j
			}
			send(J{"type": "snapshot", "job": job})
			return nil, nil
		}
		defer unsub()
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case m := <-msgs:
				if !send(m) || m["type"] == "job" {
					return nil, nil
				}
			case <-ping.C:
				if !write(": ping\n\n") {
					return nil, nil
				}
			case <-lost:
				return nil, nil
			case <-c.r.Context().Done():
				return nil, nil
			}
		}
	})

	// ---- schedules ----
	s.route("GET /api/schedules", func(c *ctx) (any, error) { return schedules(c.tenant, `ORDER BY lower(x.name)`) })
	s.route("POST /api/schedules", func(c *ctx) (any, error) {
		v, err := jobs.ValidateSchedule(c.tenant, scheduleInput(c.body), true)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO schedules (id, tenant_id, name, script_id, cron, targets, params, enabled, once, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.ScriptID, v.Cron, v.Targets, v.Params, v.Enabled, v.Once, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created schedule "%s" (%s)`, v.Name, *v.Cron), "")
		return scheduleView(c.tenant, id)
	})
	getSchedule := func(c *ctx) (*jobs.Schedule, error) {
		sc, err := db.Get[jobs.Schedule](`SELECT * FROM schedules WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if sc == nil && err == nil {
			err = notFound("Schedule")
		}
		return sc, err
	}
	s.route("PUT /api/schedules/{id}", func(c *ctx) (any, error) {
		old, err := getSchedule(c)
		if err != nil {
			return nil, err
		}
		v, err := jobs.ValidateSchedule(c.tenant, scheduleInput(merge(toObj(old), c.body)), true)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE schedules SET name = ?, script_id = ?, cron = ?, targets = ?, params = ?, enabled = ?, once = ? WHERE id = ?`,
			v.Name, v.ScriptID, v.Cron, v.Targets, v.Params, v.Enabled, v.Once, old.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Updated schedule "%s"`, v.Name), "")
		return scheduleView(c.tenant, old.ID)
	})
	s.route("DELETE /api/schedules/{id}", func(c *ctx) (any, error) {
		sc, err := getSchedule(c)
		if err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM schedules WHERE id = ?`, sc.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Deleted schedule "%s"`, sc.Name), "")
		return okResp, nil
	})
	s.route("POST /api/schedules/{id}/run", func(c *ctx) (any, error) {
		sc, err := getSchedule(c)
		if err != nil {
			return nil, err
		}
		id, err := jobs.RunSchedule(sc, c.actor())
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Ran schedule "%s" manually (job #%d)`, sc.Name, id), "")
		return J{"id": id}, nil
	})
	s.route("POST /api/cron/preview", func(c *ctx) (any, error) {
		next, err := jobs.NextRuns(c.body.str("cron"), 5, time.Now())
		if next == nil {
			next = []int64{}
		}
		return J{"next": next}, err
	})

	// ---- webhook triggers ----
	// Each trigger has its own secret URL that starts one saved script on fixed targets, nothing else.
	// Only the token's hash is stored; the URL is shown once (and can be rotated).
	triggerView := func(c *ctx, id string) (*triggerOut, error) {
		list, err := triggers(c.tenant, `AND x.id = ?`, id)
		if len(list) == 0 || err != nil {
			return nil, cmpErr(err, notFound("Webhook"))
		}
		return &list[0], nil
	}
	s.route("GET /api/triggers", func(c *ctx) (any, error) { return triggers(c.tenant, `ORDER BY lower(x.name)`) })
	s.route("POST /api/triggers", func(c *ctx) (any, error) {
		v, err := jobs.ValidateSchedule(c.tenant, scheduleInput(c.body), false)
		if err != nil {
			return nil, err
		}
		id, token := db.NewID(), sec.Token()
		if err := exec(`INSERT INTO triggers (id, tenant_id, name, script_id, targets, params, token_hash, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.ScriptID, v.Targets, v.Params, sec.SHA256(token), v.Enabled, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created webhook "%s"`, v.Name), "")
		t, err := triggerView(c, id)
		if err != nil {
			return nil, err
		}
		return struct {
			*triggerOut
			Token string `json:"token"`
		}{t, token}, nil
	})
	s.route("PUT /api/triggers/{id}", func(c *ctx) (any, error) {
		old, err := getTrigger(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := jobs.ValidateSchedule(c.tenant, scheduleInput(merge(toObj(old), c.body)), false)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE triggers SET name = ?, script_id = ?, targets = ?, params = ?, enabled = ? WHERE id = ?`,
			v.Name, v.ScriptID, v.Targets, v.Params, v.Enabled, old.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Updated webhook "%s"`, v.Name), "")
		return triggerView(c, old.ID)
	})
	s.route("POST /api/triggers/{id}/rotate", func(c *ctx) (any, error) {
		t, err := getTrigger(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		token := sec.Token()
		if err := exec(`UPDATE triggers SET token_hash = ? WHERE id = ?`, sec.SHA256(token), t.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Generated a new URL for webhook "%s"; the old one stopped working`, t.Name), "")
		return J{"token": token}, nil
	})
	s.route("DELETE /api/triggers/{id}", func(c *ctx) (any, error) {
		t, err := getTrigger(c.tenant, c.param("id"))
		if err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM triggers WHERE id = ?`, t.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Deleted webhook "%s"`, t.Name), "")
		return okResp, nil
	})
	// Called by CI, Git hosts, etc. The URL token is the only credential; the request body is ignored.
	s.route("POST /api/hooks/{token}", func(c *ctx) (any, error) {
		t, err := db.Get[jobs.Trigger](`SELECT * FROM triggers WHERE token_hash = ? AND enabled = 1`, sec.SHA256(c.param("token")))
		if t == nil || err != nil {
			return nil, cmpErr(err, httperr.New(404, "Not found"))
		}
		// No pile-ups: a burst of pushes runs once, like schedules.
		if t.LastJob != nil && jobs.IsRunning(*t.LastJob) {
			writeJSON(c.w, 409, J{"error": "Previous run is still going", "job": *t.LastJob})
			return nil, nil
		}
		id, err := jobs.RunTrigger(t)
		if err != nil {
			return nil, err
		}
		db.Event("audit", fmt.Sprintf(`Webhook "%s" started job #%d`, t.Name, id), db.EventOpts{Actor: "webhook", IP: c.ip, TenantID: t.TenantID})
		return J{"ok": true, "job": id}, nil
	}, open, raw)
}

var settingRanges = []struct {
	key           string
	lo, hi, value float64
}{{"retention_metrics_days", 1, 365, 30}, {"retention_jobs_days", 1, 3650, 90}, {"retention_events_days", 1, 3650, 180}}

func (s *Server) alertRoutes() {
	s.route("GET /api/alerts", func(c *ctx) (any, error) {
		list, err := rules(c.tenant, `ORDER BY r.created_at`)
		if err != nil {
			return nil, err
		}
		firing, err := alerts.Firing(c.tenant)
		if err != nil {
			return nil, err
		}
		history, err := db.Rows(`SELECT id, t, kind, level, server_id, actor, ip, message FROM events WHERE tenant_id = ? AND kind = 'alert' ORDER BY id DESC LIMIT 100`, c.tenant)
		if err != nil {
			return nil, err
		}
		custom, err := db.Select[probe.Custom](`SELECT * FROM custom_metrics WHERE tenant_id = ? ORDER BY lower(name)`, c.tenant)
		return J{"metrics": alerts.Metrics, "rules": list, "firing": firing, "history": history, "custom": custom}, err
	})

	// ---- custom metrics (shown on the server page, alertable here) ----
	scopeText := func(c *probe.Custom) string {
		switch {
		case c.ServerID != nil:
			if srv, _ := db.GetServer(*c.ServerID); srv != nil {
				return "on " + srv.Name
			}
		case db.Str(c.Tag) != "":
			return "for tag " + *c.Tag
		}
		return "for all servers"
	}
	s.route("POST /api/custom-metrics", func(c *ctx) (any, error) {
		v, err := alerts.ValidateCustom(c.tenant, c.body)
		if err != nil {
			return nil, err
		}
		v.ID, v.CreatedAt, v.TenantID = db.NewID(), db.Now(), c.tenant
		if err := exec(`INSERT INTO custom_metrics (id, tenant_id, name, unit, command, server_id, tag, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, c.tenant, v.Name, v.Unit, v.Command, v.ServerID, v.Tag, v.CreatedAt); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Added custom metric %q %s, runs as root: %s", v.Name, scopeText(v), v.Command), db.Str(v.ServerID))
		return v, nil
	})
	s.route("PUT /api/custom-metrics/{id}", func(c *ctx) (any, error) {
		old, err := db.Get[probe.Custom](`SELECT * FROM custom_metrics WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Custom metric"))
		}
		v, err := alerts.ValidateCustom(c.tenant, merge(toObj(old), c.body))
		if err != nil {
			return nil, err
		}
		v.ID, v.CreatedAt, v.TenantID = old.ID, old.CreatedAt, c.tenant
		if err := exec(`UPDATE custom_metrics SET name = ?, unit = ?, command = ?, server_id = ?, tag = ? WHERE id = ?`,
			v.Name, v.Unit, v.Command, v.ServerID, v.Tag, v.ID); err != nil {
			return nil, err
		}
		msg := fmt.Sprintf("Changed custom metric %q %s", v.Name, scopeText(v))
		if v.Command != old.Command {
			msg += ", runs as root: " + v.Command
		}
		c.audit(msg, db.Str(v.ServerID))
		return v, nil
	})
	s.route("DELETE /api/custom-metrics/{id}", func(c *ctx) (any, error) {
		old, err := db.Get[probe.Custom](`SELECT * FROM custom_metrics WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Custom metric"))
		}
		if err := db.Tx(func(tx db.Txn) error { // its alert rules go with it (ON DELETE CASCADE)
			for _, q := range []string{`DELETE FROM custom_values WHERE metric_id = ?`, `DELETE FROM custom_metrics WHERE id = ?`} {
				if _, err := tx.Exec(q, old.ID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Deleted custom metric %q", old.Name), db.Str(old.ServerID))
		return okResp, nil
	})
	s.route("POST /api/alerts/rules", func(c *ctx) (any, error) {
		v, err := alerts.ValidateRule(c.tenant, c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO alert_rules (id, tenant_id, server_id, tag, metric, threshold, minutes, enabled, created_at, mount, custom_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.ServerID, v.Tag, v.Metric, v.Threshold, v.Minutes, v.Enabled, db.Now(), v.Mount, v.CustomID); err != nil {
			return nil, err
		}
		c.audit("Created alert rule "+v.Metric, "")
		return ruleView(c.tenant, id)
	})
	s.route("PUT /api/alerts/rules/{id}", func(c *ctx) (any, error) {
		old, err := db.Get[alerts.Rule](`SELECT * FROM alert_rules WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant)
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Rule"))
		}
		base := toObj(old)
		base["enabled"] = old.Enabled != 0
		v, err := alerts.ValidateRule(c.tenant, merge(base, c.body))
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE alert_rules SET server_id = ?, tag = ?, metric = ?, threshold = ?, minutes = ?, enabled = ?, mount = ?, custom_id = ? WHERE id = ?`,
			v.ServerID, v.Tag, v.Metric, v.Threshold, v.Minutes, v.Enabled, v.Mount, v.CustomID, old.ID); err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM alert_state WHERE rule_id = ?`, old.ID); err != nil { // re-evaluated on the next probe
			return nil, err
		}
		c.audit("Updated alert rule "+v.Metric, "")
		return ruleView(c.tenant, old.ID)
	})
	s.route("DELETE /api/alerts/rules/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM alert_rules WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant); err != nil {
			return nil, err
		}
		c.audit("Deleted alert rule", "")
		return okResp, nil
	})

	// ---- quiet hours ----
	s.route("GET /api/mutes", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM mute_windows WHERE kind = 'once' AND end_at < ?`, db.Now()); err != nil { // finished maintenance windows
			return nil, err
		}
		list, err := mutes(c.tenant, `ORDER BY w.kind, w.start_at`)
		return J{"windows": list, "tz": localTZ()}, err
	})
	s.route("POST /api/mutes", func(c *ctx) (any, error) {
		v, err := alerts.ValidateMute(c.tenant, c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO mute_windows (id, tenant_id, name, server_id, tag, kind, start_at, end_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.ServerID, v.Tag, v.Kind, v.StartAt, v.EndAt, db.Now()); err != nil {
			return nil, err
		}
		name := ""
		if v.Name != "" {
			name = fmt.Sprintf(` "%s"`, v.Name)
		}
		c.audit(fmt.Sprintf("Added quiet hours%s (%s)", name, v.Kind), db.Str(v.ServerID))
		list, err := mutes(c.tenant, `AND w.id = ?`, id)
		if len(list) == 0 || err != nil {
			return nil, cmpErr(err, notFound("Quiet hours"))
		}
		return list[0], nil
	})
	s.route("DELETE /api/mutes/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM mute_windows WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant); err != nil {
			return nil, err
		}
		c.audit("Removed quiet hours", "")
		return okResp, nil
	})

	// ---- notifiers ----
	getNotifier := func(c *ctx, id string) (*alerts.Notifier, error) {
		n, err := db.Get[alerts.Notifier](`SELECT * FROM notifiers WHERE id = ? AND tenant_id = ?`, id, c.tenant)
		if n == nil && err == nil {
			err = notFound("Notifier")
		}
		return n, err
	}
	notifierView := func(c *ctx, id string) (any, error) {
		n, err := getNotifier(c, id)
		if err != nil {
			return nil, err
		}
		return alerts.NotifierView(n)
	}
	s.route("GET /api/notifiers", func(c *ctx) (any, error) {
		list, err := db.Select[alerts.Notifier](`SELECT * FROM notifiers WHERE tenant_id = ? ORDER BY lower(name)`, c.tenant)
		if err != nil {
			return nil, err
		}
		views := make([]*alerts.View, len(list))
		for i := range list {
			if views[i], err = alerts.NotifierView(&list[i]); err != nil {
				return nil, err
			}
		}
		return J{"types": alerts.Types, "notifiers": views, "notify_jobs": db.TenantSetting(c.tenant, "notify_jobs", "scheduled")}, nil
	})
	s.route("POST /api/notifiers", func(c *ctx) (any, error) {
		v, err := alerts.ValidateNotifier(c.body, nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO notifiers (id, tenant_id, name, type, config_enc, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, c.tenant, v.Name, v.Type, alerts.EncryptConfig(v.Config), v.Enabled, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Added notification channel "%s" (%s)`, v.Name, v.Type), "")
		return notifierView(c, id)
	})
	s.route("PUT /api/notifiers/{id}", func(c *ctx) (any, error) {
		old, err := getNotifier(c, c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := alerts.ValidateNotifier(merge(obj{"type": old.Type, "name": old.Name, "enabled": old.Enabled != 0}, c.body), old)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE notifiers SET name = ?, type = ?, config_enc = ?, enabled = ? WHERE id = ?`,
			v.Name, v.Type, alerts.EncryptConfig(v.Config), v.Enabled, old.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Updated notification channel "%s"`, v.Name), "")
		return notifierView(c, old.ID)
	})
	s.route("DELETE /api/notifiers/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM notifiers WHERE id = ? AND tenant_id = ?`, c.param("id"), c.tenant); err != nil {
			return nil, err
		}
		c.audit("Deleted notification channel", "")
		return okResp, nil
	})
	s.route("POST /api/notifiers/{id}/test", func(c *ctx) (any, error) {
		n, err := getNotifier(c, c.param("id"))
		if err != nil {
			return nil, err
		}
		if err := alerts.SendOne(n, "🔔 Fleetdeck test", "If you can read this, notifications work.", "info"); err != nil {
			return nil, httperr.New(502, "Send failed: "+err.Error())
		}
		return okResp, nil
	})

	// ---- events & settings ----
	s.route("GET /api/events", func(c *ctx) (any, error) {
		// Super admins also see instance events: sign-ins, account and instance changes.
		where, args := []string{"(e.tenant_id = ? OR (? AND e.tenant_id IS NULL))"}, []any{c.tenant, c.super}
		for _, k := range []string{"kind", "level", "server_id"} {
			if v := c.query(k); v != "" {
				where, args = append(where, "e."+k+" = ?"), append(args, v)
			}
		}
		if v := c.query("before"); v != "" {
			var n any // NaN binds as NULL: no rows, like the JS
			if f := jsNumber(v); !math.IsNaN(f) {
				n = int64(math.Ceil(f)) // id < 12.5 is id < 13
			}
			where, args = append(where, "e.id < ?"), append(args, n)
		}
		if v := c.query("q"); v != "" {
			where, args = append(where, "e.message ILIKE ?"), append(args, "%"+v+"%")
		}
		lim := math.Floor(jsNumber(c.query("limit")))
		if lim == 0 || math.IsNaN(lim) {
			lim = 100
		}
		lim = math.Min(math.Max(lim, 1), 500)
		cond := ""
		if len(where) > 0 {
			cond = "WHERE " + strings.Join(where, " AND ")
		}
		return db.Rows(fmt.Sprintf(`SELECT e.id, e.t, e.kind, e.level, e.server_id, e.actor, e.ip, e.message, s.name AS server_name
      FROM events e LEFT JOIN servers s ON s.id = e.server_id %s ORDER BY e.id DESC LIMIT %d`, cond, int(lim)), args...)
	})

	s.route("GET /api/settings", func(c *ctx) (any, error) {
		return J{"notify_jobs": db.TenantSetting(c.tenant, "notify_jobs", "scheduled")}, nil
	})
	s.route("PUT /api/settings", func(c *ctx) (any, error) {
		if v, ok := c.body["notify_jobs"]; ok {
			if v != "off" && v != "scheduled" && v != "all" {
				return nil, httperr.Bad("invalid notify_jobs")
			}
			if err := db.SetTenantSetting(c.tenant, "notify_jobs", v); err != nil {
				return nil, err
			}
		}
		c.audit("Changed settings", "")
		return okResp, nil
	})
}
