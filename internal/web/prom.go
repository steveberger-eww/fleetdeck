package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fleetdeck/internal/alerts"
	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/probe"
	"fleetdeck/internal/sec"
)

// Prometheus scraping and CSV export. Each organization has its own scrape token (only its hash is stored,
// in tenant_settings) and /api/metrics shows that organization's servers.
func (s *Server) exportRoutes() {
	s.route("GET /api/tenant/metrics-token", func(c *ctx) (any, error) {
		return J{"enabled": db.TenantSetting(c.tenant, "metrics_token", "") != "", "url": s.publicBase(c.r) + "/api/metrics"}, nil
	}, admins)
	s.route("POST /api/tenant/metrics-token", func(c *ctx) (any, error) {
		token := sec.Token()
		if err := db.SetTenantSetting(c.tenant, "metrics_token", sec.SHA256(token)); err != nil {
			return nil, err
		}
		c.audit("Generated a new Prometheus scrape token; the old one stopped working", "")
		return J{"token": token, "url": s.publicBase(c.r) + "/api/metrics"}, nil
	}, admins)
	s.route("DELETE /api/tenant/metrics-token", func(c *ctx) (any, error) {
		if err := db.SetTenantSetting(c.tenant, "metrics_token", nil); err != nil {
			return nil, err
		}
		c.audit("Turned off Prometheus scraping", "")
		return okResp, nil
	}, admins)

	// Called by Prometheus with "Authorization: Bearer <token>". The token is the only credential.
	s.route("GET /api/metrics", func(c *ctx) (any, error) {
		token, ok := strings.CutPrefix(c.r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			c.w.Header().Set("WWW-Authenticate", `Bearer realm="fleetdeck"`)
			return nil, httperr.New(401, "Send the scrape token as a bearer token")
		}
		tenant, err := db.Get[string](`SELECT tenant_id FROM tenant_settings WHERE key = 'metrics_token' AND value = ?`, sec.SHA256(token))
		if err != nil {
			return nil, err
		}
		if tenant == nil {
			return nil, httperr.New(401, "Unknown scrape token")
		}
		out, err := promText(*tenant)
		if err != nil {
			return nil, err
		}
		c.w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.w.Header().Set("Cache-Control", "no-store")
		_, _ = c.w.Write([]byte(out))
		return nil, nil
	}, open)

	// The raw per-minute metrics of a server as CSV, for the range picked on its chart.
	s.route("GET /api/servers/{id}/metrics.csv", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleViewer)
		if err != nil {
			return nil, err
		}
		pts, err := probe.Raw(srv.ID, c.query("range"))
		if err != nil {
			return nil, err
		}
		c.w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		c.w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-metrics-%s.csv"`, fileSafe(srv.Name), time.Now().UTC().Format("2006-01-02")))
		c.w.Header().Set("Cache-Control", "no-store")
		w := csv.NewWriter(c.w)
		_ = w.Write([]string{"time", "cpu_pct", "mem_pct", "swap_pct", "disk_pct", "load1", "net_rx_bytes_s", "net_tx_bytes_s", "disk_read_bytes_s", "disk_write_bytes_s", "temp_c", "gpu_pct", "gpu_mem_pct"})
		for _, p := range pts {
			_ = w.Write([]string{time.UnixMilli(p.T).UTC().Format(time.RFC3339), csvNum(p.CPU, 100), csvNum(p.Mem, 100), csvNum(p.Swap, 100), csvNum(p.Disk, 100),
				csvNum(p.Load1, 1), csvNum(p.Rx, 1), csvNum(p.Tx, 1), csvNum(p.Dr, 1), csvNum(p.Dw, 1), csvNum(p.Temp, 1), csvNum(p.GPU, 100), csvNum(p.GPUMem, 100)})
		}
		w.Flush()
		return nil, nil
	})
}

var unsafeFile = regexp.MustCompile(`[^\w.-]+`)

func fileSafe(name string) string { return strings.Trim(unsafeFile.ReplaceAllString(name, "-"), "-.") }

func csvNum(v *float64, scale float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v*scale, 'f', -1, 64)
}

// prom collects samples per metric family: the text format wants each family's samples together.
type prom struct {
	order []string
	help  map[string]string
	lines map[string][]string
}

func (p *prom) add(name, help string, v *float64, labels ...string) {
	if v == nil {
		return
	}
	if _, ok := p.help[name]; !ok {
		p.order = append(p.order, name)
		p.help[name] = help
	}
	var l []string
	for i := 0; i+1 < len(labels); i += 2 {
		l = append(l, labels[i]+`="`+promEscape.Replace(labels[i+1])+`"`)
	}
	p.lines[name] = append(p.lines[name], fmt.Sprintf("%s{%s} %s", name, strings.Join(l, ","), strconv.FormatFloat(*v, 'g', -1, 64)))
}

func (p *prom) String() string {
	var b strings.Builder
	for _, n := range p.order {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s\n", n, p.help[n], n, strings.Join(p.lines[n], "\n"))
	}
	return b.String()
}

var promEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func num(f float64) *float64 { return &f }

func bit(b bool) *float64 {
	if b {
		return num(1)
	}
	return num(0)
}

