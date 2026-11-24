'use strict';
// Fleetdeck: agentless server dashboard. Beszel-style monitoring, Semaphore-style jobs, Cockpit-style admin.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { WebSocketServer } = require('ws');
const { renderSVG } = require('uqr');
const D = require('./lib/db');
const sec = require('./lib/sec');
const ssh = require('./lib/ssh');
const probe = require('./lib/probe');
const jobs = require('./lib/jobs');
const alerts = require('./lib/alerts');
const admin = require('./lib/admin');
const repos = require('./lib/repos');

const HOST = process.env.HOST || '127.0.0.1';
const PORT = Number(process.env.PORT || 8080);
const TRUST_PROXY = process.env.FLEETDECK_TRUST_PROXY === '1';
const SESSION_MS = 7 * 864e5;
const PUBLIC = path.join(__dirname, 'public');
const NM = path.join(__dirname, 'node_modules');
const VENDOR = {
  '/vendor/htm.js': path.join(NM, 'htm/preact/standalone.module.js'),
  '/vendor/xterm.js': path.join(NM, '@xterm/xterm/lib/xterm.mjs'),
  '/vendor/xterm.css': path.join(NM, '@xterm/xterm/css/xterm.css'),
  '/vendor/addon-fit.js': path.join(NM, '@xterm/addon-fit/lib/addon-fit.mjs'),
  ...Object.fromEntries([400, 500, 600, 700].map((w) => [`/fonts/barlow-${w}.woff2`, path.join(NM, `@fontsource/barlow/files/barlow-latin-${w}-normal.woff2`)])),
  ...Object.fromEntries([400, 500].map((w) => [`/fonts/jbmono-${w}.woff2`, path.join(NM, `@fontsource/jetbrains-mono/files/jetbrains-mono-latin-${w}-normal.woff2`)])),
};
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8','.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.ico': 'image/x-icon', '.png': 'image/png', '.woff2': 'font/woff2' };

const bad = (msg, status = 400) => Object.assign(new Error(msg), { status });
const now = () => Date.now();

// ---------- validation ----------
function validateServer(b) {
  if (!b || typeof b !== 'object') throw bad('invalid body');
  const name = String(b.name || '').trim();
  const host = String(b.host || '').trim();
  const user = String(b.user || 'root').trim();
  const port = Number(b.port || 22);
  if (!name || name.length > 64) throw bad('Name is required (max 64 characters)');
  if (!/^[a-zA-Z0-9][a-zA-Z0-9.:-]*$/.test(host) || host.length > 253) throw bad('Host must be a hostname or IP address');
  if (!/^[a-zA-Z_][a-zA-Z0-9_.-]*$/.test(user) || user.length > 32) throw bad('Invalid user name');
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw bad('Port must be 1-65535');
  const key_id = b.key_id || null;
  if (key_id && !D.get('SELECT 1 FROM keys WHERE id = ?', key_id)) throw bad('Selected key does not exist');
  const tags = (Array.isArray(b.tags) ? b.tags : String(b.tags || '').split(','))
    .map((t) => String(t).trim().toLowerCase()).filter(Boolean);
  for (const t of tags) if (!/^[a-z0-9_.-]{1,32}$/.test(t)) throw bad(`Invalid tag "${t}" (letters, digits, - _ .)`);
  return { name, host, user, port, key_id, tags: [...new Set(tags)].join(','), notes: String(b.notes || '').slice(0, 4000), paused: b.paused ? 1 : 0 };
}

// ---------- views ----------
function serverView(s, spark) {
  const snapshot = s.last_json ? JSON.parse(s.last_json) : null;
  const error = probe.errors.get(s.id) || null;
  const fresh = snapshot && now() - snapshot.t < 3 * 60e3;
  const firing = D.all('SELECT message, since FROM alert_state WHERE server_id = ? ORDER BY since', s.id);
  return {
    id: s.id, name: s.name, host: s.host, port: s.port, user: s.user, key_id: s.key_id,
    tags: s.tags ? s.tags.split(',') : [], notes: s.notes, paused: !!s.paused, host_key: s.host_key, created_at: s.created_at,
    status: s.paused ? 'paused' : error ? 'down' : fresh ? 'up' : 'unknown',
    snapshot, error, ssh: ssh.status(s), has_sudo_password: !!s.sudo_enc,
    alerts: firing.length, firing,
    spark: spark || undefined,
  };
}

// undefined = keep, null = remove, string = new encrypted value
function sudoField(b) {
  if (b.clear_sudo_password) return null;
  if (typeof b.sudo_password !== 'string' || !b.sudo_password) return undefined;
  if (b.sudo_password.length > 1024 || /[\r\n]/.test(b.sudo_password)) throw bad('Invalid sudo password');
  return sec.encrypt(b.sudo_password);
}

const getServer = (id) => {
  const s = D.get('SELECT * FROM servers WHERE id = ?', id);
  if (!s) throw bad('Server not found', 404);
  return s;
};

const keyView = (k) => ({ id: k.id, name: k.name, public_key: k.public_key, created_at: k.created_at, has_passphrase: !!k.passphrase_enc,
  used_by: D.all('SELECT id, name FROM servers WHERE key_id = ?', k.id).map((r) => ({ ...r })) });

const scheduleView = (s) => ({ ...s, enabled: !!s.enabled, targets: JSON.parse(s.targets), params: JSON.parse(s.params),
  script_name: D.get('SELECT name FROM scripts WHERE id = ?', s.script_id)?.name,
  next: (() => { try { return jobs.nextRuns(s.cron, 1)[0] || null; } catch { return null; } })() });

const ruleView = (r) => ({ ...r, enabled: !!r.enabled, server_name: r.server_id ? D.get('SELECT name FROM servers WHERE id = ?', r.server_id)?.name : null });

