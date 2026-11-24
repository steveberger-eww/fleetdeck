package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/ssh"
)

// Snapshot is one probe result. It is stored as JSON in servers.last_json and sent to the browser as-is,
// so keys and null-vs-number behave as in the Node version.
type Snapshot struct {
	Hostname    string      `json:"hostname"`
	OS          string      `json:"os"`
	OSID        string      `json:"osId"`
	Kernel      string      `json:"kernel"`
	Arch        string      `json:"arch"`
	CPUModel    string      `json:"cpuModel"`
	Cores       *float64    `json:"cores"`
	Virt        string      `json:"virt"`
	IPs         []string    `json:"ips"`
	Uptime      *float64    `json:"uptime"`
	Load        []float64   `json:"load"`
	Procs       *float64    `json:"procs"`
	CPU         *float64    `json:"cpu"`
	MemTotal    float64     `json:"memTotal"`
	MemUsed     float64     `json:"memUsed"`
	Mem         *float64    `json:"mem"`
	SwapTotal   float64     `json:"swapTotal"`
	Swap        *float64    `json:"swap"`
	Disks       []Disk      `json:"disks"`
	Disk        *float64    `json:"disk"`
	Counters    Counters    `json:"counters"`
	Ifaces      []Iface     `json:"ifaces"` // all interfaces but lo and veth*; the rx/tx totals above leave out virtual ones (docker, VPN) to avoid double counting
	Temp        *float64    `json:"temp"`
	Pkg         string      `json:"pkg"`
	Updates     *float64    `json:"updates"`
	Security    *float64    `json:"security"`
	AutoUpdates *bool       `json:"autoUpdates"`
	Reboot      bool        `json:"reboot"`
	Failed      float64     `json:"failed"`
	Cockpit     bool        `json:"cockpit"`
	Docker      *Docker     `json:"docker"`
	Containers  []Container `json:"containers"` // null when Docker isn't there
	Unhealthy   *int        `json:"unhealthy"`
	ProbeUser   string      `json:"probeUser"`
	Smart       *Smart      `json:"smart"`
	// Set by the probe loop (rates against the previous probe), not by Parse.
	Rx *float64 `json:"rx"`
	Tx *float64 `json:"tx"`
	Dr *float64 `json:"dr"`
	Dw *float64 `json:"dw"`
	T  int64    `json:"t"`
}

type Disk struct {
	Mount string  `json:"mount"`
	Type  string  `json:"type"`
	Size  float64 `json:"size"`
	Used  float64 `json:"used"`
	Pct   float64 `json:"pct"`
}

// Counters are byte totals since boot: network in/out, disk read/write.
type Counters struct {
	Rx float64 `json:"rx"`
	Tx float64 `json:"tx"`
	Dr float64 `json:"dr"`
	Dw float64 `json:"dw"`
}

type Iface struct {
	Name    string   `json:"name"`
	RxTotal float64  `json:"rxTotal"`
	TxTotal float64  `json:"txTotal"`
	Rx      *float64 `json:"rx"`
	Tx      *float64 `json:"tx"`
}

type Docker struct {
	Running float64 `json:"running"`
	Total   float64 `json:"total"`
}

type Container struct {
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	State  string   `json:"state"`
	Status string   `json:"status"`
	Health *string  `json:"health"`
	CPU    *float64 `json:"cpu"`
	Mem    *float64 `json:"mem"`
}

type Smart struct {
	Status string      `json:"status"` // ok | none (no smartctl) | noroot
	Disks  []SmartDisk `json:"disks"`
}

type SmartDisk struct {
	Dev           string   `json:"dev"`
	Model         string   `json:"model"`
	Serial        string   `json:"serial"`
	Passed        bool     `json:"passed"`
	Temp          *float64 `json:"temp"`
	Hours         *float64 `json:"hours"`
	Realloc       *float64 `json:"realloc"`
	Pending       *float64 `json:"pending"`
	Uncorrectable *float64 `json:"uncorrectable"`
	Wear          *float64 `json:"wear"`
	MediaErrors   *float64 `json:"mediaErrors"`
	Critical      *float64 `json:"critical"`
	Problem       bool     `json:"problem"`
}

// num is JS Number() on script output: "" is 0; unparseable (or infinite, which JSON can't hold) is NaN.
func num(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) {
		return math.NaN()
	}
	return f
}

