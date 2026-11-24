package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

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

// Not in test.js: the history queries.
func TestSeries(t *testing.T) {
	now := db.Now()/60_000*60_000 - 30_000 // mid-minute, so now and now+1 share a bucket
	for _, q := range []string{
		`INSERT INTO metrics (server_id, t, cpu, mem, temp) VALUES ('a', ?, 0.2, 0.5, NULL), ('a', ? + 1, 0.4, NULL, 40), ('a', ? - 7200000, 0.9, 0.9, 0)`,
		`INSERT INTO container_metrics (server_id, name, t, cpu, mem) VALUES ('a', 'web', ?, 10, NULL), ('a', 'web', ? + 1, 20, NULL), ('a', 'web', ? - 7200000, 1, 1)`,
	} {
		if _, err := db.Exec(q, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Series("a", "bogus")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, [2]int64{s.Bucket, s.To - s.From}, [2]int64{60_000, 3600e3})
	eq(t, len(s.Points), 1) // only the last hour, both samples in one bucket
	p := s.Points[0]
	eq(t, []any{p.T % 60_000, *p.CPU > 0.29 && *p.CPU < 0.31, *p.Mem, *p.Temp, p.Rx}, []any{int64(0), true, 0.5, 40.0, (*float64)(nil)})
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"rx":null`) {
		t.Fatal(string(b))
	}
	day, _ := Series("a", "24h")
	eq(t, day.Bucket, int64(360_000))
	c, err := ContainerSeries("a", "web", "1h")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(c.Points), 1)
	eq(t, []any{*c.Points[0].CPU, c.Points[0].Mem}, []any{15.0, (*float64)(nil)})
	sp, err := Sparks()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(sp["a"]), 2)
	b, _ = json.Marshal(sp["a"][1])
	eq(t, string(b), fmt.Sprintf(`[%d,0.4,null]`, now+1))
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestScriptsVerbatim(t *testing.T) {
	// sha256(PROBE + "|" + SMART) of lib/probe.js's strings.
	h := sha256.Sum256([]byte(PROBE + "|" + SMART))
	eq(t, hex.EncodeToString(h[:]), "e3db0cd74a490d88b8f32337cb5b12d03045e80458c8bb2682c16d935ab30213")
}

func TestProbeParser(t *testing.T) {
	s := Parse(strings.Join([]string{
		"hostname=web1", "os=Debian GNU/Linux 12 (bookworm)", "kernel=6.1.0", "cores=4", "virt=none", "ips=10.0.0.5 fd00::5",
		"uptime=90061.5", "load=0.50 0.20 0.10", "cpu1=cpu  100 0 100 800 0 0 0 0 0 0", "cpu2=cpu  150 0 150 900 0 0 0 0 0 0",
		"mem=1000 250 400 100", "fs=/dev/sda2 ext4 2000 500 /srv/data", "fs=/dev/sda1 vfat 100 90 /boot", "fs=/dev/sda2 ext4 2000 500 /", "ino=6400 64000 /", "net=10 20", "dio=512 1024",
		"temp=48500", "pkg=apt", "updates=3", "security=1", "reboot=1", "failed=0", "cockpit=inactive", "docker=2 5", "probe_user=root",
	}, "\n"))
	eq(t, *s.CPU, 0.5)
	eq(t, *s.Mem, 0.75)
	eq(t, *s.Swap, 0.75)
	eq(t, len(s.Disks), 2) // same device collapsed
	var mounts []string
	for _, d := range s.Disks {
		mounts = append(mounts, d.Mount)
	}
	sort.Strings(mounts)
	eq(t, mounts, []string{"/", "/boot"})
	eq(t, *s.Disk, 0.25) // root mount, not the fullest
	eq(t, *s.Disks[0].InodePct, 0.1) // ino=, from df -Pi
	eq(t, s.Disks[1].InodePct, (*float64)(nil)) // vfat: df reports no inodes
	eq(t, *s.Temp, 48.5)
	eq(t, *s.Docker, Docker{Running: 2, Total: 5})
	eq(t, s.IPs, []string{"10.0.0.5", "fd00::5"})
	eq(t, s.Virt, "")
	eq(t, s.Reboot, true)
	if Parse("hostname=x").CPU != nil {
		t.Fatal("cpu without samples should be null")
	}
}

func TestProbeUpdatesAndSmart(t *testing.T) {
	s := Parse(strings.Join(smartFixture(), "\n"))
	eq(t, *s.AutoUpdates, true)
	eq(t, *Parse("auto_updates=off").AutoUpdates, false)
	if Parse("x=1").AutoUpdates != nil {
		t.Fatal("autoUpdates should be null")
	}
	var got [][2]any
	for _, d := range s.Smart.Disks {
		got = append(got, [2]any{d.Dev, d.Problem})
	}
	eq(t, got, [][2]any{{"/dev/sda", false}, {"/dev/nvme0", false}, {"/dev/sdb", true}}) // standby + junk skipped
	eq(t, *s.Smart.Disks[0].Realloc, 8.0)
	eq(t, *s.Smart.Disks[1].Wear, 3.0)
	if Parse("x=1").Smart != nil {
		t.Fatal("smart should be null when not checked this round")
	}
	eq(t, *Parse("smart=none").Smart, Smart{Status: "none", Disks: []SmartDisk{}})
}

func TestProbeContainersInterfaces(t *testing.T) {
	s := Parse(strings.Join(containerFixture(), "\n"))
	var got [][4]any
	for _, c := range s.Containers {
		h := any(nil)
		if c.Health != nil {
			h = *c.Health
		}
		row := [4]any{c.Name, h, nil, nil}
		if c.CPU != nil {
			row[2] = *c.CPU
		}
		if c.Mem != nil {
			row[3] = *c.Mem
		}
		got = append(got, row)
	}
	eq(t, got, [][4]any{{"web", "healthy", 150.25, 13107200.0}, {"db", "unhealthy", 0.0, 1.5e9}, {"job", nil, nil, nil}})
	eq(t, *s.Unhealthy, 2) // unhealthy + restarting
	if Parse("x=1").Containers != nil {
		t.Fatal("containers should be null without docker")
	}
	var ifs [][3]any
	for _, i := range s.Ifaces {
		ifs = append(ifs, [3]any{i.Name, i.RxTotal, i.TxTotal})
	}
	eq(t, ifs, [][3]any{{"eth0", 5000.0, 7000.0}, {"docker0", 300.0, 400.0}})
}

// The JSON must match what the Node version produced (testdata from lib/probe.js), and Node's last_json must load.
func TestMatchesNodeJSON(t *testing.T) {
	raw, err := os.ReadFile("testdata/node_parse.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Input, Out, Empty string }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	asMap := func(v any) map[string]any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		for _, k := range []string{"rx", "tx", "dr", "dw", "t"} { // added by the probe loop, not by parse
			delete(m, k)
		}
		return m
	}
	for _, c := range [][2]string{{fx.Input, fx.Out}, {"", fx.Empty}} {
		var want map[string]any
		_ = json.Unmarshal([]byte(c[1]), &want)
		eq(t, asMap(Parse(c[0])), want)
		var fromNode Snapshot
		if err := json.Unmarshal([]byte(c[1]), &fromNode); err != nil {
			t.Fatal(err)
		}
		eq(t, asMap(&fromNode), want)
	}
}

// smartFixture is the probe output of the JS test 'probe: automatic updates and S.M.A.R.T.'.
func smartFixture() []string {
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	type M = map[string]any
	sata := M{"smart_status": M{"passed": true}, "model_name": "WDC WD40", "temperature": M{"current": 34}, "power_on_time": M{"hours": 20000},
		"ata_smart_attributes": M{"table": []M{{"id": 5, "raw": M{"value": 8}}, {"id": 197, "raw": M{"value": 0}}, {"id": 198, "raw": M{"value": 0}}}}}
	nvme := M{"smart_status": M{"passed": true}, "model_name": "Samsung 980", "nvme_smart_health_information_log": M{"percentage_used": 3, "media_errors": 0, "critical_warning": 0}}
	pending := M{}
	for k, v := range sata {
		pending[k] = v
	}
	pending["ata_smart_attributes"] = M{"table": []M{{"id": 197, "raw": M{"value": 2}}}}
	return []string{"auto_updates=on", "smart=ok", "smartdev=/dev/sda " + j(sata), "smartdev=/dev/nvme0 " + j(nvme),
		"smartdev=/dev/sdb " + j(pending), `smartdev=/dev/sdc {"smartctl":{"exit_status":2}}`, "smartdev=/dev/sdd {broken"}
}

// containerFixture is the probe output of the JS test 'probe: containers, interfaces, network alert'.
func containerFixture() []string {
	ps := func(name, state, status string) string {
		b, _ := json.Marshal(map[string]string{"Names": name, "Image": "img", "State": state, "Status": status})
		return "ctr=" + string(b)
	}
	return []string{"docker=2 3", ps("web", "running", "Up 2 hours (healthy)"), ps("db", "running", "Up 1 hour (unhealthy)"), ps("job", "restarting", "Restarting (1) 3 seconds ago"),
		`cst={"Name":"web","CPUPerc":"150.25%","MemUsage":"12.5MiB / 1.9GiB"}`, `cst={"Name":"db","CPUPerc":"0.00%","MemUsage":"1.5GB / 4GB"}`, "cst={broken",
		"if=eth0 5000 7000", "if=docker0 300 400", "net=5000 7000"}
}
