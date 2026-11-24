// Package db is Fleetdeck's storage: Postgres (pgx) plus small query helpers.
// Queries are written with ? placeholders; the helpers rebind them to $1, $2, ...
package db

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

var (
	X       *sqlx.DB // the database; use the helpers below unless you need a transaction
	DataDir string   // absolute path of DATA_DIR (secret.key, git checkouts, temp files)
)

// DefaultTenant is the organization a fresh install (or the SQLite import) starts with.
const DefaultTenant = "default"

// Booleans are INTEGER 0/1 and timestamps BIGINT epoch ms, as in the SQLite days, so the Go side didn't change.
// Everything an organization owns has a tenant_id; per-server data (metrics, runs, alert state) is scoped through
// its server or job. Every query in web/ filters on it. scripts.tenant_id NULL = the shared library (built-ins);
// events.tenant_id NULL = instance events (sign-ins, setup).
// ponytail: tenancy is enforced in the queries, not with row-level security: the background workers (probe, jobs,
// alerts) work across tenants and would need a bypass anyway. Add RLS if queries ever get written by hand outside web/.
// Additive changes go at the end as ALTER TABLE ... ADD COLUMN IF NOT EXISTS.
const schema = `
CREATE TABLE IF NOT EXISTS tenants (id TEXT PRIMARY KEY, name TEXT NOT NULL, slug TEXT NOT NULL UNIQUE, created_at BIGINT NOT NULL);
INSERT INTO tenants (id, name, slug, created_at) VALUES ('default', 'Default', 'default', 0) ON CONFLICT DO NOTHING;

-- Instance settings (key_check, public_url, instance single sign-on, retention).
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);
-- Organization settings (notify_jobs, the organization's own single sign-on).
CREATE TABLE IF NOT EXISTS tenant_settings (
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, key TEXT NOT NULL, value TEXT, PRIMARY KEY (tenant_id, key));

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, email TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', password_hash TEXT,
  password_login INTEGER NOT NULL DEFAULT 1, -- 0 = single sign-on only (once an identity is connected)
  totp_secret TEXT, totp_last BIGINT NOT NULL DEFAULT -1, super_admin INTEGER NOT NULL DEFAULT 0,
  last_tenant TEXT, created_at BIGINT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS users_email ON users (lower(email));

-- role: admin (everything) | owner (their servers, can share them) | operator (works on assigned servers) | viewer
CREATE TABLE IF NOT EXISTS memberships (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  role TEXT NOT NULL, created_at BIGINT NOT NULL, PRIMARY KEY (user_id, tenant_id));

-- provider: 'instance' or 'tenant:<id>'; subject: the provider's sub claim.
CREATE TABLE IF NOT EXISTS identities (
  provider TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, PRIMARY KEY (provider, subject));

CREATE TABLE IF NOT EXISTS invites (
  token_hash TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email TEXT NOT NULL, role TEXT NOT NULL, created_by TEXT, expires_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS keys (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, public_key TEXT NOT NULL,
  private_enc TEXT NOT NULL, passphrase_enc TEXT, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS repos (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, url TEXT NOT NULL, branch TEXT NOT NULL DEFAULT 'main',
  auth TEXT NOT NULL DEFAULT 'none', key_id TEXT REFERENCES keys(id), username TEXT, token_enc TEXT,
  last_sync BIGINT, last_commit TEXT, last_error TEXT, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 22,
  "user" TEXT NOT NULL DEFAULT 'root', key_id TEXT REFERENCES keys(id),
  tags TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', host_key TEXT,
  paused INTEGER NOT NULL DEFAULT 0, last_json TEXT, created_at BIGINT NOT NULL,
  host_key_raw TEXT, -- full key for known_hosts (Ansible)
  sudo_enc TEXT);    -- encrypted sudo password, for users without NOPASSWD

CREATE TABLE IF NOT EXISTS metrics (
  server_id TEXT NOT NULL, t BIGINT NOT NULL,
  cpu DOUBLE PRECISION, mem DOUBLE PRECISION, swap DOUBLE PRECISION, disk DOUBLE PRECISION, load1 DOUBLE PRECISION,
  rx DOUBLE PRECISION, tx DOUBLE PRECISION, dr DOUBLE PRECISION, dw DOUBLE PRECISION, temp DOUBLE PRECISION,
  gpu DOUBLE PRECISION,     -- busiest GPU, 0-1
  gpu_mem DOUBLE PRECISION, -- its memory used, 0-1
  PRIMARY KEY (server_id, t));
CREATE INDEX IF NOT EXISTS metrics_t ON metrics(t);

-- Keyed by name, not id: compose recreates containers with new ids.
CREATE TABLE IF NOT EXISTS container_metrics (
  server_id TEXT NOT NULL, name TEXT NOT NULL, t BIGINT NOT NULL, cpu DOUBLE PRECISION, mem DOUBLE PRECISION,
  PRIMARY KEY (server_id, name, t));

CREATE TABLE IF NOT EXISTS scripts (
  id TEXT PRIMARY KEY, tenant_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', body TEXT NOT NULL,
  as_root INTEGER NOT NULL DEFAULT 1, params TEXT NOT NULL DEFAULT '[]', builtin INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL, kind TEXT NOT NULL DEFAULT 'shell', repo_id TEXT REFERENCES repos(id), playbook TEXT,
  vars TEXT NOT NULL DEFAULT '[]'); -- names of shared variables the script receives

CREATE TABLE IF NOT EXISTS jobs (
  id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  script_id TEXT, name TEXT NOT NULL, body TEXT NOT NULL,
  as_root INTEGER NOT NULL, params TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL, actor TEXT,
  created_at BIGINT NOT NULL, finished_at BIGINT,
  kind TEXT NOT NULL DEFAULT 'shell', repo_id TEXT, options TEXT NOT NULL DEFAULT '{}', commit_sha TEXT);

CREATE TABLE IF NOT EXISTS job_runs (
  job_id BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, server_id TEXT NOT NULL, server_name TEXT,
  status TEXT NOT NULL, exit_code INTEGER, output TEXT NOT NULL DEFAULT '', started_at BIGINT, finished_at BIGINT,
  PRIMARY KEY (job_id, server_id));

CREATE TABLE IF NOT EXISTS schedules (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, script_id TEXT NOT NULL REFERENCES scripts(id) ON DELETE CASCADE,
  cron TEXT NOT NULL, targets TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  last_run BIGINT, last_job BIGINT, created_at BIGINT NOT NULL,
  once INTEGER NOT NULL DEFAULT 0); -- one-off run: deleted after it fires

CREATE TABLE IF NOT EXISTS custom_metrics (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, unit TEXT NOT NULL DEFAULT '', command TEXT NOT NULL,
  server_id TEXT REFERENCES servers(id) ON DELETE CASCADE, tag TEXT, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS custom_values (
  server_id TEXT NOT NULL, metric_id TEXT NOT NULL, t BIGINT NOT NULL, v DOUBLE PRECISION,
  PRIMARY KEY (server_id, metric_id, t));

CREATE TABLE IF NOT EXISTS alert_rules (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  server_id TEXT REFERENCES servers(id) ON DELETE CASCADE, metric TEXT NOT NULL,
  threshold DOUBLE PRECISION NOT NULL DEFAULT 0, minutes INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
  created_at BIGINT NOT NULL,
  tag TEXT,   -- rule applies to servers with this tag
  mount TEXT, -- disk/inodes rules: watch this mount only
  custom_id TEXT REFERENCES custom_metrics(id) ON DELETE CASCADE); -- metric 'custom'

CREATE TABLE IF NOT EXISTS alert_state (
  rule_id TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  since BIGINT NOT NULL, message TEXT NOT NULL,
  notified INTEGER NOT NULL DEFAULT 1, -- 0 = fired during quiet hours, not sent yet
  PRIMARY KEY (rule_id, server_id));

CREATE TABLE IF NOT EXISTS notifiers (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, type TEXT NOT NULL, config_enc TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS events (
  id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, tenant_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
  t BIGINT NOT NULL, kind TEXT NOT NULL, level TEXT NOT NULL DEFAULT 'info',
  server_id TEXT, actor TEXT, ip TEXT, message TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_t ON events(t);

CREATE INDEX IF NOT EXISTS events_tenant ON events(tenant_id, id);

-- Who sees a server besides the organization's admins.
CREATE TABLE IF NOT EXISTS server_members (
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (server_id, user_id));

-- Quiet hours: alerts keep their state but don't notify. daily: minutes after local midnight; once: epoch ms.
CREATE TABLE IF NOT EXISTS mute_windows (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '', server_id TEXT REFERENCES servers(id) ON DELETE CASCADE, tag TEXT,
  kind TEXT NOT NULL, start_at BIGINT NOT NULL, end_at BIGINT NOT NULL, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS triggers (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, script_id TEXT NOT NULL REFERENCES scripts(id) ON DELETE CASCADE,
  targets TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', token_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1,
  last_run BIGINT, last_job BIGINT, created_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS variables (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, value_enc TEXT NOT NULL, secret INTEGER NOT NULL DEFAULT 1,
  description TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, UNIQUE (tenant_id, name));

-- tenant_id: the organization the session works in. locked = signed in with that organization's own single
-- sign-on: the session stays in it and has no super-admin powers.
CREATE TABLE IF NOT EXISTS sessions (
  hash TEXT PRIMARY KEY, created_at BIGINT NOT NULL, seen_at BIGINT NOT NULL, ip TEXT, ua TEXT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, tenant_id TEXT REFERENCES tenants(id) ON DELETE SET NULL,
  locked INTEGER NOT NULL DEFAULT 0);

-- Script chains: after a job of this script, run next_script_id on the same servers.
-- next_when: success | failure | always; next_ok_only = 1: only on the servers where this step succeeded.
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS next_script_id TEXT REFERENCES scripts(id) ON DELETE SET NULL;
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS next_when TEXT NOT NULL DEFAULT 'success';
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS next_ok_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS parent_job BIGINT; -- the chain step that started this job
CREATE INDEX IF NOT EXISTS jobs_parent ON jobs(parent_job);

-- Endpoint checks, run from the Fleetdeck host every minute; listed in their server's Network tab.
-- last_*: the latest result; failing_since: when the current run of failures began (NULL while it passes).
CREATE TABLE IF NOT EXISTS checks (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL, -- http: URL | tcp: host:port
  expect_status INTEGER, contains TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at BIGINT NOT NULL,
  last_t BIGINT, last_ok INTEGER, last_ms BIGINT, last_error TEXT NOT NULL DEFAULT '', failing_since BIGINT, tls_days DOUBLE PRECISION);
CREATE TABLE IF NOT EXISTS check_results (
  check_id TEXT NOT NULL REFERENCES checks(id) ON DELETE CASCADE, t BIGINT NOT NULL, ok INTEGER NOT NULL, ms BIGINT,
  PRIMARY KEY (check_id, t));
CREATE INDEX IF NOT EXISTS check_results_t ON check_results(t);

-- Passkeys (WebAuthn). id: base64url credential id; credential: the verified credential as JSON (public key, sign count).
CREATE TABLE IF NOT EXISTS passkeys (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL, credential TEXT NOT NULL, created_at BIGINT NOT NULL, last_used BIGINT);
`

