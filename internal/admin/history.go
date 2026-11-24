package admin

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleetdeck/internal/db"
)

// Transaction is one package manager run. When is the server's local time, as the log has it.
type Transaction struct {
	When     string   `json:"when"`
	Source   string   `json:"source"` // "unattended-upgrades" or the command line
	Packages []Change `json:"packages"`
}

type Change struct {
	Action  string `json:"action"` // install | upgrade | remove | downgrade | reinstall
	Name    string `json:"name"`
	Version string `json:"version"`
}

const historyMax = 30

// UpdateHistory is the last 30 package transactions, newest first. Empty when the server has no history.
func UpdateHistory(ctx context.Context, s *db.Server) ([]Transaction, error) {
	out, err := sh(ctx, s, `export LC_ALL=C
if command -v apt-get >/dev/null 2>&1; then
  echo "#apt"
  cd /var/log/apt 2>/dev/null && { for f in $(ls history.log.* 2>/dev/null | sort -t. -k3,3rn); do zcat -f "$f"; done; cat history.log 2>/dev/null; } | tail -n 3000
elif command -v dnf >/dev/null 2>&1; then
  echo "#dnf"
  for id in $(dnf -q history list 2>/dev/null | awk '$1 ~ /^[0-9]+$/ {print $1}' | head -n 30); do dnf -q history info "$id" 2>/dev/null; done
fi
true`, shOpts{timeout: 120 * time.Second})
	if err != nil {
		return nil, err
	}
	head, body, _ := strings.Cut(out, "\n")
	switch head {
	case "#apt":
		return ParseAptHistory(body), nil
	case "#dnf":
		return ParseDnfHistory(body), nil
	}
	return []Transaction{}, nil
}

// aptPkgRe matches "libssl3:amd64 (3.0.11-1, 3.0.13-1)" in an apt history line.
var aptPkgRe = regexp.MustCompile(`([^\s,(][^\s(]*) \(([^)]*)\)`)

var aptActions = map[string]string{"Install": "install", "Upgrade": "upgrade", "Remove": "remove", "Purge": "remove", "Downgrade": "downgrade", "Reinstall": "reinstall"}

// ParseAptHistory reads /var/log/apt/history.log entries (oldest first) and returns the last 30, newest first.
func ParseAptHistory(text string) []Transaction {
	list := []Transaction{}
	var cur *Transaction
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "Start-Date":
			list = append(list, Transaction{When: strings.Join(strings.Fields(v), " "), Packages: []Change{}})
			cur = &list[len(list)-1]
		case "Commandline":
			if cur != nil {
				cur.Source = v
				if strings.Contains(v, "unattended-upgrade") {
					cur.Source = "unattended-upgrades"
				}
			}
		default:
			action, ok := aptActions[k]
			if !ok || cur == nil {
				continue
			}
			for _, m := range aptPkgRe.FindAllStringSubmatch(v, -1) {
				name, _, _ := strings.Cut(m[1], ":") // drop the :arch
				vers := strings.Split(m[2], ", ")    // "old, new" for upgrades, "ver" or "ver, automatic" otherwise
				ver := vers[0]
				if (action == "upgrade" || action == "downgrade") && len(vers) > 1 {
					ver = vers[1]
				}
				cur.Packages = append(cur.Packages, Change{action, name, ver})
			}
		}
	}
	list = list[max(0, len(list)-historyMax):]
	slices.Reverse(list)
	return list
}

var (
	dnfActions = map[string]string{"install": "install", "upgrade": "upgrade", "removed": "remove", "remove": "remove", "erase": "remove", "downgrade": "downgrade", "reinstall": "reinstall"}
	// nevraRe splits name-[epoch:]version-release.arch
	nevraRe = regexp.MustCompile(`^(.+)-((?:\d+:)?[^-]+-[^-]+)\.[^.]+$`)
)

// ParseDnfHistory reads consecutive "dnf history info" outputs (dnf 4 and dnf 5), newest first as dnf lists them.
func ParseDnfHistory(text string) []Transaction {
	list := []Transaction{}
	var cur *Transaction
	inPkgs := false
	for _, line := range strings.Split(text, "\n") {
		k, v, isKV := strings.Cut(line, ":")
		key := strings.ToLower(strings.TrimSpace(k))
		switch {
		case isKV && key == "transaction id":
			list = append(list, Transaction{Packages: []Change{}})
			cur, inPkgs = &list[len(list)-1], false
		case cur == nil:
		case isKV && key == "begin time":
			cur.When = strings.TrimSpace(v)
		case isKV && key == "command line":
			cur.Source = strings.TrimSpace(v)
		case isKV && key == "packages altered":
			inPkgs = true
		case inPkgs && strings.HasPrefix(line, " "):
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			action, ok := dnfActions[strings.ToLower(f[0])] // skips "Upgraded", "Replaced", "Obsoleted": the old side
			if !ok {
				continue
			}
			c := Change{Action: action, Name: f[1]}
			if m := nevraRe.FindStringSubmatch(f[1]); m != nil {
				c.Name, c.Version = m[1], m[2]
			}
			cur.Packages = append(cur.Packages, c)
		case strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " "):
			inPkgs = false
		}
	}
	return list[:min(len(list), historyMax)]
}