// ---------- auth ----------
let setupToken = null;
const needsSetup = () => !D.setting('password_hash');
const clientIp = (req) => (TRUST_PROXY && req.headers['x-forwarded-for'] ? req.headers['x-forwarded-for'].split(',')[0].trim() : req.socket.remoteAddress) || '';
const isHttps = (req) => !!req.socket.encrypted || (TRUST_PROXY && req.headers['x-forwarded-proto'] === 'https');
const hostOf = (req) => (TRUST_PROXY && req.headers['x-forwarded-host']) || req.headers.host || '';

function cookies(req) {
  const out = {};
  for (const part of (req.headers.cookie || '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0) out[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim());
  }
  return out;
}

function session(req) {
  const tok = cookies(req).fd_session;
  if (!tok) return null;
  const hash = sec.sha256(tok);
  const s = D.get('SELECT * FROM sessions WHERE hash = ?', hash);
  if (!s) return null;
  if (now() - s.seen_at > SESSION_MS) { D.run('DELETE FROM sessions WHERE hash = ?', hash); return null; }
  if (now() - s.seen_at > 60e3) D.run('UPDATE sessions SET seen_at = ? WHERE hash = ?', now(), hash);
  return s;
}

const sessionCookie = (req, value, maxAge) =>
  `fd_session=${value}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${maxAge}${isHttps(req) ? '; Secure' : ''}`;

function createSession(req, res) {
  const tok = sec.token();
  D.run('INSERT INTO sessions (hash, created_at, seen_at, ip, ua) VALUES (?, ?, ?, ?, ?)', sec.sha256(tok), now(), now(), clientIp(req), String(req.headers['user-agent'] || '').slice(0, 300));
  res.setHeader('Set-Cookie', sessionCookie(req, tok, SESSION_MS / 1000));
}

// Per-IP lockout: 5 failures -> 15 minutes.
const failures = new Map();
function checkRate(ip) {
  const f = failures.get(ip);
  if (f?.until > now()) throw bad(`Too many failed attempts. Try again in ${Math.ceil((f.until - now()) / 60e3)} min.`, 429);
}
function recordFailure(ip) {
  const f = failures.get(ip) || { n: 0, until: 0 };
  if (++f.n >= 5) { f.until = now() + 15 * 60e3; f.n = 0; }
  failures.set(ip, f);
}

function sameOrigin(req, required) {
  const o = req.headers.origin;
  if (!o) return !required;
  try { return new URL(o).host === hostOf(req); } catch { return false; }
}

const totpSecret = () => { const e = D.setting('totp_secret'); return e ? sec.decrypt(e) : null; };
const pendingTotp = new Map(); // session hash -> secret awaiting confirmation

function checkTotp(code) {
  const secret = totpSecret();
  if (!secret) return true;
  const counter = sec.verifyTotp(secret, String(code || '').replace(/\s/g, ''));
  if (counter == null || counter <= Number(D.setting('totp_last', -1))) return false; // no replay within the window
  D.setSetting('totp_last', counter);
  return true;
}

// ---------- routing ----------
const routes = [];
const route = (method, pattern, handler, { open = false } = {}) =>
  routes.push({ method, re: new RegExp('^' + pattern.replace(/:(\w+)/g, '(?<$1>[^/]+)') + '$'), handler, open });

function readBody(req) {
  return new Promise((resolve, reject) => {
    let data = '';
    req.on('data', (c) => {
      data += c;
      if (data.length > 1_000_000) { reject(bad('Request too large', 413)); req.destroy(); }
    });
    req.on('end', () => { try { resolve(data ? JSON.parse(data) : {}); } catch { reject(bad('Invalid JSON')); } });
    req.on('error', reject);
  });
}

const audit = (ctx, message, serverId = null) => D.event('audit', message, { actor: 'admin', ip: ctx.ip, serverId });

// ---- auth routes ----
route('GET', '/api/auth/state', (ctx) => ({ setup: needsSetup(), authed: !!ctx.session, totp: !!D.setting('totp_secret') }), { open: true });

route('POST', '/api/auth/setup', (ctx) => {
  if (!needsSetup()) throw bad('Already set up', 409);
  checkRate(ctx.ip);
  const { token, password } = ctx.body;
  if (!setupToken || String(token || '').trim() !== setupToken) { recordFailure(ctx.ip); throw bad('Invalid setup token (see the server console)', 401); }
  if (String(password || '').length < 10) throw bad('Password must be at least 10 characters');
  D.setSetting('password_hash', sec.hashPassword(password));
  setupToken = null;
  createSession(ctx.req, ctx.res);
  audit(ctx, 'Initial setup completed');
  return { ok: true };
}, { open: true });

route('POST', '/api/auth/login', (ctx) => {
  checkRate(ctx.ip);
  if (needsSetup()) throw bad('Setup required', 409);
  if (!sec.verifyPassword(String(ctx.body.password || ''), D.setting('password_hash'))) {
    recordFailure(ctx.ip);
    D.event('security', 'Failed login (password)', { level: 'warn', ip: ctx.ip });
    throw bad('Wrong password', 401);
  }
  if (totpSecret() && !ctx.body.code) return { totp_required: true };
  if (!checkTotp(ctx.body.code)) {
    recordFailure(ctx.ip);
    D.event('security', 'Failed login (2FA code)', { level: 'warn', ip: ctx.ip });
    throw bad('Invalid 2FA code', 401);
  }
  failures.delete(ctx.ip);
  createSession(ctx.req, ctx.res);
  D.event('security', 'Logged in', { actor: 'admin', ip: ctx.ip });
  return { ok: true };
}, { open: true });

route('POST', '/api/auth/logout', (ctx) => {
  if (ctx.session) D.run('DELETE FROM sessions WHERE hash = ?', ctx.session.hash);
  ctx.res.setHeader('Set-Cookie', sessionCookie(ctx.req, '', 0));
  return { ok: true };
}, { open: true });

// ---- account ----
route('GET', '/api/account', (ctx) => ({
  totp: !!D.setting('totp_secret'),
  sessions: D.all('SELECT * FROM sessions ORDER BY seen_at DESC').map((s) => ({ id: s.hash.slice(0, 16), created_at: s.created_at, seen_at: s.seen_at, ip: s.ip, ua: s.ua, current: s.hash === ctx.session.hash })),
}));

route('POST', '/api/account/password', (ctx) => {
  checkRate(ctx.ip);
  if (!sec.verifyPassword(String(ctx.body.current || ''), D.setting('password_hash'))) { recordFailure(ctx.ip); throw bad('Current password is wrong', 401); }
  if (String(ctx.body.next || '').length < 10) throw bad('New password must be at least 10 characters');
  D.setSetting('password_hash', sec.hashPassword(ctx.body.next));
  D.run('DELETE FROM sessions WHERE hash != ?', ctx.session.hash);
  audit(ctx, 'Password changed; other sessions signed out');
  return { ok: true };
});

route('POST', '/api/account/totp/begin', (ctx) => {
  const secret = sec.newTotpSecret();
  pendingTotp.set(ctx.session.hash, secret);
  const uri = `otpauth://totp/Fleetdeck:admin?secret=${secret}&issuer=Fleetdeck&algorithm=SHA1&digits=6&period=30`;
  return { secret, uri, qr: 'data:image/svg+xml;base64,' + Buffer.from(renderSVG(uri, { border: 2 })).toString('base64') };
});

route('POST', '/api/account/totp/enable', (ctx) => {
  const secret = pendingTotp.get(ctx.session.hash);
  if (!secret) throw bad('Start 2FA setup first');
  if (sec.verifyTotp(secret, String(ctx.body.code || '').replace(/\s/g, '')) == null) throw bad('Code does not match, check your device clock');
  D.setSetting('totp_secret', sec.encrypt(secret));
  D.setSetting('totp_last', -1);
  pendingTotp.delete(ctx.session.hash);
  audit(ctx, 'Two-factor authentication enabled');
  return { ok: true };
});

route('POST', '/api/account/totp/disable', (ctx) => {
  checkRate(ctx.ip);
  if (!sec.verifyPassword(String(ctx.body.password || ''), D.setting('password_hash')) || !checkTotp(ctx.body.code)) {
    recordFailure(ctx.ip);
    throw bad('Password or code is wrong', 401);
  }
  D.setSetting('totp_secret', null);
  audit(ctx, 'Two-factor authentication disabled');
  return { ok: true };
});

route('DELETE', '/api/account/sessions/:id', (ctx) => {
  const r = D.run('DELETE FROM sessions WHERE substr(hash, 1, 16) = ? AND hash != ?', ctx.params.id, ctx.session.hash);
  audit(ctx, 'Revoked a session');
  return { ok: r.changes > 0 };
});

route('POST', '/api/account/sessions/revoke-others', (ctx) => {
  D.run('DELETE FROM sessions WHERE hash != ?', ctx.session.hash);
  audit(ctx, 'Revoked all other sessions');
  return { ok: true };
});

// ---- servers ----
route('GET', '/api/servers', () => {
  const sp = probe.sparks();
  return D.all('SELECT * FROM servers ORDER BY name COLLATE NOCASE').map((s) => serverView(s, sp[s.id] || []));
});

route('POST', '/api/servers', (ctx) => {
  const v = validateServer(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO servers (id, name, host, port, user, key_id, tags, notes, paused, created_at, sudo_enc) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
    id, v.name, v.host, v.port, v.user, v.key_id, v.tags, v.notes, v.paused, now(), sudoField(ctx.body) || null);
  audit(ctx, `Added server ${v.name} (${v.user}@${v.host}:${v.port})`, id);
  const s = getServer(id);
  if (!s.paused) probe.probe(s);
  return serverView(s);
});

route('GET', '/api/servers/:id', (ctx) => serverView(getServer(ctx.params.id)));

route('PUT', '/api/servers/:id', (ctx) => {
  const old = getServer(ctx.params.id);
  const v = validateServer({ ...old, tags: old.tags, ...ctx.body });
  const moved = old.host !== v.host || old.port !== v.port;
  D.run('UPDATE servers SET name = ?, host = ?, port = ?, user = ?, key_id = ?, tags = ?, notes = ?, paused = ?, host_key = ?, host_key_raw = ? WHERE id = ?',
    v.name, v.host, v.port, v.user, v.key_id, v.tags, v.notes, v.paused, moved ? null : old.host_key, moved ? null : old.host_key_raw, old.id);
  const sudo = sudoField(ctx.body);
  if (sudo !== undefined) {
    D.run('UPDATE servers SET sudo_enc = ? WHERE id = ?', sudo, old.id);
    audit(ctx, `${sudo ? 'Set' : 'Removed'} the sudo password of ${v.name}`, old.id);
  }
  if (v.paused) { D.run('DELETE FROM alert_state WHERE server_id = ?', old.id); probe.errors.delete(old.id); }
  ssh.drop(old.id);
  audit(ctx, `Updated server ${v.name}${moved ? ' (address changed, host key will be re-pinned)' : ''}`, old.id);
  const s = getServer(old.id);
  if (!s.paused) probe.probe(s);
  return serverView(s);
});

route('DELETE', '/api/servers/:id', (ctx) => {
  const s = getServer(ctx.params.id);
  ssh.drop(s.id);
  D.tx(() => {
    D.run('DELETE FROM metrics WHERE server_id = ?', s.id);
    D.run('DELETE FROM servers WHERE id = ?', s.id);
  });
  probe.errors.delete(s.id);
  audit(ctx, `Removed server ${s.name}`);
  return { ok: true };
});

route('POST', '/api/servers/:id/probe', async (ctx) => {
  await probe.probe(getServer(ctx.params.id));
  return serverView(getServer(ctx.params.id));
});

route('POST', '/api/servers/:id/test', async (ctx) => {
  const s = getServer(ctx.params.id);
  try {
    const { out } = await ssh.exec(s, 'id -un; if [ "$(id -u)" = 0 ]; then echo root; elif sudo -n true 2>/dev/null; then echo sudo; else echo none; fi', { mode: 'user', timeout: 20_000 });
    let [user, priv] = out.trim().split('\n');
    if (priv === 'none' && s.sudo_enc) {
      const r = await ssh.exec(getServer(s.id), 'id -u', { mode: 'root', timeout: 20_000 });
      priv = r.code === 0 && r.out.trim().split('\n').pop() === '0' ? 'password' : 'wrong-password';
    }
    return { ok: true, user, privilege: priv, host_key: getServer(s.id).host_key };
  } catch (e) {
    return { ok: false, error: e.message };
  }
});

route('POST', '/api/servers/:id/hostkey/accept', (ctx) => {
  const s = getServer(ctx.params.id);
  const fp = ssh.acceptHostKey(s.id);
  if (!fp) throw bad('No pending host key change for this server');
  D.event('security', `Accepted new host key ${fp} for ${s.name}`, { level: 'warn', actor: 'admin', ip: ctx.ip, serverId: s.id });
  probe.probe(s);
  return { ok: true, host_key: fp };
});

route('GET', '/api/servers/:id/metrics', (ctx) => probe.series(getServer(ctx.params.id).id, ctx.query.range));

route('GET', '/api/servers/:id/services', (ctx) => admin.services(getServer(ctx.params.id)));
route('GET', '/api/servers/:id/services/status', (ctx) => admin.serviceStatus(getServer(ctx.params.id), ctx.query.unit).then((text) => ({ text })));
route('POST', '/api/servers/:id/services', async (ctx) => {
  const s = getServer(ctx.params.id);
  const out = await admin.serviceAction(s, String(ctx.body.unit), String(ctx.body.action));
  audit(ctx, `systemctl ${ctx.body.action} ${ctx.body.unit} on ${s.name}`, s.id);
  return { ok: true, output: out };
});

route('GET', '/api/servers/:id/logs', (ctx) => admin.logs(getServer(ctx.params.id), ctx.query));
route('GET', '/api/servers/:id/packages', (ctx) => admin.packages(getServer(ctx.params.id), { refresh: ctx.query.refresh === '1' }));
route('GET', '/api/servers/:id/processes', (ctx) => admin.processes(getServer(ctx.params.id)));
route('POST', '/api/servers/:id/processes/kill', async (ctx) => {
  const s = getServer(ctx.params.id);
  await admin.kill(s, Number(ctx.body.pid), String(ctx.body.signal || 'TERM'));
  audit(ctx, `kill -${ctx.body.signal || 'TERM'} ${ctx.body.pid} on ${s.name}`, s.id);
  return { ok: true };
});
route('GET', '/api/servers/:id/containers', (ctx) => admin.containers(getServer(ctx.params.id)));
route('GET', '/api/servers/:id/containers/logs', (ctx) => admin.containerLogs(getServer(ctx.params.id), String(ctx.query.id), ctx.query.lines).then((text) => ({ text })));
route('POST', '/api/servers/:id/containers', async (ctx) => {
  const s = getServer(ctx.params.id);
  const out = await admin.containerAction(s, String(ctx.body.id), String(ctx.body.action));
  audit(ctx, `docker ${ctx.body.action} ${String(ctx.body.id).slice(0, 12)} on ${s.name}`, s.id);
  return { ok: true, output: out };
});

// ---- keys ----
route('GET', '/api/keys', () => D.all('SELECT * FROM keys ORDER BY name COLLATE NOCASE').map(keyView));

route('POST', '/api/keys', (ctx) => {
  const name = String(ctx.body.name || '').trim();
  if (!name || name.length > 64) throw bad('Name is required');
  let priv;
  let pub;
  const passphrase = ctx.body.passphrase ? String(ctx.body.passphrase) : null;
  if (ctx.body.generate) {
    ({ privateKey: priv, publicKey: pub } = ssh.generateKey(`fleetdeck-${name.replace(/[^\w.-]/g, '_')}`));
  } else {
    priv = String(ctx.body.private_key || '').trim() + '\n';
    pub = ssh.parsePrivateKey(priv, passphrase);
  }
  const id = jobs.newId();
  D.run('INSERT INTO keys (id, name, public_key, private_enc, passphrase_enc, created_at) VALUES (?, ?, ?, ?, ?, ?)',
    id, name, pub, sec.encrypt(priv), passphrase && !ctx.body.generate ? sec.encrypt(passphrase) : null, now());
  audit(ctx, `${ctx.body.generate ? 'Generated' : 'Imported'} SSH key "${name}"`);
  return keyView(D.get('SELECT * FROM keys WHERE id = ?', id));
});

route('PUT', '/api/keys/:id', (ctx) => {
  const name = String(ctx.body.name || '').trim();
  if (!name || name.length > 64) throw bad('Name is required');
  const old = D.get('SELECT name FROM keys WHERE id = ?', ctx.params.id);
  if (!old) throw bad('Key not found', 404);
  D.run('UPDATE keys SET name = ? WHERE id = ?', name, ctx.params.id);
  audit(ctx, `Renamed SSH key "${old.name}" to "${name}"`);
  return keyView(D.get('SELECT * FROM keys WHERE id = ?', ctx.params.id));
});

route('DELETE', '/api/keys/:id', (ctx) => {
  const k = D.get('SELECT * FROM keys WHERE id = ?', ctx.params.id);
  if (!k) throw bad('Key not found', 404);
  const used = D.all('SELECT name FROM servers WHERE key_id = ?', k.id);
  if (used.length) throw bad(`Key is used by: ${used.map((u) => u.name).join(', ')}`, 409);
  D.run('DELETE FROM keys WHERE id = ?', k.id);
  audit(ctx, `Deleted SSH key "${k.name}"`);
  return { ok: true };
});

// ---- scripts ----
route('GET', '/api/scripts', () => D.all('SELECT * FROM scripts ORDER BY builtin DESC, name COLLATE NOCASE').map(jobs.scriptRow));
route('POST', '/api/scripts', (ctx) => {
  const v = jobs.validateScript(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO scripts (id, name, description, body, as_root, params, builtin, created_at, kind, repo_id, playbook) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)',
    id, v.name, v.description, v.body, v.as_root, JSON.stringify(v.params), now(), v.kind, v.repo_id, v.playbook);
  audit(ctx, `Created script "${v.name}"`);
  return jobs.scriptRow(D.get('SELECT * FROM scripts WHERE id = ?', id));
});
route('PUT', '/api/scripts/:id', (ctx) => {
  if (!D.get('SELECT 1 FROM scripts WHERE id = ?', ctx.params.id)) throw bad('Script not found', 404);
  const v = jobs.validateScript(ctx.body);
  D.run('UPDATE scripts SET name = ?, description = ?, body = ?, as_root = ?, params = ?, kind = ?, repo_id = ?, playbook = ? WHERE id = ?',
    v.name, v.description, v.body, v.as_root, JSON.stringify(v.params), v.kind, v.repo_id, v.playbook, ctx.params.id);
  audit(ctx, `Edited script "${v.name}"`);
  return jobs.scriptRow(D.get('SELECT * FROM scripts WHERE id = ?', ctx.params.id));
});
route('DELETE', '/api/scripts/:id', (ctx) => {
  const s = D.get('SELECT * FROM scripts WHERE id = ?', ctx.params.id);
  if (!s) throw bad('Script not found', 404);
  D.run('DELETE FROM scripts WHERE id = ?', s.id);
  audit(ctx, `Deleted script "${s.name}"`);
  return { ok: true };
});