// Open opens the Postgres database at url and creates or updates the schema. dataDir (created 0700) holds the files.
func Open(dataDir, url string) error {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	if url == "" {
		return errors.New("DATABASE_URL is not set (e.g. postgres://fleetdeck:fleetdeck@localhost:5432/fleetdeck?sslmode=disable)")
	}
	x, err := sqlx.Open("pgx", url)
	if err != nil {
		return err
	}
	if _, err := x.Exec(schema); err != nil {
		x.Close()
		return fmt.Errorf("schema: %w", err)
	}
	if X != nil {
		X.Close()
	}
	X, DataDir = x, abs
	return nil
}

var testSchema string

// OpenTest opens a fresh, empty schema in FLEETDECK_TEST_DATABASE_URL (default: the compose.yaml database).
// DropTest removes it again.
func OpenTest(dataDir string) error {
	DropTest()
	url := os.Getenv("FLEETDECK_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://fleetdeck:fleetdeck@localhost:5432/fleetdeck?sslmode=disable"
	}
	name := "test_" + strings.ReplaceAll(NewID(), "-", "")
	admin, err := sqlx.Open("pgx", url)
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE SCHEMA " + name); err != nil {
		return err
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	testSchema = name
	if err := Open(dataDir, url+sep+"search_path="+name); err != nil {
		return err
	}
	// Test fixtures leave tenant_id out: in tests it defaults to the default organization.
	for _, t := range []string{"keys", "repos", "servers", "jobs", "schedules", "triggers", "variables", "custom_metrics", "alert_rules", "notifiers", "mute_windows", "checks"} {
		if _, err := X.Exec("ALTER TABLE " + t + " ALTER COLUMN tenant_id SET DEFAULT '" + DefaultTenant + "'"); err != nil {
			return err
		}
	}
	return nil
}

