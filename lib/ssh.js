'use strict';
// SSH layer: one pooled ssh2 connection per server, host keys pinned on first use,
// private keys decrypted only in memory. Scripts are piped over stdin to `sh -s`, so
// the remote login shell (bash, zsh, fish...) and quoting never matter.
const { Client, utils } = require('ssh2');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { StringDecoder } = require('node:string_decoder');
const { get, run, event } = require('./db');
const { decrypt } = require('./sec');
const { notify } = require('./alerts');

const IDLE_MS = 5 * 60_000;
const MAX_CHANNELS = 6; // OpenSSH's default MaxSessions is 10
const pool = new Map(); // serverId -> { ready, conn, active, queue, idle }
const mismatches = new Map(); // serverId -> fingerprint the server offered instead of the pinned one

const shq = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;
const fingerprint = (key) => 'SHA256:' + crypto.createHash('sha256').update(key).digest('base64').replace(/=+$/, '');

const WRAP = {
  user: 'sh -s',
  // Non-root users need passwordless sudo; `sudo -n` fails fast instead of hanging on a prompt.
  root: `sh -c 'if [ "$(id -u)" = 0 ]; then exec sh -s; else exec sudo -n sh -s; fi'`,
  // Use root when available, otherwise fall back to the login user (probe: more info, never fails on sudo).
  auto: `sh -c 'if [ "$(id -u)" != 0 ] && sudo -n true 2>/dev/null; then exec sudo -n sh -s; else exec sh -s; fi'`,
};

// "ssh-ed25519 AAAA..." from the wire-format key, for known_hosts files (Ansible).
const rawKey = (key) => `${key.subarray(4, 4 + key.readUInt32BE(0)).toString()} ${key.toString('base64')}`;

// ---- sudo with a stored password ----
// The password is the first stdin line, read into a shell variable (never argv, never a file), and piped
// to `sudo -S` for exactly one attempt. The script goes to a 0600 temp file so a wrong password can't
// make sudo consume script lines as further attempts. NOPASSWD rules still win via `sudo -n`.
const SUDO_PW = (onReject) => `sh -c 'IFS= read -r p; if [ "$(id -u)" = 0 ]; then unset p; exec sh -s; fi; \
f=$(mktemp) || exit 1; trap "rm -f \\"\\$f\\"" EXIT; cat > "$f"; \
if sudo -n true 2>/dev/null; then unset p; sudo -n sh "$f"; \
else printf "%s\\n" "$p" | sudo -k -S -p "" sh "$f" ${onReject}; fi'`;
const WRAP_PW = { root: SUDO_PW(''), auto: SUDO_PW('|| sh "$f"') };
const rejected = new Map(); // serverId -> sudo_enc that the server refused; skip it until it changes

function sudoPassword(server, mode) {
  if (mode === 'user' || !server.sudo_enc || rejected.get(server.id) === server.sudo_enc) return null;
  const pw = decrypt(server.sudo_enc);
  return /[\r\n]/.test(pw) ? null : pw;
}

function verifyHost(id, key) {
  const fp = fingerprint(key);
  const raw = rawKey(key);
  const row = get('SELECT name, host_key, host_key_raw FROM servers WHERE id = ?', id);
  if (!row) return false;
  if (!row.host_key) {
    run('UPDATE servers SET host_key = ?, host_key_raw = ? WHERE id = ?', fp, raw, id);
    event('security', `Pinned host key ${fp} for ${row.name}`, { serverId: id });
    return true;
  }
  if (row.host_key === fp) {
    mismatches.delete(id);
    if (!row.host_key_raw) run('UPDATE servers SET host_key_raw = ? WHERE id = ?', raw, id);
    return true;
  }
  if (mismatches.get(id)?.fp !== fp) {
    event('security', `Host key CHANGED for ${row.name}: pinned ${row.host_key}, offered ${fp}. Connection refused.`, { level: 'error', serverId: id });
    notify(`🔴 ${row.name}: host key changed`, `Pinned ${row.host_key}, offered ${fp}. Fleetdeck refuses to connect until you trust the new key in the server settings.`, 'error');
  }
  mismatches.set(id, { fp, raw });
  return false;
}

function defaultKey() {
  for (const n of ['id_ed25519', 'id_ecdsa', 'id_rsa']) {
    const f = path.join(os.homedir(), '.ssh', n);
    try {
      const data = fs.readFileSync(f);
      if (!(utils.parseKey(data) instanceof Error)) return data; // skips passphrase-protected keys
    } catch { /* missing */ }
  }
  return null;
}

function credentials(server) {
  if (server.key_id) {
    const k = get('SELECT private_enc, passphrase_enc FROM keys WHERE id = ?', server.key_id);
    if (!k) throw new Error('assigned key no longer exists');
    return { privateKey: decrypt(k.private_enc), passphrase: k.passphrase_enc ? decrypt(k.passphrase_enc) : undefined };
  }
  const cfg = {};
  if (process.env.SSH_AUTH_SOCK) cfg.agent = process.env.SSH_AUTH_SOCK;
  const key = defaultKey();
  if (key) cfg.privateKey = key;
  if (!cfg.agent && !cfg.privateKey) throw new Error('no key assigned and no ssh-agent or ~/.ssh key available');
  return cfg;
}

function connect(server) {
  return new Promise((resolve, reject) => {
    const conn = new Client();
    conn.on('ready', () => resolve(conn));
    conn.on('error', (e) => {
      const fp = mismatches.get(server.id)?.fp;
      reject(fp && /verification|Host denied/i.test(e.message) ? new Error(`HOST KEY CHANGED (offered ${fp}). Verify it, then accept it in server settings.`) : e);
    });
    try {
      conn.connect({
        host: server.host, port: server.port, username: server.user,
        readyTimeout: 15_000, keepaliveInterval: 30_000, keepaliveCountMax: 3,
        hostVerifier: (key) => verifyHost(server.id, key),
        ...credentials(server),
      });
    } catch (e) { reject(e); }
  });
}

