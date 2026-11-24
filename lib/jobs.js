'use strict';
// Semaphore-style task runner: script library, jobs on many hosts with live output, cron schedules.
const crypto = require('node:crypto');
const { all, get, run, tx, event, setting } = require('./db');
const ssh = require('./ssh');
const repos = require('./repos');
const { encrypt, decrypt } = require('./sec');

const bad = (msg, status = 400) => Object.assign(new Error(msg), { status });
const OUTPUT_CAP = 1_000_000;
const PARALLEL = 8;

const PKG = (apt, dnf) => `export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then ${apt}
elif command -v dnf >/dev/null 2>&1; then ${dnf}
else echo "No apt-get or dnf on this host"; exit 1; fi`;

const BUILTIN = [
  { id: 'update', name: 'Update packages', description: 'Refresh package lists and install all available upgrades.',
    body: PKG('apt-get update && apt-get -y -o Dpkg::Options::=--force-confold upgrade', 'dnf -y upgrade') },
  { id: 'upgrade-packages', name: 'Upgrade selected packages', description: 'Upgrade only the listed packages.',
    params: [{ name: 'PACKAGES', label: 'Packages (space separated)', default: '', required: true }],
    body: PKG('apt-get update && apt-get install -y --only-upgrade -o Dpkg::Options::=--force-confold $PACKAGES', 'dnf -y upgrade $PACKAGES') },
  { id: 'autoremove', name: 'Clean up packages', description: 'Remove unused dependencies and clear the package cache.',
    body: PKG('apt-get -y autoremove && apt-get clean', 'dnf -y autoremove && dnf clean all') },
  { id: 'unattended', name: 'Enable automatic security updates', description: 'unattended-upgrades on Debian/Ubuntu, dnf-automatic on Fedora/RHEL.',
    body: PKG(`apt-get update && apt-get install -y unattended-upgrades && printf 'APT::Periodic::Update-Package-Lists "1";\\nAPT::Periodic::Unattended-Upgrade "1";\\n' > /etc/apt/apt.conf.d/20auto-upgrades && echo enabled`,
      `dnf -y install dnf-automatic && sed -i 's/^apply_updates.*/apply_updates = yes/; s/^upgrade_type.*/upgrade_type = security/' /etc/dnf/automatic.conf && systemctl enable --now dnf-automatic.timer && echo enabled`) },
  { id: 'unattended-off', name: 'Disable automatic updates', description: 'Turns off unattended-upgrades (Debian/Ubuntu) or dnf-automatic (Fedora/RHEL). Packages stay installed.',
    body: PKG(String.raw`grep -l 'Unattended-Upgrade' /etc/apt/apt.conf.d/* 2>/dev/null | xargs -r sed -i 's/\(Unattended-Upgrade\) "[^"]*"/\1 "0"/' && echo disabled`,
      'for t in dnf-automatic.timer dnf-automatic-install.timer dnf5-automatic.timer; do systemctl disable --now "$t" 2>/dev/null; done; echo disabled') },
  { id: 'smartmontools', name: 'Install smartmontools', description: 'Disk health (S.M.A.R.T.) checks for physical disks.',
    body: PKG('apt-get update && apt-get install -y --no-install-recommends smartmontools', 'dnf -y install smartmontools') + '\nsmartctl --scan' },
  { id: 'cockpit', name: 'Install Cockpit', description: 'Install Cockpit and enable its web UI on port 9090.',
    body: PKG('apt-get update && apt-get install -y cockpit', 'dnf -y install cockpit') + '\nsystemctl enable --now cockpit.socket && echo "Cockpit: https://$(hostname -f 2>/dev/null || hostname):9090"' },
  { id: 'restart-services', name: 'Restart services', description: 'Restart several services, e.g. the ones still running old code after updates.',
    params: [{ name: 'SERVICES', label: 'Services (space separated)', default: '', required: true }],
    body: 'rc=0\nfor s in $SERVICES; do echo "== $s"; systemctl restart -- "$s" && echo restarted || rc=1; done\nexit $rc' },
  { id: 'restart-service', name: 'Restart a service', description: 'systemctl restart, then show its status.',
    params: [{ name: 'SERVICE', label: 'Service', default: '', required: true }],
    body: 'systemctl restart -- "$SERVICE" && systemctl status --no-pager -- "$SERVICE"' },
  { id: 'disk-usage', name: 'Find large directories', description: 'Largest directories on a filesystem.',
    params: [{ name: 'PATH_TO_SCAN', label: 'Path', default: '/', required: true }],
    body: 'du -xh --max-depth=2 -- "$PATH_TO_SCAN" 2>/dev/null | sort -rh | head -n 30' },
  { id: 'docker-prune', name: 'Docker cleanup', description: 'Remove stopped containers, dangling images, unused networks and build cache.',
    body: 'docker system prune -f && docker system df' },
  { id: 'authorize-key', name: 'Authorize SSH public key', description: 'Append a public key to a user\'s authorized_keys (e.g. for key rotation).',
    params: [{ name: 'PUBLIC_KEY', label: 'Public key', default: '', required: true }, { name: 'TARGET_USER', label: 'User', default: 'root', required: true }],
    body: `set -e
home=$(getent passwd "$TARGET_USER" | cut -d: -f6)
[ -n "$home" ] || { echo "no such user: $TARGET_USER"; exit 1; }
mkdir -p "$home/.ssh" && touch "$home/.ssh/authorized_keys"
grep -qxF "$PUBLIC_KEY" "$home/.ssh/authorized_keys" && echo "already present" || { echo "$PUBLIC_KEY" >> "$home/.ssh/authorized_keys"; echo added; }
chown -R "$TARGET_USER": "$home/.ssh" && chmod 700 "$home/.ssh" && chmod 600 "$home/.ssh/authorized_keys"` },
  { id: 'reboot', name: 'Reboot', description: 'Reboot the server (in 2 seconds, so the job can report back).',
    body: 'echo "Rebooting $(hostname)…"; nohup sh -c "sleep 2; systemctl reboot" >/dev/null 2>&1 &' },
  { id: 'shutdown', name: 'Shut down', description: 'Power the server off.',
    body: 'echo "Shutting down $(hostname)…"; nohup sh -c "sleep 2; systemctl poweroff" >/dev/null 2>&1 &' },
];

