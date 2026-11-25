// Package alerts: Beszel-style alerts: rules evaluated after every probe, notifications on fire and resolve.
package alerts

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/probe"
)

// Metric is an alertable value. Def is the suggested threshold when picking the metric in the UI.
type Metric struct {
	Key   string   `json:"-"`
	Label string   `json:"label"`
	Unit  string   `json:"unit"`
	Hint  string   `json:"hint,omitempty"`
	Col   string   `json:"col,omitempty"` // metrics column (expression) for N-minute averages
	Scale float64  `json:"scale,omitempty"`
	Def   *float64 `json:"def,omitempty"`
	Below bool     `json:"below,omitempty"` // fires when the value drops below the threshold
}

// MetricList marshals as a JSON object in list order: the UI's metric picker shows them in that order.
type MetricList []Metric

func (l MetricList) Get(key string) *Metric {
	for i := range l {
		if l[i].Key == key {
			return &l[i]
		}
	}
	return nil
}

func (l MetricList) MarshalJSON() ([]byte, error) {
	return ordered(l, func(m Metric) (string, any) { return m.Key, m })
}

func def(v float64) *float64 { return &v }

var Metrics = MetricList{
	{Key: "down", Label: "Unreachable", Unit: "min", Hint: "Fires when the server cannot be reached for N minutes"},
	{Key: "cpu", Label: "CPU usage", Unit: "%", Col: "cpu", Scale: 100, Def: def(90)},
	{Key: "mem", Label: "Memory usage", Unit: "%", Col: "mem", Scale: 100, Def: def(90)},
	{Key: "swap", Label: "Swap usage", Unit: "%", Col: "swap", Scale: 100, Def: def(50)},
	{Key: "disk", Label: "Disk usage (fullest mount)", Unit: "%", Def: def(90)},
	{Key: "inodes", Label: "Inode usage (fullest mount)", Unit: "%", Def: def(90)},
	{Key: "load", Label: "Load per CPU core", Unit: "", Col: "load1", Scale: 1, Def: def(1.5)},
	{Key: "temp", Label: "Temperature", Unit: "°C", Col: "temp", Scale: 1, Def: def(80)},
	{Key: "updates", Label: "Pending updates", Unit: "", Def: def(0)},
	{Key: "security", Label: "Pending security updates", Unit: "", Def: def(0)},
	{Key: "failed", Label: "Failed systemd units", Unit: "", Def: def(0)},
	{Key: "reboot", Label: "Reboot required", Unit: ""},
	{Key: "smart", Label: "Disks with S.M.A.R.T. problems", Unit: "", Def: def(0)},
	{Key: "unhealthy", Label: "Unhealthy or restarting containers", Unit: "", Def: def(0)},
	{Key: "net", Label: "Network traffic (in + out)", Unit: " MB/s", Col: "rx + tx", Scale: 1e-6, Def: def(50)},
	{Key: "gpu", Label: "GPU usage (busiest GPU)", Unit: "%", Col: "gpu", Scale: 100, Def: def(90)},
	{Key: "custom", Label: "Custom metric", Unit: "", Hint: "One of your custom metrics, from a server's Metrics panel"},
	{Key: "check", Label: "Endpoint check failing", Unit: "min", Hint: "Fires when one of the server's endpoint checks (Network tab) fails for N minutes"},
	{Key: "tls_days", Label: "TLS certificate days left", Unit: " days", Def: def(14), Below: true, Hint: "The soonest-expiring certificate of the server's HTTPS checks"},
}

// ordered writes a JSON object with keys in slice order (Go maps would sort them).
func ordered[T any](items []T, kv func(T) (string, any)) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, it := range items {
		k, v := kv(it)
		kj, _ := json.Marshal(k)
		vj, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(kj)
		b.WriteByte(':')
		b.Write(vj)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// ---- JS value semantics for request bodies (decoded JSON) ----

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// jsNum is JS Number(v).
func jsNum(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		return jsNum(string(x))
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return math.NaN()
}

// jsStr is JS String(v) for the values a JSON body can hold.
func jsStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return "null"
	}
	return fmt.Sprint(v)
}

// orEmpty is JS String(v || "").
func orEmpty(v any) string {
	if !truthy(v) {
		return ""
	}
	return jsStr(v)
}

