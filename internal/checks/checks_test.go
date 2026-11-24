package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fleetdeck/internal/db"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.OpenTest(dir); err != nil {
		panic(err)
	}
	Blocked = func(net.IP) bool { return false } // httptest listens on loopback
	code := m.Run()
	db.DropTest()
	os.RemoveAll(dir)
	os.Exit(code)
}

// tlsServer serves "hello fleet" with a certificate for 127.0.0.1 that expires in days.
func tlsServer(t *testing.T, days int) *httptest.Server {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(days) * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	Roots = x509.NewCertPool()
	Roots.AddCert(cert)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("hello fleet"))
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestRun(t *testing.T) {
	ts := tlsServer(t, 10)
	ctx := context.Background()
	r := Run(ctx, &Check{Kind: "http", Target: ts.URL, Contains: "fleet"})
	if !r.OK || r.TLSDays == nil || *r.TLSDays < 9.9 || *r.TLSDays > 10 {
		t.Fatalf("%+v", r)
	}
	if r := Run(ctx, &Check{Kind: "http", Target: ts.URL, Contains: "nope"}); r.OK || !strings.Contains(r.Error, "doesn't contain") {
		t.Errorf("%+v", r)
	}
	if r := Run(ctx, &Check{Kind: "http", Target: ts.URL + "/missing"}); r.OK || r.Error != "HTTP 404" {
		t.Errorf("%+v", r)
	}
	if r := Run(ctx, &Check{Kind: "http", Target: ts.URL + "/missing", ExpectStatus: new(404)}); !r.OK {
		t.Errorf("%+v", r)
	}
	Roots = x509.NewCertPool() // untrusted: fails, but the expiry is still known
	if r := Run(ctx, &Check{Kind: "http", Target: ts.URL}); r.OK || r.TLSDays == nil {
		t.Errorf("untrusted: %+v", r)
	}

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	if r := Run(ctx, &Check{Kind: "tcp", Target: addr}); !r.OK {
		t.Errorf("tcp: %+v", r)
	}
	l.Close() // a stopped service
	if r := Run(ctx, &Check{Kind: "tcp", Target: addr}); r.OK || !strings.Contains(r.Error, "refused") {
		t.Errorf("stopped: %+v", r)
	}
	if r := Run(ctx, &Check{Kind: "http", Target: "http://" + addr}); r.OK {
		t.Errorf("stopped http: %+v", r)
	}

	Blocked = func(ip net.IP) bool { return ip.IsLoopback() }
	defer func() { Blocked = func(net.IP) bool { return false } }()
	if r := Run(ctx, &Check{Kind: "http", Target: ts.URL}); r.OK || r.Error != errBlocked.Error() {
		t.Errorf("blocked: %+v", r)
	}
}

func TestSave(t *testing.T) {
	if _, err := db.Exec(`INSERT INTO servers (id, name, host, created_at) VALUES ('s', 's', 'h', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO checks (id, server_id, name, kind, target, created_at) VALUES ('c', 's', 'c', 'tcp', 'h:1', 0)`); err != nil {
		t.Fatal(err)
	}
	c := &Check{ID: "c"}
	since := func() *int64 {
		t.Helper()
		x, _ := db.Get[Check](`SELECT * FROM checks WHERE id = 'c'`)
		return x.FailingSince
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Save(c, Result{OK: false, Error: "down"}))
	first := since()
	time.Sleep(5 * time.Millisecond)
	must(Save(c, Result{OK: false, Error: "down"}))
	if first == nil || *since() != *first { // a run of failures keeps its start
		t.Fatal(first, since())
	}
	time.Sleep(5 * time.Millisecond)
	must(Save(c, Result{OK: true}))
	if since() != nil {
		t.Fatal("still failing after a success")
	}
	if up, err := Uptime("c", 0); err != nil || up == nil || *up < 0.33 || *up > 0.34 {
		t.Fatal(up, err)
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []map[string]any{{"kind": "http", "target": "ftp://x"}, {"kind": "http", "target": "https://u:p@x"}, {"kind": "tcp", "target": "x"},
		{"kind": "tcp", "target": "x:99999"}, {"kind": "icmp", "target": "x"}, {"kind": "http", "target": "https://x", "expect_status": 42.0}} {
		if _, err := Validate(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	c, err := Validate(map[string]any{"kind": "http", "target": " https://example.com/health ", "expect_status": 204.0})
	if err != nil || c.Name != "https://example.com/health" || *c.ExpectStatus != 204 || !c.Enabled {
		t.Fatal(c, err)
	}
}