function seed() {
  const now = Date.now();
  for (const s of BUILTIN) {
    if (get('SELECT 1 FROM scripts WHERE id = ?', s.id)) continue;
    run('INSERT INTO scripts (id, name, description, body, as_root, params, builtin, created_at) VALUES (?, ?, ?, ?, 1, ?, 1, ?)',
      s.id, s.name, s.description, s.body, JSON.stringify(s.params || []), now);
  }
}

function validateScript(b) {
  const name = String(b.name || '').trim();
  const kind = b.kind === 'ansible' ? 'ansible' : 'shell';
  const body = kind === 'shell' ? String(b.body || '') : '';
  if (!name || name.length > 100) throw bad('name required (max 100 chars)');
  if (kind === 'shell' && (!body.trim() || body.length > 100_000)) throw bad('script body required (max 100 KB)');
  if (kind === 'ansible') {
    if (!get('SELECT 1 FROM repos WHERE id = ?', b.repo_id)) throw bad('Pick a repository');
    if (!repos.validPlaybook(String(b.playbook || ''))) throw bad('Playbook must be a relative path to a .yml file, e.g. site.yml');
  }
  const params = Array.isArray(b.params) ? b.params : [];
  if (params.length > 20) throw bad('max 20 parameters');
  const vars = [...new Set((Array.isArray(b.vars) ? b.vars : []).map(String))];
  for (const n of vars) if (!get('SELECT 1 FROM variables WHERE name = ?', n)) throw bad(`unknown variable "${n}"`);
  const clash = params.find((p) => vars.includes(String(p.name || '').trim()));
  if (clash) throw bad(`"${clash.name}" is both a parameter and a shared variable`);
  return {
    vars,
    name, body, kind, repo_id: kind === 'ansible' ? b.repo_id : null, playbook: kind === 'ansible' ? String(b.playbook) : null,
    description: String(b.description || '').slice(0, 500), as_root: b.as_root === false || b.as_root === 0 ? 0 : 1,
    params: params.map((p) => {
      const n = String(p.name || '').trim();
      if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(n)) throw bad(`invalid parameter name "${n}" (use letters, digits, underscore)`);
      return { name: n, label: String(p.label || n).slice(0, 100), default: String(p.default ?? '').slice(0, 10_000), required: !!p.required };
    }),
  };
}