// ptr is a nullable number; NaN becomes null, as JSON.stringify does.
func ptr(f float64) *float64 {
	if math.IsNaN(f) {
		return nil
	}
	return &f
}

// or0 is JS `x || 0`.
func or0(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	return f
}

// at is xs[i], NaN (JS undefined in arithmetic) when out of range.
func at(xs []float64, i int) float64 {
	if i < len(xs) {
		return xs[i]
	}
	return math.NaN()
}

func gt0(p *float64) bool { return p != nil && *p > 0 }

func nums(v string) []float64 {
	f := strings.Fields(v)
	if len(f) == 0 {
		f = []string{""} // ''.split(/\s+/) is [''], so a missing value reads as [0]
	}
	out := make([]float64, len(f))
	for i, s := range f {
		out[i] = num(s)
	}
	return out
}

// n: absent or empty is null.
func n(v string) *float64 {
	if v == "" {
		return nil
	}
	return ptr(num(v))
}

var jsFloat = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?`)

// parseFloat is JS parseFloat: the leading number of s, NaN if there is none.
func parseFloat(s string) float64 {
	m := jsFloat.FindString(strings.TrimLeft(s, " \t\n\r"))
	if m == "" {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(m, 64)
	return f
}

type smartJSON struct {
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	ModelName     string `json:"model_name"`
	SCSIModelName string `json:"scsi_model_name"`
	SerialNumber  string `json:"serial_number"`
	Temperature   struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours *float64 `json:"hours"`
	} `json:"power_on_time"`
	ATA struct {
		Table []struct {
			ID  float64 `json:"id"`
			Raw struct {
				Value *float64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe struct {
		PercentageUsed  *float64 `json:"percentage_used"`
		MediaErrors     *float64 `json:"media_errors"`
		CriticalWarning *float64 `json:"critical_warning"`
	} `json:"nvme_smart_health_information_log"`
}

func parseSmart(status string, lines []string) *Smart {
	if status == "" {
		return nil
	}
	disks := []SmartDisk{}
	for _, l := range lines {
		i := strings.Index(l, " ")
		if i < 0 {
			continue
		}
		var j smartJSON
		if json.Unmarshal([]byte(l[i+1:]), &j) != nil {
			continue
		}
		if j.SmartStatus == nil {
			continue // in standby, or no SMART support (USB bridges, virtual disks)
		}
		attr := map[float64]*float64{}
		for _, a := range j.ATA.Table {
			attr[a.ID] = a.Raw.Value
		}
		model := j.ModelName
		if model == "" {
			model = j.SCSIModelName
		}
		d := SmartDisk{
			Dev: l[:i], Model: model, Serial: j.SerialNumber,
			Passed: j.SmartStatus.Passed == nil || *j.SmartStatus.Passed, Temp: j.Temperature.Current, Hours: j.PowerOnTime.Hours,
			Realloc: attr[5], Pending: attr[197], Uncorrectable: attr[198],
			Wear: j.NVMe.PercentageUsed, MediaErrors: j.NVMe.MediaErrors, Critical: j.NVMe.CriticalWarning,
		}
		// Reallocated sectors alone are common on old-but-stable disks; pending/uncorrectable ones mean data at risk.
		d.Problem = !d.Passed || gt0(d.Pending) || gt0(d.Uncorrectable) || gt0(d.Critical) || gt0(d.MediaErrors)
		disks = append(disks, d)
	}
	return &Smart{Status: status, Disks: disks}
}

// "12.5MiB / 1.9GiB" -> bytes of the first figure
var (
	units   = map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40}
	bytesRe = regexp.MustCompile(`(?i)([\d.]+)\s*([KMGT]?i?B)`)
)

func bytesOf(v string) *float64 {
	m := bytesRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	u, ok := units[strings.ToUpper(m[2])]
	if !ok {
		u = 1
	}
	return ptr(math.Floor(parseFloat(m[1])*u + 0.5)) // Math.round
}

// jsonLines decodes one JSON object per line, skipping junk.
func jsonLines[T any](lines []string) []*T {
	var out []*T
	for _, l := range lines {
		var v *T
		if json.Unmarshal([]byte(l), &v) == nil && v != nil {
			out = append(out, v)
		}
	}
	return out
}

func parseContainers(ps, stats []string) []Container {
	type stat struct{ Name, CPUPerc, MemUsage string }
	st := map[string]*stat{}
	for _, s := range jsonLines[stat](stats) {
		st[s.Name] = s
	}
	out := []Container{}
	for _, c := range jsonLines[struct{ Names, Image, State, Status string }](ps) {
		var health *string
		for _, h := range [][2]string{{"(unhealthy)", "unhealthy"}, {"(healthy)", "healthy"}, {"health: starting", "starting"}} {
			if strings.Contains(c.Status, h[0]) {
				health = &h[1]
				break
			}
		}
		ct := Container{Name: c.Names, Image: c.Image, State: c.State, Status: c.Status, Health: health}
		if s := st[c.Names]; s != nil {
			cpu := or0(parseFloat(s.CPUPerc))
			ct.CPU, ct.Mem = &cpu, bytesOf(s.MemUsage)
		}
		out = append(out, ct)
	}
	return out
}

// Parse turns probe script output into a snapshot (without rates and t, which need the previous probe).
func Parse(text string) *Snapshot {
	kv := map[string]string{}
	var disks []Disk
	diskAt := map[string]int{}
	var smartDevs, ctrs, cstats []string
	ifaces := []Iface{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		k, v := line[:i], strings.TrimSpace(line[i+1:])
		switch k {
		case "smartdev":
			smartDevs = append(smartDevs, v)
		case "ctr":
			ctrs = append(ctrs, v)
		case "cst":
			cstats = append(cstats, v)
		case "if":
			f := append(strings.Split(v, " "), "", "")
			ifaces = append(ifaces, Iface{Name: f[0], RxTotal: or0(num(f[1])), TxTotal: or0(num(f[2]))})
		case "fs":
			// One entry per device: bind mounts and btrfs subvolumes repeat the same numbers. Prefer the "/" mount.
			f := strings.Split(v, " ")
			for len(f) < 4 {
				f = append(f, "")
			}
			dev, mount, size, used := f[0], strings.Join(f[4:], " "), num(f[2]), or0(num(f[3]))
			if size > 0 {
				d := Disk{Mount: mount, Type: f[1], Size: size, Used: used, Pct: used / size}
				if j, ok := diskAt[dev]; !ok {
					diskAt[dev] = len(disks)
					disks = append(disks, d)
				} else if mount == "/" {
					disks[j] = d // a JS Map keeps the first insertion position
				}
			}
		default:
			kv[k] = v
		}
	}
	a, b := nums(kv["cpu1"]), nums(kv["cpu2"])
	a, b = a[min(1, len(a)):min(9, len(a))], b[min(1, len(b)):min(9, len(b))] // user nice system idle iowait irq softirq steal
	sum := func(x []float64) (s float64) {
		for _, c := range x {
			s += or0(c)
		}
		return
	}
	idle := func(x []float64) float64 { return or0(at(x, 3)) + or0(at(x, 4)) }
	dt := sum(b) - sum(a)
	mem := nums(kv["mem"])
	memTotal, memAvail, swapTotal, swapFree := or0(at(mem, 0)), at(mem, 1), or0(at(mem, 2)), at(mem, 3)
	net, dio, dk := nums(kv["net"]), nums(kv["dio"]), nums(kv["docker"])

	s := &Snapshot{
		Hostname: kv["hostname"], OS: kv["os"], OSID: kv["os_id"], Kernel: kv["kernel"], Arch: kv["arch"],
		CPUModel: kv["cpu_model"], Cores: n(kv["cores"]), IPs: strings.Fields(kv["ips"]), Uptime: n(kv["uptime"]),
		Procs: n(kv["procs"]), MemTotal: memTotal, SwapTotal: swapTotal, Disks: disks,
		Counters: Counters{or0(at(net, 0)), or0(at(net, 1)), or0(at(dio, 0)), or0(at(dio, 1))},
		Ifaces:   ifaces, Pkg: kv["pkg"], Updates: n(kv["updates"]), Security: n(kv["security"]),
		Reboot: kv["reboot"] == "1", Failed: or0(num(kv["failed"])), Cockpit: kv["cockpit"] == "active",
		ProbeUser: kv["probe_user"], Smart: parseSmart(kv["smart"], smartDevs),
	}
	if s.Disks == nil {
		s.Disks = []Disk{}
	}
	if v := kv["virt"]; v != "none" {
		s.Virt = v
	}
	load := nums(kv["load"])
	for _, l := range load[:min(3, len(load))] {
		s.Load = append(s.Load, or0(l)) // JS would write NaN as null; the script always prints numbers
	}
	if dt > 0 {
		s.CPU = ptr(math.Min(1, math.Max(0, 1-(idle(b)-idle(a))/dt)))
	}
	if memTotal != 0 {
		s.MemUsed = or0(memTotal - memAvail)
		s.Mem = ptr(1 - memAvail/memTotal)
	}
	if swapTotal != 0 {
		s.Swap = ptr(1 - swapFree/swapTotal)
	}
	var root *Disk
	for i := range disks {
		if disks[i].Mount == "/" {
			root = &disks[i]
			break
		}
	}
	if root == nil {
		for i := range disks {
			if root == nil || disks[i].Pct > root.Pct {
				root = &disks[i]
			}
		}
	}
	if root != nil {
		s.Disk = &root.Pct
	}
	if kv["temp"] != "" {
		s.Temp = ptr(num(kv["temp"]) / 1000)
	}
	if v := kv["auto_updates"]; v != "" {
		on := v == "on"
		s.AutoUpdates = &on
	}
	if kv["docker"] != "" {
		s.Docker = &Docker{or0(at(dk, 0)), or0(at(dk, 1))}
		s.Containers = parseContainers(ctrs, cstats)
		u := 0
		for _, c := range s.Containers {
			if (c.Health != nil && *c.Health == "unhealthy") || c.State == "restarting" {
				u++
			}
		}
		s.Unhealthy = &u
	}
	return s
}

// ProbeError is why the last probe of a server failed, and since when it has been failing.
type ProbeError struct {
	Message string `json:"message"`
	Since   int64  `json:"since"`
}

func (e *ProbeError) Error() string { return e.Message }

type prevSample struct {
	t      int64
	c      Counters
	ifaces map[string][2]float64
}

type smartSample struct {
	t     time.Time
	smart *Smart
}

var (
	mu           sync.Mutex
	prevCounters = map[string]prevSample{}    // id -> last counters
	smartCache   = map[string]smartSample{}   // SMART runs every 30 min, snapshots in between reuse it
	inflight     = map[string]chan struct{}{} // so a manual refresh waits for a probe already running
	errs         = map[string]ProbeError{}
	listeners    []func(*db.Server, *Snapshot, error)
)

// LastError is the current probe error of a server, nil when its last probe worked.
func LastError(id string) *ProbeError {
	mu.Lock()
	defer mu.Unlock()
	if e, ok := errs[id]; ok {
		return &e
	}
	return nil
}

// ClearError forgets a server's probe error (paused or deleted server).
func ClearError(id string) {
	mu.Lock()
	delete(errs, id)
	mu.Unlock()
}

// OnProbe registers fn to run after every probe: snap on success, err (a *ProbeError) on failure.
func OnProbe(fn func(s *db.Server, snap *Snapshot, err error)) {
	mu.Lock()
	listeners = append(listeners, fn)
	mu.Unlock()
}

// Probe probes the server now and waits. A probe of the same server already running is joined, not repeated.
// Failures are recorded (LastError) and passed to the OnProbe listeners.
func Probe(s *db.Server) {
	mu.Lock()
	if done, ok := inflight[s.ID]; ok {
		mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	inflight[s.ID] = done
	mu.Unlock()
	defer func() {
		mu.Lock()
		delete(inflight, s.ID)
		mu.Unlock()
		close(done)
	}()

	snap, err := collect(s)
	var pe *ProbeError
	mu.Lock()
	if err == nil {
		delete(errs, s.ID)
	} else {
		since := db.Now()
		if prev, ok := errs[s.ID]; ok {
			since = prev.Since
		}
		errs[s.ID] = ProbeError{err.Error(), since}
		pe = &ProbeError{err.Error(), since}
	}
	fns := listeners
	mu.Unlock()
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("probe listener for %s: %v", s.Name, r)
				}
			}()
			if pe != nil {
				fn(s, nil, pe)
			} else {
				fn(s, snap, nil)
			}
		}()
	}
}

func collect(s *db.Server) (snap *Snapshot, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	mu.Lock()
	sc, hasSC := smartCache[s.ID]
	mu.Unlock()
	withSmart := !hasSC || sc.smart.Status != "ok" || time.Since(sc.t) > 30*time.Minute // re-check often until it works
	script, timeout := PROBE, 30*time.Second
	if withSmart {
		script, timeout = PROBE+SMART, 90*time.Second
	}
	r, err := ssh.Exec(context.Background(), s, script, ssh.ExecOpts{Mode: ssh.ModeAuto, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		lines := strings.Split(strings.TrimSpace(r.Out), "\n")
		msg := lines[len(lines)-1]
		if msg == "" {
			code := strconv.Itoa(r.Code)
			if r.Code == ssh.NoExit {
				code = "null"
			}
			msg = "probe exited with " + code
		}
		return nil, errors.New(msg)
	}
	snap = Parse(r.Out)
	mu.Lock()
	if snap.Smart != nil {
		smartCache[s.ID] = smartSample{time.Now(), snap.Smart}
	} else if hasSC {
		snap.Smart = sc.smart
	}
	t := db.Now()
	p, hasP := prevCounters[s.ID]
	// Counters reset on reboot; clamp the negative delta to 0.
	per := func(now, before float64, ok bool) *float64 {
		if !hasP || t <= p.t || !ok {
			return nil
		}
		return ptr(math.Max(0, (now-before)/(float64(t-p.t)/1000)))
	}
	c := snap.Counters
	snap.Rx, snap.Tx, snap.Dr, snap.Dw = per(c.Rx, p.c.Rx, true), per(c.Tx, p.c.Tx, true), per(c.Dr, p.c.Dr, true), per(c.Dw, p.c.Dw, true)
	next := prevSample{t, c, map[string][2]float64{}}
	for i := range snap.Ifaces {
		f := &snap.Ifaces[i]
		b, ok := p.ifaces[f.Name]
		f.Rx, f.Tx = per(f.RxTotal, b[0], ok), per(f.TxTotal, b[1], ok)
		next.ifaces[f.Name] = [2]float64{f.RxTotal, f.TxTotal}
	}
	prevCounters[s.ID] = next
	mu.Unlock()
	snap.T = t

	js, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE servers SET last_json = ? WHERE id = ?`, string(js), s.ID); err != nil {
		return nil, err
	}
	var load1 *float64
	if len(snap.Load) > 0 {
		load1 = &snap.Load[0]
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO metrics (server_id, t, cpu, mem, swap, disk, load1, rx, tx, dr, dw, temp)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, t, snap.CPU, snap.Mem, snap.Swap, snap.Disk, load1, snap.Rx, snap.Tx, snap.Dr, snap.Dw, snap.Temp); err != nil {
		return nil, err
	}
	for _, c := range snap.Containers {
		if c.CPU != nil {
			if _, err := db.Exec(`INSERT OR REPLACE INTO container_metrics (server_id, name, t, cpu, mem) VALUES (?, ?, ?, ?, ?)`,
				s.ID, c.Name, t, c.CPU, c.Mem); err != nil {
				return nil, err
			}
		}
	}
	return snap, nil
}

