'use strict';
// Beszel-style monitoring without an agent: one POSIX sh script per minute over the pooled SSH connection.
const { all, get, run, setting } = require('./db');
const ssh = require('./ssh');

const PROBE = String.raw`
export LC_ALL=C
echo "hostname=$(hostname 2>/dev/null || cat /etc/hostname)"
. /etc/os-release 2>/dev/null
echo "os=$PRETTY_NAME"
echo "os_id=$ID"
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
echo "cpu_model=$(grep -m1 -E '^(model name|Model)' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//')"
echo "cores=$(grep -c ^processor /proc/cpuinfo)"
echo "virt=$(systemd-detect-virt 2>/dev/null)"
echo "ips=$(hostname -I 2>/dev/null)"
echo "uptime=$(cut -d' ' -f1 /proc/uptime)"
echo "load=$(cut -d' ' -f1-3 /proc/loadavg)"
echo "procs=$(ls -d /proc/[0-9]* | wc -l)"
echo "cpu1=$(head -1 /proc/stat)"
sleep 1
echo "cpu2=$(head -1 /proc/stat)"
awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2} END{printf "mem=%.0f %.0f %.0f %.0f\n", t*1024, a*1024, st*1024, sf*1024}' /proc/meminfo
df -PTk -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | awk 'NR>1 && $3>0 {printf "fs=%s %s %.0f %.0f %s\n", $1, $2, $3*1024, $4*1024, $7}'
awk 'NR>2{sub(/^ +/,""); split($0,a,":"); split(a[2],f," "); if(a[1]!~/^(lo|veth|docker|br-|virbr|cni|flannel|cali|tailscale|wg)/){r+=f[1]; t+=f[9]}} END{printf "net=%.0f %.0f\n", r, t}' /proc/net/dev
awk '$3~/^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme[0-9]+n[0-9]+|mmcblk[0-9]+)$/{r+=$6; w+=$10} END{printf "dio=%.0f %.0f\n", r*512, w*512}' /proc/diskstats
t=$(cat /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input 2>/dev/null | sort -n | tail -1)
[ -n "$t" ] && echo "temp=$t"
if command -v apt-get >/dev/null 2>&1; then
  echo "pkg=apt"
  u=$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst ')
  echo "updates=$(printf '%s' "$u" | grep -c .)"
  echo "security=$(printf '%s' "$u" | grep -ci securi)"
elif command -v dnf >/dev/null 2>&1; then
  echo "pkg=dnf"
  echo "updates=$(dnf -q --cacheonly check-update 2>/dev/null | grep -cE '^[A-Za-z0-9_.+-]+\.[A-Za-z0-9_]+ ')"
  echo "security=$(dnf -q --cacheonly updateinfo list --security 2>/dev/null | grep -c .)"
fi
r=0
[ -f /var/run/reboot-required ] && r=1
command -v needs-restarting >/dev/null 2>&1 && ! needs-restarting -r >/dev/null 2>&1 && r=1
echo "reboot=$r"
echo "failed=$(systemctl --failed --no-legend --plain 2>/dev/null | grep -c .)"
echo "cockpit=$(systemctl is-active cockpit.socket 2>/dev/null)"
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then echo "docker=$(docker ps -q | wc -l) $(docker ps -aq | wc -l)"; fi
echo "probe_user=$(id -un)"
true
`;

const nums = (v) => (v || '').trim().split(/\s+/).map(Number);

function parse(text) {
  const kv = {};
  const disks = new Map();
  for (const line of text.split('\n')) {
    const i = line.indexOf('=');
    if (i <= 0) continue;
    const k = line.slice(0, i);
    const v = line.slice(i + 1).trim();
    if (k === 'fs') {
      // One entry per device: bind mounts and btrfs subvolumes repeat the same numbers. Prefer the "/" mount.
      const [dev, type, size, used, ...rest] = v.split(' ');
      const mount = rest.join(' ');
      if (Number(size) > 0 && (!disks.has(dev) || mount === '/')) disks.set(dev, { mount, type, size: +size, used: +used, pct: +used / +size });
    } else kv[k] = v;
  }
  const a = nums(kv.cpu1).slice(1, 9); // user nice system idle iowait irq softirq steal
  const b = nums(kv.cpu2).slice(1, 9);
  const sum = (x) => x.reduce((p, c) => p + (c || 0), 0);
  const idle = (x) => (x[3] || 0) + (x[4] || 0);
  const dt = sum(b) - sum(a);
  const [memTotal, memAvail, swapTotal, swapFree] = nums(kv.mem);
  const [rx, tx] = nums(kv.net);
  const [dr, dw] = nums(kv.dio);
  const list = [...disks.values()];
  const root = list.find((d) => d.mount === '/') || list.reduce((m, d) => (!m || d.pct > m.pct ? d : m), null);
  const [running, total] = nums(kv.docker);
  const n = (v) => (v === undefined || v === '' ? null : Number(v));
  return {
    hostname: kv.hostname || '', os: kv.os || '', osId: kv.os_id || '', kernel: kv.kernel || '', arch: kv.arch || '',
    cpuModel: kv.cpu_model || '', cores: n(kv.cores), virt: kv.virt && kv.virt !== 'none' ? kv.virt : '',
    ips: (kv.ips || '').split(/\s+/).filter(Boolean), uptime: n(kv.uptime), load: nums(kv.load).slice(0, 3), procs: n(kv.procs),
    cpu: dt > 0 ? Math.min(1, Math.max(0, 1 - (idle(b) - idle(a)) / dt)) : null,
    memTotal: memTotal || 0, memUsed: memTotal ? memTotal - memAvail : 0, mem: memTotal ? 1 - memAvail / memTotal : null,
    swapTotal: swapTotal || 0, swap: swapTotal ? 1 - swapFree / swapTotal : null,
    disks: list, disk: root ? root.pct : null,
    counters: { rx: rx || 0, tx: tx || 0, dr: dr || 0, dw: dw || 0 },
    temp: kv.temp ? Number(kv.temp) / 1000 : null,
    pkg: kv.pkg || '', updates: n(kv.updates), security: n(kv.security),
    reboot: kv.reboot === '1', failed: n(kv.failed) || 0, cockpit: kv.cockpit === 'active',
    docker: kv.docker ? { running: running || 0, total: total || 0 } : null,
    probeUser: kv.probe_user || '',
  };
}