// ---- git repositories ----
route('GET', '/api/repos', () => D.all('SELECT * FROM repos ORDER BY name COLLATE NOCASE').map((r) => ({
  ...repos.repoView(r), used_by: D.all('SELECT id, name FROM scripts WHERE repo_id = ?', r.id).map((x) => ({ ...x })),
})));
route('POST', '/api/repos', (ctx) => {
  const v = repos.validateRepo(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO repos (id, name, url, branch, auth, key_id, username, token_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)',
    id, v.name, v.url, v.branch, v.auth, v.key_id, v.username, v.token_enc, now());
  audit(ctx, `Added repository "${v.name}" (${v.url})`);
  const r = repos.getRepo(id);
  repos.sync(r).catch(() => {}); // first clone in the background; errors show on the repo
  return repos.repoView(r);
});
route('PUT', '/api/repos/:id', (ctx) => {
  const old = repos.getRepo(ctx.params.id);
  if (!old) throw bad('Repository not found', 404);
  const v = repos.validateRepo({ ...old, ...ctx.body }, old);
  D.run('UPDATE repos SET name = ?, url = ?, branch = ?, auth = ?, key_id = ?, username = ?, token_enc = ?, last_error = NULL WHERE id = ?',
    v.name, v.url, v.branch, v.auth, v.key_id, v.username, v.token_enc, old.id);
  if (old.url !== v.url) repos.removeCheckout(old.id); // fresh clone for a different remote
  audit(ctx, `Updated repository "${v.name}"`);
  return repos.repoView(repos.getRepo(old.id));
});
route('DELETE', '/api/repos/:id', (ctx) => {
  const r = repos.getRepo(ctx.params.id);
  if (!r) throw bad('Repository not found', 404);
  const used = D.all('SELECT name FROM scripts WHERE repo_id = ?', r.id);
  if (used.length) throw bad(`Used by scripts: ${used.map((u) => u.name).join(', ')}`, 409);
  D.run('DELETE FROM repos WHERE id = ?', r.id);
  repos.removeCheckout(r.id);
  audit(ctx, `Deleted repository "${r.name}"`);
  return { ok: true };
});
route('POST', '/api/repos/:id/sync', async (ctx) => {
  const r = repos.getRepo(ctx.params.id);
  if (!r) throw bad('Repository not found', 404);
  try { await repos.sync(r); } catch (e) { throw bad(e.message, 502); }
  return repos.repoView(repos.getRepo(r.id));
});
route('POST', '/api/repos/:id/forget-hostkey', async (ctx) => {
  const r = repos.getRepo(ctx.params.id);
  if (!r) throw bad('Repository not found', 404);
  const host = await repos.forgetHostKey(r);
  D.event('security', `Forgot the pinned Git host key of ${host} (repository "${r.name}"); the next sync pins the new one`, { level: 'warn', actor: 'admin', ip: ctx.ip });
  try { await repos.sync(r); } catch (e) { throw bad(e.message, 502); }
  return repos.repoView(repos.getRepo(r.id));
});
route('GET', '/api/repos/:id/files', async (ctx) => {
  const r = repos.getRepo(ctx.params.id);
  if (!r) throw bad('Repository not found', 404);
  try { return await repos.files(r); } catch (e) { throw bad(e.message, 502); }
});
route('GET', '/api/tools', () => repos.tools());