var ranges = map[string]int64{"1h": 3600e3, "6h": 6 * 3600e3, "24h": 864e5, "7d": 7 * 864e5, "30d": 30 * 864e5}

func span(rangeStr string) (span, bucket int64) {
	span, ok := ranges[rangeStr]
	if !ok {
		span = ranges["1h"]
	}
	return span, max(60_000, span/240/60_000*60_000) // at most ~240 points
}

type Point struct {
	T     int64    `db:"t" json:"t"`
	CPU   *float64 `db:"cpu" json:"cpu"`
	Mem   *float64 `db:"mem" json:"mem"`
	Swap  *float64 `db:"swap" json:"swap"`
	Disk  *float64 `db:"disk" json:"disk"`
	Load1 *float64 `db:"load1" json:"load1"`
	Rx    *float64 `db:"rx" json:"rx"`
	Tx    *float64 `db:"tx" json:"tx"`
	Dr    *float64 `db:"dr" json:"dr"`
	Dw    *float64 `db:"dw" json:"dw"`
	Temp  *float64 `db:"temp" json:"temp"`
}

type ContainerPoint struct {
	T   int64    `db:"t" json:"t"`
	CPU *float64 `db:"cpu" json:"cpu"`
	Mem *float64 `db:"mem" json:"mem"`
}

