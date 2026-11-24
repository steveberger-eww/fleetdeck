package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.Open(dir); err != nil {
		panic(err)
	}
	if err := sec.Init(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func i64(v int64) *int64 { return &v }

func TestParsersStaleAndTimers(t *testing.T) {
	eq(t, ParseStale("nginx.service\ndbus.service\n\nbad name;\n"), []Stale{{"nginx.service", false}, {"dbus.service", true}})
	ts := ParseTimers(strings.Join([]string{
		"Id=apt-daily.timer", "Description=Daily apt", "Unit=apt-daily.service", "ActiveState=active", "UnitFileState=enabled",
		"NextElapseUSecRealtime=@1764000000", "LastTriggerUSec=n/a", "",
		"Id=fstrim.timer", "Unit=fstrim.service", "ActiveState=active", "UnitFileState=enabled", "NextElapseUSecRealtime=", "LastTriggerUSec=Mon 2025-11-24 00:00:00 UTC",
		"#svc", "Id=apt-daily.service", "Result=success", "ActiveState=inactive", "", "Id=fstrim.service", "Result=exit-code", "ActiveState=activating",
	}, "\n"))
	type row struct {
		unit       string
		next, last *int64
		result     string
		running    bool
	}
	var got []row
	for _, x := range ts {
		got = append(got, row{x.Unit, x.Next, x.Last, x.Result, x.Running})
	}
	eq(t, got, []row{
		{"apt-daily.timer", i64(1764000000000), nil, "success", false},
		{"fstrim.timer", nil, i64(time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC).UnixMilli()), "exit-code", true}, // pre-247 systemd: text dates
	})
}