// ---- jobs ----
route('GET', '/api/jobs', (ctx) => jobs.listJobs({ serverId: ctx.query.server, limit: ctx.query.limit, before: ctx.query.before }));
route('POST', '/api/jobs', (ctx) => {
  const b = ctx.body;
  let spec;
  if (b.script_id) {
    const sc = jobs.scriptRow(D.get('SELECT * FROM scripts WHERE id = ?', b.script_id));
    if (!sc) throw bad('Script not found', 404);
    const params = {};
    for (const p of sc.params) {
      const v = String(b.params?.[p.name] ?? p.default ?? '');
      if (p.required && !v.trim()) throw bad(`"${p.label}" is required`);
      params[p.name] = v;
    }
    spec = jobs.specFromScript(sc, params, b.options);
  } else {
    const body = String(b.body || '');
    if (!body.trim()) throw bad('Command is required');
    if (body.length > 100_000) throw bad('Command too long');
    spec = { name: String(b.name || body.trim().split('\n')[0]).slice(0, 100), body, asRoot: b.as_root !== false, params: {} };
  }
  const id = jobs.startJob({ ...spec, targets: b.targets || {}, actor: 'admin' });
  audit(ctx, `Started job #${id} "${spec.name}"`);
  return { id };
});
route('GET', '/api/jobs/:id', (ctx) => jobs.getJob(ctx.params.id) || Promise.reject(bad('Job not found', 404)));
route('POST', '/api/jobs/:id/cancel', (ctx) => {
  if (!jobs.cancelJob(ctx.params.id)) throw bad('Job is not running', 409);
  audit(ctx, `Cancelled job #${ctx.params.id}`);
  return { ok: true };
});
route('POST', '/api/jobs/:id/rerun', (ctx) => {
  const j = jobs.getJob(ctx.params.id);
  if (!j) throw bad('Job not found', 404);
  const hostRuns = j.runs.filter((r) => r.server_id !== jobs.ANSIBLE);
  const servers = (ctx.body.failed_only ? hostRuns.filter((r) => !['success', 'skipped'].includes(r.status)) : hostRuns).map((r) => r.server_id);
  const id = jobs.startJob({ scriptId: j.script_id, name: j.name, body: j.body, kind: j.kind, repoId: j.repo_id, options: j.options,
    asRoot: !!j.as_root, params: j.params, targets: { servers }, actor: 'admin' });
  audit(ctx, `Re-ran job #${j.id} as #${id}`);
  return { id };
});
route('GET', '/api/jobs/:id/stream', (ctx) => {
  const { req, res } = ctx;
  const job = jobs.getJob(ctx.params.id);
  if (!job) throw bad('Job not found', 404);
  res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no' });
  const send = (m) => res.write(`data: ${JSON.stringify(m)}\n\n`);
  send({ type: 'snapshot', job });
  const unsub = jobs.subscribe(job.id, (m) => { send(m); if (m.type === 'job') res.end(); });
  if (!unsub) return res.end();
  const ping = setInterval(() => res.write(': ping\n\n'), 25_000);
  req.on('close', () => { clearInterval(ping); unsub(); });
});