// DropTest drops the schema OpenTest made, if any.
func DropTest() {
	if testSchema == "" || X == nil {
		return
	}
	if _, err := X.Exec("DROP SCHEMA " + testSchema + " CASCADE"); err != nil {
		log.Printf("drop test schema: %v", err)
	}
	testSchema = ""
}

// Now is the current time in epoch milliseconds, the unit every timestamp column uses.
func Now() int64 { return time.Now().UnixMilli() }

// NewID returns a random UUIDv4, the format of every TEXT id.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Get scans one row into a T (struct with `db` tags, or a scalar). Returns nil, nil when there is no row.
func Get[T any](q string, args ...any) (*T, error) {
	var v T
	if err := X.Get(&v, X.Rebind(q), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

// Select scans all rows into a slice of T. Never returns a nil slice (so it encodes as [] in JSON).
func Select[T any](q string, args ...any) ([]T, error) {
	v := []T{}
	if err := X.Select(&v, X.Rebind(q), args...); err != nil {
		return nil, err
	}
	return v, nil
}

// Row is a result row as column -> value (int64, float64, string or nil), for rows passed through as JSON as-is.
type Row = map[string]any

// Rows returns all rows as maps.
func Rows(q string, args ...any) ([]Row, error) {
	rs, err := X.Queryx(X.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []Row{}
	for rs.Next() {
		r := Row{}
		if err := rs.MapScan(r); err != nil {
			return nil, err
		}
		for k, v := range r {
			if b, ok := v.([]byte); ok {
				r[k] = string(b)
			}
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Exec runs a statement.
func Exec(q string, args ...any) (sql.Result, error) { return X.Exec(X.Rebind(q), args...) }

// Txn is a transaction whose Exec and Get take ? placeholders like the helpers above.
type Txn struct{ *sqlx.Tx }

func (t Txn) Exec(q string, args ...any) (sql.Result, error) { return t.Tx.Exec(t.Rebind(q), args...) }
func (t Txn) Get(dest any, q string, args ...any) error      { return t.Tx.Get(dest, t.Rebind(q), args...) }

// Tx runs fn in a transaction; it commits when fn returns nil and rolls back otherwise.
func Tx(fn func(tx Txn) error) error {
	tx, err := X.Beginx()
	if err != nil {
		return err
	}
	if err := fn(Txn{tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Setting returns a settings value, or fallback when it is unset.
func Setting(key, fallback string) string {
	var v sql.NullString
	if err := X.Get(&v, `SELECT value FROM settings WHERE key = $1`, key); err != nil || !v.Valid {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("setting %s: %v", key, err)
		}
		return fallback
	}
	return v.String
}

// SetSetting stores a value; nil deletes the key. Non-string values are stored with fmt's %v.
func SetSetting(key string, value any) error {
	if value == nil {
		_, err := X.Exec(`DELETE FROM settings WHERE key = $1`, key)
		return err
	}
	_, err := X.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, fmt.Sprint(value))
	return err
}

// TenantSetting returns an organization's setting, or fallback when it is unset.
func TenantSetting(tenant, key, fallback string) string {
	v, err := Get[sql.NullString](`SELECT value FROM tenant_settings WHERE tenant_id = ? AND key = ?`, tenant, key)
	if err != nil {
		log.Printf("tenant setting %s: %v", key, err)
	}
	if v == nil || !v.Valid {
		return fallback
	}
	return v.String
}

// SetTenantSetting stores an organization's setting; nil deletes it.
func SetTenantSetting(tenant, key string, value any) error {
	if value == nil {
		_, err := Exec(`DELETE FROM tenant_settings WHERE tenant_id = ? AND key = ?`, tenant, key)
		return err
	}
	_, err := Exec(`INSERT INTO tenant_settings (tenant_id, key, value) VALUES (?, ?, ?) ON CONFLICT (tenant_id, key) DO UPDATE SET value = excluded.value`,
		tenant, key, fmt.Sprint(value))
	return err
}

// EventOpts are the optional columns of an event. Empty strings are stored as NULL.
type EventOpts struct {
	Level    string // info (default), warn, error
	TenantID string // "" = the server's organization, or an instance event when there is no server
	ServerID string
	Actor    string
	IP       string
}

// Event appends to the audit/event log. Failures are logged, never returned: logging must not break the action.
func Event(kind, message string, o EventOpts) {
	if o.Level == "" {
		o.Level = "info"
	}
	if _, err := Exec(`INSERT INTO events (t, kind, level, tenant_id, server_id, actor, ip, message)
    VALUES (?, ?, ?, COALESCE(?, (SELECT tenant_id FROM servers WHERE id = ?)), ?, ?, ?, ?)`,
		Now(), kind, o.Level, nullStr(o.TenantID), o.ServerID, nullStr(o.ServerID), nullStr(o.Actor), nullStr(o.IP), message); err != nil {
		log.Printf("event %s: %v (%s)", kind, err, message)
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Server is a row of the servers table. JSON tags match the column names.
type Server struct {
	ID         string  `db:"id" json:"id"`
	TenantID   string  `db:"tenant_id" json:"-"`
	Name       string  `db:"name" json:"name"`
	Host       string  `db:"host" json:"host"`
	Port       int     `db:"port" json:"port"`
	User       string  `db:"user" json:"user"`
	KeyID      *string `db:"key_id" json:"key_id"`
	Tags       string  `db:"tags" json:"tags"`
	Notes      string  `db:"notes" json:"notes"`
	HostKey    *string `db:"host_key" json:"host_key"`
	Paused     int     `db:"paused" json:"paused"`
	LastJSON   *string `db:"last_json" json:"last_json"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	HostKeyRaw *string `db:"host_key_raw" json:"host_key_raw"`
	SudoEnc    *string `db:"sudo_enc" json:"-"`
}

// GetServer loads a server by id (nil, nil if missing).
func GetServer(id string) (*Server, error) {
	return Get[Server](`SELECT * FROM servers WHERE id = ?`, id)
}

// Str dereferences a nullable string column ("" for NULL).
func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
