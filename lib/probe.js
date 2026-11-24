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
dk=''
# docker stats samples for ~2s: run it in the background, alongside the CPU sample below.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  dk=$(mktemp)
  T=''; command -v timeout >/dev/null 2>&1 && T='timeout 15' # a stuck daemon must not stall the whole probe
  $T docker stats --no-stream --no-trunc --format '{{json .}}' > "$dk" 2>/dev/null </dev/null &
fi
echo "cpu1=$(head -1 /proc/stat)"
sleep 1
echo "cpu2=$(head -1 /proc/stat)"
awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2} END{printf "mem=%.0f %.0f %.0f %.0f\n", t*1024, a*1024, st*1024, sf*1024}' /proc/meminfo
df -PTk -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | awk 'NR>1 && $3>0 {printf "fs=%s %s %.0f %.0f %s\n", $1, $2, $3*1024, $4*1024, $7}'
awk 'NR>2{sub(/^ +/,""); split($0,a,":"); split(a[2],f," "); if(a[1]!="lo" && a[1]!~/^veth/) printf "if=%s %.0f %.0f\n", a[1], f[1], f[9]; if(a[1]!~/^(lo|veth|docker|br-|virbr|cni|flannel|cali|tailscale|wg)/){r+=f[1]; t+=f[9]}} END{printf "net=%.0f %.0f\n", r, t}' /proc/net/dev
awk '$3~/^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme[0-9]+n[0-9]+|mmcblk[0-9]+)$/{r+=$6; w+=$10} END{printf "dio=%.0f %.0f\n", r*512, w*512}' /proc/diskstats
t=$(cat /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input 2>/dev/null | sort -n | tail -1)
[ -n "$t" ] && echo "temp=$t"
if command -v apt-get >/dev/null 2>&1; then
  echo "pkg=apt"
  u=$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst ')
  echo "updates=$(printf '%s' "$u" | grep -c .)"
  echo "security=$(printf '%s' "$u" | grep -ci securi)"
  a=off
  dpkg -s unattended-upgrades 2>/dev/null | grep -q '^Status: install ok installed' && apt-config dump 2>/dev/null | grep -qE 'APT::Periodic::Unattended-Upgrade "[1-9]' && a=on
  echo "auto_updates=$a"
elif command -v dnf >/dev/null 2>&1; then
  echo "pkg=dnf"
  echo "updates=$(dnf -q --cacheonly check-update 2>/dev/null | grep -cE '^[A-Za-z0-9_.+-]+\.[A-Za-z0-9_]+ ')"
  echo "security=$(dnf -q --cacheonly updateinfo list --security 2>/dev/null | grep -c .)"
  a=off
  systemctl is-enabled -q dnf-automatic-install.timer 2>/dev/null && a=on
  systemctl is-enabled -q dnf-automatic.timer dnf5-automatic.timer 2>/dev/null && grep -qE '^apply_updates *= *(yes|true|1)' /etc/dnf/automatic.conf 2>/dev/null && a=on
  echo "auto_updates=$a"
fi
r=0
[ -f /var/run/reboot-required ] && r=1
command -v needs-restarting >/dev/null 2>&1 && ! needs-restarting -r >/dev/null 2>&1 && r=1
echo "reboot=$r"
echo "failed=$(systemctl --failed --no-legend --plain 2>/dev/null | grep -c .)"
echo "cockpit=$(systemctl is-active cockpit.socket 2>/dev/null)"
if [ -n "$dk" ]; then
  echo "docker=$(docker ps -q | wc -l) $(docker ps -aq | wc -l)"
  docker ps -a --no-trunc --format '{{json .}}' 2>/dev/null | sed 's/^/ctr=/'
  wait
  sed 's/^/cst=/' "$dk"
  rm -f "$dk"
fi
echo "probe_user=$(id -un)"
true
`;

// Disk health, appended to the probe every 30 minutes (smartctl is slow-ish). -n standby: never spin up a sleeping disk.
const SMART = String.raw`
if ! command -v smartctl >/dev/null 2>&1; then echo "smart=none"
elif [ "$(id -u)" != 0 ]; then echo "smart=noroot"
else
  echo "smart=ok"
  smartctl --scan 2>/dev/null | while read -r dev _ type _; do
    echo "smartdev=$dev $(smartctl -j -n standby -H -A -i -d "$type" "$dev" 2>/dev/null | tr -d '