const prevCounters = new Map(); // id -> { t, rx, tx, dr, dw }
const inflight = new Map(); // id -> promise, so a manual refresh waits for a probe already running
const errors = new Map(); // id -> { message, since }
const listeners = [];

function probe(server) {
  if (!inflight.has(server.id)) inflight.set(server.id, runProbe(server).finally(() => inflight.delete(server.id)));
  return inflight.get(server.id);
}

async function runProbe(server) {
  try {
    const { code, out } = await ssh.exec(server, PROBE, { mode: 'auto', timeout: 30_000 });
    if (code !== 0) throw new Error(out.trim().split('\n').pop() || `probe exited with ${code}`);
    const s = parse(out);
    const t = Date.now();
    const p = prevCounters.get(server.id);
    // Counters reset on reboot; clamp the negative delta to 0.
    for (const k of ['rx', 'tx', 'dr', 'dw']) s[k] = p && t > p.t ? Math.max(0, (s.counters[k] - p[k]) / ((t - p.t) / 1000)) : null;
    prevCounters.set(server.id, { t, ...s.counters });
    s.t = t;
    errors.delete(server.id);
    run('UPDATE servers SET last_json = ? WHERE id = ?', JSON.stringify(s), server.id);
    run(`INSERT OR REPLACE INTO metrics (server_id, t, cpu, mem, swap, disk, load1, rx, tx, dr, dw, temp)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      server.id, t, s.cpu, s.mem, s.swap, s.disk, s.load[0] ?? null, s.rx, s.tx, s.dr, s.dw, s.temp);
    for (const fn of listeners) fn(server, s, null);
  } catch (e) {
    const prev = errors.get(server.id);
    errors.set(server.id, { message: e.message, since: prev?.since || Date.now() });
    for (const fn of listeners) fn(server, null, errors.get(server.id));
  }
}

const RANGES = { '1h': 3600e3, '6h': 6 * 3600e3, '24h': 864e5, '7d': 7 * 864e5, '30d': 30 * 864e5 };

function series(id, range) {
  const span = RANGES[range] || RANGES['1h'];
  const bucket = Math.max(60_000, Math.floor(span / 240 / 60_000) * 60_000); // at most ~240 points
  const rows = all(`SELECT (t / ?) * ? AS t, AVG(cpu) cpu, AVG(mem) mem, AVG(swap) swap, AVG(disk) disk, AVG(load1) load1,
      AVG(rx) rx, AVG(tx) tx, AVG(dr) dr, AVG(dw) dw, MAX(temp) temp
    FROM metrics WHERE server_id = ? AND t >= ? GROUP BY t / ? ORDER BY 1`, bucket, bucket, id, Date.now() - span, bucket);
  return { bucket, from: Date.now() - span, to: Date.now(), points: rows.map((r) => ({ ...r })) };
}

function sparks(since = Date.now() - 3600e3) {
  const out = {};
  for (const r of all('SELECT server_id, t, cpu, mem FROM metrics WHERE t >= ? ORDER BY t', since)) {
    (out[r.server_id] ||= []).push([r.t, r.cpu, r.mem]);
  }
  return out;
}

const servers = () => all('SELECT * FROM servers');

function start() {
  const pollAll = async () => {
    const list = servers().filter((s) => !s.paused);
    // ponytail: fixed concurrency of 16 probes; raise if you have hundreds of servers.
    for (let i = 0; i < list.length; i += 16) await Promise.all(list.slice(i, i + 16).map(probe));
  };
  pollAll();
  setInterval(pollAll, 60_000);
  setInterval(() => {
    const days = Number(setting('retention_metrics_days', 30));
    run('DELETE FROM metrics WHERE t < ?', Date.now() - days * 864e5);
  }, 3600e3);
}

module.exports = {
  PROBE, parse, probe, series, sparks, start, errors,
  onProbe: (fn) => listeners.push(fn),
  getServer: (id) => get('SELECT * FROM servers WHERE id = ?', id),
};
