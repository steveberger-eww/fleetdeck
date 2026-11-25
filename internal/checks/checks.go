// Package checks: HTTP(S) and TCP endpoint checks, run from the Fleetdeck host every minute.
// Each check belongs to a server (its Network tab); alerts read their state for the check and tls_days rules.
package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
)

const (
	Timeout  = 10 * time.Second
	parallel = 16
	bodyCap  = 1 << 20 // "must contain" searches the first MB
)

// Check is a row of the checks table; Last* hold the latest result.
type Check struct {
	ID           string   `db:"id" json:"id"`
	TenantID     string   `db:"tenant_id" json:"-"`
	ServerID     string   `db:"server_id" json:"server_id"`
	Name         string   `db:"name" json:"name"`
	Kind         string   `db:"kind" json:"kind"`     // http | tcp
	Target       string   `db:"target" json:"target"` // URL, or host:port
	ExpectStatus *int     `db:"expect_status" json:"expect_status"`
	Contains     string   `db:"contains" json:"contains"`
	Enabled      bool     `db:"enabled" json:"enabled"`
	CreatedAt    int64    `db:"created_at" json:"created_at"`
	LastT        *int64   `db:"last_t" json:"last_t"`
	LastOK       *bool    `db:"last_ok" json:"last_ok"`
	LastMS       *int64   `db:"last_ms" json:"last_ms"`
	LastError    string   `db:"last_error" json:"last_error"`
	FailingSince *int64   `db:"failing_since" json:"failing_since"`
	TLSDays      *float64 `db:"tls_days" json:"tls_days"` // days the certificate has left (HTTPS only)
}

// Validate checks a check from a request body. It fills Kind, Target, Name, ExpectStatus, Contains and Enabled.
func Validate(b map[string]any) (*Check, error) {
	str := func(k string) string { s, _ := b[k].(string); return strings.TrimSpace(s) }
	c := &Check{Kind: str("kind"), Target: str("target"), Name: str("name"), Contains: str("contains"), Enabled: b["enabled"] != false}
	switch c.Kind {
	case "http":
		u, err := url.Parse(c.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, httperr.Bad("The URL must start with http:// or https://")
		}
		if v, ok := b["expect_status"].(float64); ok && v != 0 {
			if v < 100 || v > 599 || v != math.Trunc(v) {
				return nil, httperr.Bad("Expected status must be 100-599")
			}
			c.ExpectStatus = new(int(v))
		}
	case "tcp":
		host, port, err := net.SplitHostPort(c.Target)
		if p, _ := strconv.Atoi(port); err != nil || host == "" || p < 1 || p > 65535 {
			return nil, httperr.Bad("TCP checks need host:port, e.g. db.example.com:5432")
		}
		c.Contains = ""
	default:
		return nil, httperr.Bad("Kind must be http or tcp")
	}
	if len(c.Target) > 2000 || len(c.Contains) > 500 || len([]rune(c.Name)) > 100 {
		return nil, httperr.Bad("Name, URL or text is too long")
	}
	if c.Name == "" {
		c.Name = c.Target
	}
	return c, nil
}

// Result is the outcome of one run.
type Result struct {
	OK      bool
	MS      int64
	Error   string
	TLSDays *float64
}

// Blocked reports addresses no check may connect to: the Fleetdeck host itself and link-local ones (cloud
// metadata). Private networks stay allowed for homelabs. A variable so tests can reach httptest servers.
// ponytail: an organization can still probe the Fleetdeck host's LAN (and read it back through "must contain");
// add a per-instance allowlist if organizations ever don't trust the instance's network.
var Blocked = func(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// Roots verifies HTTPS certificates; nil = the system pool. A variable for tests.
var Roots *x509.CertPool

var errBlocked = errors.New("connecting to this address is not allowed")

func dialer() *net.Dialer {
	return &net.Dialer{Timeout: Timeout, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || Blocked(ip) {
			return errBlocked
		}
		return nil
	}}
}

