// Package db is Fleetdeck's storage: one SQLite file (pure Go driver, no cgo) plus small query helpers.
// The schema is shared with the earlier Node version, so existing data directories keep working.
package db

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

var (
	X       *sqlx.DB // the database; use the helpers below unless you need a transaction
	DataDir string   // absolute path of DATA_DIR
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS keys (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, public_key TEXT NOT NULL,
  private_enc TEXT NOT NULL, passphrase_enc TEXT, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 22,
  user TEXT NOT NULL DEFAULT 'root', key_id TEXT REFERENCES keys(id),
  tags TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', host_key TEXT,
  paused INTEGER NOT NULL DEFAULT 0, last_json TEXT, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS metrics (
  server_id TEXT NOT NULL, t INTEGER NOT NULL,
  cpu REAL, mem REAL, swap REAL, disk REAL, load1 REAL, rx REAL, tx REAL, dr REAL, dw REAL, temp REAL,
  PRIMARY KEY (server_id, t)) WITHOUT ROWID;

-- Keyed by name, not id: compose recreates containers with new ids.
CREATE TABLE IF NOT EXISTS container_metrics (
  server_id TEXT NOT NULL, name TEXT NOT NULL, t INTEGER NOT NULL, cpu REAL, mem REAL,
  PRIMARY KEY (server_id, name, t)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scripts (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', body TEXT NOT NULL,
  as_root INTEGER NOT NULL DEFAULT 1, params TEXT NOT NULL DEFAULT '[]', builtin INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, script_id TEXT, name TEXT NOT NULL, body TEXT NOT NULL,
  as_root INTEGER NOT NULL, params TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL, actor TEXT,
  created_at INTEGER NOT NULL, finished_at INTEGER);

CREATE TABLE IF NOT EXISTS job_runs (
  job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, server_id TEXT NOT NULL, server_name TEXT,
  status TEXT NOT NULL, exit_code INTEGER, output TEXT NOT NULL DEFAULT '', started_at INTEGER, finished_at INTEGER,
  PRIMARY KEY (job_id, server_id));

CREATE TABLE IF NOT EXISTS schedules (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, script_id TEXT NOT NULL REFERENCES scripts(id) ON DELETE CASCADE,
  cron TEXT NOT NULL, targets TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  last_run INTEGER, last_job INTEGER, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS alert_rules (
  id TEXT PRIMARY KEY, server_id TEXT REFERENCES servers(id) ON DELETE CASCADE, metric TEXT NOT NULL,
  threshold REAL NOT NULL DEFAULT 0, minutes INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS alert_state (
  rule_id TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  since INTEGER NOT NULL, message TEXT NOT NULL, PRIMARY KEY (rule_id, server_id));

CREATE TABLE IF NOT EXISTS notifiers (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, config_enc TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, t INTEGER NOT NULL, kind TEXT NOT NULL, level TEXT NOT NULL DEFAULT 'info',
  server_id TEXT, actor TEXT, ip TEXT, message TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_t ON events(t);

CREATE TABLE IF NOT EXISTS repos (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, branch TEXT NOT NULL DEFAULT 'main',
  auth TEXT NOT NULL DEFAULT 'none', key_id TEXT REFERENCES keys(id), username TEXT, token_enc TEXT,
  last_sync INTEGER, last_commit TEXT, last_error TEXT, created_at INTEGER NOT NULL);

-- Quiet hours: alerts keep their state but don't notify. daily: minutes after local midnight; once: epoch ms.
CREATE TABLE IF NOT EXISTS mute_windows (
  id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', server_id TEXT REFERENCES servers(id) ON DELETE CASCADE, tag TEXT,
  kind TEXT NOT NULL, start_at INTEGER NOT NULL, end_at INTEGER NOT NULL, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS triggers (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, script_id TEXT NOT NULL REFERENCES scripts(id) ON DELETE CASCADE,
  targets TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', token_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1,
  last_run INTEGER, last_job INTEGER, created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS variables (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, value_enc TEXT NOT NULL, secret INTEGER NOT NULL DEFAULT 1,
  description TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS sessions (
  hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, seen_at INTEGER NOT NULL, ip TEXT, ua TEXT);
`

// Additive migrations for databases created by earlier versions.
var migrations = [][3]string{
	{"servers", "host_key_raw", "TEXT"}, // full key for known_hosts (Ansible)
	{"servers", "sudo_enc", "TEXT"},     // encrypted sudo password, for users without NOPASSWD
	{"scripts", "kind", "TEXT NOT NULL DEFAULT 'shell'"},
	{"scripts", "repo_id", "TEXT REFERENCES repos(id)"},
	{"scripts", "playbook", "TEXT"},
	{"jobs", "kind", "TEXT NOT NULL DEFAULT 'shell'"},
	{"jobs", "repo_id", "TEXT"},
	{"jobs", "options", "TEXT NOT NULL DEFAULT '{}'"},
	{"jobs", "commit_sha", "TEXT"},
	{"alert_rules", "tag", "TEXT"},                            // rule applies to servers with this tag
	{"alert_state", "notified", "INTEGER NOT NULL DEFAULT 1"}, // 0 = fired during quiet hours, not sent yet
	{"scripts", "vars", "TEXT NOT NULL DEFAULT '[]'"},         // names of shared variables the script receives
}

// Open creates dir (0700) and opens or creates fleetdeck.db in it.
func Open(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	dsn := "file:" + filepath.Join(abs, "fleetdeck.db") +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	x, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	if _, err := x.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	for _, m := range migrations {
		var n int
		if err := x.Get(&n, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, m[0], m[1]); err != nil {
			return err
		}
		if n == 0 {
			if _, err := x.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m[0], m[1], m[2])); err != nil {
				return fmt.Errorf("migrate %s.%s: %w", m[0], m[1], err)
			}
		}
	}
	X, DataDir = x, abs
	return nil
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
	if err := X.Get(&v, q, args...); err != nil {
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
	if err := X.Select(&v, q, args...); err != nil {
		return nil, err
	}
	return v, nil
}

// Row is a result row as column -> value (int64, float64, string or nil), for rows passed through as JSON as-is.
type Row = map[string]any

// Rows returns all rows as maps.
func Rows(q string, args ...any) ([]Row, error) {
	rs, err := X.Queryx(q, args...)
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
func Exec(q string, args ...any) (sql.Result, error) { return X.Exec(q, args...) }

// Tx runs fn in a transaction; it commits when fn returns nil and rolls back otherwise.
func Tx(fn func(tx *sqlx.Tx) error) error {
	tx, err := X.Beginx()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Setting returns a settings value, or fallback when it is unset.
func Setting(key, fallback string) string {
	var v sql.NullString
	if err := X.Get(&v, `SELECT value FROM settings WHERE key = ?`, key); err != nil || !v.Valid {
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
		_, err := X.Exec(`DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := X.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, fmt.Sprint(value))
	return err
}

// EventOpts are the optional columns of an event. Empty strings are stored as NULL.
type EventOpts struct {
	Level    string // info (default), warn, error
	ServerID string
	Actor    string
	IP       string
}

// Event appends to the audit/event log. Failures are logged, never returned: logging must not break the action.
func Event(kind, message string, o EventOpts) {
	if o.Level == "" {
		o.Level = "info"
	}
	if _, err := X.Exec(`INSERT INTO events (t, kind, level, server_id, actor, ip, message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		Now(), kind, o.Level, nullStr(o.ServerID), nullStr(o.Actor), nullStr(o.IP), message); err != nil {
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