// ---- schedules ----
route('GET', '/api/schedules', () => D.all('SELECT * FROM schedules ORDER BY name COLLATE NOCASE').map(scheduleView));
route('POST', '/api/schedules', (ctx) => {
  const v = jobs.validateSchedule(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO schedules (id, name, script_id, cron, targets, params, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)',
    id, v.name, v.script_id, v.cron, JSON.stringify(v.targets), JSON.stringify(v.params), v.enabled, now());
  audit(ctx, `Created schedule "${v.name}" (${v.cron})`);
  return scheduleView(D.get('SELECT * FROM schedules WHERE id = ?', id));
});
route('PUT', '/api/schedules/:id', (ctx) => {
  const old = D.get('SELECT * FROM schedules WHERE id = ?', ctx.params.id);
  if (!old) throw bad('Schedule not found', 404);
  const v = jobs.validateSchedule({ ...scheduleView(old), ...ctx.body });
  D.run('UPDATE schedules SET name = ?, script_id = ?, cron = ?, targets = ?, params = ?, enabled = ? WHERE id = ?',
    v.name, v.script_id, v.cron, JSON.stringify(v.targets), JSON.stringify(v.params), v.enabled, old.id);
  audit(ctx, `Updated schedule "${v.name}"`);
  return scheduleView(D.get('SELECT * FROM schedules WHERE id = ?', old.id));
});
route('DELETE', '/api/schedules/:id', (ctx) => {
  const s = D.get('SELECT * FROM schedules WHERE id = ?', ctx.params.id);
  if (!s) throw bad('Schedule not found', 404);
  D.run('DELETE FROM schedules WHERE id = ?', s.id);
  audit(ctx, `Deleted schedule "${s.name}"`);
  return { ok: true };
});
route('POST', '/api/schedules/:id/run', (ctx) => {
  const s = D.get('SELECT * FROM schedules WHERE id = ?', ctx.params.id);
  if (!s) throw bad('Schedule not found', 404);
  const id = jobs.runSchedule(s, 'admin');
  audit(ctx, `Ran schedule "${s.name}" manually (job #${id})`);
  return { id };
});
route('POST', '/api/cron/preview', (ctx) => ({ next: jobs.nextRuns(ctx.body.cron, 5) }));