')"
  done
fi
`;

function parseSmart(status, lines) {
  if (!status) return null;
  const disks = [];
  for (const l of lines) {
    const i = l.indexOf(' ');
    let j;
    try { j = JSON.parse(l.slice(i + 1)); } catch { continue; }
    if (!j.smart_status) continue; // in standby, or no SMART support (USB bridges, virtual disks)
    const attr = new Map((j.ata_smart_attributes?.table || []).map((a) => [a.id, a.raw?.value]));
    const nv = j.nvme_smart_health_information_log || {};
    const d = {
      dev: l.slice(0, i), model: j.model_name || j.scsi_model_name || '', serial: j.serial_number || '',
      passed: j.smart_status.passed !== false, temp: j.temperature?.current ?? null, hours: j.power_on_time?.hours ?? null,
      realloc: attr.get(5) ?? null, pending: attr.get(197) ?? null, uncorrectable: attr.get(198) ?? null,
      wear: nv.percentage_used ?? null, mediaErrors: nv.media_errors ?? null, critical: nv.critical_warning ?? null,
    };
    // Reallocated sectors alone are common on old-but-stable disks; pending/uncorrectable ones mean data at risk.
    d.problem = !d.passed || d.pending > 0 || d.uncorrectable > 0 || d.critical > 0 || d.mediaErrors > 0;
    disks.push(d);
  }
  return { status, disks };
}

const nums = (v) => (v || '').trim().split(/\s+/).map(Number);

// "12.5MiB / 1.9GiB" -> bytes of the first figure
const UNITS = { B: 1, KB: 1e3, MB: 1e6, GB: 1e9, TB: 1e12, KIB: 1024, MIB: 1024 ** 2, GIB: 1024 ** 3, TIB: 1024 ** 4 };
function bytesOf(v) {
  const m = String(v || '').match(/([\d.]+)\s*([KMGT]?i?B)/i);
  return m ? Math.round(parseFloat(m[1]) * (UNITS[m[2].toUpperCase()] || 1)) : null;
}
const jsonLines = (lines) => lines.map((l) => { try { return JSON.parse(l); } catch { return null; } }).filter(Boolean);
function parseContainers(ps, stats) {
  const st = new Map(jsonLines(stats).map((s) => [s.Name, s]));
  return jsonLines(ps).map((c) => {
    const s = st.get(c.Names);
    const health = /\(unhealthy\)/.test(c.Status) ? 'unhealthy' : /\(healthy\)/.test(c.Status) ? 'healthy' : /health: starting/.test(c.Status) ? 'starting' : null;
    return { name: c.Names, image: c.Image, state: c.State, status: c.Status, health,
      cpu: s ? parseFloat(s.CPUPerc) || 0 : null, mem: s ? bytesOf(s.MemUsage) : null };
  });
}

function parse(text) {
  const kv = {};
  const disks = new Map();
  const smartDevs = [];
  const ctrs = [];
  const ifaces = [];
  const cstats = [];
  for (const line of text.split('\n')) {
    const i = line.indexOf('=');
    if (i <= 0) continue;
    const k = line.slice(0, i);
    const v = line.slice(i + 1).trim();
    if (k === 'smartdev') smartDevs.push(v);
    else if (k === 'ctr') ctrs.push(v);
    else if (k === 'if') { const [name, rx, tx] = v.split(' '); ifaces.push({ name, rxTotal: +rx || 0, txTotal: +tx || 0, rx: null, tx: null }); }
    else if (k === 'cst') cstats.push(v);
    else if (k === 'fs') {
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
  const containers = kv.docker ? parseContainers(ctrs, cstats) : null;
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
    ifaces, // all interfaces but lo and veth*; the rx/tx totals above leave out virtual ones (docker, VPN) to avoid double counting
    temp: kv.temp ? Number(kv.temp) / 1000 : null,
    pkg: kv.pkg || '', updates: n(kv.updates), security: n(kv.security),
    autoUpdates: kv.auto_updates ? kv.auto_updates === 'on' : null,
    reboot: kv.reboot === '1', failed: n(kv.failed) || 0, cockpit: kv.cockpit === 'active',
    docker: kv.docker ? { running: running || 0, total: total || 0 } : null,
    containers,
    unhealthy: containers ? containers.filter((c) => c.health === 'unhealthy' || c.state === 'restarting').length : null,
    probeUser: kv.probe_user || '',
    smart: parseSmart(kv.smart, smartDevs),
  };
}

const prevCounters = new Map(); // id -> { t, rx, tx, dr, dw }
const smartCache = new Map(); // id -> { t, smart }: SMART runs every 30 min, snapshots in between reuse it
const inflight = new Map(); // id -> promise, so a manual refresh waits for a probe already running
const errors = new Map(); // id -> { message, since }
const listeners = [];

function probe(server) {
  if (!inflight.has(server.id)) inflight.set(server.id, runProbe(server).finally(() => inflight.delete(server.id)));
  return inflight.get(server.id);
}

async function runProbe(server) {
  try {
    const sc = smartCache.get(server.id);
    const withSmart = !sc || sc.smart?.status !== 'ok' || Date.now() - sc.t > 30 * 60e3; // re-check often until it works
    const { code, out } = await ssh.exec(server, withSmart ? PROBE + SMART : PROBE, { mode: 'auto', timeout: withSmart ? 90_000 : 30_000 });
    if (code !== 0) throw new Error(out.trim().split('\n').pop() || `probe exited with ${code}`);
    const s = parse(out);
    if (s.smart) smartCache.set(server.id, { t: Date.now(), smart: s.smart });
    else s.smart = sc?.smart || null;
    const t = Date.now();
    const p = prevCounters.get(server.id);
    // Counters reset on reboot; clamp the negative delta to 0.
    const per = (now, before) => (p && t > p.t && before != null ? Math.max(0, (now - before) / ((t - p.t) / 1000)) : null);
    for (const k of ['rx', 'tx', 'dr', 'dw']) s[k] = per(s.counters[k], p?.[k]);
    for (const i of s.ifaces) { const b = p?.ifaces.get(i.name); i.rx = per(i.rxTotal, b?.rx); i.tx = per(i.txTotal, b?.tx); }
    prevCounters.set(server.id, { t, ...s.counters, ifaces: new Map(s.ifaces.map((i) => [i.name, { rx: i.rxTotal, tx: i.txTotal }])) });
    s.t = t;
    errors.delete(server.id);
    run('UPDATE servers SET last_json = ? WHERE id = ?', JSON.stringify(s), server.id);
    run(`INSERT OR REPLACE INTO metrics (server_id, t, cpu, mem, swap, disk, load1, rx, tx, dr, dw, temp)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      server.id, t, s.cpu, s.mem, s.swap, s.disk, s.load[0] ?? null, s.rx, s.tx, s.dr, s.dw, s.temp);
    for (const c of s.containers || []) {
      if (c.cpu != null) run('INSERT OR REPLACE INTO container_metrics (server_id, name, t, cpu, mem) VALUES (?, ?, ?, ?, ?)', server.id, c.name, t, c.cpu, c.mem);
    }
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

function containerSeries(id, name, range) {
  const span = RANGES[range] || RANGES['1h'];
  const bucket = Math.max(60_000, Math.floor(span / 240 / 60_000) * 60_000);
  const rows = all(`SELECT (t / ?) * ? AS t, AVG(cpu) cpu, AVG(mem) mem FROM container_metrics
    WHERE server_id = ? AND name = ? AND t >= ? GROUP BY t / ? ORDER BY 1`, bucket, bucket, id, name, Date.now() - span, bucket);
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
    run('DELETE FROM container_metrics WHERE t < ?', Date.now() - days * 864e5);
  }, 3600e3);
}

module.exports = {
  PROBE, SMART, parse, probe, series, containerSeries, sparks, start, errors,
  onProbe: (fn) => listeners.push(fn),
  getServer: (id) => get('SELECT * FROM servers WHERE id = ?', id),
};
