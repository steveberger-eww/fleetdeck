// Package admin is Cockpit-lite: services, journal, packages, processes and Docker on one server.
// Every name that reaches a remote command is allowlisted AND shell-quoted.
package admin

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/ssh"
)

var (
	q = ssh.Shq
	// UnitRe matches systemd unit names that may be passed to systemctl/journalctl.
	UnitRe    = regexp.MustCompile(`^[A-Za-z0-9@._:\\-]{1,200}$`)
	container = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type shOpts struct {
	allowFail bool
	timeout   time.Duration
	maxOutput int
}

func sh(ctx context.Context, s *db.Server, script string, o shOpts) (string, error) {
	if o.timeout == 0 {
		o.timeout = 60 * time.Second
	}
	r, err := ssh.Exec(ctx, s, script, ssh.ExecOpts{Mode: ssh.ModeRoot, Timeout: o.timeout, MaxOutput: o.maxOutput})
	if err != nil {
		return "", err
	}
	if r.Code != 0 && !o.allowFail {
		lines := strings.Split(strings.TrimSpace(r.Out), "\n")
		msg := strings.Join(lines[max(0, len(lines)-3):], " ")
		if msg == "" {
			msg = "exit null"
			if r.Code != ssh.NoExit {
				msg = fmt.Sprintf("exit %d", r.Code)
			}
		}
		return "", httperr.New(502, msg)
	}
	return r.Out, nil
}

// localeCompare stand-in: case-insensitive first, so "NetworkManager" sorts among the n's as in the UI before.
func compareNames(a, b string) int {
	return cmp.Or(cmp.Compare(strings.ToLower(a), strings.ToLower(b)), cmp.Compare(a, b))
}

// ---- services ----

type Service struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
	Enabled     string `json:"enabled"`
}

var unitLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*)$`)

func Services(ctx context.Context, s *db.Server) ([]Service, error) {
	out, err := sh(ctx, s, `systemctl list-units --type=service --all --no-legend --plain --no-pager
