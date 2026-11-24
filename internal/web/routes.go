package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"rsc.io/qr"

	"fleetdeck/internal/admin"
	"fleetdeck/internal/alerts"
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
func ValidateServer(body map[string]any) (*ServerFields, error) {
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
		if ok, err := db.Get[int](`SELECT 1 FROM keys WHERE id = ?`, k); err != nil || ok == nil {
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
	in := jobs.ScriptInput{Name: b.str("name"), Description: b.str("description"), Body: b.str("body"), RepoID: b.str("repo_id"), Playbook: b.str("playbook")}
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

func getServer(id string) (*db.Server, error) {
	s, err := db.GetServer(id)
	if s == nil && err == nil {
		err = notFound("Server")
	}
	return s, err
}

func serverViewByID(id string) (any, error) {
	s, err := getServer(id)
	if err != nil {
		return nil, err
	}
	return serverView(s, nil)
}

type keyRow struct {
	ID            string  `db:"id"`
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

func keyViewByID(id string) (any, error) {
	k, err := db.Get[keyRow](`SELECT * FROM keys WHERE id = ?`, id)
	if k == nil || err != nil {
		return nil, cmpErr(err, notFound("Key"))
	}
	return keyView(*k)
}

func scriptView(id string) (any, error) {
	sc, err := jobs.GetScript(id)
	if sc == nil || err != nil {
		return nil, cmpErr(err, notFound("Script"))
	}
	return sc, nil
}

type scheduleOut struct {
	jobs.Schedule
	ScriptName *string `db:"script_name" json:"script_name,omitempty"`
	Next       *int64  `db:"-" json:"next"`
}

func schedules(where string, args ...any) ([]scheduleOut, error) {
	list, err := db.Select[scheduleOut](`SELECT x.*, s.name AS script_name FROM schedules x LEFT JOIN scripts s ON s.id = x.script_id `+where, args...)
	for i := range list {
		if next, err := jobs.NextRuns(list[i].Cron, 1, time.Now()); err == nil && len(next) > 0 {
			list[i].Next = &next[0]
		}
	}
	return list, err
}

func scheduleView(id string) (any, error) {
	list, err := schedules(`WHERE x.id = ?`, id)
	if len(list) == 0 || err != nil {
		return nil, cmpErr(err, notFound("Schedule"))
	}
	return list[0], nil
}

type triggerOut struct {
	jobs.Trigger
	ScriptName *string `db:"script_name" json:"script_name,omitempty"`
}

func triggers(where string, args ...any) ([]triggerOut, error) {
	return db.Select[triggerOut](`SELECT x.*, s.name AS script_name FROM triggers x LEFT JOIN scripts s ON s.id = x.script_id `+where, args...)
}

func getTrigger(id string) (*jobs.Trigger, error) {
	t, err := db.Get[jobs.Trigger](`SELECT * FROM triggers WHERE id = ?`, id)
	if t == nil && err == nil {
		err = notFound("Webhook")
	}
	return t, err
}

type ruleOut struct {
	alerts.Rule
	Enabled    bool    `db:"-" json:"enabled"`
	ServerName *string `db:"server_name" json:"server_name"`
}

func rules(where string, args ...any) ([]ruleOut, error) {
	list, err := db.Select[ruleOut](`SELECT r.*, s.name AS server_name FROM alert_rules r LEFT JOIN servers s ON s.id = r.server_id `+where, args...)
	for i := range list {
		list[i].Enabled = list[i].Rule.Enabled != 0
	}
	return list, err
}

func ruleView(id string) (any, error) {
	list, err := rules(`WHERE r.id = ?`, id)
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

func mutes(where string, args ...any) ([]muteOut, error) {
	list, err := db.Select[muteOut](`SELECT w.*, s.name AS server_name FROM mute_windows w LEFT JOIN servers s ON s.id = w.server_id `+where, args...)
	for i := range list {
		list[i].Active = alerts.InWindow(&list[i].Mute, time.Now())
	}
	return list, err
}

// Secret values never leave the server; plain ones are shown so they can be edited.
func variableView(v *jobs.Variable) (any, error) {
	used, err := varUsers(v.Name)
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

func varUsers(name string) ([]idName, error) {
	return db.Select[idName](`SELECT id, name FROM scripts WHERE EXISTS (SELECT 1 FROM json_each(scripts.vars) WHERE value = ?) ORDER BY name`, name)
}

func getVariable(id string) (*jobs.Variable, error) {
	v, err := db.Get[jobs.Variable](`SELECT * FROM variables WHERE id = ?`, id)
	if v == nil && err == nil {
		err = notFound("Variable")
	}
	return v, err
}

func getRepo(id string) (*repos.Repo, error) {
	r, err := repos.GetRepo(id)
	if r == nil && err == nil {
		err = notFound("Repository")
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
	s.authRoutes()
	s.ssoRoutes()
	s.serverRoutes()
	s.libraryRoutes()
	s.jobRoutes()
	s.alertRoutes()
}

func (s *Server) authRoutes() {
	s.route("GET /api/auth/state", func(c *ctx) (any, error) {
		sso := ""
		if ssoLinked() {
			if u, err := url.Parse(db.Setting("oidc_issuer", "")); err == nil {
				sso = u.Hostname()
			}
		}
		return J{"setup": needsSetup(), "authed": c.sess != nil, "totp": db.Setting("totp_secret", "") != "", "sso": sso, "password": passwordLogin()}, nil
	}, open)

	s.route("POST /api/auth/setup", func(c *ctx) (any, error) {
		if !needsSetup() {
			return nil, httperr.New(409, "Already set up")
		}
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		tok := strings.TrimSpace(c.body.str("token"))
		s.mu.Lock()
		valid := s.setupToken != "" && tok == s.setupToken
		s.mu.Unlock()
		if !valid {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Invalid setup token (see the server console)")
		}
		pw := c.body.str("password")
		if jsLen(pw) < 10 {
			return nil, httperr.Bad("Password must be at least 10 characters")
		}
		s.mu.Lock()
		claimed := s.setupToken != ""
		s.setupToken = ""
		s.mu.Unlock()
		if !claimed { // a concurrent setup won
			return nil, httperr.New(409, "Already set up")
		}
		if err := db.SetSetting("password_hash", sec.HashPassword(pw)); err != nil {
			return nil, err
		}
		if err := c.createSession(); err != nil {
			return nil, err
		}
		c.audit("Initial setup completed", "")
		return okResp, nil
	}, open)

	s.route("POST /api/auth/login", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		if needsSetup() {
			return nil, httperr.New(409, "Setup required")
		}
		if !passwordLogin() {
			return nil, httperr.New(403, "Password sign-in is turned off. Use single sign-on.")
		}
		if !sec.VerifyPassword(c.body.str("password"), db.Setting("password_hash", "")) {
			s.recordFailure(c.ip)
			db.Event("security", "Failed login (password)", db.EventOpts{Level: "warn", IP: c.ip})
			return nil, httperr.New(401, "Wrong password")
		}
		secret, err := totpSecret()
		if err != nil {
			return nil, err
		}
		if secret != "" && !c.body.truthy("code") {
			return J{"totp_required": true}, nil
		}
		if ok, err := s.checkTOTP(c.body["code"]); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			s.recordFailure(c.ip)
			db.Event("security", "Failed login (2FA code)", db.EventOpts{Level: "warn", IP: c.ip})
			return nil, httperr.New(401, "Invalid 2FA code")
		}
		s.mu.Lock()
		delete(s.failures, c.ip)
		s.mu.Unlock()
		if err := c.createSession(); err != nil {
			return nil, err
		}
		db.Event("security", "Logged in", db.EventOpts{Actor: "admin", IP: c.ip})
		return okResp, nil
	}, open)

	s.route("POST /api/auth/logout", func(c *ctx) (any, error) {
		if c.sess != nil {
			if err := exec(`DELETE FROM sessions WHERE hash = ?`, c.sess.Hash); err != nil {
				return nil, err
			}
		}
		c.w.Header().Set("Set-Cookie", s.sessionCookie(c.r, "", 0))
		return okResp, nil
	}, open)

	// ---- account ----
	s.route("GET /api/account", func(c *ctx) (any, error) {
		list, err := db.Select[session](`SELECT * FROM sessions ORDER BY seen_at DESC`)
		if err != nil {
			return nil, err
		}
		out := []J{}
		for _, x := range list {
			out = append(out, J{"id": x.Hash[:16], "created_at": x.CreatedAt, "seen_at": x.SeenAt, "ip": x.IP, "ua": x.UA, "current": x.Hash == c.sess.Hash})
		}
		return J{"totp": db.Setting("totp_secret", "") != "", "sessions": out}, nil
	})

	s.route("POST /api/account/password", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		if !sec.VerifyPassword(c.body.str("current"), db.Setting("password_hash", "")) {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Current password is wrong")
		}
		next := c.body.str("next")
		if jsLen(next) < 10 {
			return nil, httperr.Bad("New password must be at least 10 characters")
		}
		if err := db.SetSetting("password_hash", sec.HashPassword(next)); err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM sessions WHERE hash != ?`, c.sess.Hash); err != nil {
			return nil, err
		}
		c.audit("Password changed; other sessions signed out", "")
		return okResp, nil
	})

	s.route("POST /api/account/totp/begin", func(c *ctx) (any, error) {
		if secret, err := totpSecret(); err != nil || secret != "" {
			return nil, cmpErr(err, httperr.New(409, "Two-factor sign-in is already on. Turn it off first (needs password and code)."))
		}
		secret := sec.NewTOTPSecret()
		s.mu.Lock()
		s.pendingTOTP[c.sess.Hash] = secret
		s.mu.Unlock()
		uri := "otpauth://totp/Fleetdeck:admin?secret=" + secret + "&issuer=Fleetdeck&algorithm=SHA1&digits=6&period=30"
		img, err := qrDataURI(uri)
		return J{"secret": secret, "uri": uri, "qr": img}, err
	})

	s.route("POST /api/account/totp/enable", func(c *ctx) (any, error) {
		s.mu.Lock()
		secret := s.pendingTOTP[c.sess.Hash]
		s.mu.Unlock()
		if secret == "" {
			return nil, httperr.Bad("Start 2FA setup first")
		}
		if _, ok := sec.VerifyTOTP(secret, strings.Join(strings.Fields(c.body.str("code")), ""), time.Now()); !ok {
			return nil, httperr.Bad("Code does not match, check your device clock")
		}
		if err := db.SetSetting("totp_secret", sec.Encrypt(secret)); err != nil {
			return nil, err
		}
		if err := db.SetSetting("totp_last", -1); err != nil {
			return nil, err
		}
		s.mu.Lock()
		delete(s.pendingTOTP, c.sess.Hash)
		s.mu.Unlock()
		c.audit("Two-factor authentication enabled", "")
		return okResp, nil
	})

	s.route("POST /api/account/totp/disable", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		ok := sec.VerifyPassword(c.body.str("password"), db.Setting("password_hash", ""))
		if ok {
			var err error
			if ok, err = s.checkTOTP(c.body["code"]); err != nil {
				return nil, err
			}
		}
		if !ok {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Password or code is wrong")
		}
		if err := db.SetSetting("totp_secret", nil); err != nil {
			return nil, err
		}
		c.audit("Two-factor authentication disabled", "")
		return okResp, nil
	})

	s.route("DELETE /api/account/sessions/{id}", func(c *ctx) (any, error) {
		r, err := db.Exec(`DELETE FROM sessions WHERE substr(hash, 1, 16) = ? AND hash != ?`, c.param("id"), c.sess.Hash)
		if err != nil {
			return nil, err
		}
		n, _ := r.RowsAffected()
		c.audit("Revoked a session", "")
		return J{"ok": n > 0}, nil
	})

	s.route("POST /api/account/sessions/revoke-others", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM sessions WHERE hash != ?`, c.sess.Hash); err != nil {
			return nil, err
		}
		c.audit("Revoked all other sessions", "")
		return okResp, nil
	})
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
		list, err := db.Select[db.Server](`SELECT * FROM servers ORDER BY name COLLATE NOCASE`)
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
		v, err := ValidateServer(c.body)
		if err != nil {
			return nil, err
		}
		sudo, _, err := sudoField(c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO servers (id, name, host, port, user, key_id, tags, notes, paused, created_at, sudo_enc) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.Host, v.Port, v.User, v.KeyID, v.Tags, v.Notes, v.Paused, db.Now(), sudo); err != nil {
			return nil, err
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

	s.route("GET /api/servers/{id}", func(c *ctx) (any, error) { return serverViewByID(c.param("id")) })

	s.route("PUT /api/servers/{id}", func(c *ctx) (any, error) {
		old, err := getServer(c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := ValidateServer(merge(toObj(old), c.body))
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
		if err := exec(`UPDATE servers SET name = ?, host = ?, port = ?, user = ?, key_id = ?, tags = ?, notes = ?, paused = ?, host_key = ?, host_key_raw = ? WHERE id = ?`,
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
		srv, err := getServer(c.param("id"))
		if err != nil {
			return nil, err
		}
		ssh.Drop(srv.ID)
		if err := db.Tx(func(tx *sqlx.Tx) error {
			for _, q := range []string{`DELETE FROM metrics WHERE server_id = ?`, `DELETE FROM container_metrics WHERE server_id = ?`, `DELETE FROM servers WHERE id = ?`} {
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
		srv, err := getServer(c.param("id"))
		if err != nil {
			return nil, err
		}
		probe.Probe(srv)
		return serverViewByID(srv.ID)
	})

	s.route("POST /api/servers/{id}/test", func(c *ctx) (any, error) {
		srv, err := getServer(c.param("id"))
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
		srv, err := getServer(c.param("id"))
		if err != nil {
			return nil, err
		}
		fp, ok := ssh.AcceptHostKey(srv.ID)
		if !ok {
			return nil, httperr.Bad("No pending host key change for this server")
		}
		db.Event("security", fmt.Sprintf("Accepted new host key %s for %s", fp, srv.Name), db.EventOpts{Level: "warn", Actor: "admin", IP: c.ip, ServerID: srv.ID})
		go probe.Probe(srv)
		return J{"ok": true, "host_key": fp}, nil
	})

	// Admin views read with the request's context (they stop when the browser leaves);
	// actions use detached(c), so they finish like they did in the JS server.
	onServer := func(pattern string, fn func(c *ctx, srv *db.Server) (any, error)) {
		s.route(pattern, func(c *ctx) (any, error) {
			srv, err := getServer(c.param("id"))
			if err != nil {
				return nil, err
			}
			return fn(c, srv)
		})
	}
	onServer("GET /api/servers/{id}/metrics", func(c *ctx, srv *db.Server) (any, error) { return probe.Series(srv.ID, c.query("range")) })
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

	// ---- authorized_keys across the fleet ----
	s.route("GET /api/accounts", func(c *ctx) (any, error) {
		list, err := db.Select[db.Server](`SELECT * FROM servers WHERE paused = 0 ORDER BY name COLLATE NOCASE`)
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
		}](`SELECT id, name, public_key FROM keys`)
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
	})

	s.route("POST /api/servers/{id}/authorized-keys/revoke", func(c *ctx) (any, error) {
		srv, err := getServer(c.param("id"))
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
			db.EventOpts{Level: "warn", Actor: "admin", IP: c.ip, ServerID: srv.ID})
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
	s.route("GET /api/keys", func(c *ctx) (any, error) {
		list, err := db.Select[keyRow](`SELECT * FROM keys ORDER BY name COLLATE NOCASE`)
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
		if err := exec(`INSERT INTO keys (id, name, public_key, private_enc, passphrase_enc, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, name, pub, sec.Encrypt(priv), passEnc, db.Now()); err != nil {
			return nil, err
		}
		verb := "Imported"
		if gen {
			verb = "Generated"
		}
		c.audit(fmt.Sprintf(`%s SSH key "%s"`, verb, name), "")
		return keyViewByID(id)
	})

	s.route("PUT /api/keys/{id}", func(c *ctx) (any, error) {
		name := strings.TrimSpace(c.body.str("name"))
		if name == "" || jsLen(name) > 64 {
			return nil, httperr.Bad("Name is required")
		}
		old, err := db.Get[string](`SELECT name FROM keys WHERE id = ?`, c.param("id"))
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Key"))
		}
		if err := exec(`UPDATE keys SET name = ? WHERE id = ?`, name, c.param("id")); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Renamed SSH key "%s" to "%s"`, *old, name), "")
		return keyViewByID(c.param("id"))
	})

	s.route("DELETE /api/keys/{id}", func(c *ctx) (any, error) {
		k, err := db.Get[keyRow](`SELECT * FROM keys WHERE id = ?`, c.param("id"))
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
	})

	// ---- scripts ----
	s.route("GET /api/scripts", func(c *ctx) (any, error) {
		return db.Select[jobs.Script](`SELECT * FROM scripts ORDER BY builtin DESC, name COLLATE NOCASE`)
	})
	s.route("POST /api/scripts", func(c *ctx) (any, error) {
		v, err := jobs.ValidateScript(scriptInput(c.body))
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO scripts (id, name, description, body, as_root, params, builtin, created_at, kind, repo_id, playbook, vars) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
			id, v.Name, v.Description, v.Body, v.AsRoot, v.Params, db.Now(), v.Kind, v.RepoID, v.Playbook, v.Vars); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created script "%s"`, v.Name), "")
		return scriptView(id)
	})
	s.route("PUT /api/scripts/{id}", func(c *ctx) (any, error) {
		if _, err := scriptView(c.param("id")); err != nil {
			return nil, err
		}
		v, err := jobs.ValidateScript(scriptInput(c.body))
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE scripts SET name = ?, description = ?, body = ?, as_root = ?, params = ?, kind = ?, repo_id = ?, playbook = ?, vars = ? WHERE id = ?`,
			v.Name, v.Description, v.Body, v.AsRoot, v.Params, v.Kind, v.RepoID, v.Playbook, v.Vars, c.param("id")); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Edited script "%s"`, v.Name), "")
		return scriptView(c.param("id"))
	})
	s.route("DELETE /api/scripts/{id}", func(c *ctx) (any, error) {
		sc, err := jobs.GetScript(c.param("id"))
		if sc == nil || err != nil {
			return nil, cmpErr(err, notFound("Script"))
		}
		if sc.Builtin {
			return nil, httperr.New(409, "Built-in scripts can't be deleted (the server pages use them). Edit it instead.")
		}
		if err := exec(`DELETE FROM scripts WHERE id = ?`, sc.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Deleted script "%s"`, sc.Name), "")
		return okResp, nil
	})

	// ---- shared variables ----
	s.route("GET /api/variables", func(c *ctx) (any, error) {
		list, err := db.Select[jobs.Variable](`SELECT * FROM variables ORDER BY name`)
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
		v, err := jobs.ValidateVariable(variableInput(c.body), nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO variables (id, name, value_enc, secret, description, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.ValueEnc, v.Secret, v.Description, db.Now()); err != nil {
			return nil, err
		}
		secret := ""
		if v.Secret != 0 {
			secret = "secret "
		}
		c.audit(fmt.Sprintf("Created %svariable %s", secret, v.Name), "")
		nv, err := getVariable(id)
		if err != nil {
			return nil, err
		}
		return variableView(nv)
	})
	s.route("PUT /api/variables/{id}", func(c *ctx) (any, error) {
		old, err := getVariable(c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := jobs.ValidateVariable(variableInput(c.body), old)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE variables SET value_enc = ?, secret = ?, description = ? WHERE id = ?`, v.ValueEnc, v.Secret, v.Description, old.ID); err != nil {
			return nil, err
		}
		c.audit("Updated variable "+v.Name, "")
		nv, err := getVariable(old.ID)
		if err != nil {
			return nil, err
		}
		return variableView(nv)
	})
	s.route("DELETE /api/variables/{id}", func(c *ctx) (any, error) {
		v, err := getVariable(c.param("id"))
		if err != nil {
			return nil, err
		}
		used, err := varUsers(v.Name)
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
		list, err := db.Select[repos.Repo](`SELECT * FROM repos ORDER BY name COLLATE NOCASE`)
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
		v, err := repos.ValidateRepo(repoInput(c.body), nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO repos (id, name, url, branch, auth, key_id, username, token_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.URL, v.Branch, v.Auth, v.KeyID, v.Username, v.TokenEnc, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Added repository "%s" (%s)`, v.Name, v.URL), "")
		r, err := getRepo(id)
		if err != nil {
			return nil, err
		}
		go repos.Sync(r, nil) // first clone in the background; errors show on the repo
		return repos.RepoView(r), nil
	})
	s.route("PUT /api/repos/{id}", func(c *ctx) (any, error) {
		old, err := getRepo(c.param("id"))
		if err != nil {
			return nil, err
		}
		base := obj{"name": old.Name, "url": old.URL, "branch": old.Branch, "auth": old.Auth, "key_id": db.Str(old.KeyID), "username": db.Str(old.Username)}
		v, err := repos.ValidateRepo(repoInput(merge(base, c.body)), old)
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
		r, err := getRepo(old.ID)
		if err != nil {
			return nil, err
		}
		return repos.RepoView(r), nil
	})
	s.route("DELETE /api/repos/{id}", func(c *ctx) (any, error) {
		r, err := getRepo(c.param("id"))
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
		r, err := getRepo(r.ID)
		if err != nil {
			return nil, err
		}
		return repos.RepoView(r), nil
	}
	s.route("POST /api/repos/{id}/sync", func(c *ctx) (any, error) {
		r, err := getRepo(c.param("id"))
		if err != nil {
			return nil, err
		}
		return syncView(r)
	})
	s.route("POST /api/repos/{id}/forget-hostkey", func(c *ctx) (any, error) {
		r, err := getRepo(c.param("id"))
		if err != nil {
			return nil, err
		}
		host, err := repos.ForgetHostKey(r)
		if err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf(`Forgot the pinned Git host key of %s (repository "%s"); the next sync pins the new one`, host, r.Name),
			db.EventOpts{Level: "warn", Actor: "admin", IP: c.ip})
		return syncView(r)
	})
	s.route("GET /api/repos/{id}/files", func(c *ctx) (any, error) {
		r, err := getRepo(c.param("id"))
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
		return jobs.ListJobs(jobs.ListOpts{Server: c.query("server"), Limit: c.query("limit"), Before: c.query("before"), Status: c.query("status")})
	})

	s.route("POST /api/jobs", func(c *ctx) (any, error) {
		b := c.body
		var spec jobs.Spec
		if b.truthy("script_id") {
			sc, err := jobs.GetScript(jsString(b["script_id"]))
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
		spec.Targets, spec.Actor = targetsOf(b["targets"]), "admin"
		id, err := jobs.StartJob(spec)
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Started job #%d "%s"`, id, spec.Name), "")
		return J{"id": id}, nil
	})

	getJob := func(c *ctx) (*jobs.Job, error) {
		id, ok := jobID(c)
		if !ok {
			return nil, notFound("Job")
		}
		j, err := jobs.GetJob(id)
		if j == nil && err == nil {
			err = notFound("Job")
		}
		return j, err
	}
	s.route("GET /api/jobs/{id}", func(c *ctx) (any, error) { return getJob(c) })

	s.route("POST /api/jobs/{id}/cancel", func(c *ctx) (any, error) {
		if id, ok := jobID(c); !ok || !jobs.CancelJob(id) {
			return nil, httperr.New(409, "Job is not running")
		}
		c.audit("Cancelled job #"+c.param("id"), "")
		return okResp, nil
	})

	s.route("POST /api/jobs/{id}/rerun", func(c *ctx) (any, error) {
		j, err := getJob(c)
		if err != nil {
			return nil, err
		}
		servers := []string{}
		for _, r := range j.Runs {
			if r.ServerID != jobs.Ansible && (!c.body.truthy("failed_only") || (r.Status != "success" && r.Status != "skipped")) {
				servers = append(servers, r.ServerID)
			}
		}
		id, err := jobs.StartJob(jobs.Spec{ScriptID: db.Str(j.ScriptID), Name: j.Name, Body: j.Body, Kind: j.Kind, RepoID: db.Str(j.RepoID), Options: j.Options,
			AsRoot: j.AsRoot != 0, Params: j.Params, Targets: jobs.Targets{Servers: servers, Tags: []string{}}, Actor: "admin"})
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Re-ran job #%d as #%d", j.ID, id), "")
		return J{"id": id}, nil
	})

	s.route("GET /api/jobs/{id}/stream", func(c *ctx) (any, error) {
		job, err := getJob(c)
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
			select {
			case msgs <- m:
			default:
				once.Do(func() { close(lost) })
			}
		})
		if !live {
			if j, err := jobs.GetJob(job.ID); err == nil && j != nil {
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
	s.route("GET /api/schedules", func(c *ctx) (any, error) { return schedules(`ORDER BY x.name COLLATE NOCASE`) })
	s.route("POST /api/schedules", func(c *ctx) (any, error) {
		v, err := jobs.ValidateSchedule(scheduleInput(c.body), true)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO schedules (id, name, script_id, cron, targets, params, enabled, once, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.ScriptID, v.Cron, v.Targets, v.Params, v.Enabled, v.Once, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created schedule "%s" (%s)`, v.Name, *v.Cron), "")
		return scheduleView(id)
	})
	getSchedule := func(c *ctx) (*jobs.Schedule, error) {
		sc, err := db.Get[jobs.Schedule](`SELECT * FROM schedules WHERE id = ?`, c.param("id"))
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
		v, err := jobs.ValidateSchedule(scheduleInput(merge(toObj(old), c.body)), true)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE schedules SET name = ?, script_id = ?, cron = ?, targets = ?, params = ?, enabled = ?, once = ? WHERE id = ?`,
			v.Name, v.ScriptID, v.Cron, v.Targets, v.Params, v.Enabled, v.Once, old.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Updated schedule "%s"`, v.Name), "")
		return scheduleView(old.ID)
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
		id, err := jobs.RunSchedule(sc, "admin")
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
	triggerView := func(id string) (*triggerOut, error) {
		list, err := triggers(`WHERE x.id = ?`, id)
		if len(list) == 0 || err != nil {
			return nil, cmpErr(err, notFound("Webhook"))
		}
		return &list[0], nil
	}
	s.route("GET /api/triggers", func(c *ctx) (any, error) { return triggers(`ORDER BY x.name COLLATE NOCASE`) })
	s.route("POST /api/triggers", func(c *ctx) (any, error) {
		v, err := jobs.ValidateSchedule(scheduleInput(c.body), false)
		if err != nil {
			return nil, err
		}
		id, token := db.NewID(), sec.Token()
		if err := exec(`INSERT INTO triggers (id, name, script_id, targets, params, token_hash, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.ScriptID, v.Targets, v.Params, sec.SHA256(token), v.Enabled, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Created webhook "%s"`, v.Name), "")
		t, err := triggerView(id)
		if err != nil {
			return nil, err
		}
		return struct {
			*triggerOut
			Token string `json:"token"`
		}{t, token}, nil
	})
	s.route("PUT /api/triggers/{id}", func(c *ctx) (any, error) {
		old, err := getTrigger(c.param("id"))
		if err != nil {
			return nil, err
		}
		v, err := jobs.ValidateSchedule(scheduleInput(merge(toObj(old), c.body)), false)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE triggers SET name = ?, script_id = ?, targets = ?, params = ?, enabled = ? WHERE id = ?`,
			v.Name, v.ScriptID, v.Targets, v.Params, v.Enabled, old.ID); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Updated webhook "%s"`, v.Name), "")
		return triggerView(old.ID)
	})
	s.route("POST /api/triggers/{id}/rotate", func(c *ctx) (any, error) {
		t, err := getTrigger(c.param("id"))
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
		t, err := getTrigger(c.param("id"))
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
		db.Event("audit", fmt.Sprintf(`Webhook "%s" started job #%d`, t.Name, id), db.EventOpts{Actor: "webhook", IP: c.ip})
		return J{"ok": true, "job": id}, nil
	}, open, raw)
}

var settingRanges = []struct {
	key           string
	lo, hi, value float64
}{{"retention_metrics_days", 1, 365, 30}, {"retention_jobs_days", 1, 3650, 90}, {"retention_events_days", 1, 3650, 180}}

func (s *Server) alertRoutes() {
	s.route("GET /api/alerts", func(c *ctx) (any, error) {
		list, err := rules(`ORDER BY r.created_at`)
		if err != nil {
			return nil, err
		}
		firing, err := alerts.Firing()
		if err != nil {
			return nil, err
		}
		history, err := db.Rows(`SELECT * FROM events WHERE kind = 'alert' ORDER BY id DESC LIMIT 100`)
		return J{"metrics": alerts.Metrics, "rules": list, "firing": firing, "history": history}, err
	})
	s.route("POST /api/alerts/rules", func(c *ctx) (any, error) {
		v, err := alerts.ValidateRule(c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO alert_rules (id, server_id, tag, metric, threshold, minutes, enabled, created_at, mount) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.ServerID, v.Tag, v.Metric, v.Threshold, v.Minutes, v.Enabled, db.Now(), v.Mount); err != nil {
			return nil, err
		}
		c.audit("Created alert rule "+v.Metric, "")
		return ruleView(id)
	})
	s.route("PUT /api/alerts/rules/{id}", func(c *ctx) (any, error) {
		old, err := db.Get[alerts.Rule](`SELECT * FROM alert_rules WHERE id = ?`, c.param("id"))
		if old == nil || err != nil {
			return nil, cmpErr(err, notFound("Rule"))
		}
		base := toObj(old)
		base["enabled"] = old.Enabled != 0
		v, err := alerts.ValidateRule(merge(base, c.body))
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE alert_rules SET server_id = ?, tag = ?, metric = ?, threshold = ?, minutes = ?, enabled = ?, mount = ? WHERE id = ?`,
			v.ServerID, v.Tag, v.Metric, v.Threshold, v.Minutes, v.Enabled, v.Mount, old.ID); err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM alert_state WHERE rule_id = ?`, old.ID); err != nil { // re-evaluated on the next probe
			return nil, err
		}
		c.audit("Updated alert rule "+v.Metric, "")
		return ruleView(old.ID)
	})
	s.route("DELETE /api/alerts/rules/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM alert_rules WHERE id = ?`, c.param("id")); err != nil {
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
		list, err := mutes(`ORDER BY w.kind, w.start_at`)
		return J{"windows": list, "tz": localTZ()}, err
	})
	s.route("POST /api/mutes", func(c *ctx) (any, error) {
		v, err := alerts.ValidateMute(c.body)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO mute_windows (id, name, server_id, tag, kind, start_at, end_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.ServerID, v.Tag, v.Kind, v.StartAt, v.EndAt, db.Now()); err != nil {
			return nil, err
		}
		name := ""
		if v.Name != "" {
			name = fmt.Sprintf(` "%s"`, v.Name)
		}
		c.audit(fmt.Sprintf("Added quiet hours%s (%s)", name, v.Kind), db.Str(v.ServerID))
		list, err := mutes(`WHERE w.id = ?`, id)
		if len(list) == 0 || err != nil {
			return nil, cmpErr(err, notFound("Quiet hours"))
		}
		return list[0], nil
	})
	s.route("DELETE /api/mutes/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM mute_windows WHERE id = ?`, c.param("id")); err != nil {
			return nil, err
		}
		c.audit("Removed quiet hours", "")
		return okResp, nil
	})

	// ---- notifiers ----
	getNotifier := func(id string) (*alerts.Notifier, error) {
		n, err := db.Get[alerts.Notifier](`SELECT * FROM notifiers WHERE id = ?`, id)
		if n == nil && err == nil {
			err = notFound("Notifier")
		}
		return n, err
	}
	notifierView := func(id string) (any, error) {
		n, err := getNotifier(id)
		if err != nil {
			return nil, err
		}
		return alerts.NotifierView(n)
	}
	s.route("GET /api/notifiers", func(c *ctx) (any, error) {
		list, err := db.Select[alerts.Notifier](`SELECT * FROM notifiers ORDER BY name COLLATE NOCASE`)
		if err != nil {
			return nil, err
		}
		views := make([]*alerts.View, len(list))
		for i := range list {
			if views[i], err = alerts.NotifierView(&list[i]); err != nil {
				return nil, err
			}
		}
		return J{"types": alerts.Types, "notifiers": views, "notify_jobs": db.Setting("notify_jobs", "scheduled")}, nil
	})
	s.route("POST /api/notifiers", func(c *ctx) (any, error) {
		v, err := alerts.ValidateNotifier(c.body, nil)
		if err != nil {
			return nil, err
		}
		id := db.NewID()
		if err := exec(`INSERT INTO notifiers (id, name, type, config_enc, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, v.Name, v.Type, alerts.EncryptConfig(v.Config), v.Enabled, db.Now()); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf(`Added notification channel "%s" (%s)`, v.Name, v.Type), "")
		return notifierView(id)
	})
	s.route("PUT /api/notifiers/{id}", func(c *ctx) (any, error) {
		old, err := getNotifier(c.param("id"))
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
		return notifierView(old.ID)
	})
	s.route("DELETE /api/notifiers/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM notifiers WHERE id = ?`, c.param("id")); err != nil {
			return nil, err
		}
		c.audit("Deleted notification channel", "")
		return okResp, nil
	})
	s.route("POST /api/notifiers/{id}/test", func(c *ctx) (any, error) {
		n, err := getNotifier(c.param("id"))
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
		var where []string
		var args []any
		for _, k := range []string{"kind", "level", "server_id"} {
			if v := c.query(k); v != "" {
				where, args = append(where, "e."+k+" = ?"), append(args, v)
			}
		}
		if v := c.query("before"); v != "" {
			var n any // NaN binds as NULL: no rows, like the JS
			if f := jsNumber(v); !math.IsNaN(f) {
				n = f
			}
			where, args = append(where, "e.id < ?"), append(args, n)
		}
		if v := c.query("q"); v != "" {
			where, args = append(where, "e.message LIKE ?"), append(args, "%"+v+"%")
		}
		lim := math.Floor(jsNumber(c.query("limit")))
		if lim == 0 || math.IsNaN(lim) {
			lim = 100
		}
		lim = math.Min(math.Max(lim, 1), 500) // negative LIMIT = unlimited in SQLite
		cond := ""
		if len(where) > 0 {
			cond = "WHERE " + strings.Join(where, " AND ")
		}
		return db.Rows(fmt.Sprintf(`SELECT e.*, s.name AS server_name FROM events e LEFT JOIN servers s ON s.id = e.server_id %s ORDER BY e.id DESC LIMIT %d`, cond, int(lim)), args...)
	})

	s.route("GET /api/settings", func(c *ctx) (any, error) {
		out := J{"notify_jobs": db.Setting("notify_jobs", "scheduled"), "secret_from_env": os.Getenv("FLEETDECK_SECRET") != ""}
		for _, r := range settingRanges {
			if n := jsNumber(db.Setting(r.key, strconv.Itoa(int(r.value)))); !math.IsNaN(n) && !math.IsInf(n, 0) {
				out[r.key] = n
			} else {
				out[r.key] = nil
			}
		}
		return out, nil
	})
	s.route("PUT /api/settings", func(c *ctx) (any, error) {
		for _, r := range settingRanges {
			if !c.body.has(r.key) {
				continue
			}
			v := math.Floor(c.body.num(r.key) + 0.5) // Math.round
			if !(v >= r.lo && v <= r.hi) {
				return nil, httperr.Bad(fmt.Sprintf("%s must be %d-%d", r.key, int(r.lo), int(r.hi)))
			}
			if err := db.SetSetting(r.key, int64(v)); err != nil {
				return nil, err
			}
		}
		if v, ok := c.body["notify_jobs"]; ok {
			if v != "off" && v != "scheduled" && v != "all" {
				return nil, httperr.Bad("invalid notify_jobs")
			}
			if err := db.SetSetting("notify_jobs", v); err != nil {
				return nil, err
			}
		}
		c.audit("Changed settings", "")
		return okResp, nil
	})
}