// ---- alerts & notifiers ----
route('GET', '/api/alerts', () => ({
  metrics: alerts.METRICS,
  rules: D.all('SELECT * FROM alert_rules ORDER BY created_at').map(ruleView),
  firing: alerts.firing(),
  history: D.all("SELECT * FROM events WHERE kind = 'alert' ORDER BY id DESC LIMIT 100").map((e) => ({ ...e })),
}));
route('POST', '/api/alerts/rules', (ctx) => {
  const v = alerts.validateRule(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO alert_rules (id, server_id, tag, metric, threshold, minutes, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)', id, v.server_id, v.tag, v.metric, v.threshold, v.minutes, v.enabled, now());
  audit(ctx, `Created alert rule ${v.metric}`);
  return ruleView(D.get('SELECT * FROM alert_rules WHERE id = ?', id));
});
route('PUT', '/api/alerts/rules/:id', (ctx) => {
  const old = D.get('SELECT * FROM alert_rules WHERE id = ?', ctx.params.id);
  if (!old) throw bad('Rule not found', 404);
  const v = alerts.validateRule({ ...old, enabled: !!old.enabled, ...ctx.body });
  D.run('UPDATE alert_rules SET server_id = ?, tag = ?, metric = ?, threshold = ?, minutes = ?, enabled = ? WHERE id = ?', v.server_id, v.tag, v.metric, v.threshold, v.minutes, v.enabled, old.id);
  D.run('DELETE FROM alert_state WHERE rule_id = ?', old.id); // re-evaluated on the next probe
  audit(ctx, `Updated alert rule ${v.metric}`);
  return ruleView(D.get('SELECT * FROM alert_rules WHERE id = ?', old.id));
});
route('DELETE', '/api/alerts/rules/:id', (ctx) => {
  D.run('DELETE FROM alert_rules WHERE id = ?', ctx.params.id);
  audit(ctx, 'Deleted alert rule');
  return { ok: true };
});

route('GET', '/api/notifiers', () => ({
  types: Object.fromEntries(Object.entries(alerts.TYPES).map(([k, t]) => [k, { label: t.label, fields: t.fields }])),
  notifiers: D.all('SELECT * FROM notifiers ORDER BY name COLLATE NOCASE').map(alerts.notifierView),
  notify_jobs: D.setting('notify_jobs', 'scheduled'),
}));
route('POST', '/api/notifiers', (ctx) => {
  const v = alerts.validateNotifier(ctx.body);
  const id = jobs.newId();
  D.run('INSERT INTO notifiers (id, name, type, config_enc, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?)', id, v.name, v.type, alerts.encryptConfig(v.config), v.enabled, now());
  audit(ctx, `Added notification channel "${v.name}" (${v.type})`);
  return alerts.notifierView(D.get('SELECT * FROM notifiers WHERE id = ?', id));
});
route('PUT', '/api/notifiers/:id', (ctx) => {
  const old = D.get('SELECT * FROM notifiers WHERE id = ?', ctx.params.id);
  if (!old) throw bad('Notifier not found', 404);
  const v = alerts.validateNotifier({ type: old.type, name: old.name, enabled: !!old.enabled, ...ctx.body }, old);
  D.run('UPDATE notifiers SET name = ?, type = ?, config_enc = ?, enabled = ? WHERE id = ?', v.name, v.type, alerts.encryptConfig(v.config), v.enabled, old.id);
  audit(ctx, `Updated notification channel "${v.name}"`);
  return alerts.notifierView(D.get('SELECT * FROM notifiers WHERE id = ?', old.id));
});
route('DELETE', '/api/notifiers/:id', (ctx) => {
  D.run('DELETE FROM notifiers WHERE id = ?', ctx.params.id);
  audit(ctx, 'Deleted notification channel');
  return { ok: true };
});
route('POST', '/api/notifiers/:id/test', async (ctx) => {
  const n = D.get('SELECT * FROM notifiers WHERE id = ?', ctx.params.id);
  if (!n) throw bad('Notifier not found', 404);
  try { await alerts.sendOne(n, '🔔 Fleetdeck test', 'If you can read this, notifications work.', 'info'); } catch (e) { throw bad(`Send failed: ${e.message}`, 502); }
  return { ok: true };
});

// ---- events & settings ----
route('GET', '/api/events', (ctx) => {
  const where = [];
  const p = [];
  for (const k of ['kind', 'level', 'server_id']) if (ctx.query[k]) { where.push(`${k} = ?`); p.push(ctx.query[k]); }
  if (ctx.query.before) { where.push('id < ?'); p.push(Number(ctx.query.before)); }
  if (ctx.query.q) { where.push('message LIKE ?'); p.push(`%${ctx.query.q}%`); }
  const lim = Math.min(Number(ctx.query.limit) || 100, 500);
  return D.all(`SELECT e.*, s.name AS server_name FROM events e LEFT JOIN servers s ON s.id = e.server_id
    ${where.length ? 'WHERE ' + where.map((w) => 'e.' + w).join(' AND ') : ''} ORDER BY e.id DESC LIMIT ${lim}`, ...p).map((e) => ({ ...e }));
});

const SETTINGS = { retention_metrics_days: [1, 365, 30], retention_jobs_days: [1, 3650, 90], retention_events_days: [1, 3650, 180] };
route('GET', '/api/settings', () => ({
  ...Object.fromEntries(Object.entries(SETTINGS).map(([k, [, , d]]) => [k, Number(D.setting(k, d))])),
  notify_jobs: D.setting('notify_jobs', 'scheduled'),
  secret_from_env: !!process.env.FLEETDECK_SECRET,
}));
route('PUT', '/api/settings', (ctx) => {
  for (const [k, [lo, hi]] of Object.entries(SETTINGS)) {
    if (ctx.body[k] === undefined) continue;
    const v = Math.round(Number(ctx.body[k]));
    if (!(v >= lo && v <= hi)) throw bad(`${k} must be ${lo}-${hi}`);
    D.setSetting(k, v);
  }
  if (ctx.body.notify_jobs !== undefined) {
    if (!['off', 'scheduled', 'all'].includes(ctx.body.notify_jobs)) throw bad('invalid notify_jobs');
    D.setSetting('notify_jobs', ctx.body.notify_jobs);
  }
  audit(ctx, 'Changed settings');
  return { ok: true };
});

// ---------- HTTP plumbing ----------
function securityHeaders(req, res) {
  const host = /^[\w.:[\]-]+$/.test(hostOf(req)) ? hostOf(req) : '';
  res.setHeader('Content-Security-Policy', [
    "default-src 'self'", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data:",
    `connect-src 'self'${host ? ` ws://${host} wss://${host}` : ''}`, "font-src 'self'", "object-src 'none'",
    "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'",
  ].join('; '));
  res.setHeader('X-Content-Type-Options', 'nosniff');
  res.setHeader('X-Frame-Options', 'DENY');
  res.setHeader('Referrer-Policy', 'no-referrer');
  res.setHeader('Cross-Origin-Opener-Policy', 'same-origin');
  res.setHeader('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');
  if (isHttps(req)) res.setHeader('Strict-Transport-Security', 'max-age=31536000');
}

function serveStatic(req, res, pathname) {
  let file = VENDOR[pathname];
  if (!file) {
    file = path.join(PUBLIC, pathname === '/' ? 'index.html' : pathname);
    if (!file.startsWith(PUBLIC + path.sep)) return false;
  }
  let data;
  try { data = fs.readFileSync(file); } catch { return false; }
  res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-cache' });
  res.end(data);
  return true;
}

const json = (res, status, obj) => {
  if (res.headersSent) return res.end();
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(obj));
};

