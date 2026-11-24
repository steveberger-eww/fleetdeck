'use strict';
// Semaphore-style Git repositories + Ansible playbook runs.
// Repos are shallow clones under DATA_DIR/repos, synced before every run. Ansible runs on the Fleetdeck host,
// with an inventory built from Fleetdeck's servers and known_hosts built from Fleetdeck's pinned host keys.
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { get, run, DATA } = require('./db');
const { encrypt, decrypt } = require('./sec');
const { sudoUsable: sshUsable } = require('./ssh');

const REPOS = path.join(DATA, 'repos');
const GIT_KNOWN_HOSTS = path.join(DATA, 'git_known_hosts');
// Private scratch space for decrypted keys, inventories and vars. Wiped on start, so a crash mid-run
// never leaves a decrypted key behind.
const TMP = path.join(DATA, 'tmp');
fs.rmSync(TMP, { recursive: true, force: true });
fs.mkdirSync(TMP, { recursive: true, mode: 0o700 });
const tempDir = (prefix) => fs.mkdtempSync(path.join(TMP, prefix));
const bad = (msg, status = 400) => Object.assign(new Error(msg), { status });
const shq = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;
const lastLine = (s) => String(s).trim().split('\n').filter(Boolean).pop() || '';
// The useful line of a git failure, not git's generic closing advice.
const gitError = (s) => String(s).split('\n').map((l) => l.trim()).find((l) => /Host key verification failed|Permission denied|fatal:|error:|not found|Could not resolve/i.test(l) && !/Could not read from remote/.test(l)) || lastLine(s);
const HOSTKEY_CHANGED = 'Host key verification failed';

// ---------- validation ----------
const URL_RE = /^(https:\/\/[A-Za-z0-9.-]+(:\d+)?\/\S+|ssh:\/\/[\w.-]+@[A-Za-z0-9.-]+(:\d+)?\/\S+|[\w.-]+@[A-Za-z0-9.-]+:[\w~][\w./~-]*)$/;
const BRANCH_RE = /^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$/;
const PLAYBOOK_RE = /^[A-Za-z0-9_][A-Za-z0-9_./-]*\.ya?ml$/;

function validateRepo(b, existing) {
  const name = String(b.name || '').trim();
  const url = String(b.url || '').trim();
  const branch = String(b.branch || 'main').trim();
  const auth = b.auth || 'none';
  if (!name || name.length > 100) throw bad('Name is required');
  if (!URL_RE.test(url) || url.length > 500) throw bad('Use an https:// URL, ssh://user@host/path, or user@host:path');
  if (!BRANCH_RE.test(branch) || branch.includes('..')) throw bad('Invalid branch name');
  if (!['none', 'ssh', 'token'].includes(auth)) throw bad('Invalid authentication type');
  const isHttps = url.startsWith('https://');
  const out = { name, url, branch, auth, key_id: null, username: null, token_enc: null };
  if (auth === 'ssh') {
    if (isHttps) throw bad('SSH key authentication needs an SSH URL (git@host:org/repo.git)');
    if (!get('SELECT 1 FROM keys WHERE id = ?', b.key_id)) throw bad('Pick an SSH key');
    out.key_id = b.key_id;
  }
  if (auth === 'token') {
    if (!isHttps) throw bad('Token authentication needs an https:// URL');
    out.username = String(b.username || 'git').trim();
    if (!/^[\w.@-]{1,100}$/.test(out.username)) throw bad('Invalid username');
    const token = String(b.token || '');
    if (token) out.token_enc = encrypt(token);
    else if (existing?.token_enc) out.token_enc = existing.token_enc; // blank on edit = keep
    else throw bad('Access token is required');
  }
  return out;
}

const validPlaybook = (p) => PLAYBOOK_RE.test(p) && !p.split('/').includes('..');

