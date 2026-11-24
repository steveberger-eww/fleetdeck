'use strict';
// Beszel-style alerts: rules evaluated after every probe, notifications on fire and resolve.
const crypto = require('node:crypto');
const { all, get, run, event, setting, setSetting } = require('./db');
const { encrypt, decrypt } = require('./sec');

const bad = (msg) => Object.assign(new Error(msg), { status: 400 });

const METRICS = {
  down: { label: 'Unreachable', unit: 'min', hint: 'Fires when the server cannot be reached for N minutes' },
  cpu: { label: 'CPU usage', unit: '%', col: 'cpu', scale: 100 },
  mem: { label: 'Memory usage', unit: '%', col: 'mem', scale: 100 },
  swap: { label: 'Swap usage', unit: '%', col: 'swap', scale: 100 },
  disk: { label: 'Disk usage (fullest mount)', unit: '%' },
  load: { label: 'Load per CPU core', unit: '' , col: 'load1', scale: 1 },
  temp: { label: 'Temperature', unit: '°C', col: 'temp', scale: 1 },
  updates: { label: 'Pending updates', unit: '' },
  security: { label: 'Pending security updates', unit: '' },
  failed: { label: 'Failed systemd units', unit: '' },
  reboot: { label: 'Reboot required', unit: '' },
};

function validateRule(b) {
  if (!METRICS[b.metric]) throw bad('unknown metric');
  const threshold = Number(b.threshold ?? 0);
  const minutes = Math.round(Number(b.minutes ?? 0));
  if (!Number.isFinite(threshold) || threshold < 0) throw bad('invalid threshold');
  if (!Number.isFinite(minutes) || minutes < 0 || minutes > 1440) throw bad('minutes must be 0-1440');
  const server_id = b.server_id || null;
  if (server_id && !get('SELECT 1 FROM servers WHERE id = ?', server_id)) throw bad('unknown server');
  const tag = b.tag ? String(b.tag).trim().toLowerCase() : null;
  if (tag && !/^[a-z0-9_.-]{1,32}$/.test(tag)) throw bad('invalid tag');
  if (tag && server_id) throw bad('Pick a server or a tag, not both');
  return { metric: b.metric, threshold, minutes, server_id, tag, enabled: b.enabled === false ? 0 : 1 };
}

function currentValue(rule, server, snap) {
  const m = METRICS[rule.metric];
  if (m.col && rule.minutes > 0) {
    const r = get(`SELECT AVG(${m.col}) v FROM metrics WHERE server_id = ? AND t >= ?`, server.id, Date.now() - rule.minutes * 60e3);
    if (r?.v == null) return null;
    return rule.metric === 'load' ? r.v / (snap.cores || 1) : r.v * m.scale;
  }
  switch (rule.metric) {
    case 'cpu': case 'mem': case 'swap': return snap[rule.metric] == null ? null : snap[rule.metric] * 100;
    case 'temp': return snap.temp;
    case 'load': return snap.load[0] / (snap.cores || 1);
    case 'disk': return snap.disks.length ? Math.max(...snap.disks.map((d) => d.pct)) * 100 : null;
    case 'reboot': return snap.reboot ? 1 : 0;
    default: return snap[rule.metric];
  }
}

const fmt = (v) => (Number.isInteger(v) ? String(v) : v.toFixed(1));

function evaluate(server, snap, err) {
  const rules = all('SELECT * FROM alert_rules WHERE enabled = 1 AND (server_id IS NULL OR server_id = ?)', server.id);
  const tags = (server.tags || '').split(',');
  for (const r of rules) {
    // Tag no longer on this server: drop its alert silently instead of leaving it firing forever.
    if (r.tag && !tags.includes(r.tag)) { run('DELETE FROM alert_state WHERE rule_id = ? AND server_id = ?', r.id, server.id); continue; }
    const m = METRICS[r.metric];
    let firing = false;
    let msg = '';
    if (r.metric === 'down') {
      firing = !!err && Date.now() - err.since >= r.minutes * 60e3;
      msg = `${server.name} is unreachable${err ? `: ${err.message}` : ''}`;
    } else {
      if (!snap) continue; // unknown while unreachable; keep the current state
      const v = currentValue(r, server, snap);
      firing = v != null && v > r.threshold;
      msg = r.metric === 'reboot' ? `${server.name} needs a reboot`
        : `${server.name}: ${m.label} is ${v == null ? '?' : fmt(v)}${m.unit} (threshold ${fmt(r.threshold)}${m.unit}${r.minutes && m.col ? `, ${r.minutes} min avg` : ''})`;
    }
    const cur = get('SELECT 1 FROM alert_state WHERE rule_id = ? AND server_id = ?', r.id, server.id);
    if (firing && !cur) {
      run('INSERT INTO alert_state (rule_id, server_id, since, message) VALUES (?, ?, ?, ?)', r.id, server.id, Date.now(), msg);
      event('alert', `Firing: ${msg}`, { level: 'error', serverId: server.id });
      notify(`🔴 ${server.name}: ${m.label}`, msg, 'error');
    } else if (!firing && cur) {
      run('DELETE FROM alert_state WHERE rule_id = ? AND server_id = ?', r.id, server.id);
      event('alert', `Resolved: ${server.name} ${m.label}`, { level: 'success', serverId: server.id });
      notify(`✅ ${server.name}: ${m.label} resolved`, `${server.name}: ${m.label} is back to normal`, 'success');
    } else if (firing) {
      run('UPDATE alert_state SET message = ? WHERE rule_id = ? AND server_id = ?', msg, r.id, server.id);
    }
  }
}