async function handle(req, res) {
  securityHeaders(req, res);
  const url = new URL(req.url, 'http://localhost');
  if (!url.pathname.startsWith('/api/')) {
    if (req.method === 'GET' && serveStatic(req, res, url.pathname)) return;
    return json(res, 404, { error: 'Not found' });
  }
  const mutating = req.method !== 'GET' && req.method !== 'HEAD';
  if (mutating && !sameOrigin(req, false)) return json(res, 403, { error: 'Cross-origin request blocked' });
  if (mutating && req.headers['content-length'] !== '0' && req.headers['content-length'] !== undefined && !(req.headers['content-type'] || '').startsWith('application/json')) {
    return json(res, 415, { error: 'JSON only' });
  }
  for (const r of routes) {
    if (r.method !== req.method) continue;
    const m = url.pathname.match(r.re);
    if (!m) continue;
    const ctx = { req, res, ip: clientIp(req), params: m.groups || {}, query: Object.fromEntries(url.searchParams), session: session(req) };
    if (!r.open && !ctx.session) return json(res, 401, { error: 'Not signed in' });
    ctx.body = mutating ? await readBody(req) : {};
    const out = await r.handler(ctx);
    if (out !== undefined && !res.headersSent) json(res, 200, out);
    return;
  }
  json(res, 404, { error: 'Not found' });
}