// ---------- process helper ----------
const baseEnv = () => ({
  PATH: process.env.PATH, HOME: process.env.HOME || os.homedir(), LANG: process.env.LANG || 'C.UTF-8',
  ...(process.env.SSH_AUTH_SOCK ? { SSH_AUTH_SOCK: process.env.SSH_AUTH_SOCK } : {}),
}); // deliberately not process.env: FLEETDECK_SECRET must not reach git or playbooks

function sh(cmd, args, { cwd, env = baseEnv(), onData, onCancel, timeout = 30 * 60_000 } = {}) {
  return new Promise((resolve) => {
    let out = '';
    const p = spawn(cmd, args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'], detached: true });
    const kill = (sig = 'SIGTERM') => { try { process.kill(-p.pid, sig); } catch { /* gone */ } };
    const timer = setTimeout(() => { onData?.(`\n[timed out after ${timeout / 60_000} min]\n`); kill(); }, timeout);
    onCancel?.(kill);
    const add = (d) => { const s = d.toString(); if (out.length < 4_000_000) out += s; onData?.(s); };
    p.stdout.on('data', add);
    p.stderr.on('data', add);
    p.on('error', (e) => { clearTimeout(timer); resolve({ code: 127, out: e.code === 'ENOENT' ? `${cmd} is not installed on the Fleetdeck host` : e.message }); });
    p.on('close', (code) => { clearTimeout(timer); resolve({ code: code ?? 1, out }); });
  });
}

// Serialize work per repo so a sync never rewrites files under a running playbook.
// ponytail: one run per repo at a time; copy the checkout per job if you need parallel runs from one repo.
const locks = new Map();
function withLock(key, fn) {
  const next = (locks.get(key) || Promise.resolve()).then(fn, fn);
  locks.set(key, next.catch(() => {}));
  return next;
}

// Decrypts a stored key to a 0600 file inside a private temp dir (removed after the run).
async function writeKey(dir, keyId) {
  const file = path.join(dir, `key-${keyId}`);
  if (fs.existsSync(file)) return file;
  const k = get('SELECT private_enc, passphrase_enc FROM keys WHERE id = ?', keyId);
  if (!k) throw new Error('An assigned SSH key no longer exists');
  const pem = decrypt(k.private_enc);
  fs.writeFileSync(file, pem.endsWith('\n') ? pem : pem + '\n', { mode: 0o600 });
  if (k.passphrase_enc) {
    // ponytail: the passphrase is briefly visible to local `ps`; prefer keys without passphrase (Fleetdeck encrypts them at rest).
    const r = await sh('ssh-keygen', ['-p', '-q', '-P', decrypt(k.passphrase_enc), '-N', '', '-f', file]);
    if (r.code !== 0) throw new Error('Could not unlock the passphrase-protected key');
  }
  return file;
}

// ---------- git ----------
const repoDir = (id) => path.join(REPOS, id);

async function gitEnv(repo, tmp) {
  const cfg = [['core.hooksPath', '/dev/null'], ['protocol.file.allow', 'never'], ['submodule.recurse', 'false']];
  if (repo.auth === 'token') cfg.push(['http.extraHeader', `Authorization: Basic ${Buffer.from(`${repo.username}:${decrypt(repo.token_enc)}`).toString('base64')}`]);
  const env = { ...baseEnv(), GIT_TERMINAL_PROMPT: '0', GIT_ALLOW_PROTOCOL: 'https:ssh', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_COUNT: String(cfg.length) };
  cfg.forEach(([k, v], i) => { env[`GIT_CONFIG_KEY_${i}`] = k; env[`GIT_CONFIG_VALUE_${i}`] = v; }); // via env, so tokens never show up in argv
  const key = repo.auth === 'ssh' ? `-i ${shq(await writeKey(tmp, repo.key_id))} -o IdentitiesOnly=yes ` : '';
  env.GIT_SSH_COMMAND = `ssh ${key}-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${shq(GIT_KNOWN_HOSTS)}`;
  return env;
}

async function syncUnlocked(repo, onData = () => {}) {
  const dir = repoDir(repo.id);
  const tmp = tempDir('git-');
  try {
    const env = await gitEnv(repo, tmp);
    let r;
    if (!fs.existsSync(path.join(dir, '.git'))) {
      fs.mkdirSync(REPOS, { recursive: true, mode: 0o700 });
      fs.rmSync(dir, { recursive: true, force: true });
      onData(`$ git clone --depth 1 --branch ${repo.branch} ${repo.url}\n`);
      r = await sh('git', ['clone', '--depth', '1', '--branch', repo.branch, '--single-branch', '--no-recurse-submodules', '--', repo.url, dir], { env, onData });
    } else {
      onData(`$ git fetch --depth 1 origin ${repo.branch}\n`);
      r = await sh('git', ['-C', dir, 'fetch', '--depth', '1', 'origin', repo.branch], { env, onData });
      if (r.code === 0) r = await sh('git', ['-C', dir, 'reset', '-q', '--hard', 'FETCH_HEAD'], { env, onData });
      if (r.code === 0) r = await sh('git', ['-C', dir, 'clean', '-q', '-fdx', '-e', '.fleetdeck'], { env, onData });
    }
    if (r.code !== 0) {
      const msg = gitError(r.out) || 'git failed';
      throw new Error(/REMOTE HOST IDENTIFICATION HAS CHANGED/.test(r.out) ? `${HOSTKEY_CHANGED}: the Git server's host key changed. Verify it, then trust the new key on the repository.` : msg);
    }
    const head = await sh('git', ['-C', dir, 'log', '-1', '--format=%h %s'], { env });
    const commit = head.out.trim().slice(0, 200);
    run('UPDATE repos SET last_sync = ?, last_commit = ?, last_error = NULL WHERE id = ?', Date.now(), commit, repo.id);
    return commit;
  } catch (e) {
    run('UPDATE repos SET last_sync = ?, last_error = ? WHERE id = ?', Date.now(), e.message, repo.id);
    throw e;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

const getRepo = (id) => get('SELECT * FROM repos WHERE id = ?', id);
const sync = (repo, onData) => withLock(repo.id, () => syncUnlocked(getRepo(repo.id) || repo, onData));

async function files(repo) {
  const dir = repoDir(repo.id);
  if (!fs.existsSync(path.join(dir, '.git'))) await sync(repo);
  const r = await sh('git', ['-C', dir, 'ls-files'], { env: { ...baseEnv(), GIT_CONFIG_NOSYSTEM: '1' } });
  // Likely playbooks first: top level and playbooks/, never vars or role internals.
  return r.out.split('\n').filter((f) => validPlaybook(f) && !/(^|\/)(roles|group_vars|host_vars|vars|defaults|templates|files|tasks|handlers|meta|molecule|\.github|\.gitlab)\//.test(f))
    .sort((a, b) => a.split('/').length - b.split('/').length || a.localeCompare(b)).slice(0, 500);
}

// "git@host:path" / "ssh://user@host:port/path" -> known_hosts name; null for https.
function sshHost(url) {
  let m = url.match(/^ssh:\/\/[^@]+@([^:/]+)(?::(\d+))?\//);
  if (m) return m[2] && m[2] !== '22' ? `[${m[1]}]:${m[2]}` : m[1];
  m = url.match(/^[\w.-]+@([A-Za-z0-9.-]+):/);
  return m ? m[1] : null;
}
async function forgetHostKey(repo) {
  const host = sshHost(repo.url);
  if (!host) throw bad('Only SSH repositories have a host key');
  if (fs.existsSync(GIT_KNOWN_HOSTS)) await sh('ssh-keygen', ['-R', host, '-f', GIT_KNOWN_HOSTS]);
  return host;
}

function removeCheckout(id) { fs.rmSync(repoDir(id), { recursive: true, force: true }); }

const repoView = (r) => ({
  id: r.id, name: r.name, url: r.url, branch: r.branch, auth: r.auth, key_id: r.key_id, username: r.username, has_token: !!r.token_enc,
  last_sync: r.last_sync, last_commit: r.last_commit, last_error: r.last_error, created_at: r.created_at,
});

// ---------- ansible ----------
let toolCache = null;
async function tools() {
  if (toolCache && Date.now() - toolCache.t < 60_000) return toolCache.v;
  const ver = async (cmd) => { const r = await sh(cmd, ['--version'], { timeout: 20_000 }); return r.code === 0 ? lastLine(r.out.split('\n')[0]) : null; };
  const v = { git: await ver('git'), ansible: await ver('ansible-playbook') };
  toolCache = { t: Date.now(), v };
  return v;
}

const RECAP = /^(\S+)\s+:\s+ok=(\d+)\s+changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)(?:\s+skipped=(\d+))?/;

// Splits ansible output per host: each host gets the task headers it took part in plus its result lines.
function hostSplitter(aliases) {
  const perHost = new Map([...aliases].map((a) => [a, '']));
  const recap = new Map();
  let task = '';
  let seen = new Set();
  let buf = '';
  let inRecap = false;
  let cont = null; // host whose multi-line result ("=> {" ... "}") is still being printed
  const add = (a, l) => { const out = perHost.get(a); if (out.length < 500_000) perHost.set(a, `${out}${l}\n`); };
  const line = (l) => {
    if (cont) { add(cont, l); if (l === '}') cont = null; return; }
    if (/^(TASK|RUNNING HANDLER|PLAY) \[/.test(l)) { task = l; seen = new Set(); inRecap = false; return; }
    if (l.startsWith('PLAY RECAP')) { inRecap = true; return; }
    if (inRecap) { const m = l.match(RECAP); if (m && perHost.has(m[1])) recap.set(m[1], { ok: +m[2], changed: +m[3], unreachable: +m[4], failed: +m[5] }); return; }
    const m = l.match(/^\w[\w ]*: \[([^\]\s]+)/); // "ok: [web1]", "fatal: [web1 -> localhost]: ..."
    if (m && perHost.has(m[1])) {
      if (!seen.has(m[1])) { add(m[1], task); seen.add(m[1]); }
      add(m[1], l);
      if (l.endsWith('=> {')) cont = m[1];
    }
  };
  return {
    write(d) { buf += d; let i; while ((i = buf.indexOf('\n')) >= 0) { line(buf.slice(0, i)); buf = buf.slice(i + 1); } },
    end() { if (buf) line(buf); return { perHost, recap }; },
  };
}

// Runs a playbook against Fleetdeck servers. Returns { code, commit, alias: Map(serverId -> alias), perHost, recap }.
function runPlaybook({ repoId, playbook, servers, become, vars, check, onData, onCancel }) {
  return withLock(repoId, async () => {
    const repo = getRepo(repoId);
    if (!repo) throw new Error('The repository of this script was deleted');
    if (!validPlaybook(playbook)) throw new Error('Invalid playbook path');
    const commit = await syncUnlocked(repo, onData);
    onData(`At commit ${commit}\n\n`);
    const dir = repoDir(repoId);
    if (!fs.existsSync(path.join(dir, playbook))) throw new Error(`Playbook ${playbook} does not exist on branch ${repo.branch}`);

    const tmp = tempDir('ansible-');
    try {
      const extra = path.join(dir, '.fleetdeck');
      const env = {
        ...baseEnv(), PYTHONUNBUFFERED: '1', ANSIBLE_NOCOLOR: '1', ANSIBLE_FORCE_COLOR: '0', ANSIBLE_RETRY_FILES_ENABLED: 'False',
        ANSIBLE_HOST_KEY_CHECKING: 'True', ANSIBLE_LOCAL_TEMP: path.join(tmp, 'tmp'),
        // Same host-key pinning as the rest of Fleetdeck: only the pinned keys are trusted.
        ANSIBLE_SSH_COMMON_ARGS: `-o StrictHostKeyChecking=yes -o UserKnownHostsFile=${path.join(tmp, 'known_hosts')}`,
        ANSIBLE_COLLECTIONS_PATH: [path.join(extra, 'collections'), '~/.ansible/collections', '/usr/share/ansible/collections'].join(':'),
        ANSIBLE_ROLES_PATH: [path.join(dir, 'roles'), path.join(extra, 'roles'), '~/.ansible/roles', '/usr/share/ansible/roles', '/etc/ansible/roles'].join(':'),
      };
      for (const [kind, file] of [['collection', 'collections/requirements.yml'], ['role', 'roles/requirements.yml']]) {
        if (!fs.existsSync(path.join(dir, file))) continue;
        onData(`$ ansible-galaxy ${kind} install -r ${file}\n`);
        const r = await sh('ansible-galaxy', [kind, 'install', '-r', file, '-p', path.join(extra, `${kind}s`)], { cwd: dir, env, onData, onCancel });
        if (r.code !== 0) throw new Error(`ansible-galaxy ${kind} install failed`);
      }

      const alias = new Map();
      const used = new Set();
      const hosts = {};
      const groups = {};
      const known = [];
      for (const s of servers) {
        if (!s.host_key_raw) { onData(`! Skipping ${s.name}: its host key is not pinned yet. Open the server once so Fleetdeck can connect.\n`); continue; }
        let a = s.name.replace(/[^A-Za-z0-9_.-]/g, '_');
        while (used.has(a)) a += '_';
        used.add(a);
        alias.set(s.id, a);
        known.push(`${s.port === 22 ? s.host : `[${s.host}]:${s.port}`} ${s.host_key_raw}`);
        hosts[a] = { ansible_host: s.host, ansible_port: s.port, ansible_user: s.user };
        if (s.key_id) hosts[a].ansible_ssh_private_key_file = await writeKey(tmp, s.key_id);
        // Lives only in the 0600 inventory inside the private temp dir, deleted after the run.
        if (become && sshUsable(s)) hosts[a].ansible_become_password = decrypt(s.sudo_enc);
        for (const t of (s.tags || '').split(',').filter(Boolean)) ((groups[t.replace(/[^A-Za-z0-9_]/g, '_')] ||= { hosts: {} }).hosts[a] = {});
      }
      if (!alias.size) throw new Error('None of the target servers can be used');
      fs.writeFileSync(path.join(tmp, 'known_hosts'), known.join('\n') + '\n', { mode: 0o600 });
      // JSON is valid YAML, so the yaml inventory plugin reads it; no INI quoting pitfalls.
      fs.writeFileSync(path.join(tmp, 'inventory.yml'), JSON.stringify({ all: { hosts, children: groups, vars: { ansible_become: !!become } } }), { mode: 0o600 });
      fs.writeFileSync(path.join(tmp, 'vars.json'), JSON.stringify(vars || {}), { mode: 0o600 });

      const args = ['-i', path.join(tmp, 'inventory.yml'), '-e', `@${path.join(tmp, 'vars.json')}`, ...(check ? ['--check', '--diff'] : []), playbook];
      onData(`$ ansible-playbook${check ? ' --check --diff' : ''} ${playbook}  (${alias.size} host${alias.size === 1 ? '' : 's'})\n`);
      const split = hostSplitter(alias.values());
      const r = await sh('ansible-playbook', args, { cwd: dir, env, onCancel, onData: (d) => { split.write(d); onData(d); } });
      return { code: r.code, commit, alias, ...split.end() };
    } finally {
      fs.rmSync(tmp, { recursive: true, force: true });
    }
  });
}

module.exports = { validateRepo, validPlaybook, sync, files, removeCheckout, forgetHostKey, sshHost, HOSTKEY_CHANGED, repoView, getRepo, tools, runPlaybook, hostSplitter };