func TestNetworkExposure(t *testing.T) {
	ps := func(name, ports string) string {
		b, _ := json.Marshal(map[string]string{"Names": name, "Ports": ports})
		return string(b)
	}
	out := func(ufwDocker []string, extraRules ...string) string {
		l := []string{
			"#ss",
			`tcp   LISTEN 0 4096   0.0.0.0:22   0.0.0.0:*  users:(("sshd",pid=812,fd=3))`,
			`tcp   LISTEN 0 4096      [::]:22      [::]:*  users:(("sshd",pid=812,fd=4))`,
			`tcp   LISTEN 0 4096 127.0.0.1:5432 0.0.0.0:* users:(("postgres",pid=9,fd=5))`,
			`udp   UNCONN 0 0   127.0.0.53%lo:53 0.0.0.0:* users:(("systemd-resolve",pid=7,fd=13))`,
			`tcp   LISTEN 0 4096   0.0.0.0:8080 0.0.0.0:* users:(("docker-proxy",pid=1,fd=4))`,
			`tcp   LISTEN 0 511    0.0.0.0:3000 0.0.0.0:* users:(("node",pid=2,fd=4))`,
			"#addr", "lo UNKNOWN 127.0.0.1/8 ::1/128", "eth0@if5 UP 203.0.113.5/24 fe80::1/64",
			"#route", "default via 203.0.113.1 dev eth0 proto dhcp metric 100",
			"#dns", "Global: 1.1.1.1", "Link 2 (eth0): 203.0.113.1 2001:db8::53",
			"#ufw", "Status: active", "Logging: on (low)", "Default: deny (incoming), allow (outgoing), deny (routed)", "New profiles: skip", "",
			"To                         Action      From", "--                         ------      ----", "22/tcp                     ALLOW IN    Anywhere", "3000                       ALLOW IN    10.0.0.0/8", "Anywhere (v6) on tailscale0 ALLOW IN    Anywhere (v6)",
			"#ufwrules", "### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in", "### tuple ### allow any 3000 0.0.0.0/0 any 10.0.0.0/8 in", "### tuple ### allow any any 0.0.0.0/0 any 0.0.0.0/0 in_tailscale0",
		}
		l = append(l, extraRules...)
		l = append(l, "#ufwdocker")
		l = append(l, ufwDocker...)
		l = append(l, "#firewalld", "#docker", ps("web", "0.0.0.0:8080->80/tcp, :::8080->80/tcp"), ps("db", "127.0.0.1:6379->6379/tcp"), ps("dns", "0.0.0.0:5353->53/udp"), ps("x", "80/tcp"))
		return strings.Join(l, "\n")
	}
	n := ParseNetwork(out(nil))
	ex := map[string]string{}
	find := func(port int) *Port {
		for _, p := range n.Ports {
			if p.Port == port {
				return p
			}
		}
		return nil
	}
	for _, p := range n.Ports {
		ex[fmt.Sprintf("%d/%s", p.Port, p.Proto)] = p.Exposure
	}
	eq(t, ex, map[string]string{"22/tcp": "open", "53/udp": "local", "3000/tcp": "restricted", "5353/udp": "bypass", "5432/tcp": "local", "6379/tcp": "local", "8080/tcp": "bypass"})
	eq(t, find(8080).Published, []Published{{"web", 80}})
	eq(t, find(22).Addrs, []string{"0.0.0.0", "::"})
	eq(t, n.Firewall["kind"], "ufw")
	rules := n.Firewall["rules"].([][]string)
	eq(t, rules[1], []string{"3000", "ALLOW IN", "10.0.0.0/8"})
	eq(t, rules[2], []string{"Anywhere (v6) on tailscale0", "ALLOW IN", "Anywhere (v6)"}) // single space before ALLOW
	eq(t, []any{*n.Gateway, *n.GatewayDev, n.DNS}, []any{"203.0.113.1", "eth0", []string{"1.1.1.1", "203.0.113.1", "2001:db8::53"}})
	eq(t, n.Addrs[1], Iface{"eth0", "UP", []string{"203.0.113.5/24", "fe80::1/64"}})

	// With ufw-docker active, `ufw route` rules on the container port decide; the rest is blocked.
	d := ParseNetwork(out([]string{"written", "active"}, "### tuple ### route:allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in"))
	eq(t, *d.UfwDocker, "active")
	var docker [][2]any
	for _, p := range d.Ports {
		if p.Docker {
			docker = append(docker, [2]any{p.Port, p.Exposure})
		}
	}
	eq(t, docker, [][2]any{{5353, "blocked"}, {8080, "open"}})
	// No firewall at all: everything not on loopback is open.
	eq(t, ParseNetwork(`#ss` + "\n" + `tcp LISTEN 0 1 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1,fd=1))`).Ports[0].Exposure, "open")

	// Input checks run before any SSH call, so a zero server is fine.
	ctx := context.Background()
	if _, err := UfwAllow(ctx, &db.Server{}, UfwAllowOpts{Port: 0, Proto: "tcp"}); err == nil {
		t.Fatal("port 0 accepted")
	}
	if _, err := UfwAllow(ctx, &db.Server{}, UfwAllowOpts{Port: 80, Proto: "tcp; reboot"}); err == nil {
		t.Fatal("bad proto accepted")
	}
}

func TestAuthorizedKeys(t *testing.T) {
	blob := "AAAAC3NzaC1lZDI1NTE5AAAAIGq0iQ5nQm8hJ3pHjW2Q0Yg3x8n6yWb1tH0r3u9k2Xc4"
	users := ParseAccounts(strings.Join([]string{
		"user=root:0:1:/bin/bash", `key=root from="10.0.0.0/8",command="echo a b" ssh-ed25519 ` + blob + " alice@laptop",
		"user=git:998:0:/usr/sbin/nologin", "key=git ssh-ed25519 " + blob, "key=git not a key", "key=ghost ssh-ed25519 " + blob,
	}, "\n"))
	var counts [][2]any
	for _, u := range users {
		counts = append(counts, [2]any{u.Name, len(u.Keys)})
	}
	eq(t, counts, [][2]any{{"root", 1}, {"git", 1}})
	eq(t, users[0].Keys[0], Key{Type: "ssh-ed25519", Blob: blob, FP: users[1].Keys[0].FP, Comment: "alice@laptop", Options: `from="10.0.0.0/8",command="echo a b"`})
	if !regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`).MatchString(users[0].Keys[0].FP) {
		t.Fatal(users[0].Keys[0].FP)
	}
	ctx := context.Background()
	if _, err := RevokeKey(ctx, &db.Server{}, "root; id", blob); err == nil || err.Error() != "invalid user" {
		t.Fatal(err)
	}
	if _, err := RevokeKey(ctx, &db.Server{}, "root", blob+"|d;e"); err == nil || err.Error() != "invalid key" {
		t.Fatal(err)
	}
}