// ---------- terminal (websocket) ----------
const wss = new WebSocketServer({ noServer: true, maxPayload: 1 << 20 });

function onUpgrade(req, socket, head) {
  const m = new URL(req.url, 'http://localhost').pathname.match(/^\/api\/servers\/([\w-]+)\/terminal$/);
  const deny = (code, msg) => { socket.write(`HTTP/1.1 ${code} ${msg}\r\nConnection: close\r\n\r\n`); socket.destroy(); };
  if (!m) return deny(404, 'Not Found');
  if (!sameOrigin(req, true)) return deny(403, 'Forbidden'); // blocks cross-site WebSocket hijacking
  if (!session(req)) return deny(401, 'Unauthorized');
  const server = D.get('SELECT * FROM servers WHERE id = ?', m[1]);
  if (!server) return deny(404, 'Not Found');
  wss.handleUpgrade(req, socket, head, (ws) => terminal(ws, server, req));
}

function terminal(ws, server, req) {
  const u = new URL(req.url, 'http://localhost');
  const clamp = (v, d, max) => Math.min(Math.max(Number(v) || d, 2), max);
  let stream = null;
  D.event('audit', `Opened terminal on ${server.name}`, { actor: 'admin', ip: clientIp(req), serverId: server.id });
  ssh.shell(server, { cols: clamp(u.searchParams.get('cols'), 80, 500), rows: clamp(u.searchParams.get('rows'), 24, 200) }, (st) => {
    stream = st;
    st.on('data', (d) => ws.readyState === 1 && ws.send(d));
    st.stderr?.on('data', (d) => ws.readyState === 1 && ws.send(d));
    st.on('close', () => ws.close(1000, 'session ended'));
  }).catch((e) => {
    if (ws.readyState === 1) { ws.send(`\r\n\x1b[31m${e.message}\x1b[0m\r\n`); ws.close(1011, 'ssh error'); }
  });
  // Protocol: JSON text frames. {t:'i', d:string} = keystrokes, {t:'r', c, r} = resize.
  ws.on('message', (data) => {
    let msg;
    try { msg = JSON.parse(data.toString()); } catch { return; }
    if (!stream) return;
    if (msg.t === 'i' && typeof msg.d === 'string') stream.write(msg.d);
    else if (msg.t === 'r') stream.setWindow(clamp(msg.r, 24, 200), clamp(msg.c, 80, 500), 0, 0);
  });
  ws.on('close', () => stream?.close());
}

// ---------- start ----------
function start() {
  const loopback = ['127.0.0.1', 'localhost', '::1'].includes(HOST);
  if (needsSetup()) {
    setupToken = sec.token().slice(0, 24);
    console.log(`\n  First run: open Fleetdeck and enter this setup token:\n\n    ${setupToken}\n`);
  }
  if (!loopback && !TRUST_PROXY) console.warn('  Warning: listening on a public interface without FLEETDECK_TRUST_PROXY=1; put Fleetdeck behind HTTPS.');
  jobs.start();
  alerts.seed();
  probe.onProbe(alerts.evaluate);
  jobs.onJobDone(alerts.onJobDone);
  probe.start();
  setInterval(() => D.run('DELETE FROM sessions WHERE seen_at < ?', now() - SESSION_MS), 3600e3);

  const server = http.createServer((req, res) => handle(req, res).catch((e) => {
    if (!e.status || e.status >= 500) console.error(e);
    json(res, e.status || 500, { error: e.message });
  }));
  server.on('upgrade', onUpgrade);
  server.listen(PORT, HOST, () => console.log(`  Fleetdeck on http://${HOST.includes(':') ? `[${HOST}]` : HOST}:${PORT}`));
}

module.exports = { validateServer };
if (require.main === module) start();