echo '---'
systemctl list-unit-files --type=service --no-legend --no-pager`, shOpts{})
	if err != nil {
		return nil, err
	}
	parts := strings.Split(out, "\n---\n")
	files := ""
	if len(parts) > 1 {
		files = parts[1]
	}
	enabled := map[string]string{}
	var fileOrder []string
	for _, l := range strings.Split(files, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			if _, ok := enabled[f[0]]; !ok {
				fileOrder = append(fileOrder, f[0])
			}
			enabled[f[0]] = at(f, 1)
		}
	}
	list := map[string]Service{}
	for _, l := range strings.Split(parts[0], "\n") {
		if m := unitLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil && strings.HasSuffix(m[1], ".service") {
			list[m[1]] = Service{m[1], m[2], m[3], m[4], m[5], enabled[m[1]]}
		}
	}
	// Installed but never loaded units only show up in list-unit-files.
	for _, unit := range fileOrder {
		if _, ok := list[unit]; !ok && !strings.Contains(unit, "@.") {
			list[unit] = Service{unit, "loaded", "inactive", "dead", "", enabled[unit]}
		}
	}
	res := make([]Service, 0, len(list))
	for _, v := range list {
		res = append(res, v)
	}
	slices.SortFunc(res, func(a, b Service) int { return compareNames(a.Unit, b.Unit) })
	return res, nil
}

var serviceActions = []string{"start", "stop", "restart", "reload", "enable", "disable"}

func ServiceAction(ctx context.Context, s *db.Server, unit, action string) (string, error) {
	if !UnitRe.MatchString(unit) {
		return "", httperr.Bad("invalid unit name")
	}
	if !slices.Contains(serviceActions, action) {
		return "", httperr.Bad("invalid action")
	}
	return sh(ctx, s, fmt.Sprintf("systemctl %s -- %s 2>&1", action, q(unit)), shOpts{})
}

func ServiceStatus(ctx context.Context, s *db.Server, unit string) (string, error) {
	if !UnitRe.MatchString(unit) {
		return "", httperr.Bad("invalid unit name")
	}
	return sh(ctx, s, "SYSTEMD_COLORS=0 systemctl status --no-pager --lines=40 --full -- "+q(unit), shOpts{allowFail: true})
}

// ---- timers ----
// `systemctl show` instead of list-timers: its dates contain spaces. --timestamp=unix needs systemd 247+, so fall back.
const timersScript = `units=$(systemctl list-units --type=timer --all --no-legend --plain --no-pager | awk '{print $1}')
[ -n "$units" ] || exit 0
P=Id,Description,Unit,ActiveState,UnitFileState,NextElapseUSecRealtime,LastTriggerUSec
systemctl show --timestamp=unix -p $P $units 2>/dev/null || systemctl show -p $P $units
echo '#svc'
svcs=$(systemctl show -p Unit --value $units | sort -u)
[ -n "$svcs" ] && systemctl show -p Id,Result,ActiveState $svcs
true`

var blankLine = regexp.MustCompile(`\n\s*\n`)

func showRecords(text string) []map[string]string {
	var res []map[string]string
	for _, block := range blankLine.Split(text, -1) {
		r := map[string]string{}
		for _, l := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				r[k] = strings.TrimSpace(v)
			}
		}
		if r["Id"] != "" {
			res = append(res, r)
		}
	}
	return res
}

var dayName = regexp.MustCompile(`^[A-Za-z]{3} `)

// "@1732451234" (unix) or "Tue 2026-11-24 04:00:00 UTC"; empty or n/a when never.
func stamp(v string) *int64 {
	if v == "" || v == "n/a" || v == "0" {
		return nil
	}
	ms := int64(0)
	if rest, ok := strings.CutPrefix(v, "@"); ok {
		if f, err := strconv.ParseFloat(rest, 64); err == nil {
			ms = int64(f * 1000)
		}
	} else {
		v = dayName.ReplaceAllString(v, "")
		if t, err := time.Parse("2006-01-02 15:04:05 MST", v); err == nil {
			// Go invents a zero offset for zone names it doesn't know; only trust real UTC names then.
			if name, off := t.Zone(); off != 0 || slices.Contains([]string{"UTC", "GMT", "UT"}, name) {
				ms = t.UnixMilli()
			}
		} else if t, err := time.ParseInLocation("2006-01-02 15:04:05", v, time.Local); err == nil {
			ms = t.UnixMilli()
		}
	}
	if ms == 0 {
		return nil
	}
	return &ms
}

type Timer struct {
	Unit        string `json:"unit"`
	Description string `json:"description"`
	Activates   string `json:"activates"`
	Active      string `json:"active"`
	Enabled     string `json:"enabled"`
	Next        *int64 `json:"next"`
	Last        *int64 `json:"last"`
	NextText    string `json:"nextText"`
	LastText    string `json:"lastText"`
	Result      string `json:"result"`
	Running     bool   `json:"running"`
}

func ParseTimers(out string) []Timer {
	t, svc, _ := strings.Cut(out, "#svc")
	if i := strings.Index(svc, "#svc"); i >= 0 {
		svc = svc[:i]
	}
	services := map[string]map[string]string{}
	for _, r := range showRecords(svc) {
		services[r["Id"]] = r
	}
	res := []Timer{}
	for _, r := range showRecords(t) {
		sv := services[r["Unit"]]
		res = append(res, Timer{
			Unit: r["Id"], Description: r["Description"], Activates: r["Unit"], Active: r["ActiveState"], Enabled: r["UnitFileState"],
			Next: stamp(r["NextElapseUSecRealtime"]), Last: stamp(r["LastTriggerUSec"]),
			NextText: r["NextElapseUSecRealtime"], LastText: r["LastTriggerUSec"],
			Result: sv["Result"], Running: sv["ActiveState"] == "activating" || sv["ActiveState"] == "active",
		})
	}
	slices.SortStableFunc(res, func(a, b Timer) int { return compareNames(a.Unit, b.Unit) })
	return res
}

func Timers(ctx context.Context, s *db.Server) ([]Timer, error) {
	out, err := sh(ctx, s, timersScript, shOpts{})
	if err != nil {
		return nil, err
	}
	return ParseTimers(out), nil
}

// ---- services still running replaced code ----
// A process that maps a deleted binary or library from a system path still runs the pre-update version.
// Same idea as needrestart / `dnf needs-restarting -s`, but needs neither. Map each process to its unit via its cgroup.
const staleScript = `for d in /proc/[0-9]*; do
  grep -qE ' /(usr|lib|lib64|bin|sbin|opt)/[^ ]* \(deleted\)$' "$d/maps" 2>/dev/null || continue
  sed -n 's#.*/system\.slice/\([^/]*\.service\).*#\1#p' "$d/cgroup" 2>/dev/null | head -n 1
done | sort -u`

// Restarting these can end sessions or networking; offer them one by one only.
var careful = regexp.MustCompile(`^(dbus|dbus-broker|systemd-logind|systemd-networkd|NetworkManager|networking|wpa_supplicant|gdm|sddm|lightdm|display-manager|(serial-)?getty@.*|user@.*)\.service$`)

type Stale struct {
	Unit    string `json:"unit"`
	Careful bool   `json:"careful"`
}

func ParseStale(out string) []Stale {
	res := []Stale{}
	for _, l := range strings.Split(out, "\n") {
		if u := strings.TrimSpace(l); UnitRe.MatchString(u) {
			res = append(res, Stale{u, careful.MatchString(u)})
		}
	}
	return res
}

func StaleServices(ctx context.Context, s *db.Server) ([]Stale, error) {
	out, err := sh(ctx, s, staleScript, shOpts{})
	if err != nil {
		return nil, err
	}
	return ParseStale(out), nil
}

// ---- accounts and authorized_keys ----
// Users who can log in (uid 0 or >= 1000 with a real shell), plus anyone with an authorized_keys file (e.g. a git user).
const accountsScript = `getent passwd | while IFS=: read -r name _ uid _ _ home shell; do
  keys=$(cat "$home/.ssh/authorized_keys" "$home/.ssh/authorized_keys2" 2>/dev/null | grep -vE '^[[:space:]]*(#|$)')
  login=1; case "$shell" in */nologin|*/false|'') login=0;; esac
  [ -n "$keys" ] || { [ "$login" = 1 ] && { [ "$uid" = 0 ] || [ "$uid" -ge 1000 ]; }; } || continue
  echo "user=$name:$uid:$login:$shell"
  [ -n "$keys" ] && printf '%s\n' "$keys" | sed "s|^|key=$name |"