// promText is the latest state of an organization's servers in the Prometheus text format. Paused servers are
// left out; unreachable ones only report fleetdeck_up 0, so stale values don't look current.
func promText(tenant string) (string, error) {
	servers, err := db.Select[db.Server](`SELECT * FROM servers WHERE tenant_id = ? AND paused = 0 ORDER BY lower(name)`, tenant)
	if err != nil {
		return "", err
	}
	firing, err := alerts.Firing(tenant)
	if err != nil {
		return "", err
	}
	nFiring := map[string]float64{}
	for _, f := range firing {
		nFiring[f.ServerID]++
	}
	customs, err := db.Select[probe.Custom](`SELECT * FROM custom_metrics WHERE tenant_id = ?`, tenant)
	if err != nil {
		return "", err
	}
	customName := map[string]string{}
	for _, m := range customs {
		customName[m.ID] = m.Name
	}
	type check struct {
		ServerID string   `db:"server_id"`
		Name     string   `db:"name"`
		LastOK   *int     `db:"last_ok"`
		TLSDays  *float64 `db:"tls_days"`
	}
	checks, err := db.Select[check](`SELECT server_id, name, last_ok, tls_days FROM checks WHERE tenant_id = ? AND enabled = 1 ORDER BY name`, tenant)
	if err != nil {
		return "", err
	}

	p := &prom{help: map[string]string{}, lines: map[string][]string{}}
	for _, s := range servers {
		id := []string{"server", s.Name, "server_id", s.ID}
		var snap *probe.Snapshot
		if s.LastJSON != nil && probe.LastError(s.ID) == nil {
			if err := json.Unmarshal([]byte(*s.LastJSON), &snap); err != nil {
				snap = nil
			}
		}
		p.add("fleetdeck_up", "1 when the last probe of the server worked.", bit(snap != nil), id...)
		p.add("fleetdeck_alerts_firing", "Alerts currently firing for the server.", num(nFiring[s.ID]), id...)
		for _, c := range checks {
			if c.ServerID == s.ID && c.LastOK != nil {
				p.add("fleetdeck_check_up", "1 when the endpoint check passed on its last run.", bit(*c.LastOK == 1), append(id, "check", c.Name)...)
				p.add("fleetdeck_check_tls_days_left", "Days until the check's TLS certificate expires.", c.TLSDays, append(id, "check", c.Name)...)
			}
		}
		if snap == nil {
			continue
		}
		p.add("fleetdeck_last_probe_timestamp_seconds", "When the server was last probed.", num(float64(snap.T)/1000), id...)
		p.add("fleetdeck_uptime_seconds", "Time since the server booted.", snap.Uptime, id...)
		p.add("fleetdeck_cpu_usage_ratio", "CPU usage, 0-1.", snap.CPU, id...)
		p.add("fleetdeck_cpu_cores", "Number of CPU cores.", snap.Cores, id...)
		if len(snap.Load) > 0 {
			p.add("fleetdeck_load1", "1-minute load average.", &snap.Load[0], id...)
		}
		p.add("fleetdeck_memory_usage_ratio", "Memory usage, 0-1.", snap.Mem, id...)
		p.add("fleetdeck_memory_total_bytes", "Total memory.", &snap.MemTotal, id...)
		p.add("fleetdeck_swap_usage_ratio", "Swap usage, 0-1.", snap.Swap, id...)
		for _, d := range snap.Disks {
			p.add("fleetdeck_filesystem_size_bytes", "Size of the filesystem.", num(d.Size), append(id, "mount", d.Mount)...)
			p.add("fleetdeck_filesystem_used_bytes", "Used space on the filesystem.", num(d.Used), append(id, "mount", d.Mount)...)
			p.add("fleetdeck_filesystem_inode_usage_ratio", "Inode usage, 0-1.", d.InodePct, append(id, "mount", d.Mount)...)
		}
		p.add("fleetdeck_network_receive_bytes_per_second", "Network traffic in.", snap.Rx, id...)
		p.add("fleetdeck_network_transmit_bytes_per_second", "Network traffic out.", snap.Tx, id...)
		p.add("fleetdeck_disk_read_bytes_per_second", "Disk reads.", snap.Dr, id...)
		p.add("fleetdeck_disk_write_bytes_per_second", "Disk writes.", snap.Dw, id...)
		p.add("fleetdeck_temperature_celsius", "Highest temperature sensor.", snap.Temp, id...)
		p.add("fleetdeck_gpu_usage_ratio", "Usage of the busiest GPU, 0-1.", snap.GPU, id...)
		p.add("fleetdeck_updates_pending", "Package updates available.", snap.Updates, id...)
		p.add("fleetdeck_security_updates_pending", "Security updates available.", snap.Security, id...)
		p.add("fleetdeck_reboot_required", "1 when the server needs a reboot.", bit(snap.Reboot), id...)
		p.add("fleetdeck_failed_units", "Failed systemd units.", &snap.Failed, id...)
		if snap.Unhealthy != nil {
			p.add("fleetdeck_containers_unhealthy", "Unhealthy or restarting containers.", num(float64(*snap.Unhealthy)), id...)
		}
		for cid, v := range snap.Custom {
			if name, ok := customName[cid]; ok {
				p.add("fleetdeck_custom_metric", "A custom metric (Metrics panel of the server).", v.V, append(id, "metric", name)...)
			}
		}
	}
	return p.String(), nil
}