const scriptRow = (r) => r && { ...r, params: JSON.parse(r.params), vars: JSON.parse(r.vars || '[]'), as_root: !!r.as_root, builtin: !!r.builtin };

// ---- shared variables ----
const VAR_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
function validateVariable(b, existing) {
  const name = existing ? existing.name : String(b.name || '').trim(); // name is the reference scripts use: fixed once created
  if (!VAR_NAME.test(name)) throw bad('Name: letters, digits and underscore, not starting with a digit');
  if (!existing && get('SELECT 1 FROM variables WHERE name = ?', name)) throw bad(`A variable named ${name} already exists`, 409);
  const secret = b.secret === undefined ? (existing ? !!existing.secret : true) : !!b.secret;
  let value = b.value == null ? '' : String(b.value);
  if (existing && !value && existing.secret) value = decrypt(existing.value_enc); // blank secret on edit = keep
  if (!value) throw bad('Value is required');
  if (value.length > 64_000) throw bad('Value too long (max 64 KB)');
  return { name, secret: secret ? 1 : 0, value_enc: encrypt(value), description: String(b.description || '').slice(0, 300) };
}

// Decrypted only while a job starts; never written to the job row. Secret values are masked in output.
function sharedVars(scriptId) {
  const names = scriptId ? JSON.parse(get('SELECT vars FROM scripts WHERE id = ?', scriptId)?.vars || '[]') : [];
  const vars = {};
  const secrets = [];
  for (const n of names) {
    const v = get('SELECT * FROM variables WHERE name = ?', n);
    if (!v) continue;
    vars[n] = decrypt(v.value_enc);
    if (v.secret && vars[n].length >= 3) secrets.push(vars[n]);
  }
  return { vars, secrets };
}
// ponytail: masks per output chunk; a secret split across two SSH packets slips through. Buffer by line if that matters.
const masker = (secrets) => (s) => secrets.reduce((out, x) => out.split(x).join('••••••'), s);

// Everything needed to start a job from a saved script.
const specFromScript = (sc, params, options = {}) => ({
  scriptId: sc.id, name: sc.name, kind: sc.kind, body: sc.kind === 'ansible' ? sc.playbook : sc.body,
  repoId: sc.repo_id, asRoot: sc.as_root, params, options: sc.kind === 'ansible' ? { check: !!options.check } : {},
});

// ---- targets ----
const tagsOf = (s) => s.tags.split(',').map((t) => t.trim()).filter(Boolean);
function resolveTargets(targets = {}) {
  const ids = new Set(targets.servers || []);
  const tags = new Set(targets.tags || []);
  return all('SELECT * FROM servers ORDER BY name').filter((s) => targets.all || ids.has(s.id) || tagsOf(s).some((t) => tags.has(t)));
}

// ---- jobs ----
const live = new Map(); // jobId -> { runs: Map<serverId, {status, output, cancel}>, subs: Set<fn>, cancelled }
const doneListeners = [];

const emit = (job, msg) => { for (const fn of job.subs) fn(msg); };
function append(r, d) {
  r.output += d;
  if (r.output.length > OUTPUT_CAP) r.output = '[…output truncated…]\n' + r.output.slice(-Math.floor(OUTPUT_CAP * 0.9));
}

const ANSIBLE = '__ansible__'; // pseudo-run holding the full ansible-playbook log