done; true`

// Options (command="…", from="…") may precede the key, so find the type token instead of splitting on spaces.
var keyRe = regexp.MustCompile(`(?:^|\s)((?:ssh|ecdsa|sk)-[\w@.-]+)\s+([A-Za-z0-9+/]{40,}={0,3})(?:\s+(.*))?$`)

type Key struct {
	Type    string `json:"type"`
	Blob    string `json:"blob"`
	FP      string `json:"fp"`
	Comment string `json:"comment"`
	Options string `json:"options"`
}

// BlobFingerprint is the fingerprint of a base64 key blob, decoded leniently like Node's Buffer.from(b, 'base64').
func BlobFingerprint(blob string) string {
	b, _ := base64.RawStdEncoding.DecodeString(strings.TrimRight(blob, "="))
	return ssh.Fingerprint(b)
}

// ParseKeyLine parses one authorized_keys line; nil when it holds no key.
func ParseKeyLine(line string) *Key {
	m := keyRe.FindStringSubmatchIndex(line)
	if m == nil {
		return nil
	}
	g := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return line[m[2*i]:m[2*i+1]]
	}
	return &Key{Type: g(1), Blob: g(2), FP: BlobFingerprint(g(2)), Comment: strings.TrimSpace(g(3)), Options: strings.TrimSpace(line[:m[0]])}
}

type Account struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	Login bool   `json:"login"`
	Shell string `json:"shell"`
	Keys  []Key  `json:"keys"`
}

func ParseAccounts(out string) []Account {
	users := []Account{}
	idx := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(l, "user="); ok {
			f := strings.Split(rest, ":")
			uid, _ := strconv.Atoi(at(f, 1))
			u := Account{Name: f[0], UID: uid, Login: at(f, 2) == "1", Shell: at(f, 3), Keys: []Key{}}
			if i, ok := idx[u.Name]; ok {
				users[i] = u
			} else {
				idx[u.Name] = len(users)
				users = append(users, u)
			}
		} else if rest, ok := strings.CutPrefix(l, "key="); ok {
			name, line, ok := strings.Cut(rest, " ")
			if !ok {
				continue
			}
			if i, ok := idx[name]; ok {
				if k := ParseKeyLine(line); k != nil {
					users[i].Keys = append(users[i].Keys, *k)
				}
			}
		}
	}
	return users
}

func Accounts(ctx context.Context, s *db.Server) ([]Account, error) {
	out, err := sh(ctx, s, accountsScript, shOpts{})
	if err != nil {
		return nil, err
	}
	return ParseAccounts(out), nil
}

// RE2 caps repetitions at 1000, so the blob's {40,16000} length is checked separately.
var (
	blobRe = regexp.MustCompile(`^([A-Za-z0-9+/]+)={0,3}$`)
	userRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}$`)
)

// RevokeKey deletes every line holding this key from the user's authorized_keys files. sed -i keeps owner and mode.
func RevokeKey(ctx context.Context, s *db.Server, user, blob string) (string, error) {
	if !userRe.MatchString(user) {
		return "", httperr.Bad("invalid user")
	}
	if m := blobRe.FindStringSubmatch(blob); m == nil || len(m[1]) < 40 || len(m[1]) > 16000 {
		return "", httperr.Bad("invalid key")
	}
	return sh(ctx, s, `U=`+q(user)+`; B=`+q(blob)+`
home=$(getent passwd "$U" | cut -d: -f6); [ -n "$home" ] || { echo "no such user: $U"; exit 1; }
n=0
for f in "$home/.ssh/authorized_keys" "$home/.ssh/authorized_keys2"; do
  [ -f "$f" ] && grep -qF -- "$B" "$f" && sed -i "\|$B|d" "$f" && n=$((n+1)) && echo "removed from $f"
done
[ $n -gt 0 ] || { echo "key not found for $U"; exit 1; }`, shOpts{})
}