type SeriesOf[P any] struct {
	Bucket int64 `json:"bucket"`
	From   int64 `json:"from"`
	To     int64 `json:"to"`
	Points []P   `json:"points"`
}

// Series is a server's metric history for a range (1h, 6h, 24h, 7d, 30d; default 1h), averaged per bucket.
func Series(id, rangeStr string) (*SeriesOf[Point], error) {
	sp, bucket := span(rangeStr)
	now := db.Now()
	pts, err := db.Select[Point](`SELECT (t / ?) * ? AS t, AVG(cpu) cpu, AVG(mem) mem, AVG(swap) swap, AVG(disk) disk, AVG(load1) load1,
      AVG(rx) rx, AVG(tx) tx, AVG(dr) dr, AVG(dw) dw, MAX(temp) temp
    FROM metrics WHERE server_id = ? AND t >= ? GROUP BY t / ? ORDER BY 1`, bucket, bucket, id, now-sp, bucket)
	if err != nil {
		return nil, err
	}
	return &SeriesOf[Point]{bucket, now - sp, now, pts}, nil
}

// ContainerSeries is Series for one container (by name: compose recreates containers with new ids).
func ContainerSeries(id, name, rangeStr string) (*SeriesOf[ContainerPoint], error) {
	sp, bucket := span(rangeStr)
	now := db.Now()
	pts, err := db.Select[ContainerPoint](`SELECT (t / ?) * ? AS t, AVG(cpu) cpu, AVG(mem) mem FROM container_metrics
    WHERE server_id = ? AND name = ? AND t >= ? GROUP BY t / ? ORDER BY 1`, bucket, bucket, id, name, now-sp, bucket)
	if err != nil {
		return nil, err
	}
	return &SeriesOf[ContainerPoint]{bucket, now - sp, now, pts}, nil
}

