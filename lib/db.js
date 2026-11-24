'use strict';
const { DatabaseSync } = require('node:sqlite');
const path = require('node:path');
const fs = require('node:fs');

const DATA = path.resolve(process.env.DATA_DIR || path.join(__dirname, '..', 'data'));
fs.mkdirSync(DATA, { recursive: true, mode: 0o700 });

const db = new DatabaseSync(path.join(DATA, 'fleetdeck.db'));
db.exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

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
`);

// Additive migrations for databases created by earlier versions.
function addColumn(table, col, def) {
  if (!db.prepare(`PRAGMA table_info(${table})`).all().some((c) => c.name === col)) db.exec(`ALTER TABLE ${table} ADD COLUMN ${col} ${def}`);
}
addColumn('servers', 'host_key_raw', 'TEXT'); // full key for known_hosts (Ansible)
addColumn('servers', 'sudo_enc', 'TEXT'); // encrypted sudo password, for users without NOPASSWD
addColumn('scripts', 'kind', "TEXT NOT NULL DEFAULT 'shell'");
addColumn('scripts', 'repo_id', 'TEXT REFERENCES repos(id)');
addColumn('scripts', 'playbook', 'TEXT');
addColumn('jobs', 'kind', "TEXT NOT NULL DEFAULT 'shell'");
addColumn('jobs', 'repo_id', 'TEXT');
addColumn('jobs', 'options', "TEXT NOT NULL DEFAULT '{}'");
addColumn('jobs', 'commit_sha', 'TEXT');
addColumn('alert_rules', 'tag', 'TEXT'); // rule applies to servers with this tag
addColumn('alert_state', 'notified', 'INTEGER NOT NULL DEFAULT 1'); // 0 = fired during quiet hours, not sent yet
addColumn('scripts', 'vars', "TEXT NOT NULL DEFAULT '[]'"); // names of shared variables the script receives

const cache = new Map();
const stmt = (sql) => {
  if (!cache.has(sql)) cache.set(sql, db.prepare(sql));
  return cache.get(sql);
};
const all = (sql, ...p) => stmt(sql).all(...p);
const get = (sql, ...p) => stmt(sql).get(...p);
const run = (sql, ...p) => stmt(sql).run(...p);

function tx(fn) {
  db.exec('BEGIN');
  try { const r = fn(); db.exec('COMMIT'); return r; } catch (e) { db.exec('ROLLBACK'); throw e; }
}

const setting = (key, fallback = null) => get('SELECT value FROM settings WHERE key = ?', key)?.value ?? fallback;
const setSetting = (key, value) => value == null
  ? run('DELETE FROM settings WHERE key = ?', key)
  : run('INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value', key, String(value));

function event(kind, message, { level = 'info', serverId = null, actor = null, ip = null } = {}) {
  run('INSERT INTO events (t, kind, level, server_id, actor, ip, message) VALUES (?, ?, ?, ?, ?, ?, ?)',
    Date.now(), kind, level, serverId, actor, ip, message);
}

module.exports = { db, DATA, all, get, run, tx, setting, setSetting, event };