function drop(id) {
  const e = pool.get(id);
  pool.delete(id);
  if (e) { clearTimeout(e.idle); e.ready.then((c) => c.end(), () => {}); }
}

function entry(server) {
  let e = pool.get(server.id);
  if (!e) {
    e = { active: 0, queue: [], idle: null };
    e.ready = connect(server).then((conn) => {
      e.conn = conn;
      conn.on('close', () => { if (pool.get(server.id) === e) pool.delete(server.id); });
      conn.on('error', () => {});
      return conn;
    });
    e.ready.catch(() => { if (pool.get(server.id) === e) pool.delete(server.id); });
    pool.set(server.id, e);
  }
  return e;
}

async function withConn(server, fn) {
  const e = entry(server);
  const conn = await e.ready;
  clearTimeout(e.idle);
  if (e.active >= MAX_CHANNELS) await new Promise((r) => e.queue.push(r)); // slot is handed over, not re-counted
  else e.active++;
  try {
    return await fn(conn);
  } finally {
    const next = e.queue.shift();
    if (next) next();
    else if (--e.active === 0) e.idle = setTimeout(() => drop(server.id), IDLE_MS);
  }
}

function execOnce(server, script, { mode = 'root', onData, timeout = 0, onCancel, maxOutput = 2_000_000 }) {
  if (mode === 'root' && server.sudo_enc && rejected.get(server.id) === server.sudo_enc) {
    const msg = 'The stored sudo password was rejected by this server. Update it in the server settings.\n';
    onData?.(msg);
    return Promise.resolve({ code: 1, out: msg });
  }
  const pw = sudoPassword(server, mode);
  let seen = '';
  return withConn(server, (conn) => new Promise((resolve, reject) => {
    conn.exec(pw ? WRAP_PW[mode] : WRAP[mode], (err, stream) => {
      if (err) return reject(err);
      let out = '';
      let code = null;
      let timer = null;
      const add = (s) => { if (!s) return; if (pw && seen.length < 4096) seen += s; if (out.length < maxOutput) out += s; onData?.(s); };
      const dOut = new StringDecoder('utf8');
      const dErr = new StringDecoder('utf8');
      stream.on('data', (d) => add(dOut.write(d)));
      stream.stderr.on('data', (d) => add(dErr.write(d)));
      stream.on('exit', (c, sig) => { code = c ?? (sig ? 128 : null); });
      stream.on('close', () => {
        clearTimeout(timer);
        add(dOut.end()); add(dErr.end());
        if (pw && /incorrect password attempt|Sorry, try again/.test(seen)) {
          rejected.set(server.id, server.sudo_enc);
          event('security', `${server.name} rejected the stored sudo password; Fleetdeck stops using it until it is changed`, { level: 'warn', serverId: server.id });
        }
        resolve({ code, out });
      });
      // ponytail: closing the channel stops output, but a remote process without a PTY may keep running.
      const cancel = () => { try { stream.signal('KILL'); } catch {} stream.close(); };
      if (timeout) timer = setTimeout(() => { add(`\n[timed out after ${Math.round(timeout / 1000)}s]\n`); cancel(); }, timeout);
      onCancel?.(cancel);
      stream.end((pw ? `${pw}\n` : '') + (script.endsWith('\n') ? script : script + '\n'));
    });
  }));
}

// Retries once when a pooled connection died silently between uses.
async function exec(server, script, opts = {}) {
  try {
    return await execOnce(server, script, opts);
  } catch (e) {
    if (!/Not connected|No response|ECONNRESET|channel/i.test(e.message)) throw e;
    drop(server.id);
    return execOnce(server, script, opts);
  }
}

function shell(server, { cols, rows }, onStream) {
  return withConn(server, (conn) => new Promise((resolve, reject) => {
    conn.shell({ term: 'xterm-256color', cols, rows }, (err, stream) => {
      if (err) return reject(err);
      onStream(stream);
      stream.on('close', resolve);
    });
  }));
}

function parsePrivateKey(pem, passphrase) {
  const k = utils.parseKey(pem, passphrase || undefined);
  if (k instanceof Error) throw Object.assign(new Error(`Invalid key: ${k.message}`), { status: 400 });
  const key = Array.isArray(k) ? k[0] : k;
  if (!key.isPrivateKey()) throw Object.assign(new Error('That is a public key; paste the private key'), { status: 400 });
  return `${key.type} ${key.getPublicSSH().toString('base64')}`;
}

function generateKey(comment) {
  const k = utils.generateKeyPairSync('ed25519', { comment });
  return { privateKey: k.private, publicKey: k.public.trim() };
}

const status = (s) => ({ connected: !!pool.get(s.id)?.conn, hostKeyMismatch: mismatches.get(s.id)?.fp || null,
  sudoRejected: !!s.sudo_enc && rejected.get(s.id) === s.sudo_enc });
const sudoUsable = (s) => !!s.sudo_enc && rejected.get(s.id) !== s.sudo_enc;
const acceptHostKey = (id) => {
  const m = mismatches.get(id);
  if (!m) return null;
  run('UPDATE servers SET host_key = ?, host_key_raw = ? WHERE id = ?', m.fp, m.raw, id);
  mismatches.delete(id);
  drop(id);
  return m.fp;
};

module.exports = { exec, shell, drop, shq, parsePrivateKey, generateKey, status, acceptHostKey, fingerprint, sudoUsable, WRAP_PW };