// Sparks is the last hour of [t, cpu, mem] per server id, for the overview's sparklines.
func Sparks() (map[string][][3]any, error) {
	rows, err := db.Select[struct {
		ServerID string   `db:"server_id"`
		T        int64    `db:"t"`
		CPU      *float64 `db:"cpu"`
		Mem      *float64 `db:"mem"`
	}](`SELECT server_id, t, cpu, mem FROM metrics WHERE t >= ? ORDER BY t`, db.Now()-3600e3)
	if err != nil {
		return nil, err
	}
	out := map[string][][3]any{}
	for _, r := range rows {
		out[r.ServerID] = append(out[r.ServerID], [3]any{r.T, r.CPU, r.Mem})
	}
	return out, nil
}

func pollAll() {
	list, err := db.Select[db.Server](`SELECT * FROM servers WHERE paused = 0`)
	if err != nil {
		log.Printf("probe: %v", err)
		return
	}
	// ponytail: fixed concurrency of 16 probes; raise if you have hundreds of servers.
	for i := 0; i < len(list); i += 16 {
		var wg sync.WaitGroup
		for j := i; j < min(i+16, len(list)); j++ {
			wg.Go(func() { Probe(&list[j]) })
		}
		wg.Wait()
	}
}

func prune() {
	days := num(db.Setting("retention_metrics_days", "30"))
	if math.IsNaN(days) {
		return
	}
	cutoff := db.Now() - int64(days*864e5)
	for _, q := range []string{`DELETE FROM metrics WHERE t < ?`, `DELETE FROM container_metrics WHERE t < ?`} {
		if _, err := db.Exec(q, cutoff); err != nil {
			log.Printf("probe retention: %v", err)
		}
	}
}

// Start probes every unpaused server now and every minute, and prunes old metrics hourly.
// A round doesn't wait for a slow previous one; servers still being probed are joined, not probed twice.
func Start() {
	go pollAll()
	go func() {
		for range time.Tick(time.Minute) {
			go pollAll()
		}
	}()
	go func() {
		for range time.Tick(time.Hour) {
			prune()
		}
	}()
}