function startJob({ scriptId = null, name, body, kind = 'shell', repoId = null, options = {}, asRoot = true, params = {}, targets, actor }) {
  const servers = resolveTargets(targets);
  if (!servers.length) throw bad('No servers match the selected targets');
  const shared = sharedVars(scriptId);
  const mask = masker(shared.secrets);
  if (kind === 'ansible') return startAnsibleJob({ scriptId, name, playbook: body, repoId, options, asRoot, params, servers, actor, shared, mask });
  const prelude = Object.entries({ ...shared.vars, ...params }).map(([k, v]) => {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k)) throw bad(`invalid parameter name ${k}`);
    return `export ${k}=${ssh.shq(v)}\n`;
  }).join('');
  const jobId = tx(() => {
    const r = run('INSERT INTO jobs (script_id, name, body, as_root, params, status, actor, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)',
      scriptId, name, body, asRoot ? 1 : 0, JSON.stringify(params), 'running', actor, Date.now());
    const id = Number(r.lastInsertRowid);
    for (const s of servers) run('INSERT INTO job_runs (job_id, server_id, server_name, status) VALUES (?, ?, ?, ?)', id, s.id, s.name, 'queued');
    return id;
  });
  const job = { runs: new Map(servers.map((s) => [s.id, { status: 'queued', output: '', cancel: null }])), subs: new Set(), cancelled: false, mask };
  live.set(jobId, job);
  event('job', `Job #${jobId} "${name}" started on ${servers.map((s) => s.name).join(', ')}`, { actor });

  (async () => {
    const queue = [...servers];
    const worker = async () => { for (let s; (s = queue.shift());) await runOne(jobId, job, s, prelude + body, asRoot); };
    await Promise.all(Array.from({ length: Math.min(PARALLEL, servers.length) }, worker));
    finishJob(jobId, job, name, actor);
  })();
  return jobId;
}

const saveRun = (jobId, serverId, r, code = null) => run('UPDATE job_runs SET status = ?, exit_code = ?, output = ?, started_at = COALESCE(started_at, ?), finished_at = ? WHERE job_id = ? AND server_id = ?',
  r.status, code, r.output, Date.now(), r.status === 'running' ? null : Date.now(), jobId, serverId);

function finishJob(jobId, job, name, actor) {
  const statuses = [...job.runs.values()].map((r) => r.status);
  const status = job.cancelled ? 'cancelled' : statuses.every((x) => x === 'success' || x === 'skipped') ? 'success' : 'failed';
  run('UPDATE jobs SET status = ?, finished_at = ? WHERE id = ?', status, Date.now(), jobId);
  emit(job, { type: 'job', status });
  live.delete(jobId);
  for (const fn of doneListeners) fn({ id: jobId, name, status, actor, runs: statuses });
}