// ---- network: what is actually reachable ----
// Listening sockets, plus Docker's published ports, judged against the real firewall. Docker DNATs published ports
// in the nat table, so they never pass ufw's INPUT rules: "ufw deny 8080" does nothing for them unless the
// ufw-docker rules route Docker traffic through ufw (then `ufw route` rules on the CONTAINER port decide).
const networkScript = `
echo '#ss'; ss -Htulpn 2>/dev/null || ss -tulpn 2>/dev/null | tail -n +2
echo '#addr'; ip -br addr 2>/dev/null
echo '#route'; ip route show default 2>/dev/null
echo '#dns'; resolvectl dns 2>/dev/null || grep '^nameserver' /etc/resolv.conf 2>/dev/null
echo '#ufw'; command -v ufw >/dev/null 2>&1 && ufw status verbose 2>/dev/null
echo '#ufwrules'; grep '^### tuple' /etc/ufw/user.rules 2>/dev/null
echo '#ufwdocker'; grep -q '^# BEGIN UFW AND DOCKER' /etc/ufw/after.rules 2>/dev/null && echo written
iptables -S DOCKER-USER 2>/dev/null | grep -q ufw-user-forward && echo active
echo '#firewalld'; systemctl is-active -q firewalld 2>/dev/null && firewall-cmd --list-all 2>/dev/null
echo '#docker'; command -v docker >/dev/null 2>&1 && docker ps --format '{{json .}}' 2>/dev/null
true`

var sectionRe = regexp.MustCompile(`^#[a-z]+$`)

func sections(out string) map[string][]string {
	s := map[string][]string{}
	cur := ""
	for _, l := range strings.Split(out, "\n") {
		if sectionRe.MatchString(l) {
			cur = l[1:]
			s[cur] = []string{}
		} else if cur != "" && strings.TrimSpace(l) != "" {
			s[cur] = append(s[cur], l)
		}
	}
	return s
}

// at returns f[i], or "" past the end (JS's undefined).
func at(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

type socket struct {
	proto, iface, addr, process string
	port                        int
}

var (
	ifaceRe     = regexp.MustCompile(`%([^:\]]+)`)
	scopeRe     = regexp.MustCompile(`%[^\]]*`)
	bracketsRe  = regexp.MustCompile(`^\[|\]$`)
	ssProcessRe = regexp.MustCompile(`users:\(\("([^"]+)"`)
)

func parseSs(lines []string) []socket {
	var res []socket
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 5 || !(strings.HasPrefix(f[0], "tcp") || strings.HasPrefix(f[0], "udp")) {
			continue
		}
		local := f[4]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, _ := strconv.Atoi(local[i+1:])
		if port == 0 {
			continue
		}
		proto := "udp"
		if strings.HasPrefix(f[0], "tcp") {
			proto = "tcp"
		}
		addr := bracketsRe.ReplaceAllString(replaceFirst(scopeRe, local[:i], ""), "")
		res = append(res, socket{proto, first(ifaceRe, local), addr, first(ssProcessRe, l), port})
	}
	return res
}

func replaceFirst(re *regexp.Regexp, s, repl string) string {
	if m := re.FindStringIndex(s); m != nil {
		return s[:m[0]] + repl + s[m[1]:]
	}
	return s
}

type published struct {
	container, hostIP, proto string
	port, containerPort      int
}

// "0.0.0.0:8080->80/tcp, :::8080->80/tcp, 127.0.0.1:5432->5432/tcp". ponytail: a port range only counts its first port.
var publishedRe = regexp.MustCompile(`(?i)(?:(\[?[0-9a-f.:]*\]?):)?(\d+)(?:-\d+)?->(\d+)(?:-\d+)?/(tcp|udp)`)

func parseDocker(lines []string) []published {
	var res []published
	for _, l := range lines {
		var c struct{ Names, Ports string }
		if json.Unmarshal([]byte(l), &c) != nil {
			continue
		}
		for _, m := range publishedRe.FindAllStringSubmatch(c.Ports, -1) {
			host := m[1]
			if host == "" {
				host = "0.0.0.0"
			}
			port, _ := strconv.Atoi(m[2])
			cport, _ := strconv.Atoi(m[3])
			res = append(res, published{c.Names, bracketsRe.ReplaceAllString(host, ""), strings.ToLower(m[4]), port, cport})
		}
	}
	return res
}

type tuple struct {
	route                          bool
	action, proto, dport, src, dir string
}

// /etc/ufw/user.rules holds every rule with ports already resolved (app profiles too), in evaluation order:
// "### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in" or "### tuple ### route:allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in"
func parseTuples(lines []string) []tuple {
	var res []tuple
	for _, l := range lines {
		f := strings.Fields(strings.Replace(l, "### tuple ###", "", 1))
		if len(f) == 0 {
			f = []string{""}
		}
		res = append(res, tuple{strings.HasPrefix(f[0], "route:"), strings.Replace(f[0], "route:", "", 1), at(f, 1), at(f, 2), at(f, 5), f[len(f)-1]})
	}
	return res
}

func portIn(spec string, port int) bool {
	if spec == "any" {
		return true
	}
	for _, p := range strings.Split(spec, ",") {
		a, b, _ := strings.Cut(p, ":")
		lo, _ := strconv.Atoi(a)
		hi, _ := strconv.Atoi(b)
		if hi != 0 && port >= lo && port <= hi || hi == 0 && port == lo {
			return true
		}
	}
	return false
}

