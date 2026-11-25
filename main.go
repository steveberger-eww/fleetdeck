// Fleetdeck: agentless server dashboard. Beszel-style monitoring, Semaphore-style jobs, Cockpit-style admin.
package main

import (
	"cmp"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleetdeck/internal/alerts"
	"fleetdeck/internal/db"
	"fleetdeck/internal/jobs"
	"fleetdeck/internal/probe"
	"fleetdeck/internal/repos"
	"fleetdeck/internal/sec"
	"fleetdeck/internal/ssh"
	"fleetdeck/internal/web"
)

//go:embed public
var public embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fleetdeck:", err)
		os.Exit(1)
	}
}

func run() error {
	host := cmp.Or(os.Getenv("HOST"), "127.0.0.1")
	port, err := strconv.Atoi(cmp.Or(os.Getenv("PORT"), "8080"))
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("invalid PORT %q", os.Getenv("PORT"))
	}
	trustProxy := os.Getenv("FLEETDECK_TRUST_PROXY") == "1"

	if err := db.Open(cmp.Or(os.Getenv("DATA_DIR"), "data"), os.Getenv("DATABASE_URL")); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if err := db.ImportSQLite(); err != nil {
		return fmt.Errorf("import from SQLite: %w", err)
	}
	if err := sec.Init(); err != nil {
		return err
	}
	if err := repos.Init(); err != nil {
		return fmt.Errorf("repos: %w", err)
	}
	static, err := fs.Sub(public, "public")
	if err != nil {
		return err
	}
	srv := web.New(static, trustProxy)
	if tok := srv.SetupToken(); tok != "" {
		fmt.Printf("\n  First run: open Fleetdeck and enter this setup token:\n\n    %s\n\n", tok)
	}
	if !slices.Contains([]string{"127.0.0.1", "localhost", "::1"}, host) && !trustProxy {
		fmt.Fprintln(os.Stderr, "  Warning: listening on a public interface without FLEETDECK_TRUST_PROXY=1; put Fleetdeck behind HTTPS.")
	}
	if err := jobs.Start(); err != nil {
		return fmt.Errorf("jobs: %w", err)
	}
	probe.OnProbe(alerts.Evaluate)
	jobs.OnJobDone(alerts.OnJobDone)
	ssh.Notify = alerts.Notify
	probe.Start()
	go func() {
		for range time.Tick(time.Hour) {
			if _, err := db.Exec(`DELETE FROM sessions WHERE seen_at < ?`, db.Now()-web.SessionTTL.Milliseconds()); err != nil {
				log.Printf("session cleanup: %v", err)
			}
		}
	}()

	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	shown := host
	if strings.Contains(host, ":") {
		shown = "[" + host + "]"
	}
	fmt.Printf("  Fleetdeck on http://%s:%d\n", shown, port)
	return (&http.Server{Handler: srv, ReadHeaderTimeout: time.Minute}).Serve(ln)
}
