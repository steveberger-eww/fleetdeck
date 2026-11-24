'use strict';
// Cockpit-lite: services, journal, packages, processes and Docker on one server.
// Every name that reaches a remote command is allowlisted AND shell-quoted.
const ssh = require('./ssh');

const bad = (msg) => Object.assign(new Error(msg), { status: 400 });
const q = ssh.shq;
const UNIT = /^[A-Za-z0-9@._:\\-]{1,200}$/;
const CONTAINER = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;

async function sh(server, script, opts = {}) {
  const { code, out } = await ssh.exec(server, script, { mode: 'root', timeout: 60_000, ...opts });
  if (code !== 0 && !opts.allowFail) throw Object.assign(new Error(out.trim().split('\n').slice(-3).join(' ') || `exit ${code}`), { status: 502 });
  return out;
}

// ---- services ----
async function services(server) {
  const out = await sh(server, `systemctl list-units --type=service --all --no-legend --plain --no-pager
echo '---'
systemctl list-unit-files --type=service --no-legend --no-pager`);
  const [units, files] = out.split('\n---\n');
  const enabled = new Map();
  for (const l of (files || '').split('\n')) {
    const [unit, state] = l.trim().split(/\s+/);
    if (unit) enabled.set(unit, state);
  }
  const list = new Map();
  for (const l of units.split('\n')) {
    const m = l.trim().match(/^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*)$/);
    if (m && m[1].endsWith('.service')) list.set(m[1], { unit: m[1], load: m[2], active: m[3], sub: m[4], description: m[5], enabled: enabled.get(m[1]) || '' });
  }
  // Installed but never loaded units only show up in list-unit-files.
  for (const [unit, state] of enabled) if (!list.has(unit) && !unit.includes('@.')) list.set(unit, { unit, load: 'loaded', active: 'inactive', sub: 'dead', description: '', enabled: state });
  return [...list.values()].sort((a, b) => a.unit.localeCompare(b.unit));
}

const SERVICE_ACTIONS = ['start', 'stop', 'restart', 'reload', 'enable', 'disable'];
function serviceAction(server, unit, action) {
  if (!UNIT.test(unit)) throw bad('invalid unit name');
  if (!SERVICE_ACTIONS.includes(action)) throw bad('invalid action');
  return sh(server, `systemctl ${action} -- ${q(unit)} 2>&1`);
}

function serviceStatus(server, unit) {
  if (!UNIT.test(unit)) throw bad('invalid unit name');
  return sh(server, `SYSTEMD_COLORS=0 systemctl status --no-pager --lines=40 --full -- ${q(unit)}`, { allowFail: true });
}

// ---- journal ----
const SINCE = { '15m': '15 min ago', '1h': '1 hour ago', '6h': '6 hours ago', '24h': '1 day ago', '7d': '7 days ago' };
async function logs(server, { unit, priority, since, lines }) {
  const args = ['--no-pager', '-o', 'json', '-n', String(Math.min(Math.max(Number(lines) || 300, 10), 5000))];
  if (unit) { if (!UNIT.test(unit)) throw bad('invalid unit name'); args.push('-u', unit); }
  if (priority !== undefined && priority !== '') { if (!/^[0-7]$/.test(priority)) throw bad('invalid priority'); args.push('-p', priority); }
  if (since) { if (!SINCE[since]) throw bad('invalid range'); args.push('--since', SINCE[since]); }
  const out = await sh(server, `journalctl ${args.map(q).join(' ')}`, { allowFail: true, maxOutput: 8_000_000 });
  const entries = [];
  for (const line of out.split('\n')) {
    if (!line.startsWith('{')) continue;
    try {
      const e = JSON.parse(line);
      const msg = Array.isArray(e.MESSAGE) ? Buffer.from(e.MESSAGE).toString('utf8') : String(e.MESSAGE ?? '');
      entries.push({ t: Math.floor(Number(e.__REALTIME_TIMESTAMP) / 1000), p: Number(e.PRIORITY ?? 6), id: e.SYSLOG_IDENTIFIER || e._COMM || '', unit: e._SYSTEMD_UNIT || '', msg });
    } catch { /* partial line */ }
  }
  return entries;
}