// cut is JS s.slice(0, n): n UTF-16 code units (never splitting a character).
func cut(s string, n int) string {
	for i, r := range s {
		if n -= utf16.RuneLen(r); n < 0 {
			return s[:i]
		}
	}
	return s
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// jsRound is Math.round.
func jsRound(f float64) float64 { return math.Floor(f + 0.5) }

var tagRe = regexp.MustCompile(`^[a-z0-9_.-]{1,32}$`)

// scope validates the server_id / tag pair that rules and quiet hours share.
func scope(tenant string, b map[string]any) (serverID, tag *string, err error) {
	if truthy(b["server_id"]) {
		id := jsStr(b["server_id"])
		ok, err := db.Get[int](`SELECT 1 FROM servers WHERE id = ? AND tenant_id = ?`, id, tenant)
		if err != nil {
			return nil, nil, err
		}
		if ok == nil {
			return nil, nil, httperr.Bad("unknown server")
		}
		serverID = &id
	}
	if truthy(b["tag"]) {
		t := strings.ToLower(strings.TrimSpace(jsStr(b["tag"])))
		tag = &t
		if t != "" && !tagRe.MatchString(t) {
			return nil, nil, httperr.Bad("invalid tag")
		}
		if t != "" && serverID != nil {
			return nil, nil, httperr.Bad("Pick a server or a tag, not both")
		}
	}
	return serverID, tag, nil
}

func enabled(b map[string]any) int {
	if v, ok := b["enabled"].(bool); ok && !v {
		return 0
	}
	return 1
}

// Rule is a row of alert_rules. ValidateRule fills everything but ID and CreatedAt.
type Rule struct {
	ID        string  `db:"id" json:"id"`
	TenantID  string  `db:"tenant_id" json:"-"`
	ServerID  *string `db:"server_id" json:"server_id"`
	Metric    string  `db:"metric" json:"metric"`
	Threshold float64 `db:"threshold" json:"threshold"`
	Minutes   int64   `db:"minutes" json:"minutes"`
	Enabled   int     `db:"enabled" json:"enabled"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
	Tag       *string `db:"tag" json:"tag"`
	Mount     *string `db:"mount" json:"mount"`         // disk and inodes only; nil = the fullest mount
	CustomID  *string `db:"custom_id" json:"custom_id"` // metric "custom" only
}

// Label names what a rule watches, e.g. "Disk usage on /data" for a mount-specific rule.
func (r *Rule) Label(m *Metric) string {
	if r.Mount == nil {
		return m.Label
	}
	return strings.TrimSuffix(m.Label, " (fullest mount)") + " on " + *r.Mount
}

var mountRe = regexp.MustCompile(`^/[^\x00-\x1f]{0,255}$`)

// ValidateRule checks a rule of the organization tenant from a request body (decoded JSON).
func ValidateRule(tenant string, b map[string]any) (*Rule, error) {
	metric, _ := b["metric"].(string)
	if Metrics.Get(metric) == nil {
		return nil, httperr.Bad("unknown metric")
	}
	threshold, minutes := jsNum(b["threshold"]), jsRound(jsNum(b["minutes"]))
	if !finite(threshold) || threshold < 0 {
		return nil, httperr.Bad("invalid threshold")
	}
	if !finite(minutes) || minutes < 0 || minutes > 1440 {
		return nil, httperr.Bad("minutes must be 0-1440")
	}
	serverID, tag, err := scope(tenant, b)
	if err != nil {
		return nil, err
	}
	var mount *string
	if mt := strings.TrimSpace(orEmpty(b["mount"])); mt != "" && (metric == "disk" || metric == "inodes") {
		if !mountRe.MatchString(mt) {
			return nil, httperr.Bad("The mount must be an absolute path like /data")
		}
		mount = &mt
	}
	var customID *string
	if metric == "custom" {
		id := orEmpty(b["custom_id"])
		ok, err := db.Get[int](`SELECT 1 FROM custom_metrics WHERE id = ? AND tenant_id = ?`, id, tenant)
		if err != nil {
			return nil, err
		}
		if ok == nil {
			return nil, httperr.Bad("Pick a custom metric")
		}
		customID = &id
	}
	return &Rule{Metric: metric, Threshold: threshold, Minutes: int64(minutes), ServerID: serverID, Tag: tag, Mount: mount, CustomID: customID, Enabled: enabled(b)}, nil
}

// ValidateCustom checks a custom metric from a request body. It fills everything but ID and CreatedAt.
func ValidateCustom(tenant string, b map[string]any) (*probe.Custom, error) {
	c := &probe.Custom{Name: cut(strings.TrimSpace(orEmpty(b["name"])), 60), Unit: cut(strings.TrimSpace(orEmpty(b["unit"])), 16),
		Command: strings.TrimSpace(orEmpty(b["command"]))}
	if c.Name == "" {
		return nil, httperr.Bad("Name is required")
	}
	if c.Command == "" {
		return nil, httperr.Bad("Command is required")
	}
	if len(c.Command) > 2000 || strings.ContainsRune(c.Command, 0) {
		return nil, httperr.Bad("The command must be at most 2000 characters")
	}
	var err error
	if c.ServerID, c.Tag, err = scope(tenant, b); err != nil {
		return nil, err
	}
	return c, nil
}

// UnitSuffix puts a space before a unit, except for % and degrees: "12 GB", "95%", "40°C".
func UnitSuffix(u string) string {
	if u == "" || strings.HasPrefix(u, "%") || strings.HasPrefix(u, "°") {
		return u
	}
	return " " + u
}

func currentValue(r *Rule, m *Metric, s *db.Server, snap *probe.Snapshot) (*float64, error) {
	cores := 1.0
	if snap.Cores != nil && *snap.Cores != 0 {
		cores = *snap.Cores
	}
	if m.Col != "" && r.Minutes > 0 {
		v, err := db.Get[sql.NullFloat64](fmt.Sprintf(`SELECT AVG(%s) v FROM metrics WHERE server_id = ? AND t >= ?`, m.Col), s.ID, db.Now()-r.Minutes*60e3)
		if err != nil || v == nil || !v.Valid {
			return nil, err
		}
		if r.Metric == "load" {
			return ptr(v.Float64 / cores), nil
		}
		return ptr(v.Float64 * m.Scale), nil
	}
	pct := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		return ptr(*p * 100)
	}
	switch r.Metric {
	case "cpu":
		return pct(snap.CPU), nil
	case "mem":
		return pct(snap.Mem), nil
	case "swap":
		return pct(snap.Swap), nil
	case "temp":
		return snap.Temp, nil
	case "gpu":
		return pct(snap.GPU), nil
	case "custom":
		id := db.Str(r.CustomID)
		if r.Minutes > 0 {
			v, err := db.Get[sql.NullFloat64](`SELECT AVG(v) v FROM custom_values WHERE server_id = ? AND metric_id = ? AND t >= ?`, s.ID, id, db.Now()-r.Minutes*60e3)
			if err != nil || v == nil || !v.Valid {
				return nil, err
			}
			return ptr(v.Float64), nil
		}
		return snap.Custom[id].V, nil
	case "net":
		if snap.Rx == nil {
			return nil, nil
		}
		tx := 0.0 // rx + null is rx in JS
		if snap.Tx != nil {
			tx = *snap.Tx
		}
		return ptr((*snap.Rx + tx) / 1e6), nil
	case "load":
		l := math.NaN() // JS: undefined / cores
		if len(snap.Load) > 0 {
			l = snap.Load[0]
		}
		return ptr(l / cores), nil
	case "disk", "inodes":
		// With a mount: that mount only, nil (never firing) when it's gone. Without: the fullest mount.
		var hi *float64
		for _, d := range snap.Disks {
			p := &d.Pct
			if r.Metric == "inodes" {
				p = d.InodePct
			}
			if p != nil && (r.Mount == nil || *r.Mount == d.Mount) && (hi == nil || *p > *hi) {
				hi = p
			}
		}
		return pct(hi), nil
	case "reboot":
		if snap.Reboot {
			return ptr(1), nil
		}
		return ptr(0), nil
	case "smart":
		if snap.Smart == nil || snap.Smart.Status != "ok" {
			return nil, nil
		}
		n := 0
		for _, d := range snap.Smart.Disks {
			if d.Problem {
				n++
			}
		}
		return ptr(float64(n)), nil
	case "updates":
		return snap.Updates, nil
	case "security":
		return snap.Security, nil
	case "failed":
		return ptr(snap.Failed), nil
	case "unhealthy":
		if snap.Unhealthy == nil {
			return nil, nil
		}
		return ptr(float64(*snap.Unhealthy)), nil
	}
	return nil, nil
}

func ptr(f float64) *float64 { return &f }

// fmtNum is the JS fmt: integers as is, others with one decimal (toFixed rounds ties away from zero).
func fmtNum(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	if a := math.Abs(v) * 4; a == math.Trunc(a) && math.Mod(a, 2) == 1 { // exactly halfway at the second decimal (x.25, x.75)
		return strconv.FormatFloat(math.Copysign(math.Ceil(math.Abs(v)*10)/10, v), 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// ---- quiet hours ----

// Mute is a row of mute_windows. daily: start/end are minutes after local midnight; once: epoch ms.
type Mute struct {
	ID        string  `db:"id" json:"id"`
	TenantID  string  `db:"tenant_id" json:"-"`
	Name      string  `db:"name" json:"name"`
	ServerID  *string `db:"server_id" json:"server_id"`
	Tag       *string `db:"tag" json:"tag"`
	Kind      string  `db:"kind" json:"kind"`
	StartAt   int64   `db:"start_at" json:"start_at"`
	EndAt     int64   `db:"end_at" json:"end_at"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
}

var hhmm = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// ValidateMute checks a quiet-hours window from a request body. It fills everything but ID and CreatedAt.
func ValidateMute(tenant string, b map[string]any) (*Mute, error) {
	w := &Mute{Kind: "daily"}
	if b["kind"] == "once" {
		w.Kind = "once"
	}
	if w.Kind == "daily" {
		s, e := hhmm.FindStringSubmatch(orEmpty(b["start"])), hhmm.FindStringSubmatch(orEmpty(b["end"]))
		if s == nil || e == nil {
			return nil, httperr.Bad("Times must be HH:MM")
		}
		atoi := func(x string) int64 { n, _ := strconv.ParseInt(x, 10, 64); return n }
		w.StartAt, w.EndAt = atoi(s[1])*60+atoi(s[2]), atoi(e[1])*60+atoi(e[2])
		if w.StartAt == w.EndAt {
			return nil, httperr.Bad("Start and end must differ")
		}
	} else {
		num := func(k string) float64 { // Number(undefined) is NaN, Number(null) is 0
			if v, ok := b[k]; ok {
				return jsNum(v)
			}
			return math.NaN()
		}
		start, end := num("start"), num("end")
		if !(start > 0 && end > start) {
			return nil, httperr.Bad("The end must be after the start")
		}
		if end < float64(db.Now()) {
			return nil, httperr.Bad("That window is already over")
		}
		if end-start > 31*864e5 {
			return nil, httperr.Bad("One-time windows can be at most 31 days")
		}
		w.StartAt, w.EndAt = int64(start), int64(end)
	}
	var err error
	if w.ServerID, w.Tag, err = scope(tenant, b); err != nil {
		return nil, err
	}
	w.Name = cut(strings.TrimSpace(orEmpty(b["name"])), 100)
	return w, nil
}

// InWindow reports whether now falls in the window; daily windows use the Fleetdeck host's local time.
func InWindow(w *Mute, now time.Time) bool {
	if w.Kind == "once" {
		ms := now.UnixMilli()
		return ms >= w.StartAt && ms < w.EndAt
	}
	l := now.Local()
	m := int64(l.Hour()*60 + l.Minute())
	if w.StartAt < w.EndAt {
		return m >= w.StartAt && m < w.EndAt
	}
	return m >= w.StartAt || m < w.EndAt // e.g. 22:00-06:00
}

// QuietFor returns the quiet-hours window that currently covers the server, or nil.
func QuietFor(s *db.Server, now time.Time) (*Mute, error) {
	ws, err := db.Select[Mute](`SELECT * FROM mute_windows WHERE tenant_id = ? AND (server_id IS NULL OR server_id = ?)`, s.TenantID, s.ID)
	if err != nil {
		return nil, err
	}
	tags := strings.Split(s.Tags, ",")
	for i := range ws {
		if (ws[i].Tag == nil || *ws[i].Tag == "" || slices.Contains(tags, *ws[i].Tag)) && InWindow(&ws[i], now) {
			return &ws[i], nil
		}
	}
	return nil, nil
}

// Evaluate checks the rules for a server after a probe (snap on success, err on failure) and notifies on
// fire and resolve. Its signature matches probe.OnProbe.
func Evaluate(s *db.Server, snap *probe.Snapshot, err error) {
	if e := evaluate(s, snap, err); e != nil {
		log.Printf("alerts for %s: %v", s.Name, e)
	}
}

func evaluate(s *db.Server, snap *probe.Snapshot, perr error) error {
	rules, err := db.Select[Rule](`SELECT * FROM alert_rules WHERE tenant_id = ? AND enabled = 1 AND (server_id IS NULL OR server_id = ?)`, s.TenantID, s.ID)
	if err != nil || len(rules) == 0 {
		return err
	}
	quiet, err := QuietFor(s, time.Now())
	if err != nil {
		return err
	}
	tags := strings.Split(s.Tags, ",")
	var customs map[string]*probe.Custom
	for i := range rules {
		r := &rules[i]
		// Tag no longer on this server: drop its alert silently instead of leaving it firing forever.
		if r.Tag != nil && *r.Tag != "" && !slices.Contains(tags, *r.Tag) {
			if _, err := db.Exec(`DELETE FROM alert_state WHERE rule_id = ? AND server_id = ?`, r.ID, s.ID); err != nil {
				return err
			}
			continue
		}
		m := Metrics.Get(r.Metric)
		if m == nil {
			continue
		}
		if r.Metric == "custom" { // name and unit come from the custom metric
			if customs == nil {
				list, err := db.Select[probe.Custom](`SELECT * FROM custom_metrics WHERE tenant_id = ?`, s.TenantID)
				if err != nil {
					return err
				}
				customs = map[string]*probe.Custom{}
				for j := range list {
					customs[list[j].ID] = &list[j]
				}
			}
			c := customs[db.Str(r.CustomID)]
			if c == nil {
				continue
			}
			cm := *m
			cm.Label, cm.Unit = c.Name, UnitSuffix(c.Unit)
			m = &cm
		}
		var firing bool
		var msg string
		if r.Metric == "check" || r.Metric == "tls_days" { // run from Fleetdeck: known even while the server is unreachable
			if firing, msg, err = checkState(r, m, s); err != nil {
				return err
			}
		} else if r.Metric == "down" {
			msg = s.Name + " is unreachable"
			if perr != nil {
				since := db.Now()
				var pe *probe.ProbeError
				if errors.As(perr, &pe) {
					since = pe.Since
				}
				firing = db.Now()-since >= r.Minutes*60e3
				msg += ": " + perr.Error()
			}
		} else {
			if snap == nil {
				continue // unknown while unreachable; keep the current state
			}
			v, err := currentValue(r, m, s, snap)
			if err != nil {
				return err
			}
			firing = v != nil && (*v > r.Threshold && !m.Below || *v < r.Threshold && m.Below)
			if r.Metric == "reboot" {
				msg = s.Name + " needs a reboot"
			} else {
				vs := "?"
				if v != nil {
					vs = fmtNum(*v)
				}
				avg := ""
				if r.Minutes != 0 && (m.Col != "" || r.Metric == "custom") {
					avg = fmt.Sprintf(", %d min avg", r.Minutes)
				}
				msg = fmt.Sprintf("%s: %s is %s%s (threshold %s%s%s)", s.Name, r.Label(m), vs, m.Unit, fmtNum(r.Threshold), m.Unit, avg)
			}
		}
		cur, err := db.Get[int](`SELECT notified FROM alert_state WHERE rule_id = ? AND server_id = ?`, r.ID, s.ID)
		if err != nil {
			return err
		}
		switch {
		case firing && cur == nil:
			notified, note := 1, ""
			if quiet != nil {
				notified, note = 0, " (quiet hours, not sent)"
				if quiet.Name != "" {
					note = " (quiet hours: " + quiet.Name + ", not sent)"
				}
			}
			if _, err := db.Exec(`INSERT INTO alert_state (rule_id, server_id, since, message, notified) VALUES (?, ?, ?, ?, ?)`,
				r.ID, s.ID, db.Now(), msg, notified); err != nil {
				return err
			}
			db.Event("alert", "Firing: "+msg+note, db.EventOpts{Level: "error", ServerID: s.ID})
			if quiet == nil {
				Notify(s.TenantID, "🔴 "+s.Name+": "+r.Label(m), msg, "error")
			}
		case !firing && cur != nil:
			if _, err := db.Exec(`DELETE FROM alert_state WHERE rule_id = ? AND server_id = ?`, r.ID, s.ID); err != nil {
				return err
			}
			db.Event("alert", "Resolved: "+s.Name+" "+r.Label(m), db.EventOpts{Level: "success", ServerID: s.ID})
			// Only tell people it's fixed if they were told it broke. Muted-and-gone stays silent.
			if *cur != 0 {
				Notify(s.TenantID, "✅ "+s.Name+": "+r.Label(m)+" resolved", s.Name+": "+r.Label(m)+" is back to normal", "success")
			}
		case firing:
			// Still firing when quiet hours end: send what was held back.
			send := *cur == 0 && quiet == nil
			notified := *cur
			if send {
				notified = 1
			}
			if _, err := db.Exec(`UPDATE alert_state SET message = ?, notified = ? WHERE rule_id = ? AND server_id = ?`, msg, notified, r.ID, s.ID); err != nil {
				return err
			}
			if send {
				Notify(s.TenantID, "🔴 "+s.Name+": "+r.Label(m), msg+" (still firing after quiet hours)", "error")
			}
		}
	}
	return nil
}

// checkState evaluates the check and tls_days rules from the latest results of the server's endpoint checks.
func checkState(r *Rule, m *Metric, s *db.Server) (bool, string, error) {
	type row struct {
		Name         string   `db:"name"`
		LastError    string   `db:"last_error"`
		FailingSince *int64   `db:"failing_since"`
		TLSDays      *float64 `db:"tls_days"`
	}
	list, err := db.Select[row](`SELECT name, last_error, failing_since, tls_days FROM checks WHERE server_id = ? AND enabled = 1 ORDER BY name`, s.ID)
	if err != nil {
		return false, "", err
	}
	if r.Metric == "check" {
		var bad []string
		for _, c := range list {
			if c.FailingSince != nil && db.Now()-*c.FailingSince >= r.Minutes*60e3 {
				bad = append(bad, fmt.Sprintf("%s (%s, for %d min)", c.Name, c.LastError, (db.Now()-*c.FailingSince)/60e3))
			}
		}
		return len(bad) > 0, s.Name + ": endpoint check failing: " + strings.Join(bad, "; "), nil
	}
	var low *row
	for i, c := range list {
		if c.TLSDays != nil && (low == nil || *c.TLSDays < *low.TLSDays) {
			low = &list[i]
		}
	}
	if low == nil {
		return false, "", nil
	}
	return *low.TLSDays < r.Threshold, fmt.Sprintf("%s: the TLS certificate of %s expires in %s%s (alert below %s%s)",
		s.Name, low.Name, fmtNum(math.Floor(*low.TLSDays)), m.Unit, fmtNum(r.Threshold), m.Unit), nil
}

// FiringAlert is a currently firing alert, for the alerts page.
type FiringAlert struct {
	RuleID     string `db:"rule_id" json:"rule_id"`
	ServerID   string `db:"server_id" json:"server_id"`
	Since      int64  `db:"since" json:"since"`
	Message    string `db:"message" json:"message"`
	Metric     string `db:"metric" json:"metric"`
	ServerName string `db:"server_name" json:"server_name"`
}

// Firing lists an organization's firing alerts.
func Firing(tenant string) ([]FiringAlert, error) {
	return db.Select[FiringAlert](`SELECT a.rule_id, a.server_id, a.since, a.message, r.metric, s.name AS server_name
  FROM alert_state a JOIN alert_rules r ON r.id = a.rule_id JOIN servers s ON s.id = a.server_id WHERE s.tenant_id = ? ORDER BY a.since DESC`, tenant)
}

// Seed adds the default rules (unreachable 2 min, disk > 90%, memory > 90% for 10 min) to a new organization.
func Seed(tenant string) error {
	now := db.Now()
	for _, r := range []struct {
		metric             string
		threshold, minutes int
	}{{"down", 0, 2}, {"disk", 90, 0}, {"mem", 90, 10}} {
		if _, err := db.Exec(`INSERT INTO alert_rules (id, tenant_id, server_id, metric, threshold, minutes, enabled, created_at) VALUES (?, ?, NULL, ?, ?, ?, 1, ?)`,
			db.NewID(), tenant, r.metric, r.threshold, r.minutes, now); err != nil {
			return err
		}
	}
	return nil
}

// OnJobDone notifies about failed jobs, per the notify_jobs setting (off | scheduled | all).
func OnJobDone(tenant string, id int64, name, status, actor string, runStatuses []string) {
	mode := db.TenantSetting(tenant, "notify_jobs", "scheduled")
	if status != "failed" || mode == "off" {
		return
	}
	if mode == "scheduled" && !strings.HasPrefix(actor, "schedule") && !strings.HasPrefix(actor, "webhook") && !strings.HasPrefix(actor, "chain") { // unattended runs
		return
	}
	bad := 0
	for _, s := range runStatuses {
		if s != "success" {
			bad++
		}
	}
	Notify(tenant, "❌ Job failed: "+name, fmt.Sprintf(`Job #%d "%s" failed (%d/%d hosts).`, id, name, bad, len(runStatuses)), "error")
}