// First match wins, like ufw. Rules limited to some sources make a port "restricted" unless a later rule opens it to all.
func ufwVerdict(rules []tuple, port int, proto string, route bool, fallback string) string {
	some := false
	for _, r := range rules {
		if r.route != route || (!route && !strings.HasPrefix(r.dir, "in")) || (r.proto != "any" && r.proto != proto) || !portIn(r.dport, port) {
			continue
		}
		// "allow in on tailscale0" (dir in_tailscale0) only opens that interface: counts as some sources, not everyone.
		anySrc := (r.src == "0.0.0.0/0" || r.src == "::/0") && r.dir == "in"
		if r.action == "allow" || r.action == "limit" {
			if anySrc {
				return "open"
			}
			some = true
		} else if anySrc {
			if some {
				return "restricted"
			}
			return "blocked"
		}
	}
	if fallback == "allow" {
		return "open"
	}
	if some {
		return "restricted"
	}
	return "blocked"
}

// "To  Action  From" columns; long values leave only one space, so split on the action keyword instead.
var ufwRowRe = regexp.MustCompile(`^(.*?)\s+((?:ALLOW|DENY|REJECT|LIMIT)(?: (?:IN|OUT|FWD))?)\s+(.*)$`)

func ufwRuleRow(l string) []string {
	if m := ufwRowRe.FindStringSubmatch(l); m != nil {
		return m[1:]
	}
	return []string{l, "", ""}
}

var loopbackRe = regexp.MustCompile(`^127\.`)

func isLocal(addr, iface string) bool {
	return iface == "lo" || loopbackRe.MatchString(addr) || addr == "::1"
}

type Published struct {
	Container     string `json:"container"`
	ContainerPort int    `json:"containerPort"`
}

type Port struct {
	Proto         string      `json:"proto"`
	Port          int         `json:"port"`
	Addrs         []string    `json:"addrs"`
	Processes     []string    `json:"processes"`
	Local         bool        `json:"local"`
	Published     []Published `json:"published"`
	Docker        bool        `json:"docker,omitempty"`
	ContainerPort int         `json:"containerPort,omitempty"`
	Exposure      string      `json:"exposure"` // local, open, restricted, blocked, bypass (Docker skips ufw), unknown (firewalld)
}

type Iface struct {
	Name  string   `json:"name"`
	State string   `json:"state,omitempty"`
	Addrs []string `json:"addrs"`
}

type NetworkInfo struct {
	Ports []*Port `json:"ports"`
	// {kind: "ufw", incoming, outgoing, routed, rules: [[to, action, from]]} | {kind: "firewalld", raw} | {kind: "ufw-inactive"|"none"}
	Firewall   map[string]any `json:"firewall"`
	UfwDocker  *string        `json:"ufwDocker"` // "active", "written" or null
	Docker     bool           `json:"docker"`
	Addrs      []Iface        `json:"addrs"`
	Gateway    *string        `json:"gateway"`
	GatewayDev *string        `json:"gatewayDev"`
	DNS        []string       `json:"dns"`
}