function startAnsibleJob({ scriptId, name, playbook, repoId, options, asRoot, params, servers, actor, shared, mask }) {
  const jobId = tx(() => {
    const r = run('INSERT INTO jobs (script_id, name, body, as_root, params, status, actor, created_at, kind, repo_id, options) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
      scriptId, name, playbook, asRoot ? 1 : 0, JSON.stringify(params), 'running', actor, Date.now(), 'ansible', repoId, JSON.stringify(options));
    const id = Number(r.lastInsertRowid);
    run('INSERT INTO job_runs (job_id, server_id, server_name, status) VALUES (?, ?, ?, ?)', id, ANSIBLE, 'ansible-playbook', 'queued');
    for (const s of servers) run('INSERT INTO job_runs (job_id, server_id, server_name, status) VALUES (?, ?, ?, ?)', id, s.id, s.name, 'queued');
    return id;
  });
  const job = { runs: new Map([[ANSIBLE, { status: 'queued', output: '', cancel: null }], ...servers.map((s) => [s.id, { status: 'queued', output: '', cancel: null }])]), subs: new Set(), cancelled: false, mask };
  live.set(jobId, job);
  event('job', `Job #${jobId} "${name}" (ansible ${playbook}${options.check ? ', dry run' : ''}) started on ${servers.map((s) => s.name).join(', ')}`, { actor });

  (async () => {
    const main = job.runs.get(ANSIBLE);
    const set = (id, status, code = null) => { const r = job.runs.get(id); r.status = status; saveRun(jobId, id, r, code); emit(job, { type: 'run', server: id, status, code }); };
    const out = (d) => { d = mask(d); append(main, d); emit(job, { type: 'data', server: ANSIBLE, data: d }); };
    for (const id of job.runs.keys()) set(id, 'running');
    let res = null;
    try {
      // Connect once to servers that were never reached, so their host keys are pinned for known_hosts.
      await Promise.all(servers.filter((s) => !s.host_key_raw).map((s) => ssh.exec(s, 'true', { mode: 'user', timeout: 20_000 }).catch(() => {})));
      const fresh = servers.map((s) => get('SELECT * FROM servers WHERE id = ?', s.id) || s);
      res = await repos.runPlaybook({ repoId, playbook, servers: fresh, become: asRoot, vars: { ...shared.vars, ...params }, check: options.check, onData: out,
        // Cancelled while waiting for the repo lock or syncing: kill galaxy/ansible-playbook as soon as they spawn.
        onCancel: (c) => { main.cancel = c; if (job.cancelled) c(); } });
      if (res.commit) run('UPDATE jobs SET commit_sha = ? WHERE id = ?', res.commit, jobId);
    } catch (e) {
      out(`\n[error: ${e.message}]\n`);
    }
    for (const s of servers) {
      const r = job.runs.get(s.id);
      const a = res?.alias.get(s.id);
      const st = a && res.recap.get(a);
      r.output = mask((a && res.perHost.get(a)) || '(no output for this server: see the ansible-playbook log)');
      if (st) r.output += `\nok=${st.ok} changed=${st.changed} unreachable=${st.unreachable} failed=${st.failed}\n`;
      set(s.id, job.cancelled ? 'cancelled' : st ? (st.failed || st.unreachable ? 'failed' : 'success') : res?.code === 0 ? 'skipped' : 'failed');
    }
    set(ANSIBLE, job.cancelled ? 'cancelled' : res?.code === 0 ? 'success' : 'failed', res?.code ?? null);
    finishJob(jobId, job, name, actor);
  })();
  return jobId;
}

// A run whose connection drops mid-script (no exit code) may just be the server rebooting, e.g. after a kernel
// update. Wait for it to come back: an uptime shorter than the run means it rebooted, and the run counts as done.
const REBOOT_WAIT = 10 * 60_000;
const bootedAfter = (uptimeOut, since, now = Date.now()) => parseFloat(uptimeOut) * 1000 < now - since;

async function rebootedSince(server, since, job) {
  for (const end = Date.now() + REBOOT_WAIT; Date.now() < end && !job.cancelled;) {
    await new Promise((r) => setTimeout(r, 10_000));
    try {
      const { code, out } = await ssh.exec(server, 'cat /proc/uptime', { mode: 'user', timeout: 15_000 });
      if (code === 0) return bootedAfter(out, since);
    } catch { /* still down */ }
  }
  return false;
}

async function runOne(jobId, job, server, script, asRoot) {
  const r = job.runs.get(server.id);
  const save = (code = null) => saveRun(jobId, server.id, r, code);
  if (job.cancelled) { r.status = 'cancelled'; save(); emit(job, { type: 'run', server: server.id, status: r.status }); return; }
  r.status = 'running';
  save();
  emit(job, { type: 'run', server: server.id, status: 'running' });
  const note = (msg) => { append(r, msg); emit(job, { type: 'data', server: server.id, data: msg }); };
  const started = Date.now();
  let code = null;
  let rebooted = false;
  try {
    ({ code } = await ssh.exec(server, script, {
      mode: asRoot ? 'root' : 'user', maxOutput: 0,
      onCancel: (c) => { r.cancel = c; if (job.cancelled) c(); }, // cancelled while connecting
      onData: (d) => { d = job.mask(d); append(r, d); emit(job, { type: 'data', server: server.id, data: d }); },
    }));
    if (code === null && !job.cancelled) {
      note(`\n[connection lost before the script finished; waiting up to ${REBOOT_WAIT / 60_000} min in case ${server.name} is rebooting…]\n`);
      rebooted = await rebootedSince(server, started, job);
      note(rebooted ? `[${server.name} rebooted and is back up: counting this run as successful. Check the output above to be sure.]\n`
        : `[${server.name} did not come back with a fresh boot: the script may not have finished.]\n`);
    }
  } catch (e) {
    note(`[connection error: ${e.message}]\n`);
  }
  if (code !== 0 && /sudo: (a password is required|a terminal is required)/.test(r.output)) {
    note('[hint: the SSH user needs passwordless sudo, or store a sudo password in the server settings.]\n');
  }
  r.status = job.cancelled ? 'cancelled' : code === 0 || rebooted ? 'success' : 'failed';
  save(code);
  emit(job, { type: 'run', server: server.id, status: r.status, code });
}

