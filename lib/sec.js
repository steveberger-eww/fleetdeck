'use strict';
// Crypto helpers: secrets at rest (AES-256-GCM), password hashing (scrypt), TOTP (RFC 6238).
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { DATA, get, setting, setSetting } = require('./db');

function loadMasterKey() {
  if (process.env.FLEETDECK_SECRET) return crypto.scryptSync(process.env.FLEETDECK_SECRET, 'fleetdeck-master', 32);
  const file = path.join(DATA, 'secret.key');
  if (!fs.existsSync(file)) fs.writeFileSync(file, crypto.randomBytes(32).toString('base64'), { mode: 0o600 });
  return Buffer.from(fs.readFileSync(file, 'utf8').trim(), 'base64');
}
const MASTER = loadMasterKey();

function encrypt(plain) {
  const iv = crypto.randomBytes(12);
  const c = crypto.createCipheriv('aes-256-gcm', MASTER, iv);
  const ct = Buffer.concat([c.update(String(plain), 'utf8'), c.final()]);
  return 'v1:' + Buffer.concat([iv, c.getAuthTag(), ct]).toString('base64');
}

function decrypt(blob) {
  const raw = Buffer.from(String(blob).slice(3), 'base64');
  const d = crypto.createDecipheriv('aes-256-gcm', MASTER, raw.subarray(0, 12));
  d.setAuthTag(raw.subarray(12, 28));
  return Buffer.concat([d.update(raw.subarray(28)), d.final()]).toString('utf8');
}

function hashPassword(pw) {
  const salt = crypto.randomBytes(16);
  const hash = crypto.scryptSync(pw, salt, 32, { N: 32768, r: 8, p: 1, maxmem: 64 * 1024 * 1024 });
  return `scrypt$32768$${salt.toString('base64')}$${hash.toString('base64')}`;
}

function verifyPassword(pw, stored) {
  const [alg, n, salt, hash] = String(stored || '').split('$');
  if (alg !== 'scrypt') return false;
  const want = Buffer.from(hash, 'base64');
  const got = crypto.scryptSync(String(pw), Buffer.from(salt, 'base64'), want.length, { N: Number(n), r: 8, p: 1, maxmem: 64 * 1024 * 1024 });
  return crypto.timingSafeEqual(got, want);
}

// Refuse to start with a different master key than the data was encrypted with (e.g. FLEETDECK_SECRET added later):
// otherwise every stored key and token would fail to decrypt one by one at runtime.
(function checkMasterKey() {
  // Data from before this check has no canary: verify against any existing secret instead.
  const canary = setting('key_check') || get(`SELECT private_enc v FROM keys UNION ALL SELECT config_enc FROM notifiers
    UNION ALL SELECT token_enc FROM repos WHERE token_enc IS NOT NULL UNION ALL SELECT value FROM settings WHERE key = 'totp_secret' LIMIT 1`)?.v;
  try {
    if (canary) decrypt(canary);
    if (!setting('key_check')) setSetting('key_check', encrypt('fleetdeck'));
  } catch {
    throw new Error(process.env.FLEETDECK_SECRET
      ? 'FLEETDECK_SECRET does not match the key this data was encrypted with. Use the previous secret (or unset it to use data/secret.key).'
      : `${path.join(DATA, 'secret.key')} does not match the key this data was encrypted with. Restore the original secret.key or set the FLEETDECK_SECRET that was used.`);
  }
})();

const token = () => crypto.randomBytes(32).toString('base64url');
const sha256 = (s) => crypto.createHash('sha256').update(s).digest('hex');

const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
function base32(buf) {
  let bits = 0, value = 0, out = '';
  for (const byte of buf) {
    value = (value << 8) | byte; bits += 8;
    while (bits >= 5) { out += B32[(value >>> (bits - 5)) & 31]; bits -= 5; }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}
function unbase32(str) {
  let bits = 0, value = 0;
  const out = [];
  for (const ch of str.replace(/=+$/, '').toUpperCase()) {
    const i = B32.indexOf(ch);
    if (i < 0) continue;
    value = (value << 5) | i; bits += 5;
    if (bits >= 8) { out.push((value >>> (bits - 8)) & 255); bits -= 8; }
  }
  return Buffer.from(out);
}

function totp(secret, counter) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(counter));
  const h = crypto.createHmac('sha1', unbase32(secret)).update(msg).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0');
}

// Returns the matched counter (so callers can refuse reuse) or null. Accepts ±1 step of clock drift.
function verifyTotp(secret, code, now = Date.now()) {
  const c = Math.floor(now / 30000);
  for (const d of [0, -1, 1]) {
    const want = totp(secret, c + d);
    if (code && crypto.timingSafeEqual(Buffer.from(want), Buffer.from(String(code).padStart(6, '0').slice(0, 6)))) return c + d;
  }
  return null;
}

const newTotpSecret = () => base32(crypto.randomBytes(20));

module.exports = { encrypt, decrypt, hashPassword, verifyPassword, token, sha256, totp, verifyTotp, newTotpSecret, base32 };