var (
	ufwActiveRe  = regexp.MustCompile(`(?m)^Status: active`)
	ufwDefaultRe = regexp.MustCompile(`(?m)^Default: (\w+) \(incoming\), (\w+) \(outgoing\)(?:, (\w+) \(routed\))?`)
	dashLineRe   = regexp.MustCompile(`(?m)^--.*$`)
	viaRe        = regexp.MustCompile(`via (\S+)(?: dev (\S+))?`)
	dnsPrefixRe  = regexp.MustCompile(`^[^:]*:`)
	ipRe         = regexp.MustCompile(`(?i)^[0-9a-f.:]+$`)
)

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ParseNetwork(out string) NetworkInfo {
	s := sections(out)
	ufwText := strings.Join(s["ufw"], "\n")
	ufwActive := ufwActiveRe.MatchString(ufwText)
	def := ufwDefaultRe.FindStringSubmatch(ufwText)
	var ufwDocker *string
	if slices.Contains(s["ufwdocker"], "active") {
		ufwDocker = strp("active")
	} else if slices.Contains(s["ufwdocker"], "written") {
		ufwDocker = strp("written")
	}
	tuples := parseTuples(s["ufwrules"])
	firewall := map[string]any{}
	incoming := ""
	switch {
	case ufwActive:
		firewall["kind"] = "ufw"
		if def != nil {
			incoming = def[1]
			firewall["incoming"], firewall["outgoing"] = def[1], def[2]
		}
		firewall["routed"] = strp(at(def, 3))
		rules := [][]string{}
		if parts := dashLineRe.Split(ufwText, -1); len(parts) > 1 {
			for _, l := range strings.Split(parts[1], "\n") {
				if l = strings.TrimSpace(l); l != "" {
					rules = append(rules, ufwRuleRow(l))
				}
			}
		}
		firewall["rules"] = rules
	case len(s["firewalld"]) > 0:
		firewall["kind"], firewall["raw"] = "firewalld", strings.Join(s["firewalld"], "\n")
	case len(s["ufw"]) > 0:
		firewall["kind"] = "ufw-inactive"
	default:
		firewall["kind"] = "none"
	}
	kind := firewall["kind"]

	ports := []*Port{}
	rows := map[string]*Port{}
	row := func(proto string, port int) *Port {
		k := fmt.Sprintf("%d/%s", port, proto)
		if rows[k] == nil {
			rows[k] = &Port{Proto: proto, Port: port, Addrs: []string{}, Processes: []string{}, Local: true, Published: []Published{}}
			ports = append(ports, rows[k])
		}
		return rows[k]
	}
	for _, l := range parseSs(s["ss"]) {
		r := row(l.proto, l.port)
		addr := cmp.Or(l.addr, "*")
		if !slices.Contains(r.Addrs, addr) {
			r.Addrs = append(r.Addrs, addr)
		}
		if l.process != "" && !slices.Contains(r.Processes, l.process) {
			r.Processes = append(r.Processes, l.process)
		}
		if !isLocal(l.addr, l.iface) {
			r.Local = false
		}
	}
	for _, d := range parseDocker(s["docker"]) {
		r := row(d.proto, d.port)
		p := Published{d.container, d.containerPort}
		if !slices.Contains(r.Published, p) { // v4 + v6 entries
			r.Published = append(r.Published, p)
		}
		if !slices.Contains(r.Addrs, d.hostIP) {
			r.Addrs = append(r.Addrs, d.hostIP)
		}
		if !isLocal(d.hostIP, "") {
			r.Local, r.Docker, r.ContainerPort = false, true, d.containerPort
		}
	}
	for _, r := range ports {
		switch {
		case r.Local:
			r.Exposure = "local"
		case kind == "firewalld":
			r.Exposure = "unknown"
		case kind != "ufw":
			r.Exposure = "open"
		case r.Docker && ufwDocker != nil && *ufwDocker == "active":
			r.Exposure = ufwVerdict(tuples, r.ContainerPort, r.Proto, true, "deny")
		default:
			r.Exposure = ufwVerdict(tuples, r.Port, r.Proto, false, incoming)
			if r.Docker && r.Exposure != "open" {
				r.Exposure = "bypass" // ufw says no, Docker's NAT rules say yes
			}
		}
	}
	slices.SortStableFunc(ports, func(a, b *Port) int { return cmp.Or(cmp.Compare(a.Port, b.Port), cmp.Compare(a.Proto, b.Proto)) })

	var route []string
	if len(s["route"]) > 0 {
		route = viaRe.FindStringSubmatch(s["route"][0])
	}
	addrs := []Iface{}
	for _, l := range s["addr"] {
		f := strings.Fields(l)
		name, _, _ := strings.Cut(f[0], "@")
		a := []string{}
		if len(f) > 2 {
			a = f[2:]
		}
		addrs = append(addrs, Iface{name, at(f, 1), a})
	}
	dns := []string{}
	for _, l := range s["dns"] {
		for _, x := range strings.Fields(dnsPrefixRe.ReplaceAllString(l, " ")) {
			if ipRe.MatchString(x) && strings.ContainsAny(x, ".:") && !slices.Contains(dns, x) {
				dns = append(dns, x)
			}
		}
	}
	return NetworkInfo{Ports: ports, Firewall: firewall, UfwDocker: ufwDocker, Docker: len(s["docker"]) > 0, Addrs: addrs,
		Gateway: strp(at(route, 1)), GatewayDev: strp(at(route, 2)), DNS: dns}
}

func Network(ctx context.Context, s *db.Server) (NetworkInfo, error) {
	out, err := sh(ctx, s, networkScript, shOpts{})
	if err != nil {
		return NetworkInfo{}, err
	}
	return ParseNetwork(out), nil
}

type UfwAllowOpts struct {
	Port  int
	Proto string
	Route bool
}

func UfwAllow(ctx context.Context, s *db.Server, o UfwAllowOpts) (string, error) {
	if o.Port < 1 || o.Port > 65535 {
		return "", httperr.Bad("invalid port")
	}
	if o.Proto != "tcp" && o.Proto != "udp" {
		return "", httperr.Bad("invalid protocol")
	}
	script := fmt.Sprintf("ufw allow %d/%s 2>&1", o.Port, o.Proto)
	if o.Route {
		script = fmt.Sprintf("ufw route allow proto %s from any to any port %d 2>&1", o.Proto, o.Port)
	}
	return sh(ctx, s, script, shOpts{})
}

// ---- journal ----

var since = map[string]string{"15m": "15 min ago", "1h": "1 hour ago", "6h": "6 hours ago", "24h": "1 day ago", "7d": "7 days ago"}

// LogsOpts are the raw query parameters; "" means not given.
type LogsOpts struct{ Unit, Priority, Since, Lines string }

type LogEntry struct {
	T    int64  `json:"t"`
	P    int    `json:"p"`
	ID   string `json:"id"`
	Unit string `json:"unit"`
	Msg  string `json:"msg"`
}

// clampLines is Math.min(Math.max(Number(v) || 300, 10), 5000).
func clampLines(v string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || n == 0 || math.IsNaN(n) {
		n = 300
	}
	return strconv.FormatFloat(min(max(n, 10), 5000), 'f', -1, 64)
}

var priorityRe = regexp.MustCompile(`^[0-7]$`)