function getJob(id) {
  const j = get('SELECT * FROM jobs WHERE id = ?', id);
  if (!j) return null;
  const runs = all('SELECT * FROM job_runs WHERE job_id = ? ORDER BY server_name', id).map((r) => ({ ...r }));
  const l = live.get(Number(id));
  if (l) for (const r of runs) Object.assign(r, { status: l.runs.get(r.server_id).status, output: l.runs.get(r.server_id).output });
  return { ...j, params: JSON.parse(j.params), options: JSON.parse(j.options || '{}'), runs };
}

function listJobs({ serverId, limit = 50, before, status } = {}) {
  const lim = Math.min(Math.max(Math.floor(Number(limit)) || 50, 1), 200);
  const where = [];
  const p = [];
  if (status) { where.push('status = ?'); p.push(String(status)); }
  if (serverId) { where.push('id IN (SELECT job_id FROM job_runs WHERE server_id = ?)'); p.push(serverId); }
  if (before) { where.push('id < ?'); p.push(Number(before)); }
  const rows = all(`SELECT id, script_id, name, kind, status, actor, created_at, finished_at FROM jobs ${where.length ? 'WHERE ' + where.join(' AND ') : ''} ORDER BY id DESC LIMIT ${lim}`, ...p);
  return rows.map((j) => ({
    ...j,
    runs: all("SELECT server_id, server_name, status, exit_code FROM job_runs WHERE job_id = ? AND server_id != '__ansible__'", j.id).map((r) => {
      const l = live.get(j.id)?.runs.get(r.server_id);
      return { ...r, status: l ? l.status : r.status };
    }),
  }));
}

function subscribe(id, fn) {
  const job = live.get(Number(id));
  if (!job) return null;
  job.subs.add(fn);
  return () => job.subs.delete(fn);
}

function cancelJob(id) {
  const job = live.get(Number(id));
  if (!job) return false;
  job.cancelled = true;
  for (const r of job.runs.values()) r.cancel?.();
  return true;
}

// ---- cron ----
const MACROS = { '@hourly': '0 * * * *', '@daily': '0 0 * * *', '@midnight': '0 0 * * *', '@weekly': '0 0 * * 0', '@monthly': '0 0 1 * *', '@yearly': '0 0 1 1 *', '@annually': '0 0 1 1 *' };
const LIMITS = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]];

function parseCron(expr) {
  const src = String(expr || '').trim();
  const parts = (MACROS[src] || src).split(/\s+/);
  if (parts.length !== 5) throw bad('Cron needs 5 fields: minute hour day-of-month month day-of-week');
  const sets = parts.map((p, i) => {
    const [lo, hi] = LIMITS[i];
    const set = new Set();
    for (const item of p.split(',')) {
      const m = item.match(/^(\*|(\d+)(?:-(\d+))?)(?:\/(\d+))?$/);
      if (!m) throw bad(`Invalid cron field "${item}"`);
      const star = m[1] === '*';
      const a = star ? lo : +m[2];
      const b = star ? hi : m[3] !== undefined ? +m[3] : m[4] ? hi : a;
      const step = m[4] ? +m[4] : 1;
      if (a < lo || b > hi || a > b || step < 1) throw bad(`Cron field "${item}" is out of range ${lo}-${hi}`);
      for (let v = a; v <= b; v += step) set.add(i === 4 && v === 7 ? 0 : v);
    }
    return set;
  });
  const [min, hour, dom, mon, dow] = sets;
  const domAny = parts[2] === '*';
  const dowAny = parts[4] === '*';
  return (d) => min.has(d.getMinutes()) && hour.has(d.getHours()) && mon.has(d.getMonth() + 1) &&
    (domAny || dowAny ? dom.has(d.getDate()) && dow.has(d.getDay()) : dom.has(d.getDate()) || dow.has(d.getDay()));
}