// ---- packages ----
async function packages(server, { refresh } = {}) {
  const out = await sh(server, `export LC_ALL=C
if command -v apt-get >/dev/null 2>&1; then
  ${refresh ? 'apt-get update -qq >/dev/null 2>&1' : ':'}
  echo "#apt"; apt list --upgradable 2>/dev/null
elif command -v dnf >/dev/null 2>&1; then
  echo "#dnf"; dnf -q ${refresh ? '--refresh' : '--cacheonly'} check-update 2>/dev/null
  echo "#security"; dnf -q --cacheonly updateinfo list --security 2>/dev/null
fi
true`, { timeout: 180_000 });
  const lines = out.split('\n');
  const list = [];
  if (lines[0] === '#apt') {
    for (const l of lines.slice(1)) {
      const m = l.match(/^([^/\s]+)\/(\S+)\s+(\S+)\s+(\S+)\s+\[upgradable from: ([^\]]+)\]/);
      if (m) list.push({ name: m[1], source: m[2], version: m[3], arch: m[4], from: m[5], security: /securi/i.test(m[2]) });
    }
  } else if (lines[0] === '#dnf') {
    const sec = new Set();
    const i = lines.indexOf('#security');
    for (const l of lines.slice(i + 1)) { const p = l.trim().split(/\s+/); if (p[2]) sec.add(p[2].replace(/-[^-]+-[^-]+$/, '')); }
    for (const l of lines.slice(1, i < 0 ? undefined : i)) {
      const m = l.match(/^(\S+)\.(\S+)\s+(\S+)\s+(\S+)$/);
      if (m) list.push({ name: m[1], arch: m[2], version: m[3], source: m[4], from: '', security: sec.has(m[1]) });
    }
  }
  return { manager: (lines[0] || '').slice(1), packages: list };
}

// ---- processes ----
async function processes(server) {
  const out = await sh(server, 'ps -eo pid=,user=,pcpu=,pmem=,rss=,etimes=,comm=,args= --sort=-pcpu | head -n 150');
  return out.split('\n').filter(Boolean).map((l) => {
    const p = l.trim().split(/\s+/);
    return { pid: +p[0], user: p[1], cpu: +p[2], mem: +p[3], rss: +p[4] * 1024, elapsed: +p[5], comm: p[6], args: p.slice(7).join(' ') };
  });
}

function kill(server, pid, signal) {
  if (!Number.isInteger(pid) || pid < 2) throw bad('invalid pid');
  if (!['TERM', 'KILL', 'HUP', 'INT'].includes(signal)) throw bad('invalid signal');
  return sh(server, `kill -s ${signal} ${pid} 2>&1`);
}

// ---- docker ----
async function containers(server) {
  const out = await sh(server, `command -v docker >/dev/null 2>&1 || { echo "#none"; exit 0; }
docker ps -a --no-trunc --format '{{json .}}'
echo '#stats'
docker stats --no-stream --no-trunc --format '{{json .}}'`, { timeout: 60_000 });
  if (out.startsWith('#none')) return { installed: false, containers: [] };
  const [ps, st] = out.split('#stats');
  const parse = (s) => s.split('\n').filter((l) => l.startsWith('{')).map((l) => { try { return JSON.parse(l); } catch { return null; } }).filter(Boolean);
  const stats = new Map(parse(st || '').map((s) => [s.ID, s]));
  return {
    installed: true,
    containers: parse(ps).map((c) => {
      const s = stats.get(c.ID) || [...stats.values()].find((x) => c.ID.startsWith(x.ID)) || {};
      return { id: c.ID, name: c.Names, image: c.Image, state: c.State, status: c.Status, ports: c.Ports, created: c.CreatedAt,
        cpu: s.CPUPerc || '', mem: s.MemUsage || '', memPct: s.MemPerc || '', net: s.NetIO || '', block: s.BlockIO || '' };
    }),
  };
}

const CONTAINER_ACTIONS = ['start', 'stop', 'restart', 'pause', 'unpause', 'rm'];
function containerAction(server, id, action) {
  if (!CONTAINER.test(id)) throw bad('invalid container id');
  if (!CONTAINER_ACTIONS.includes(action)) throw bad('invalid action');
  return sh(server, `docker ${action} -- ${q(id)} 2>&1`);
}

function containerLogs(server, id, lines = 300) {
  if (!CONTAINER.test(id)) throw bad('invalid container id');
  const n = Math.min(Math.max(Number(lines) || 300, 10), 5000);
  return sh(server, `docker logs --timestamps --tail ${n} -- ${q(id)} 2>&1`, { allowFail: true, maxOutput: 4_000_000 });
}

module.exports = { services, serviceAction, serviceStatus, logs, packages, processes, kill, containers, containerAction, containerLogs, UNIT };