// Run performs a check once.
func Run(ctx context.Context, c *Check) Result {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := time.Now()
	r := Result{}
	err := func() error {
		if c.Kind == "tcp" {
			conn, err := dialer().DialContext(ctx, "tcp", c.Target)
			if err == nil {
				conn.Close()
			}
			return err
		}
		var notAfter time.Time
		// Verified by hand so the expiry is known even when the certificate is invalid (the crypto/tls example).
		tc := &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
			certs := cs.PeerCertificates
			notAfter = certs[0].NotAfter
			opts := x509.VerifyOptions{DNSName: cs.ServerName, Roots: Roots, Intermediates: x509.NewCertPool()}
			for _, x := range certs[1:] {
				opts.Intermediates.AddCert(x)
			}
			_, err := certs[0].Verify(opts)
			return err
		}}
		client := &http.Client{
			Transport:     &http.Transport{DialContext: dialer().DialContext, TLSClientConfig: tc, DisableKeepAlives: true, Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // check this URL, not where it points
		}
		req, err := http.NewRequestWithContext(ctx, "GET", c.Target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "Fleetdeck endpoint check")
		resp, err := client.Do(req)
		if !notAfter.IsZero() {
			r.TLSDays = new(time.Until(notAfter).Hours() / 24)
		}
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if c.ExpectStatus != nil && resp.StatusCode != *c.ExpectStatus {
			return fmt.Errorf("HTTP %d, expected %d", resp.StatusCode, *c.ExpectStatus)
		}
		if c.ExpectStatus == nil && resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if c.Contains != "" {
			body, err := io.ReadAll(io.LimitReader(resp.Body, bodyCap))
			if err != nil {
				return err
			}
			if !strings.Contains(string(body), c.Contains) {
				return fmt.Errorf("the page doesn't contain %q", c.Contains)
			}
		}
		return nil
	}()
	r.MS, r.OK = time.Since(start).Milliseconds(), err == nil
	if err != nil {
		r.Error = cleanErr(err)
	}
	return r
}

// cleanErr drops the Go prefixes ("Get \"https://…\": dial tcp 1.2.3.4:443: ") nobody needs to read.
func cleanErr(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return fmt.Sprintf("no answer within %d s", int(Timeout.Seconds()))
	}
	if errors.Is(err, errBlocked) {
		return errBlocked.Error()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

// Save stores a result: the latest state on the check, and a row in check_results for history.
func Save(c *Check, r Result) error {
	now, ok := db.Now(), 0
	if r.OK {
		ok = 1
	}
	if _, err := db.Exec(`UPDATE checks SET last_t = ?, last_ok = ?, last_ms = ?, last_error = ?, tls_days = ?,
    failing_since = CASE WHEN ? THEN NULL ELSE COALESCE(failing_since, ?) END WHERE id = ?`,
		now, ok, r.MS, r.Error, r.TLSDays, r.OK, now, c.ID); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO check_results (check_id, t, ok, ms) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, c.ID, now, ok, r.MS)
	return err
}

// RunAndSave runs a check now and stores the result.
func RunAndSave(ctx context.Context, c *Check) error { return Save(c, Run(ctx, c)) }

func tick() {
	list, err := db.Select[Check](`SELECT c.* FROM checks c JOIN servers s ON s.id = c.server_id WHERE c.enabled = 1 AND s.paused = 0`)
	if err != nil {
		log.Printf("checks: %v", err)
		return
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := range list {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := RunAndSave(context.Background(), &list[i]); err != nil {
				log.Printf("check %s: %v", list[i].Target, err)
			}
		})
	}
	wg.Wait()
}

// Start runs every enabled check once a minute.
func Start() {
	go func() {
		for {
			tick()
			time.Sleep(time.Minute - time.Duration(time.Now().UnixMilli()%60_000)*time.Millisecond)
		}
	}()
}

// Uptime is the share of successful runs since t (nil without runs).
func Uptime(id string, since int64) (*float64, error) {
	v, err := db.Get[*float64](`SELECT AVG(ok)::float8 FROM check_results WHERE check_id = ? AND t >= ?`, id, since)
	if err != nil || v == nil {
		return nil, err
	}
	return *v, nil
}