function nextRuns(expr, n = 5, from = Date.now()) {
  const match = parseCron(expr);
  const d = new Date(from);
  d.setSeconds(0, 0);
  const out = [];
  for (let i = 0; i < 366 * 1440 && out.length < n; i++) {
    d.setMinutes(d.getMinutes() + 1);
    if (match(d)) out.push(d.getTime());
  }
  return out;
}

// Schedules and webhook triggers share this shape; triggers have no cron.
function validateSchedule(b, { cron = true } = {}) {
  const name = String(b.name || '').trim();
  if (!name || name.length > 100) throw bad('name required');
  if (!get('SELECT 1 FROM scripts WHERE id = ?', b.script_id)) throw bad('unknown script');
  if (cron) parseCron(b.cron);
  const t = b.targets || {};
  const targets = { all: !!t.all, servers: (t.servers || []).map(String), tags: (t.tags || []).map(String) };
  if (!targets.all && !targets.servers.length && !targets.tags.length) throw bad('pick at least one target');
  return { name, script_id: b.script_id, cron: cron ? String(b.cron).trim() : null, targets, params: b.params || {}, enabled: b.enabled === false ? 0 : 1 };
}

// Start a saved schedule or trigger row (script_id, params and targets as JSON) and record it as its last run.
function runSaved(table, s, actor) {
  const script = scriptRow(get('SELECT * FROM scripts WHERE id = ?', s.script_id));
  const defaults = Object.fromEntries(script.params.map((p) => [p.name, p.default]));
  const id = startJob({
    ...specFromScript(script, { ...defaults, ...JSON.parse(s.params) }),
    name: `${script.name} · ${s.name}`, targets: JSON.parse(s.targets), actor,
  });
  run(`UPDATE ${table} SET last_run = ?, last_job = ? WHERE id = ?`, Date.now(), id, s.id);
  return id;
}
const runSchedule = (s, actor) => runSaved('schedules', s, actor || `schedule: ${s.name}`);
const runTrigger = (t) => runSaved('triggers', t, `webhook: ${t.name}`);

function start() {
  seed();
  run("UPDATE job_runs SET status = 'interrupted' WHERE status IN ('running', 'queued')");
  run("UPDATE jobs SET status = 'interrupted', finished_at = ? WHERE status = 'running'", Date.now());
  const tick = () => {
    const now = new Date();
    now.setSeconds(0, 0);
    for (const s of all('SELECT * FROM schedules WHERE enabled = 1')) {
      try {
        if (!parseCron(s.cron)(now)) continue;
        // No pile-ups: a slow or hung run blocks the next one instead of stacking more jobs on the same hosts.
        if (s.last_job && live.has(s.last_job)) {
          event('schedule', `Schedule "${s.name}" skipped: job #${s.last_job} from its previous run is still running`, { level: 'warn' });
          continue;
        }
        runSchedule(s);
      } catch (e) {
        event('schedule', `Schedule "${s.name}" failed to start: ${e.message}`, { level: 'error' });
      }
    }
    setTimeout(tick, 60_000 - (Date.now() % 60_000) + 100);
  };
  setTimeout(tick, 60_000 - (Date.now() % 60_000) + 100);
  setInterval(() => {
    run('DELETE FROM jobs WHERE created_at < ?', Date.now() - Number(setting('retention_jobs_days', 90)) * 864e5);
    run('DELETE FROM events WHERE t < ?', Date.now() - Number(setting('retention_events_days', 180)) * 864e5);
  }, 3600e3);
}

module.exports = {
  BUILTIN, seed, bootedAfter, validateScript, validateVariable, masker, scriptRow, specFromScript, ANSIBLE, resolveTargets, startJob, getJob, listJobs, subscribe, cancelJob,
  parseCron, nextRuns, validateSchedule, runSchedule, runTrigger, isRunning: (id) => live.has(Number(id)), start, onJobDone: (fn) => doneListeners.push(fn),
  newId: () => crypto.randomUUID(),
};