// LogsCommand is the journalctl command for o; a bad filter is an HTTP 400 error.
func LogsCommand(o LogsOpts) (string, error) {
	args := []string{"--no-pager", "-o", "json", "-n", clampLines(o.Lines)}
	if o.Unit != "" {
		if !UnitRe.MatchString(o.Unit) {
			return "", httperr.Bad("invalid unit name")
		}
		args = append(args, "-u", o.Unit)
	}
	if o.Priority != "" {
		if !priorityRe.MatchString(o.Priority) {
			return "", httperr.Bad("invalid priority")
		}
		args = append(args, "-p", o.Priority)
	}
	if o.Since != "" {
		if since[o.Since] == "" {
			return "", httperr.Bad("invalid range")
		}
		args = append(args, "--since", since[o.Since])
	}
	for i, a := range args {
		args[i] = q(a)
	}
	return "journalctl " + strings.Join(args, " "), nil
}

func Logs(ctx context.Context, s *db.Server, o LogsOpts) ([]LogEntry, error) {
	cmd, err := LogsCommand(o)
	if err != nil {
		return nil, err
	}
	out, err := sh(ctx, s, cmd, shOpts{allowFail: true, maxOutput: 8_000_000})
	if err != nil {
		return nil, err
	}
	return parseJournal(out), nil
}

func parseJournal(out string) []LogEntry {
	str := func(v any) string { s, _ := v.(string); return s }
	num := func(v any, def float64) float64 {
		switch x := v.(type) {
		case nil:
			return def
		case float64:
			return x
		case string:
			f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
			return f
		}
		return 0
	}
	entries := []LogEntry{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue // partial line
		}
		msg := ""
		switch m := e["MESSAGE"].(type) {
		case string:
			msg = m
		case []any: // non-UTF-8 or binary messages come as byte arrays
			b := make([]byte, 0, len(m))
			for _, x := range m {
				f, _ := x.(float64)
				b = append(b, byte(f))
			}
			msg = string(b)
			if !utf8.ValidString(msg) {
				msg = strings.ToValidUTF8(msg, "�")
			}
		case nil:
		default:
			msg = fmt.Sprint(m)
		}
		entries = append(entries, LogEntry{
			T: int64(math.Floor(num(e["__REALTIME_TIMESTAMP"], 0) / 1000)), P: int(num(e["PRIORITY"], 6)),
			ID: cmp.Or(str(e["SYSLOG_IDENTIFIER"]), str(e["_COMM"])), Unit: str(e["_SYSTEMD_UNIT"]), Msg: msg,
		})
	}
	return entries
}

// ---- packages ----

type Package struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	From     string `json:"from"`
	Security bool   `json:"security"`
}

type PackageList struct {
	Manager  string    `json:"manager"` // "apt", "dnf" or ""
	Packages []Package `json:"packages"`
}

var (
	aptRe       = regexp.MustCompile(`^([^/\s]+)/(\S+)\s+(\S+)\s+(\S+)\s+\[upgradable from: ([^\]]+)\]`)
	securiRe    = regexp.MustCompile(`(?i)securi`)
	dnfRe       = regexp.MustCompile(`^(\S+)\.(\S+)\s+(\S+)\s+(\S+)$`)
	dnfVerRelRe = regexp.MustCompile(`-[^-]+-[^-]+$`)
)

func Packages(ctx context.Context, s *db.Server, refresh bool) (PackageList, error) {
	aptRefresh, dnfCache := ":", "--cacheonly"
	if refresh {
		aptRefresh, dnfCache = "apt-get update -qq >/dev/null 2>&1", "--refresh"
	}
	out, err := sh(ctx, s, `export LC_ALL=C
if command -v apt-get >/dev/null 2>&1; then
  `+aptRefresh+`
  echo "#apt"; apt list --upgradable 2>/dev/null
elif command -v dnf >/dev/null 2>&1; then
  echo "#dnf"; dnf -q `+dnfCache+` check-update 2>/dev/null
  echo "#security"; dnf -q --cacheonly updateinfo list --security 2>/dev/null
fi
true`, shOpts{timeout: 180 * time.Second})
	if err != nil {
		return PackageList{}, err
	}
	return parsePackages(out), nil
}

func parsePackages(out string) PackageList {
	lines := strings.Split(out, "\n")
	list := []Package{}
	switch lines[0] {
	case "#apt":
		for _, l := range lines[1:] {
			if m := aptRe.FindStringSubmatch(l); m != nil {
				list = append(list, Package{Name: m[1], Source: m[2], Version: m[3], Arch: m[4], From: m[5], Security: securiRe.MatchString(m[2])})
			}
		}
	case "#dnf":
		sec := map[string]bool{}
		i := slices.Index(lines, "#security")
		for _, l := range lines[i+1:] {
			if p := strings.Fields(l); len(p) > 2 && p[2] != "" {
				sec[dnfVerRelRe.ReplaceAllString(p[2], "")] = true
			}
		}
		end := len(lines)
		if i >= 0 {
			end = i
		}
		for _, l := range lines[1:max(end, 1)] {
			if m := dnfRe.FindStringSubmatch(l); m != nil {
				list = append(list, Package{Name: m[1], Arch: m[2], Version: m[3], Source: m[4], Security: sec[m[1]]})
			}
		}
	}
	manager := ""
	if lines[0] != "" {
		manager = lines[0][1:]
	}
	return PackageList{manager, list}
}