const firing = () => all(`SELECT a.rule_id, a.server_id, a.since, a.message, r.metric, s.name AS server_name
  FROM alert_state a JOIN alert_rules r ON r.id = a.rule_id JOIN servers s ON s.id = a.server_id ORDER BY a.since DESC`);

// ---- notifiers ----
const post = async (url, body, headers = {}) => {
  const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(body), signal: AbortSignal.timeout(10_000) });
  if (!r.ok) throw new Error(`HTTP ${r.status} ${(await r.text()).slice(0, 200)}`);
};

const TYPES = {
  webhook: { label: 'Webhook (JSON POST)', fields: [{ key: 'url', label: 'URL', secret: true }],
    send: (c, title, message, level) => post(c.url, { title, message, level, time: new Date().toISOString() }) },
  ntfy: { label: 'ntfy', fields: [{ key: 'url', label: 'Topic URL (https://ntfy.sh/my-topic)' }, { key: 'token', label: 'Access token (optional)', secret: true, optional: true }],
    send: (c, title, message, level) => {
      const u = new URL(c.url);
      const topic = u.pathname.split('/').filter(Boolean).pop();
      return post(`${u.origin}${u.pathname.slice(0, u.pathname.lastIndexOf('/')) || ''}/`, { topic, title, message, priority: level === 'error' ? 4 : 3 },
        c.token ? { Authorization: `Bearer ${c.token}` } : {});
    } },
  discord: { label: 'Discord', fields: [{ key: 'url', label: 'Webhook URL', secret: true }],
    send: (c, title, message) => post(c.url, { content: `**${title}**\n${message}` }) },
  slack: { label: 'Slack', fields: [{ key: 'url', label: 'Incoming webhook URL', secret: true }],
    send: (c, title, message) => post(c.url, { text: `*${title}*\n${message}` }) },
  telegram: { label: 'Telegram', fields: [{ key: 'token', label: 'Bot token', secret: true }, { key: 'chat_id', label: 'Chat ID' }],
    send: (c, title, message) => post(`https://api.telegram.org/bot${c.token}/sendMessage`, { chat_id: c.chat_id, text: `${title}\n${message}` }) },
  gotify: { label: 'Gotify', fields: [{ key: 'url', label: 'Server URL' }, { key: 'token', label: 'App token', secret: true }],
    send: (c, title, message, level) => post(`${c.url.replace(/\/$/, '')}/message`, { title, message, priority: level === 'error' ? 8 : 4 }, { 'X-Gotify-Key': c.token }) },
};

function validateNotifier(b, existing) {
  const t = TYPES[b.type];
  if (!t) throw bad('unknown notifier type');
  const name = String(b.name || '').trim();
  if (!name || name.length > 100) throw bad('name required');
  const old = existing ? JSON.parse(decrypt(existing.config_enc)) : {};
  if (existing && b.config === undefined) return { name, type: b.type, config: old, enabled: b.enabled === false ? 0 : 1 };
  const config = {};
  for (const f of t.fields) {
    let v = String(b.config?.[f.key] ?? '').trim();
    if (!v && f.secret && old[f.key]) v = old[f.key]; // blank secret on edit = keep
    if (!v && !f.optional) throw bad(`${f.label} is required`);
    if (v && /url/i.test(f.key) && !/^https?:\/\//.test(v)) throw bad(`${f.label} must start with http:// or https://`);
    config[f.key] = v;
  }
  return { name, type: b.type, config, enabled: b.enabled === false ? 0 : 1 };
}

// Secrets never leave the server; the UI only learns whether they are set.
function notifierView(n) {
  const c = JSON.parse(decrypt(n.config_enc));
  const config = {};
  for (const f of TYPES[n.type].fields) config[f.key] = f.secret ? (c[f.key] ? '••••••' : '') : c[f.key];
  return { id: n.id, name: n.name, type: n.type, enabled: !!n.enabled, config };
}

async function sendOne(n, title, message, level) {
  await TYPES[n.type].send(JSON.parse(decrypt(n.config_enc)), title, message, level);
}

function notify(title, message, level = 'info') {
  for (const n of all('SELECT * FROM notifiers WHERE enabled = 1')) {
    sendOne(n, title, message, level).catch((e) => event('notify', `Notification via "${n.name}" failed: ${e.message}`, { level: 'error' }));
  }
}

function seed() {
  if (setting('seeded_rules')) return;
  const now = Date.now();
  for (const [metric, threshold, minutes] of [['down', 0, 2], ['disk', 90, 0], ['mem', 90, 10]]) {
    run('INSERT INTO alert_rules (id, server_id, metric, threshold, minutes, enabled, created_at) VALUES (?, NULL, ?, ?, ?, 1, ?)',
      crypto.randomUUID(), metric, threshold, minutes, now);
  }
  setSetting('seeded_rules', '1');
}

function onJobDone(job) {
  const mode = setting('notify_jobs', 'scheduled'); // off | scheduled | all
  if (job.status !== 'failed' || mode === 'off') return;
  if (mode === 'scheduled' && !String(job.actor || '').startsWith('schedule')) return;
  notify(`❌ Job failed: ${job.name}`, `Job #${job.id} "${job.name}" failed (${job.runs.filter((s) => s !== 'success').length}/${job.runs.length} hosts).`, 'error');
}

module.exports = { METRICS, TYPES, validateRule, evaluate, firing, validateNotifier, notifierView, sendOne, notify, seed, onJobDone, encryptConfig: (c) => encrypt(JSON.stringify(c)) };