// ---- processes ----

type Process struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	RSS     int64   `json:"rss"`
	Elapsed int64   `json:"elapsed"`
	Comm    string  `json:"comm"`
	Args    string  `json:"args"`
}

func Processes(ctx context.Context, s *db.Server) ([]Process, error) {
	out, err := sh(ctx, s, "ps -eo pid=,user=,pcpu=,pmem=,rss=,etimes=,comm=,args= --sort=-pcpu | head -n 150", shOpts{})
	if err != nil {
		return nil, err
	}
	num := func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
	res := []Process{}
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		p := strings.Fields(l)
		if len(p) == 0 {
			p = []string{""}
		}
		args := ""
		if len(p) > 7 {
			args = strings.Join(p[7:], " ")
		}
		res = append(res, Process{int(num(p[0])), at(p, 1), num(at(p, 2)), num(at(p, 3)), int64(num(at(p, 4))) * 1024, int64(num(at(p, 5))), at(p, 6), args})
	}
	return res, nil
}

var signals = []string{"TERM", "KILL", "HUP", "INT"}

func Kill(ctx context.Context, s *db.Server, pid int, signal string) (string, error) {
	if pid < 2 {
		return "", httperr.Bad("invalid pid")
	}
	if !slices.Contains(signals, signal) {
		return "", httperr.Bad("invalid signal")
	}
	return sh(ctx, s, fmt.Sprintf("kill -s %s %d 2>&1", signal, pid), shOpts{})
}

// ---- docker ----

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
	CPU     string `json:"cpu"`
	Mem     string `json:"mem"`
	MemPct  string `json:"memPct"`
	Net     string `json:"net"`
	Block   string `json:"block"`
}

type ContainerList struct {
	Installed  bool        `json:"installed"`
	Containers []Container `json:"containers"`
}

type dockerLine struct {
	ID, Names, Image, State, Status, Ports, CreatedAt string
	CPUPerc, MemUsage, MemPerc, NetIO, BlockIO        string
}

func jsonLines(s string) []dockerLine {
	var res []dockerLine
	for _, l := range strings.Split(s, "\n") {
		var d dockerLine
		if strings.HasPrefix(l, "{") && json.Unmarshal([]byte(l), &d) == nil {
			res = append(res, d)
		}
	}
	return res
}

func Containers(ctx context.Context, s *db.Server) (ContainerList, error) {
	out, err := sh(ctx, s, `command -v docker >/dev/null 2>&1 || { echo "#none"; exit 0; }
docker ps -a --no-trunc --format '{{json .}}'
echo '#stats'
docker stats --no-stream --no-trunc --format '{{json .}}'`, shOpts{timeout: 60 * time.Second})
	if err != nil {
		return ContainerList{}, err
	}
	return parseContainers(out), nil
}

func parseContainers(out string) ContainerList {
	if strings.HasPrefix(out, "#none") {
		return ContainerList{false, []Container{}}
	}
	parts := strings.Split(out, "#stats")
	stats := jsonLines(at(parts, 1))
	res := []Container{}
	for _, c := range jsonLines(parts[0]) {
		var st dockerLine
		if i := slices.IndexFunc(stats, func(x dockerLine) bool { return x.ID == c.ID }); i >= 0 {
			st = stats[i]
		} else if i := slices.IndexFunc(stats, func(x dockerLine) bool { return strings.HasPrefix(c.ID, x.ID) }); i >= 0 {
			st = stats[i]
		}
		res = append(res, Container{c.ID, c.Names, c.Image, c.State, c.Status, c.Ports, c.CreatedAt, st.CPUPerc, st.MemUsage, st.MemPerc, st.NetIO, st.BlockIO})
	}
	return ContainerList{true, res}
}

var containerActions = []string{"start", "stop", "restart", "pause", "unpause", "rm"}

func ContainerAction(ctx context.Context, s *db.Server, id, action string) (string, error) {
	if !container.MatchString(id) {
		return "", httperr.Bad("invalid container id")
	}
	if !slices.Contains(containerActions, action) {
		return "", httperr.Bad("invalid action")
	}
	return sh(ctx, s, fmt.Sprintf("docker %s -- %s 2>&1", action, q(id)), shOpts{})
}

// ContainerLogs returns the last lines (raw query value; "" = 300, clamped to 10..5000).
func ContainerLogs(ctx context.Context, s *db.Server, id, lines string) (string, error) {
	if !container.MatchString(id) {
		return "", httperr.Bad("invalid container id")
	}
	return sh(ctx, s, fmt.Sprintf("docker logs --timestamps --tail %s -- %s 2>&1", clampLines(lines), q(id)), shOpts{allowFail: true, maxOutput: 4_000_000})
}
