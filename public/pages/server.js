import {
  html, useState, useEffect, useRef, useMemo, api, go, store, useStore, useLoad, useQuery, useHold, useDirty, serversStore, refreshServers, toast, confirmBox,
  Btn, Th, useSort, StatusChip, LineChart, Empty, Modal, Footer, Field, ParamInput, Loading, ErrorNote, Output, Icon, Copy, Menu, CellBar,
  bytes, rate, pct, ago, dur, uptime, when, logTime, level, eol, traffic, can,
} from '../ui.js';
import { ServerForm, serverBody, PRIV_TEXT } from './overview.js';
import { JobsTable, openRun, confirmRun } from './jobs.js';
import { muteFor } from './alerts.js';

const TABS = [['overview', 'Overview'], ['updates', 'Updates'], ['services', 'Services'], ['logs', 'Logs'], ['processes', 'Processes'], ['containers', 'Containers'], ['network', 'Network'], ['terminal', 'Terminal'], ['jobs', 'Jobs'], ['settings', 'Settings']];
export const TAB_LABEL = Object.fromEntries(TABS);

async function startJob(server, scriptId, params) {
  const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: scriptId, params, targets: { servers: [server.id] } } });
  go(`/jobs/${id}`);
}
const NOT_ALLOWED = html`<${Empty} title="Not available with your role" />`;
const logsHref = (s, unit) => `#/servers/${s.id}/logs?unit=${encodeURIComponent(unit)}`;

// Counts on the tab that fixes them, instead of a row of chips above the tabs.
function tabBadge(k, n) {
  if (!n) return null;
  if (k === 'updates' && n.updates) return html`<span class=${`count ${n.security ? 'soft' : 'warn'}`} title=${n.security ? `${n.security} security` : ''}>${n.updates}</span>`;
  if (k === 'services' && n.failed) return html`<span class="count soft" title="Failed units">${n.failed}</span>`;
  if (k === 'containers' && n.unhealthy) return html`<span class="count soft" title="Unhealthy containers">${n.unhealthy}</span>`;
  return null;
}

// Ordered, one bar per kind: host key → unreachable → sudo → alerts.
function Attention({ s, acceptKey }) {
  const bars = [];
  if (s.ssh?.hostKeyMismatch) bars.push(html`<div class="alert-bar" role="alert"><span class="grow"><b>The host key changed.</b> Fleetdeck refuses to connect, because this can mean someone is intercepting the connection. Pinned: <span class="mono">${s.host_key}</span>, offered: <span class="mono">${s.ssh.hostKeyMismatch}</span></span>${can('owner') && html`<${Btn} kind="danger" onClick=${acceptKey}>Trust new key</${Btn}>`}</div>`);
  else if (s.status === 'down') bars.push(html`<div class="alert-bar" role="alert"><span class="grow"><b>Unreachable${s.error ? ` for ${dur(Date.now() - s.error.since)}` : ''}.</b> ${s.error?.message}</span>${can('owner') && html`<a class="btn sm" href=${`#/servers/${s.id}/settings`}>Connection settings</a>`}</div>`);
  if (s.ssh?.sudoRejected) bars.push(html`<div class="alert-bar warn"><span class="grow"><b>The server rejected the stored sudo password.</b> Fleetdeck stopped using it so the account doesn't get locked. Update it to resume admin actions.</span>${can('owner') && html`<a class="btn sm" href=${`#/servers/${s.id}/settings`}>Update password</a>`}</div>`);
  const f = (s.firing || []).filter((a) => !(s.status === 'down' && a.metric === 'down')); // the unreachable bar already says it
  const rules = can('admin') && html`<a class="btn sm" href="#/alerts">Alert rules</a>`;
  if (f.length === 1) bars.push(html`<div class="alert-bar"><span class="grow"><b>Alert firing ${ago(f[0].since)}:</b> ${f[0].message}</span>${rules}</div>`);
  else if (f.length > 1) bars.push(html`<div class="alert-bar"><span class="grow"><b>${f.length} alerts firing</b><ul>${f.map((a) => html`<li>${a.message} <span class="muted">(${ago(a.since)})</span></li>`)}</ul></span>${rules}</div>`);
  return bars.length ? html`<div class="attention">${bars}</div>` : null;
}

export function ServerPage({ id, tab }) {
  const servers = useStore(serversStore);
  const s = servers?.find((x) => x.id === id);
  useEffect(() => { document.querySelector('.tabs [aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' }); }, [tab, !!s]);
  if (!servers) return html`<div class="page"><${Loading} /></div>`;
  if (!s) return html`<div class="page"><${Empty} title="Server not found" action=${html`<a class="btn" href="#/">Back to the fleet</a>`}>It may have been removed.</${Empty}></div>`;
  const n = s.snapshot;
  const op = can('operator');

  const reboot = async () => {
    if (!await confirmRun({ scriptId: 'reboot', name: 'Reboot', count: 1, names: [s.name] })) return;
    await startJob(s, 'reboot');
  };
  const acceptKey = async () => {
    if (!await confirmBox({ title: 'Trust the new host key?', body: `Only do this if you know why the key changed (reinstall, new server at the same address). Offered key: ${s.ssh.hostKeyMismatch}`, confirm: 'Trust new key', danger: true })) return;
    await api(`/api/servers/${s.id}/hostkey/accept`, { method: 'POST' });
    toast('New host key trusted', 'ok');
    refreshServers();
  };
  const mute = async (h) => { await muteFor(s.id, h, `Maintenance on ${s.name}`); toast(`Notifications for ${s.name} muted for ${h} h`, 'ok', { label: 'Quiet hours', href: '#/alerts' }); };
  const cockpitUrl = `https://${s.host.includes(':') ? `[${s.host}]` : s.host}:9090`;

  let body;
  if (tab === 'updates') body = html`<${UpdatesTab} s=${s} />`;
  else if (tab === 'services') body = html`<${ServicesTab} s=${s} />`;
  else if (tab === 'logs') body = html`<${LogsTab} s=${s} />`;
  else if (tab === 'processes') body = html`<${ProcessesTab} s=${s} />`;
  else if (tab === 'containers') body = html`<${ContainersTab} s=${s} />`;
  else if (tab === 'network') body = html`<div class="stack"><${NetworkTab} s=${s} /><${ChecksPanel} s=${s} /></div>`;
  else if (tab === 'terminal') body = op ? html`<${TerminalTab} s=${s} />` : NOT_ALLOWED;
  else if (tab === 'jobs') body = html`<div class="stack">${can('admin') && html`<${ScheduledPanel} s=${s} />`}<section class="panel"><${JobsTable} serverId=${s.id} /></section></div>`;
  else if (tab === 'settings') body = can('owner') ? html`<${SettingsTab} s=${s} />` : NOT_ALLOWED;
  else body = html`<${OverviewTab} s=${s} />`;

  // Containers only with Docker or Proxmox (the route still works and explains). Terminal from operator, settings from owner up.
  const tabs = TABS.filter(([k]) => (k !== 'containers' || n?.docker || n?.pve || tab === 'containers') && (k !== 'terminal' || op) && (k !== 'settings' || can('owner')));
  return html`<div class="page">
    <div class="srv-h">
      <div class="id grow">
        <div class="row"><h1>${s.name}</h1><${StatusChip} status=${s.status} />${s.tags.map((t) => html`<span class="tag">${t}</span>`)}</div>
        <div class="meta"><span class="mono">${s.user}@${s.host}${s.port !== 22 ? `:${s.port}` : ''}</span>
          ${n && html`<span>${n.os}${eol(n) && html` <span class=${`chip ${eol(n).kind}`} title="No more security updates from the vendor after this date">${eol(n).text}</span>`}</span><span>Up ${uptime(n.uptime)}</span><span>Checked ${ago(n.t)}</span>`}</div>
      </div>
      <div class="row">
        ${op && html`<${Btn} icon="refresh" title="Check now" onClick=${async () => { await api(`/api/servers/${s.id}/probe`, { method: 'POST' }); await refreshServers(); }} />`}
        ${op && tab !== 'terminal' && html`<a class="btn" href=${`#/servers/${s.id}/terminal`}><${Icon} name="term" /><span class="hide-sm">Terminal</span></a>`}
        ${op && html`<${Btn} icon="play" onClick=${() => openRun({ targets: { servers: [s.id] } })}>Run script</${Btn}>`}
        ${(op || n?.cockpit) && html`<${Menu} label=${`More actions for ${s.name}`}>
          ${n?.cockpit ? html`<a class="mi" href=${cockpitUrl} target="_blank" rel="noopener noreferrer"><${Icon} name="ext" />Open Cockpit</a>`
            : n && html`<button class="mi" title="Storage, networking and firewall management" onClick=${() => startJob(s, 'cockpit').catch((e) => toast(e.message, 'bad'))}><${Icon} name="plus" />Install Cockpit</button>`}
          ${can('admin') && html`<button class="mi" onClick=${() => mute(1).catch((e) => toast(e.message, 'bad'))}><${Icon} name="mute" />Mute alerts for 1 h</button>
          <button class="mi" onClick=${() => mute(4).catch((e) => toast(e.message, 'bad'))}><${Icon} name="mute" />Mute alerts for 4 h</button>`}
          ${op && html`<hr />
          <button class="mi bad" onClick=${() => reboot().catch((e) => toast(e.message, 'bad'))}><${Icon} name="power" />Reboot…</button>`}
        </${Menu}>`}
      </div>
    </div>

    <${Attention} s=${s} acceptKey=${acceptKey} />

    <nav class="tabs" aria-label="Server sections">${tabs.map(([k, label]) => html`<a href=${`#/servers/${s.id}${k === 'overview' ? '' : `/${k}`}`} aria-current=${tab === k ? 'page' : undefined}>${label}${tabBadge(k, n)}</a>`)}</nav>
    ${body}
  </div>`;
}

// ---------- overview: charts + system info ----------
const RANGES = ['1h', '6h', '24h', '7d', '30d'];
const P = (v) => `${Math.round(v * 100)}%`;
const RangePicker = ({ value, onChange }) => html`<div class="seg" role="group" aria-label="Time range">${RANGES.map((r) => html`<button aria-pressed=${r === value} onClick=${() => onChange(r)}>${r}</button>`)}</div>`;

function OverviewTab({ s }) {
  const [range, setRange] = useQuery('range', '1h');
  const m = useLoad(() => api(`/api/servers/${s.id}/metrics?range=${range}`), [s.id, range], { poll: 60_000 });
  const sync = useMemo(() => store(null), []); // shared crosshair
  const n = s.snapshot;
  const hasTemp = m.data?.points.some((p) => p.temp != null);
  const hasGPU = n?.gpus?.length > 0 || m.data?.points.some((p) => p.gpu != null);
  const custom = useLoad(() => api(`/api/servers/${s.id}/custom?range=${range}`), [s.id, range], { poll: 60_000 });
  const [edit, setEdit] = useState(null); // custom metric being added ({}) or edited
  return html`<div class="stack">
    <div class="row"><h2>Metrics</h2><div class="row right">${can('admin') && html`<${Btn} size="sm" icon="plus" onClick=${() => setEdit({})}>Add custom metric</${Btn}>`}<${RangePicker} value=${range} onChange=${setRange} /></div></div>
    ${m.error && html`<${ErrorNote} error=${m.error} retry=${m.reload} />`}
    ${custom.error && html`<${ErrorNote} error=${custom.error} retry=${custom.reload} />`}
    <div class="charts">
      <${LineChart} sync=${sync} title="CPU" data=${m.data} yMax=${1} fmt=${P} series=${[{ key: 'cpu', label: 'CPU', color: 'var(--s1)' }]} current=${n && `${pct(n.cpu)} of ${n.cores} cores`} />
      <${LineChart} sync=${sync} title="Memory" data=${m.data} yMax=${1} fmt=${P} series=${[{ key: 'mem', label: 'Memory', color: 'var(--s1)' }, { key: 'swap', label: 'Swap', color: 'var(--s2)' }]} current=${n && `${bytes(n.memUsed)} of ${bytes(n.memTotal)}`} />
      <${LineChart} sync=${sync} title="Network" data=${m.data} fmt=${(v) => rate(v)} series=${[{ key: 'rx', label: 'In', color: 'var(--s1)' }, { key: 'tx', label: 'Out', color: 'var(--s2)' }]} current=${traffic(s.traffic)} />
      <${LineChart} sync=${sync} title="Disk I/O" data=${m.data} fmt=${(v) => rate(v)} series=${[{ key: 'dr', label: 'Read', color: 'var(--s1)' }, { key: 'dw', label: 'Write', color: 'var(--s2)' }]} />
      <${LineChart} sync=${sync} title="Load (1 min)" data=${m.data} fmt=${(v) => v.toFixed(v < 10 ? 1 : 0)} series=${[{ key: 'load1', label: 'Load', color: 'var(--s1)' }]} current=${n && `${n.load.map((l) => l.toFixed(2)).join(', ')}`} />
      ${hasTemp && html`<${LineChart} sync=${sync} title="Temperature" data=${m.data} fmt=${(v) => `${Math.round(v)}°`} series=${[{ key: 'temp', label: 'Temperature', color: 'var(--s1)' }]} current=${n?.temp != null && `${Math.round(n.temp)} °C`} />`}
      ${hasGPU && html`<${LineChart} sync=${sync} title="GPU" data=${m.data} yMax=${1} fmt=${P} series=${[{ key: 'gpu', label: 'Usage', color: 'var(--s1)' }, { key: 'gpu_mem', label: 'Memory', color: 'var(--s2)' }]} current=${n?.gpu != null && `${pct(n.gpu)}${n.gpus.length > 1 ? ` (busiest of ${n.gpus.length})` : ''}`} />`}
      ${custom.data?.map((c) => html`<${LineChart} sync=${sync} title=${c.unit ? `${c.name} (${c.unit})` : c.name} data=${c.series} fmt=${customNum} series=${[{ key: 'v', label: c.name, color: 'var(--s1)' }]}
        current=${customNow(n?.custom?.[c.id], c.unit)} actions=${can('admin') && html`<${Btn} size="sm" kind="ghost" icon="edit" title=${`Edit ${c.name}`} onClick=${() => setEdit(c)} />`} />`)}
    </div>
    ${edit && html`<${CustomMetricDialog} s=${s} metric=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); custom.reload(true); }} />`}
    ${n && html`<div class="grid-2">
      <section class="panel"><div class="panel-h"><h2>Filesystems</h2></div>
        <div class="table-wrap"><table class="t"><thead><tr><th>Mount</th><th class="hide-sm">Type</th><th>Used</th><th class="num hide-sm">Inodes</th><th class="num">Size</th></tr></thead>
        <tbody>${n.disks.map((d) => html`<tr><td class="mono">${d.mount}</td><td class="ink2 hide-sm">${d.type}</td>
          <td><div class="row nw"><div class="bar grow"><i class=${level(d.pct, 0.8, 0.9)} style=${{ width: `${d.pct * 100}%` }} /></div><span class="small nowrap">${pct(d.pct)}</span></div></td>
          <td class=${`num small nowrap hide-sm ${d.inodePct >= 0.9 ? 'err' : 'ink2'}`}>${d.inodePct != null ? pct(d.inodePct) : '–'}</td>
          <td class="num ink2 nowrap">${bytes(d.used)} / ${bytes(d.size)}</td></tr>`)}</tbody></table></div></section>
      <section class="panel"><div class="panel-h"><h2>System</h2></div><div class="panel-b">
        <dl class="kv">
          <dt class="sub">Hardware</dt>
          <dt>CPU</dt><dd>${n.cpuModel || '–'}, ${n.cores} cores</dd>
          <dt>Memory</dt><dd>${bytes(n.memTotal)}${n.swapTotal ? `, ${bytes(n.swapTotal)} swap` : ', no swap'}</dd>
          ${n.gpus?.length > 0 && html`<dt>GPU</dt><dd>${n.gpus.map((g) => html`<div>${g.name}${g.memTotal ? `, ${bytes(g.memTotal)}` : ''}${g.temp != null ? `, ${Math.round(g.temp)} °C` : ''}${g.power != null ? `, ${Math.round(g.power)} W` : ''}</div>`)}</dd>`}
          ${n.virt && html`<dt>Virtualization</dt><dd>${n.virt}</dd>`}
          <dt class="sub">Software</dt>
          <dt>OS</dt><dd>${n.os}</dd>
          <dt>Kernel</dt><dd class="mono">${n.kernel} (${n.arch})</dd>
          <dt>Packages</dt><dd>${n.pkg || 'unknown'}${n.updates != null ? `, ${n.updates} pending` : ''}${n.autoUpdates != null ? `, automatic updates ${n.autoUpdates ? 'on' : 'off'}` : ''}</dd>
          ${n.docker && html`<dt>Docker</dt><dd>${n.docker.running} of ${n.docker.total} containers running</dd>`}
          <dt>Booted</dt><dd>${when(n.t - n.uptime * 1000)}</dd>
          <dt>Processes</dt><dd>${n.procs}</dd>
          <dt class="sub">Access</dt>
          <dt>Hostname</dt><dd class="mono">${n.hostname}</dd>
          <dt>Addresses</dt><dd class="mono">${n.ips.join(', ') || '–'}</dd>
          <dt>Checks run as</dt><dd>${n.probeUser}${n.probeUser !== 'root' && html` <span class="chip warn">no passwordless sudo</span>${can('owner') && html` <a class="small" href=${`#/servers/${s.id}/settings`}>Settings</a>`}`}</dd>
          ${s.notes && html`<dt>Notes</dt><dd class="pre-wrap">${s.notes}</dd>`}
        </dl></div></section>
    </div>`}
    ${n && html`<${DiskHealth} s=${s} />`}
  </div>`;
}

// ---------- custom metrics ----------
const customNum = (v) => (Math.abs(v) >= 100 ? Math.round(v) : +v.toFixed(2)).toLocaleString();
const unitOf = (u) => (!u ? '' : /^[%°]/.test(u) ? u : ` ${u}`);
// Latest reading from the snapshot: the value, or why there is none (missing until the next probe after adding it).
const customNow = (x, unit) => (!x ? 'Waiting for the next check' : x.v != null ? `${customNum(x.v)}${unitOf(unit)}` : `⚠ ${x.err}`);

function CustomMetricDialog({ s, metric, onClose, onSaved }) {
  const servers = useStore(serversStore) || [];
  const tags = [...new Set(servers.flatMap((x) => x.tags))].sort();
  // scope: '' = all servers, 'tag:<name>', or a server id. New metrics start on this server.
  const [v, setV] = useState({ name: '', unit: '', command: '', ...metric, scope: metric.id ? metric.server_id || (metric.tag ? `tag:${metric.tag}` : '') : s.id });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  const save = async () => {
    setErr('');
    try {
      const tag = v.scope.startsWith('tag:') ? v.scope.slice(4) : null;
      const body = { name: v.name, unit: v.unit, command: v.command, tag, server_id: tag ? null : v.scope || null };
      await api(metric.id ? `/api/custom-metrics/${metric.id}` : '/api/custom-metrics', { method: metric.id ? 'PUT' : 'POST', body });
      if (!metric.id) toast(`${v.name} added. The first value arrives with the next check, within a minute.`, 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  const del = async () => {
    if (!await confirmBox({ title: `Delete "${metric.name}"?`, body: 'Its history and any alert rules that use it are deleted too.', confirm: 'Delete metric', danger: true })) return;
    await api(`/api/custom-metrics/${metric.id}`, { method: 'DELETE' });
    onSaved();
  };
  return html`<${Modal} title=${metric.id ? `Edit ${metric.name}` : 'Add custom metric'} onClose=${onClose} dirty=${dirty}
    footer=${html`${metric.id && html`<${Btn} kind="quiet-danger" icon="trash" onClick=${del}>Delete</${Btn}>`}<span class="grow" /><${Footer} onClose=${onClose} onSave=${save} label="Save metric" disabled=${!v.name.trim() || !v.command.trim()} />`}>
    <div class="alert-bar warn"><span class="grow"><b>The command runs as root</b> on every server it applies to, once a minute, with the same rights as Fleetdeck's own checks (root via <span class="mono">sudo -n</span> where available). Adding or changing it is recorded in the audit log.</span></div>
    <div class="cols">
      <${Field} label="Name"><input value=${v.name} onInput=${set('name')} placeholder="Backups" maxlength="60" autofocus /></${Field}>
      <${Field} label="Unit (optional)"><input value=${v.unit} onInput=${set('unit')} placeholder="files" maxlength="16" /></${Field}>
    </div>
    <${Field} label="Command" hint="Must print one number. Anything else is recorded as missing. Stopped after 5 seconds."><textarea class="mono" spellcheck="false" value=${v.command} onInput=${set('command')} placeholder="ls /var/backups | wc -l" /></${Field}>
    <${Field} label="Applies to" hint=${v.scope.startsWith('tag:') ? 'Includes servers that get this tag later.' : null}><select value=${v.scope} onChange=${set('scope')}>
      <option value=${s.id}>Only ${s.name}</option>
      ${tags.length > 0 && html`<optgroup label="Servers tagged">${tags.map((t) => html`<option value=${`tag:${t}`}>${t}</option>`)}</optgroup>`}
      <option value="">All servers</option></select></${Field}>
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}

// S.M.A.R.T. from the probe (every 30 min). Virtual machines have no SMART, so stay quiet there.
function DiskHealth({ s }) {
  const sm = s.snapshot.smart;
  if (!sm || (s.snapshot.virt && !sm.disks.length)) return null;
  if (sm.status === 'none') return can('operator') ? html`<div class="alert-bar info"><span class="grow">Install smartmontools to watch the health of this server's disks (S.M.A.R.T.).</span><${Btn} size="sm" onClick=${() => startJob(s, 'smartmontools')}>Install smartmontools</${Btn}></div>` : null;
  if (sm.status === 'noroot') return null; // smartctl needs root; the System panel already says sudo is missing
  if (!sm.disks.length) return null;
  const n = (v) => (v == null ? '–' : v);
  return html`<section class="panel"><div class="panel-h"><h2>Disk health</h2><span class="small muted">S.M.A.R.T., checked every 30 minutes. Sleeping disks are skipped.</span></div>
    <div class="table-wrap"><table class="t"><thead><tr><th>Health</th><th>Disk</th><th class="num">Temp</th><th class="num hide-sm">Powered on</th><th class="num hide-sm" title="Reallocated sectors">Realloc.</th><th class="num" title="Pending + uncorrectable sectors">Bad sectors</th><th class="num" title="NVMe: percentage of rated endurance used">Wear</th></tr></thead>
    <tbody>${sm.disks.map((d) => html`<tr>
      <td><span class=${`chip ${!d.passed ? 'bad' : d.problem ? 'warn' : 'ok'}`}>${!d.passed ? 'Failing' : d.problem ? 'Warning' : 'OK'}</span></td>
      <td><span class="mono">${d.dev}</span><div class="small muted">${d.model}${d.serial ? ` · ${d.serial}` : ''}</div></td>
      <td class="num">${d.temp == null ? '–' : `${d.temp} °C`}</td>
      <td class="num ink2 hide-sm">${d.hours == null ? '–' : dur(d.hours * 3600e3)}</td>
      <td class="num hide-sm">${n(d.realloc)}</td>
      <td class=${`num ${d.pending > 0 || d.uncorrectable > 0 ? 'err' : ''}`}>${d.pending == null && d.uncorrectable == null ? (d.mediaErrors == null ? '–' : `${d.mediaErrors} media err.`) : (d.pending || 0) + (d.uncorrectable || 0)}</td>
      <td class="num">${d.wear == null ? '–' : `${d.wear}%`}</td></tr>`)}</tbody></table></div></section>`;
}

// ---------- updates ----------
function UpdatesTab({ s }) {
  const [refresh, setRefresh] = useState(0);
  const [view, setView] = useState('available');
  const p = useLoad(() => api(`/api/servers/${s.id}/packages${refresh ? '?refresh=1' : ''}`), [s.id, refresh]);
  const [sel, setSel] = useState(() => new Set());
  const list = p.data?.packages || [];
  const sort = useSort(list, 'security', 1, (x) => (x.security ? 0 : 1)); // security fixes first
  const toggle = (name) => setSel((x) => { const n = new Set(x); n.has(name) ? n.delete(name) : n.add(name); return n; });
  const auto = s.snapshot?.autoUpdates;
  const op = can('operator');
  const [later, setLater] = useState(false);
  const timers = useLoad(() => (auto ? api(`/api/servers/${s.id}/timers`) : null), [s.id, auto]);
  const autoNext = timers.data?.find((x) => AUTO_TIMERS.includes(x.unit) && x.active === 'active' && x.next)?.next;
  const toggleAuto = async (e) => {
    e.target.checked = auto; // reflect reality until the job has run and the next probe reports it
    if (auto && !await confirmBox({ title: `Turn off automatic updates on ${s.name}?`, body: 'Security fixes then only arrive when you install updates yourself or on a schedule.', confirm: 'Turn off', danger: true })) return;
    await startJob(s, auto ? 'unattended-off' : 'unattended');
  };
  return html`<div class="stack"><${RestartsPanel} s=${s} />
    <section class="panel">
    <div class="panel-h"><h2 class="sr">Updates</h2><div class="seg" role="group" aria-label="Updates view">${[['available', 'Available updates'], ['history', 'History']].map(([k, l]) => html`<button aria-pressed=${view === k} onClick=${() => setView(k)}>${l}</button>`)}</div>
      ${p.data && html`<span class="small muted">${p.data.manager || 'unknown package manager'}</span>`}
      ${view === 'available' && html`<div class="row right">
        <${Btn} size="sm" icon="refresh" onClick=${() => setRefresh((r) => r + 1)} disabled=${p.loading}>Refresh package lists</${Btn}>
        ${op && sel.size > 0 && html`<${Btn} size="sm" onClick=${() => startJob(s, 'upgrade-packages', { PACKAGES: [...sel].join(' ') })}>Upgrade ${sel.size} selected</${Btn}>`}
        ${can('admin') && list.length > 0 && html`<${Btn} size="sm" icon="clock" onClick=${() => setLater(true)}>Later…</${Btn}>`}
        ${op && list.length > 0 && html`<${Btn} size="sm" kind="primary" onClick=${() => startJob(s, 'update')}>Upgrade all ${list.length}</${Btn}>`}
      </div>`}</div>
    ${later && html`<${ScheduleRun} s=${s} scriptId="update" onClose=${() => setLater(false)} />`}
    ${auto != null && html`<div class="panel-b row b-bottom">
      <label class="check"><input type="checkbox" class="switch" role="switch" checked=${auto} disabled=${!op} onChange=${(e) => toggleAuto(e).catch((x) => toast(x.message, 'bad'))} />Automatic security updates</label>
      <span class="small muted">${auto ? `${s.snapshot.pkg === 'apt' ? 'unattended-upgrades' : 'dnf-automatic'} installs updates by itself${autoNext ? `, next run ${when(autoNext)}` : ''}.` : 'Updates are only installed when you or a schedule run them.'}</span></div>`}
    ${view === 'history' ? html`<${UpdateHistory} s=${s} />` : p.loading ? html`<${Loading} />` : p.error ? html`<div class="panel-b"><${ErrorNote} error=${p.error} retry=${p.reload} /></div>`
      : !list.length ? html`<${Empty} title="Everything is up to date">Fleetdeck checks the package cache every minute. Refresh the package lists to look for new releases now.</${Empty}>`
      : html`<div class="table-wrap"><table class="t"><thead><tr><th class="shrink"><input type="checkbox" aria-label="Select all" disabled=${!op} checked=${sel.size === list.length} onChange=${() => setSel(sel.size === list.length ? new Set() : new Set(list.map((x) => x.name)))} /></th>
        <${Th} sort=${sort} k="security" get=${(x) => (x.security ? 0 : 1)} class="shrink"><span class="sr">Security</span></${Th}><${Th} sort=${sort} k="name">Package</${Th}><${Th} sort=${sort} k="from" class="hide-sm">Installed</${Th}><${Th} sort=${sort} k="version">Available</${Th}><${Th} sort=${sort} k="source" class="hide-sm">Source</${Th}></tr></thead>
        <tbody>${sort.rows.map((x) => html`<tr><td><input type="checkbox" disabled=${!op} checked=${sel.has(x.name)} onChange=${() => toggle(x.name)} aria-label=${`Select ${x.name}`} /></td>
          <td>${x.security && html`<span class="chip warn">security</span>`}</td>
          <td class="mono">${x.name}</td><td class="mono ink2 hide-sm">${x.from || '–'}</td><td class="mono">${x.version}</td><td class="ink2 hide-sm">${x.source}</td></tr>`)}</tbody></table></div>`}
  </section></div>`;
}

const ACTION_WORDS = { install: 'installed', upgrade: 'upgraded', remove: 'removed', downgrade: 'downgraded', reinstall: 'reinstalled' };
const changeSummary = (pkgs) => Object.entries(Object.groupBy(pkgs, (x) => x.action)).map(([a, l]) => `${l.length} ${ACTION_WORDS[a] || a}`).join(', ') || 'No package changes';

function UpdateHistory({ s }) {
  const h = useLoad(() => api(`/api/servers/${s.id}/packages/history`), [s.id]);
  if (h.loading) return html`<${Loading} />`;
  if (h.error) return html`<div class="panel-b"><${ErrorNote} error=${h.error} retry=${h.reload} /></div>`;
  if (!h.data.length) return html`<${Empty} title="No update history">Fleetdeck reads the apt history log or dnf history. Neither has entries on this server.</${Empty}>`;
  return html`<div class="table-wrap"><table class="t"><thead><tr><th>When</th><th class="hide-sm">Source</th><th>Changes</th></tr></thead>
    <tbody>${h.data.map((x) => html`<tr><td class="nowrap ink2 shrink" title="Server's local time">${x.when}</td>
      <td class="ink2 hide-sm">${x.source === 'unattended-upgrades' ? html`<span class="chip">automatic</span>` : html`<span class="mono small">${x.source || '–'}</span>`}</td>
      <td>${x.packages.length ? html`<details><summary>${changeSummary(x.packages)}</summary><ul class="pkgs">${x.packages.map((c) => html`<li><span class="ink2">${c.action}</span> <span class="mono">${c.name}</span> <span class="mono muted">${c.version}</span></li>`)}</ul></details>` : changeSummary(x.packages)}</td></tr>`)}</tbody></table></div>`;
}

// The systemd timers that install updates for unattended-upgrades / dnf-automatic.
const AUTO_TIMERS = ['apt-daily-upgrade.timer', 'dnf-automatic-install.timer', 'dnf-automatic.timer', 'dnf5-automatic.timer'];

// ---------- one-off runs ("in 3 hours", "at 22:00") ----------
const HHMM = /^([01]?\d|2[0-3]):([0-5]\d)$/;
function runAtOf(mode, hours, time) {
  if (mode === 'in') return hours > 0 ? Date.now() + hours * 3600e3 : null;
  const m = HHMM.exec(time.trim());
  if (!m) return null;
  const d = new Date();
  d.setHours(+m[1], +m[2], 0, 0);
  if (d <= Date.now()) d.setDate(d.getDate() + 1); // already past today: tomorrow
  return d.getTime();
}

function ScheduleRun({ s, scriptId = 'update', onClose }) {
  const scripts = useLoad(() => api('/api/scripts'));
  const [v, setV] = useState({ script_id: scriptId, params: {}, mode: 'at', hours: 2, time: '22:00' });
  const [err, setErr] = useState('');
  const script = scripts.data?.find((x) => x.id === v.script_id);
  const runAt = runAtOf(v.mode, Number(v.hours), v.time);
  const save = async () => {
    setErr('');
    try {
      await api('/api/schedules', { method: 'POST', body: { name: `${script?.name || v.script_id} on ${s.name}`.slice(0, 100), script_id: v.script_id, params: v.params, targets: { servers: [s.id] }, run_at: runAt } });
      toast(`Scheduled for ${when(runAt)}`, 'ok', { label: 'Show', href: `#/servers/${s.id}/jobs` });
      onClose(true);
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${`Run later on ${s.name}`} onClose=${onClose}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Schedule" disabled=${!runAt || !script} />`}>
    <${Field} label="Script">${scripts.loading ? html`<${Loading} />` : html`<select value=${v.script_id} onChange=${(e) => setV({ ...v, script_id: e.target.value, params: {} })}>${scripts.data.map((x) => html`<option value=${x.id}>${x.name}</option>`)}</select>`}</${Field}>
    ${script?.params.map((p) => html`<${ParamInput} p=${p} value=${v.params[p.name] ?? p.default} onChange=${(x) => setV({ ...v, params: { ...v.params, [p.name]: x } })} />`)}
    <div class="seg" role="group" aria-label="When">${[['at', 'At a time'], ['in', 'In some hours']].map(([k, l]) => html`<button type="button" aria-pressed=${v.mode === k} onClick=${() => setV({ ...v, mode: k })}>${l}</button>`)}</div>
    ${v.mode === 'at'
      ? html`<${Field} label="Time (24-hour)" hint="Today, or tomorrow if that time has passed"><input class="mono" inputmode="numeric" placeholder="22:00" value=${v.time} onInput=${(e) => setV({ ...v, time: e.target.value })} autofocus /></${Field}>`
      : html`<${Field} label="Hours from now"><input type="number" min="0.25" step="0.25" value=${v.hours} onInput=${(e) => setV({ ...v, hours: e.target.value })} autofocus /></${Field}>`}
    <p class="small ink2">${runAt ? `Runs ${when(runAt)}, once.` : 'Enter a time like 22:00.'}</p>
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}

// Schedules that reach this server (directly, by tag or "all"), with one-off runs cancellable here.
function ScheduledPanel({ s }) {
  const list = useLoad(() => api('/api/schedules'), [], { poll: 60_000 });
  const [add, setAdd] = useState(false);
  const mine = (list.data || []).filter((x) => x.enabled && (x.targets.all || x.targets.servers?.includes(s.id) || x.targets.tags?.some((t) => s.tags.includes(t))))
    .sort((a, b) => (a.next || Infinity) - (b.next || Infinity));
  const cancel = async (x) => {
    if (!await confirmBox({ title: `Cancel "${x.name}"?`, confirm: 'Cancel run', danger: true })) return;
    await api(`/api/schedules/${x.id}`, { method: 'DELETE' });
    list.reload(true);
  };
  return html`<section class="panel">
    <div class="panel-h"><h2>Scheduled</h2><a class="small" href="#/schedules">All schedules</a>
      <${Btn} size="sm" icon="clock" class="right" onClick=${() => setAdd(true)}>Run later…</${Btn}></div>
    ${list.error ? html`<div class="panel-b"><${ErrorNote} error=${list.error} retry=${list.reload} /></div>`
      : !list.loading && !mine.length ? html`<div class="panel-b small muted">Nothing scheduled on this server.</div>`
      : mine.length > 0 && html`<div class="table-wrap"><table class="t"><tbody>${mine.map((x) => html`<tr>
        <td><b>${x.name}</b><div class="small muted">${x.script_name}</div></td>
        <td class="ink2">${x.once ? 'Once' : html`<span class="mono">${x.cron}</span>`}</td>
        <td class="nowrap">${x.next ? when(x.next) : '–'}</td>
        <td class="act">${x.once && html`<${Btn} size="sm" kind="quiet-danger" onClick=${() => cancel(x).catch((e) => toast(e.message, 'bad'))}>Cancel</${Btn}>`}</td></tr>`)}</tbody></table></div>`}
    ${add && html`<${ScheduleRun} s=${s} onClose=${(saved) => { setAdd(false); if (saved) list.reload(true); }} />`}
  </section>`;
}

// Services whose binaries or libraries were replaced by an update but are still running the old code.
function RestartsPanel({ s }) {
  const r = useLoad(() => api(`/api/servers/${s.id}/restarts`), [s.id]);
  if (r.loading || !r.data?.length) return r.error ? html`<${ErrorNote} error=${`Couldn't check for services to restart: ${r.error}`} retry=${r.reload} />` : null;
  const safe = r.data.filter((x) => !x.careful).map((x) => x.unit);
  const op = can('operator');
  const one = async (x) => {
    if (x.careful && !await confirmBox({ title: `Restart ${x.unit}?`, body: 'Restarting it can end logins, desktop sessions or networking on this server.', confirm: 'Restart', danger: true })) return;
    await startJob(s, 'restart-services', { SERVICES: x.unit });
  };
  return html`<section class="panel">
    <div class="panel-h"><h2>Services to restart</h2><span class="small muted">Still running code that an update replaced. Restart them to finish the update without a reboot.</span>
      ${op && safe.length > 0 && html`<${Btn} size="sm" kind="primary" class="right" onClick=${() => startJob(s, 'restart-services', { SERVICES: safe.join(' ') })}>Restart ${safe.length === r.data.length ? 'all' : `${safe.length} safe`}</${Btn}>`}</div>
    <div class="panel-b row">${r.data.map((x) => html`<span class="row nw"><span class=${`chip ${x.careful ? 'warn' : ''}`} title=${x.careful ? 'Can interrupt sessions or networking: restart on its own' : ''}>${x.unit.replace(/\.service$/, '')}</span>
      ${op && html`<${Btn} size="sm" kind="ghost" icon="refresh" title=${`Restart ${x.unit}`} onClick=${() => one(x)} />`}</span>`)}</div>
  </section>`;
}

// ---------- services ----------
const cap = (w) => w[0].toUpperCase() + w.slice(1);
function ServicesTab({ s }) {
  const svc = useLoad(() => api(`/api/servers/${s.id}/services`), [s.id]);
  const failedCount = (svc.data || []).filter((u) => u.active === 'failed').length;
  // Open on "Failed" when something failed; otherwise "Running".
  const [f, setFilter] = useQuery('show');
  const filter = f || (failedCount ? 'failed' : 'running');
  const [q, setQ] = useQuery('q');
  const [status, setStatus] = useState(null);
  const op = can('operator');
  const list = useMemo(() => (svc.data || []).filter((u) => {
    if (filter === 'running' && u.active !== 'active') return false;
    if (filter === 'failed' && u.active !== 'failed') return false;
    if (filter === 'inactive' && u.active !== 'inactive') return false;
    return !q || `${u.unit} ${u.description}`.toLowerCase().includes(q.toLowerCase());
  }), [svc.data, q, filter]);
  const sort = useSort(list, 'unit');
  const act = async (u, action) => {
    if (['stop', 'disable'].includes(action) && !await confirmBox({ title: `${cap(action)} ${u.unit}?`, confirm: cap(action), danger: true })) return false;
    await api(`/api/servers/${s.id}/services`, { method: 'POST', body: { unit: u.unit, action } });
    toast(`${u.unit}: ${action} done`, 'ok');
    svc.reload(true);
    return true;
  };
  const showStatus = async (u) => setStatus({ u, text: (await api(`/api/servers/${s.id}/services/status?unit=${encodeURIComponent(u.unit)}`)).text });
  const fromModal = async (action) => { if (await act(status.u, action)) await showStatus({ ...status.u, active: action === 'stop' ? 'inactive' : 'active', sub: 'running' }); };
  return html`<section class="panel">
    <div class="panel-h">
      <div class="seg" role="group" aria-label="Filter">${[['running', 'Running'], ['failed', `Failed${failedCount ? ` (${failedCount})` : ''}`], ['inactive', 'Stopped'], ['all', 'All'], ['timers', 'Timers']].map(([k, l]) => html`<button aria-pressed=${filter === k} onClick=${() => setFilter(k)}>${l}</button>`)}</div>
      <input type="search" placeholder="Filter services" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter services" />
      <${Btn} size="sm" kind="ghost" icon="refresh" class="right" onClick=${() => svc.reload()}>Refresh</${Btn}>
    </div>
    ${filter === 'timers' ? html`<${TimersTable} s=${s} q=${q} />`
      : svc.loading && !svc.data ? html`<${Loading} />` : svc.error ? html`<div class="panel-b"><${ErrorNote} error=${svc.error} retry=${svc.reload} /></div>`
      : !list.length ? html`<${Empty} title="No services match" />`
      : html`<div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="active">State</${Th}><${Th} sort=${sort} k="unit">Service</${Th}><${Th} sort=${sort} k="description" class="hide-sm">Description</${Th}><${Th} sort=${sort} k="enabled" class="hide-sm">Start at boot</${Th}><th class="act"></th></tr></thead>
      <tbody>${sort.rows.map((u) => html`<tr class="click" onClick=${(e) => !e.target.closest('button,a') && showStatus(u).catch((x) => toast(x.message, 'bad'))}>
        <td><span class=${`chip ${u.active === 'active' ? 'ok' : u.active === 'failed' ? 'bad' : ''}`}>${u.active === 'active' ? u.sub : u.active}</span></td>
        <td><button class="btn ghost sm cell" title="Show status" onClick=${() => showStatus(u)}>${u.unit.replace(/\.service$/, '')}</button></td>
        <td class="ink2 trunc max-cell hide-sm">${u.description}</td>
        <td class="ink2 hide-sm">${u.enabled}</td>
        <td class="act">${op && html`<div class="row nw">
          ${u.active === 'active' ? html`<${Btn} size="sm" kind="ghost" onClick=${() => act(u, 'restart')}>Restart</${Btn}><${Btn} size="sm" kind="quiet-danger" onClick=${() => act(u, 'stop')}>Stop</${Btn}>`
            : html`<${Btn} size="sm" kind="ghost" onClick=${() => act(u, 'start')}>Start</${Btn}>`}
          ${u.enabled === 'enabled' ? html`<${Btn} size="sm" kind="quiet-danger" onClick=${() => act(u, 'disable')}>Disable</${Btn}>`
            : u.enabled === 'disabled' ? html`<${Btn} size="sm" kind="ghost" onClick=${() => act(u, 'enable')}>Enable</${Btn}>` : ''}
        </div>`}</td></tr>`)}</tbody></table></div>`}
    ${status && html`<${Modal} wide title=${status.u.unit} onClose=${() => setStatus(null)}
      footer=${html`<a class="btn ghost" href=${logsHref(s, status.u.unit)}>Open logs</a><span class="grow" />
        ${!op ? '' : status.u.active === 'active' ? html`<${Btn} kind="danger" onClick=${() => fromModal('stop')}>Stop</${Btn}><${Btn} kind="primary" onClick=${() => fromModal('restart')}>Restart</${Btn}>`
          : html`<${Btn} kind="primary" onClick=${() => fromModal('start')}>Start</${Btn}>`}`}>
      <${Output} name=${status.u.unit} text=${status.text} /></${Modal}>`}
  </section>`;
}

// systemd timers: the cron jobs of modern distros.
function TimersTable({ s, q }) {
  const t = useLoad(() => api(`/api/servers/${s.id}/timers`), [s.id]);
  const list = useMemo(() => (t.data || []).filter((x) => !q || `${x.unit} ${x.description}`.toLowerCase().includes(q.toLowerCase())), [t.data, q]);
  const sort = useSort(list, 'next');
  const op = can('operator');
  const svc = (unit, action) => api(`/api/servers/${s.id}/services`, { method: 'POST', body: { unit, action } });
  const runNow = async (x) => { await svc(x.activates, 'start'); toast(`${x.activates} started`, 'ok'); t.reload(true); };
  const toggle = async (x) => {
    const on = x.enabled !== 'enabled';
    if (!on && !await confirmBox({ title: `Disable ${x.unit}?`, body: `${x.activates} stops running on its schedule.`, confirm: 'Disable', danger: true })) return;
    for (const a of on ? ['enable', 'start'] : ['stop', 'disable']) await svc(x.unit, a);
    toast(`${x.unit} ${on ? 'enabled' : 'disabled'}`, 'ok');
    t.reload(true);
  };
  const time = (ms, text) => (ms ? html`<span title=${when(ms)}>${ago(ms)}</span>` : html`<span class="muted" title=${text}>${text && text !== 'n/a' ? text : '–'}</span>`);
  if (t.loading && !t.data) return html`<${Loading} />`;
  if (t.error) return html`<div class="panel-b"><${ErrorNote} error=${t.error} retry=${t.reload} /></div>`;
  if (!list.length) return html`<${Empty} title="No timers match" />`;
  return html`<div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="unit">Timer</${Th}><${Th} sort=${sort} k="next">Next run</${Th}><${Th} sort=${sort} k="last" desc class="hide-sm">Last run</${Th}><${Th} sort=${sort} k="result">Last result</${Th}><${Th} sort=${sort} k="enabled" class="hide-sm">Start at boot</${Th}><th class="act"></th></tr></thead>
    <tbody>${sort.rows.map((x) => html`<tr>
      <td><span class="mono">${x.unit.replace(/\.timer$/, '')}</span><div class="small muted trunc max-cell">${x.description}</div></td>
      <td class="nowrap">${x.active === 'active' ? time(x.next, x.nextText) : html`<span class="muted">inactive</span>`}</td>
      <td class="nowrap ink2 hide-sm">${time(x.last, x.lastText)}</td>
      <td>${x.running ? html`<span class="chip info">running</span>` : x.result && x.last ? html`<span class=${`chip ${x.result === 'success' ? 'ok' : 'bad'}`}>${x.result}</span>` : ''}</td>
      <td class="ink2 hide-sm">${x.enabled}</td>
      <td class="act"><div class="row nw">
        ${op && html`<${Btn} size="sm" kind="ghost" icon="play" disabled=${!x.activates || x.running} onClick=${() => runNow(x)}>Run now</${Btn}>`}
        ${op && ['enabled', 'disabled'].includes(x.enabled) && html`<${Btn} size="sm" kind=${x.enabled === 'enabled' ? 'quiet-danger' : 'ghost'} onClick=${() => toggle(x)}>${x.enabled === 'enabled' ? 'Disable' : 'Enable'}</${Btn}>`}
        ${x.activates && html`<a class="btn sm ghost" href=${logsHref(s, x.activates)}>Logs</a>`}
      </div></td></tr>`)}</tbody></table></div>`;
}

// ---------- logs ----------
const PRIOS = [['', 'All levels'], ['3', 'Errors'], ['4', 'Warnings and worse'], ['6', 'Info and worse']];
const PRIO_NAME = ['emerg', 'alert', 'crit', 'error', 'warn', 'notice', 'info', 'debug'];
function LogsTab({ s }) {
  const [unit, setUnit] = useQuery('unit');
  const [priority, setPriority] = useQuery('level');
  const [since, setSince] = useQuery('since', '1h');
  const [lines, setLines] = useQuery('lines', '500');
  const [q, setQ] = useQuery('q');
  const [follow, setFollow] = useQuery('follow');
  const [draft, setDraft] = useState(unit);
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => setDraft(unit), [unit]);
  useEffect(() => { if (draft === unit) return; const t = setTimeout(() => setUnit(draft), 400); return () => clearTimeout(t); }, [draft]);
  const qs = new URLSearchParams(Object.entries({ unit, priority, since, lines }).filter(([, v]) => v)).toString();
  // Following pauses while you scroll back through older entries.
  const logs = useLoad(() => api(`/api/servers/${s.id}/logs?${qs}`), [s.id, qs], { poll: follow ? 5000 : 0, paused: scrolled });
  const list = useMemo(() => {
    const n = q.toLowerCase();
    return (logs.data || []).filter((e) => !n || `${e.id} ${e.msg}`.toLowerCase().includes(n)).reverse();
  }, [logs.data, q]);
  return html`<section class="panel">
    <div class="panel-h">
      <input class="mono" placeholder="Unit, e.g. nginx" value=${draft} onInput=${(e) => setDraft(e.target.value)} aria-label="Unit" />
      <select value=${priority} onChange=${(e) => setPriority(e.target.value)} aria-label="Level">${PRIOS.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>
      <select value=${since} onChange=${(e) => setSince(e.target.value)} aria-label="Time range">${[['15m', 'Last 15 min'], ['1h', 'Last hour'], ['6h', 'Last 6 hours'], ['24h', 'Last day'], ['7d', 'Last week']].map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>
      <select value=${lines} onChange=${(e) => setLines(e.target.value)} aria-label="Lines">${['200', '500', '2000', '5000'].map((v) => html`<option value=${v}>${v} lines</option>`)}</select>
      <input type="search" placeholder="Search" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Search messages" />
      <div class="row nw right">
        <div class="seg"><button aria-pressed=${!!follow} onClick=${() => setFollow(follow ? '' : '1')} title="Reload every 5 seconds">${follow && html`<span class=${`lamp ${scrolled ? 'paused' : 'running'}`} aria-hidden="true" />`}${follow && scrolled ? 'Paused' : 'Follow'}</button></div>
        <${Btn} size="sm" kind="ghost" icon="refresh" onClick=${() => logs.reload()}>Refresh</${Btn}>
      </div>
    </div>
    ${logs.loading && !logs.data ? html`<${Loading} />` : logs.error ? html`<div class="panel-b"><${ErrorNote} error=${logs.error} retry=${logs.reload} /></div>`
      : !list.length ? html`<${Empty} title="No log entries">Nothing matches these filters. Widen the time range or clear the search.</${Empty}>`
      : html`<div class="logs" tabindex="0" onScroll=${(e) => setScrolled(e.target.scrollTop > 40)}>${list.map((e) => html`<div class=${`e p${e.p}`}>
          <span class="muted">${logTime(e.t)}</span><span class=${`p${e.p}`}>${PRIO_NAME[e.p]}</span><span class="trunc ink2" title=${e.unit}>${e.id}</span><span class="m">${e.msg}</span></div>`)}</div>`}
  </section>`;
}

// ---------- processes ----------
function ProcessesTab({ s }) {
  const [held, bind] = useHold();
  const ps = useLoad(() => api(`/api/servers/${s.id}/processes`), [s.id], { poll: 10_000, paused: held });
  const [q, setQ] = useQuery('q');
  const filtered = useMemo(() => (ps.data || []).filter((p) => !q || `${p.args} ${p.user} ${p.pid}`.toLowerCase().includes(q.toLowerCase())), [ps.data, q]);
  const sort = useSort(filtered, 'cpu', -1);
  const list = sort.rows.slice(0, 80);
  const kill = async (p, signal) => {
    if (!await confirmBox({ title: `Send ${signal} to ${p.comm} (${p.pid})?`, body: p.args, confirm: signal === 'KILL' ? 'Force kill' : 'Terminate', danger: true })) return;
    await api(`/api/servers/${s.id}/processes/kill`, { method: 'POST', body: { pid: p.pid, signal } });
    toast(`Sent ${signal} to ${p.pid}`, 'ok');
    ps.reload(true);
  };
  return html`<section class="panel">
    <div class="panel-h">
      <input type="search" placeholder="Filter processes" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter processes" />
      <span class="small muted right" aria-live="polite">${held ? 'Paused while you point at the table' : 'Updates every 10 seconds'}</span>
    </div>
    ${ps.loading && !ps.data ? html`<${Loading} />` : ps.error ? html`<div class="panel-b"><${ErrorNote} error=${ps.error} retry=${ps.reload} /></div>`
      : html`<div class="table-wrap" ...${bind}><table class="t"><thead><tr><${Th} sort=${sort} k="pid" class="num">PID</${Th}><${Th} sort=${sort} k="user" class="hide-sm">User</${Th}><${Th} sort=${sort} k="cpu" class="num" desc>CPU</${Th}><${Th} sort=${sort} k="mem" class="num" desc>Memory</${Th}><${Th} sort=${sort} k="rss" class="num hide-sm" desc>RSS</${Th}><${Th} sort=${sort} k="elapsed" desc class="hide-sm">Running</${Th}><${Th} sort=${sort} k="args">Command</${Th}><th class="act"></th></tr></thead>
      <tbody>${list.map((p) => html`<tr><td class="num mono">${p.pid}</td><td class="ink2 hide-sm">${p.user}</td><td class="num">${p.cpu.toFixed(1)}%</td><td class="num">${p.mem.toFixed(1)}%</td>
        <td class="num ink2 hide-sm">${bytes(p.rss)}</td><td class="ink2 nowrap hide-sm">${dur(p.elapsed * 1000)}</td><td class="mono trunc max-cell" title=${p.args}>${p.args}</td>
        <td class="act">${can('operator') && html`<div class="row nw"><${Btn} size="sm" kind="ghost" onClick=${() => kill(p, 'TERM')}>Terminate</${Btn}><${Btn} size="sm" kind="quiet-danger" onClick=${() => kill(p, 'KILL')}>Force kill</${Btn}></div>`}</td></tr>`)}</tbody></table></div>`}
  </section>`;
}

// ---------- containers: Proxmox guests and Docker ----------
function ContainersTab({ s }) {
  const n = s.snapshot;
  return html`<div class="stack">
    ${n?.pve && html`<${GuestsPanel} s=${s} pve=${n.pve} />`}
    ${(!n?.pve || n.docker) && html`<${DockerPanel} s=${s} />`}
  </div>`;
}

// Read-only, from the per-minute probe. Managing guests stays in Proxmox.
function GuestsPanel({ s, pve }) {
  const [hist, setHist] = useState(null);
  const sort = useSort(pve.guests, 'vmid');
  const host = s.host.includes(':') ? `[${s.host}]` : s.host;
  return html`<section class="panel">
    <div class="panel-h"><h2>Virtual machines & LXC</h2><span class="small muted">${pve.guests.filter((g) => g.status === 'running').length} of ${pve.guests.length} running on node ${pve.node}</span>
      <a class="btn ghost sm right" href=${`https://${host}:8006`} target="_blank" rel="noopener noreferrer"><${Icon} name="ext" size=${14} />Open Proxmox</a></div>
    ${pve.error ? html`<${Empty} title="Can't read the guests">${pve.error}</${Empty}>` : !pve.guests.length ? html`<${Empty} title="No guests on this node" />` : html`<div class="table-wrap"><table class="t">
      <thead><tr><${Th} sort=${sort} k="status">State</${Th}><${Th} sort=${sort} k="vmid" class="num">ID</${Th}><${Th} sort=${sort} k="name">Name</${Th}><${Th} sort=${sort} k="type" class="hide-sm">Type</${Th}><${Th} sort=${sort} k="cpu" class="num" desc>CPU</${Th}><${Th} sort=${sort} k="mem" class="num" desc>Memory</${Th}><${Th} sort=${sort} k="disk" class="num hide-sm" desc>Disk</${Th}><${Th} sort=${sort} k="uptime" class="hide-sm" desc>Up</${Th}><th class="act"></th></tr></thead>
      <tbody>${sort.rows.map((g) => {
        const on = g.status === 'running';
        return html`<tr>
        <td><span class=${`chip ${on ? 'ok' : g.status === 'stopped' ? '' : 'warn'}`}>${g.status}</span></td>
        <td class="num mono">${g.vmid}</td><td class="mono">${g.name}</td><td class="ink2 hide-sm">${g.type === 'lxc' ? 'LXC' : 'VM'}</td>
        <td class="num">${on ? html`<${CellBar} value=${g.cpu}><span title=${`of ${g.maxcpu} cores`}>${pct(g.cpu)}</span></${CellBar}>` : '–'}</td>
        <td class="num nowrap">${on && g.maxmem ? html`<${CellBar} value=${g.mem / g.maxmem}><span title=${`${bytes(g.mem)} of ${bytes(g.maxmem)}`}>${bytes(g.mem)}</span></${CellBar}>` : html`<span class="ink2">${bytes(g.maxmem)}</span>`}</td>
        <td class="num ink2 nowrap hide-sm">${g.disk ? `${bytes(g.disk)} of ${bytes(g.maxdisk)}` : bytes(g.maxdisk)}</td>
        <td class="ink2 nowrap hide-sm">${on ? uptime(g.uptime) : '–'}</td>
        <td class="act"><${Btn} size="sm" kind="ghost" onClick=${() => setHist(g)}>History</${Btn}></td></tr>`;
      })}</tbody></table></div>`}
    ${hist && html`<${Modal} wide title=${`History: ${hist.name}`} onClose=${() => setHist(null)}><${ContainerHistory} s=${s} name=${`pve:${hist.vmid}`} /></${Modal}>`}
  </section>`;
}

function DockerPanel({ s }) {
  const [held, bind] = useHold();
  const c = useLoad(() => api(`/api/servers/${s.id}/containers`), [s.id], { poll: 15_000, paused: held });
  const [logs, setLogs] = useState(null);
  const [hist, setHist] = useState(null);
  const sort = useSort(c.data?.containers, 'name');
  const act = async (x, action) => {
    if (['stop', 'rm'].includes(action) && !await confirmBox({ title: `${action === 'rm' ? 'Remove' : 'Stop'} ${x.name}?`, body: action === 'rm' ? 'The container is deleted. Its volumes are kept.' : null, confirm: action === 'rm' ? 'Remove' : 'Stop', danger: true })) return;
    await api(`/api/servers/${s.id}/containers`, { method: 'POST', body: { id: x.id, action } });
    toast(`${x.name}: ${action === 'rm' ? 'removed' : `${action} done`}`, 'ok');
    c.reload(true);
  };
  const showLogs = async (x) => setLogs({ name: x.name, text: (await api(`/api/servers/${s.id}/containers/logs?id=${encodeURIComponent(x.id)}&lines=500`)).text });
  if (c.loading && !c.data) return html`<section class="panel"><${Loading} /></section>`;
  if (c.error) return html`<${ErrorNote} error=${c.error} retry=${c.reload} />`;
  if (!c.data.installed) return html`<section class="panel"><${Empty} title="Docker isn't installed on this server">Install Docker and its containers show up here with live stats.</${Empty}></section>`;
  const cores = s.snapshot?.cores || 1;
  const op = can('operator');
  return html`<section class="panel">
    <div class="panel-h"><h2>Containers</h2><span class="small muted">${c.data.containers.filter((x) => x.state === 'running').length} of ${c.data.containers.length} running${held ? ' · paused while you point at the table' : ''}</span>
      ${op && html`<${Btn} size="sm" kind="ghost" class="right" onClick=${() => startJob(s, 'docker-prune')}>Clean up unused data</${Btn}>`}</div>
    ${!c.data.containers.length ? html`<${Empty} title="No containers" />` : html`<div class="table-wrap" ...${bind}><table class="t">
      <thead><tr><${Th} sort=${sort} k="state">State</${Th}><${Th} sort=${sort} k="name">Name</${Th}><${Th} sort=${sort} k="image" class="hide-sm">Image</${Th}><${Th} sort=${sort} k="status" class="hide-sm">Status</${Th}><${Th} sort=${sort} k="cpu" get=${(x) => parseFloat(x.cpu)} class="num" desc>CPU</${Th}><${Th} sort=${sort} k="mem" get=${(x) => parseFloat(x.memPct)} class="num" desc>Memory</${Th}><${Th} sort=${sort} k="ports" class="hide-sm">Ports</${Th}><th class="act"></th></tr></thead>
      <tbody>${sort.rows.map((x) => html`<tr>
        <td><div class="row nw"><span class=${`chip ${x.state === 'running' ? 'ok' : ['exited', 'dead'].includes(x.state) ? '' : 'warn'}`}>${x.state}</span>
          ${/\(unhealthy\)/.test(x.status) ? html`<span class="chip bad">unhealthy</span>` : /\(healthy\)/.test(x.status) ? html`<span class="chip ok">healthy</span>` : ''}</div></td>
        <td class="mono">${x.name}</td><td class="mono ink2 trunc max-cell sm hide-sm" title=${x.image}>${x.image}</td><td class="ink2 nowrap hide-sm">${x.status}</td>
        <td class="num">${x.cpu ? html`<${CellBar} value=${parseFloat(x.cpu) / 100 / cores}>${x.cpu}</${CellBar}>` : '–'}</td>
        <td class="num nowrap">${x.memPct ? html`<${CellBar} value=${parseFloat(x.memPct) / 100}><span title=${x.mem}>${x.memPct}</span></${CellBar}>` : html`<span class="ink2">${x.mem}</span>`}</td>
        <td class="mono small ink2 trunc max-cell sm hide-sm" title=${x.ports}>${x.ports}</td>
        <td class="act"><div class="row nw">
          <${Btn} size="sm" kind="ghost" onClick=${() => setHist(x.name)}>History</${Btn}>
          <${Btn} size="sm" kind="ghost" onClick=${() => showLogs(x)}>Logs</${Btn}>
          ${!op ? '' : x.state === 'running' ? html`<${Btn} size="sm" kind="ghost" onClick=${() => act(x, 'restart')}>Restart</${Btn}><${Btn} size="sm" kind="quiet-danger" onClick=${() => act(x, 'stop')}>Stop</${Btn}>`
            : html`<${Btn} size="sm" kind="ghost" onClick=${() => act(x, 'start')}>Start</${Btn}><${Btn} size="sm" kind="quiet-danger" onClick=${() => act(x, 'rm')}>Remove</${Btn}>`}
        </div></td></tr>`)}</tbody></table></div>`}
    ${logs && html`<${Modal} wide title=${`Logs: ${logs.name}`} onClose=${() => setLogs(null)}><${Output} name=${logs.name} text=${logs.text} /></${Modal}>`}
    ${hist && html`<${Modal} wide title=${`History: ${hist}`} onClose=${() => setHist(null)}><${ContainerHistory} s=${s} name=${hist} /></${Modal}>`}
  </section>`;
}

// Recorded every minute by the probe, kept as long as server metrics.
function ContainerHistory({ s, name }) {
  const [range, setRange] = useState('6h');
  const m = useLoad(() => api(`/api/servers/${s.id}/containers/metrics?name=${encodeURIComponent(name)}&range=${range}`), [s.id, name, range]);
  const sync = useMemo(() => store(null), []);
  return html`<div class="stack">
    <${RangePicker} value=${range} onChange=${setRange} />
    ${m.error && html`<${ErrorNote} error=${m.error} retry=${m.reload} />`}
    <${LineChart} sync=${sync} title="CPU" data=${m.data} fmt=${(v) => `${v.toFixed(v < 10 ? 1 : 0)}%`} series=${[{ key: 'cpu', label: 'CPU (100% = one core)', color: 'var(--s1)' }]} />
    <${LineChart} sync=${sync} title="Memory" data=${m.data} fmt=${(v) => bytes(v)} series=${[{ key: 'mem', label: 'Memory', color: 'var(--s1)' }]} />
  </div>`;
}

// ---------- network: open ports, firewall, interfaces ----------
const EXPOSURE = {
  open: ['', 'Open to everyone'], restricted: ['', 'Open to some sources'], blocked: ['ok', 'Blocked by firewall'],
  bypass: ['bad', 'Open: Docker bypasses ufw'], local: ['', 'This server only'], unknown: ['', 'Depends on firewalld'],
};

function NetworkTab({ s }) {
  const net = useLoad(() => api(`/api/servers/${s.id}/network`), [s.id]);
  const [all, setAll] = useQuery('ports');
  const [reach, setReach] = useState(null);
  const [ufwDocker, setUfwDocker] = useState(false);
  const n = net.data;
  if (net.loading && !n) return html`<section class="panel"><${Loading} /></section>`;
  if (net.error) return html`<${ErrorNote} error=${net.error} retry=${net.reload} />`;
  const exposed = n.ports.filter((p) => p.exposure !== 'local');
  const list = all ? n.ports : exposed;
  const bypass = n.ports.filter((p) => p.exposure === 'bypass');
  const op = can('operator');
  const canUfwDocker = op && n.docker && n.firewall.kind === 'ufw' && n.ufwDocker !== 'active';
  const check = async () => setReach(await api(`/api/servers/${s.id}/network/reach`, { method: 'POST', body: { ports: exposed.filter((p) => p.proto === 'tcp').map((p) => p.port) } }));
  const allow = async (p) => {
    const route = p.docker && n.ufwDocker === 'active';
    const port = route ? p.containerPort : p.port;
    if (!await confirmBox({ title: `Open ${p.port}/${p.proto} to everyone?`, confirm: 'Allow', danger: true,
      body: route ? `Runs "ufw route allow proto ${p.proto} from any to any port ${port}" (${port} is the port inside the container). Every container listening on ${port} becomes reachable.` : `Runs "ufw allow ${port}/${p.proto}".` })) return;
    await api(`/api/servers/${s.id}/network/allow`, { method: 'POST', body: { port, proto: p.proto, route } });
    toast(`${p.port}/${p.proto} allowed`, 'ok');
    net.reload(true);
  };
  const ifaces = new Map((n.addrs || []).map((a) => [a.name, { ...a }]));
  for (const i of s.snapshot?.ifaces || []) ifaces.set(i.name, { ...(ifaces.get(i.name) || { name: i.name, addrs: [] }), ...i });
  return html`<div class="stack">
    ${bypass.length > 0 && html`<div class="alert-bar"><span class="grow"><b>Docker publishes ${bypass.map((p) => `${p.port}/${p.proto}`).join(', ')} past ufw.</b> These ports are reachable even though ufw blocks them, because Docker's own rules come first.</span>
      ${op && html`<${Btn} size="sm" onClick=${() => setUfwDocker(true)}>Let ufw control Docker</${Btn}>`}</div>`}
    ${n.ufwDocker === 'written' && html`<div class="alert-bar warn"><span class="grow"><b>The ufw-docker rules are written but not active.</b> Reboot the server to apply them.</span></div>`}

    <section class="panel">
      <div class="panel-h"><h2>Open ports</h2><span class="small muted">What is listening, and whether the firewall lets it through.</span>
        <div class="seg" role="group" aria-label="Show"><button aria-pressed=${!all} onClick=${() => setAll('')}>Reachable (${exposed.length})</button><button aria-pressed=${!!all} onClick=${() => setAll('all')}>All (${n.ports.length})</button></div>
        <div class="row right">
          ${op && html`<${Btn} size="sm" title="Try a TCP connection to each port from the Fleetdeck host" onClick=${check} disabled=${!exposed.some((p) => p.proto === 'tcp')}>Test from Fleetdeck</${Btn}>`}
          <${Btn} size="sm" kind="ghost" icon="refresh" title="Refresh" onClick=${() => { setReach(null); net.reload(); }} /></div></div>
      ${!list.length ? html`<${Empty} title=${all ? 'Nothing is listening' : 'Nothing is reachable from outside'}>${!all && n.ports.length ? 'Every listening port is bound to this server only.' : ''}</${Empty}>`
        : html`<div class="table-wrap"><table class="t"><thead><tr><th class="num">Port</th><th class="hide-sm">Listening on</th><th>Program</th><th>Exposure</th>${reach && html`<th title="TCP connect from the Fleetdeck host">From Fleetdeck</th>`}<th class="act"></th></tr></thead>
        <tbody>${list.map((p) => html`<tr>
          <td class="num mono nowrap">${p.port}<span class="muted">/${p.proto}</span></td>
          <td class="mono small ink2 hide-sm">${p.addrs.map((a) => (a === '0.0.0.0' || a === '*' ? 'all IPv4' : a === '::' ? 'all IPv6' : a)).join(', ')}</td>
          <td>${p.published.length ? p.published.map((x) => html`<div><span class="tag">docker</span> ${x.container} <span class="muted small">→ ${x.containerPort}</span></div>`) : html`<span class="mono">${p.processes.join(', ') || '–'}</span>`}</td>
          <td><span class=${`chip ${EXPOSURE[p.exposure][0]}`}>${EXPOSURE[p.exposure][1]}</span></td>
          ${reach && html`<td>${p.proto !== 'tcp' || reach[p.port] == null ? html`<span class="muted">–</span>` : reach[p.port] ? html`<span class=${`chip ${p.exposure === 'blocked' ? 'bad' : ''}`} title=${p.exposure === 'blocked' ? 'The firewall should block this, but it answered' : ''}>Reachable</span>` : html`<span class="chip">No answer</span>`}</td>`}
          <td class="act">${op && ['blocked', 'restricted'].includes(p.exposure) && html`<${Btn} size="sm" kind="ghost" onClick=${() => allow(p)}>Allow</${Btn}>`}</td></tr>`)}</tbody></table></div>`}
      ${reach && html`<div class="panel-b small muted b-top">"From Fleetdeck" shows what the Fleetdeck host can reach. If it sits in the same network or VPN, the internet may see less.</div>`}
    </section>

    <div class="grid-2">
      <${FirewallPanel} n=${n} onUfwDocker=${canUfwDocker ? () => setUfwDocker(true) : null} />
      <section class="panel"><div class="panel-h"><h2>Interfaces</h2>
        <span class="small muted">${n.gateway ? `Gateway ${n.gateway}${n.gatewayDev ? ` via ${n.gatewayDev}` : ''}` : 'No default route'}${n.dns.length ? ` · DNS ${n.dns.join(', ')}` : ''}</span></div>
        <div class="table-wrap"><table class="t"><thead><tr><th>Interface</th><th>Addresses</th><th class="num">Traffic</th></tr></thead>
        <tbody>${[...ifaces.values()].filter((i) => i.name !== 'lo' && !/^veth/.test(i.name)).map((i) => {
          const local = i.addrs.filter((a) => /^fe80:/i.test(a)); // link-local: noise in the table, kept in the tooltip
          const shown = i.addrs.filter((a) => !/^fe80:/i.test(a));
          return html`<tr>
          <td class="nowrap"><span class="mono">${i.name}</span>${i.state && i.state !== 'UP' && i.state !== 'UNKNOWN' ? html` <span class="chip">${i.state.toLowerCase()}</span>` : ''}</td>
          <td class="mono small ink2 wrap-any" title=${local.length ? `Link-local: ${local.join(', ')}` : ''}>${shown.length ? shown.map((a) => html`<div>${a}</div>`) : local.length ? html`<span class="muted">link-local only</span>` : '–'}</td>
          <td class="num small nowrap" title=${i.rxTotal != null ? `${bytes(i.rxTotal)} received, ${bytes(i.txTotal)} sent since boot` : ''}>${i.rx == null ? '–' : html`<div>↓ ${rate(i.rx)}</div><div class="ink2">↑ ${rate(i.tx)}</div>`}</td></tr>`;
        })}</tbody></table></div></section>
    </div>
    ${ufwDocker && html`<${UfwDockerDialog} s=${s} ports=${n.ports.filter((p) => p.docker)} onClose=${() => setUfwDocker(false)} />`}
  </div>`;
}

// ---------- endpoint checks (run from the Fleetdeck host, not over SSH) ----------
function ChecksPanel({ s }) {
  const list = useLoad(() => api(`/api/servers/${s.id}/checks`), [s.id], { poll: 60_000 });
  const [edit, setEdit] = useState(null);
  const owner = can('owner');
  const runNow = async (c) => { await api(`/api/servers/${s.id}/checks/${c.id}/run`, { method: 'POST' }); list.reload(true); };
  const del = async (c) => {
    if (!await confirmBox({ title: `Remove the check "${c.name}"?`, body: 'Its history goes with it.', confirm: 'Remove check', danger: true })) return;
    await api(`/api/servers/${s.id}/checks/${c.id}`, { method: 'DELETE' });
    list.reload(true);
  };
  const tls = (d) => (d == null ? '–' : html`<span class=${`chip ${d < 7 ? 'bad' : d < 21 ? 'warn' : ''}`} title="Days until the certificate expires">${d < 0 ? 'expired' : `${Math.floor(d)} d`}</span>`);
  return html`<section class="panel">
    <div class="panel-h"><h2>Endpoint checks</h2><span class="small muted">HTTP(S) and TCP checks from the Fleetdeck host, every minute.</span>
      ${owner && html`<${Btn} size="sm" icon="plus" class="right" onClick=${() => setEdit({})}>Add check</${Btn}>`}</div>
    ${list.loading && !list.data ? html`<${Loading} />` : list.error ? html`<div class="panel-b"><${ErrorNote} error=${list.error} retry=${list.reload} /></div>`
      : !list.data.length ? html`<${Empty} title="No checks">Is the website up, and when does its certificate expire? Add a check, then an "Endpoint check failing" or "TLS certificate" rule under Alerts.</${Empty}>`
      : html`<div class="table-wrap"><table class="t"><thead><tr><th>Status</th><th>Check</th><th class="num">Response</th><th>TLS</th><th class="num hide-sm">24 h</th><th class="act"></th></tr></thead>
      <tbody>${list.data.map((c) => html`<tr>
        <td>${!c.enabled ? html`<span class="chip">off</span>` : c.last_ok == null ? html`<span class="chip">pending</span>`
          : c.last_ok ? html`<span class="chip ok">up</span>` : html`<span class="chip bad" title=${c.failing_since ? `Since ${when(c.failing_since)}` : ''}>failing</span>`}</td>
        <td><b>${c.name}</b>${c.name !== c.target && html` <span class="mono small ink2">${c.target}</span>`}${c.last_error && html`<div class="small err">${c.last_error}</div>`}</td>
        <td class="num ink2 nowrap">${c.last_ms != null ? `${c.last_ms} ms` : '–'}</td>
        <td>${c.kind === 'http' && c.target.startsWith('https:') ? tls(c.tls_days) : html`<span class="muted">–</span>`}</td>
        <td class="num ink2 hide-sm">${pct(c.uptime, c.uptime < 1 ? 1 : 0)}</td>
        <td class="act"><div class="row nw">
          ${can('operator') && html`<${Btn} size="sm" kind="ghost" onClick=${() => runNow(c)}>Check now</${Btn}>`}
          ${owner && html`<${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setEdit(c)} /><${Btn} size="sm" kind="ghost" icon="trash" title="Remove" onClick=${() => del(c)} />`}
        </div></td></tr>`)}</tbody></table></div>`}
    ${edit && html`<${CheckDialog} s=${s} check=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(true); }} />`}
  </section>`;
}

function CheckDialog({ s, check, onClose, onSaved }) {
  const [v, setV] = useState({ kind: 'http', target: '', name: '', contains: '', enabled: true, ...check, expect_status: check.expect_status ?? '' });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const http = v.kind === 'http';
  const set = (k) => (e) => setV({ ...v, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  const save = async () => {
    setErr('');
    try {
      const body = { ...v, name: v.name === v.target ? '' : v.name, expect_status: Number(v.expect_status) || 0 };
      await api(check.id ? `/api/servers/${s.id}/checks/${check.id}` : `/api/servers/${s.id}/checks`, { method: check.id ? 'PUT' : 'POST', body });
      toast('Check saved', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${check.id ? 'Edit check' : 'Add check'} onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Save check" disabled=${!v.target} />`}>
    <div class="seg" role="group" aria-label="Check type">
      <button aria-pressed=${http} onClick=${() => setV({ ...v, kind: 'http' })}>HTTP(S)</button>
      <button aria-pressed=${!http} onClick=${() => setV({ ...v, kind: 'tcp' })}>TCP port</button></div>
    <${Field} label=${http ? 'URL' : 'Host and port'} hint=${http ? 'Redirects are not followed: a 301 counts as up.' : null}>
      <input class="mono" value=${v.target} onInput=${set('target')} placeholder=${http ? 'https://example.com/health' : `${s.host}:5432`} autofocus /></${Field}>
    ${http && html`<div class="cols">
      <${Field} label="Expected status" hint="Empty: anything below 400"><input type="number" min="100" max="599" value=${v.expect_status} onInput=${set('expect_status')} /></${Field}>
      <${Field} label="Page must contain (optional)"><input value=${v.contains} onInput=${set('contains')} /></${Field}></div>`}
    <${Field} label="Name (optional)"><input value=${v.name === v.target ? '' : v.name} onInput=${set('name')} placeholder=${v.target} /></${Field}>
    <label class="check"><input type="checkbox" class="switch" role="switch" checked=${v.enabled} onChange=${set('enabled')} />Enabled</label>
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}

function FirewallPanel({ n, onUfwDocker }) {
  const f = n.firewall;
  return html`<section class="panel"><div class="panel-h"><h2>Firewall</h2>
    ${f.kind === 'ufw' ? html`<span class="chip ok">ufw active</span>` : f.kind === 'ufw-inactive' ? html`<span class="chip warn">ufw off</span>` : f.kind === 'firewalld' ? html`<span class="chip ok">firewalld active</span>` : html`<span class="chip warn">None</span>`}</div>
    <div class="panel-b stack sm">
      ${f.kind === 'ufw' && html`<p class="small ink2">Incoming: <b>${f.incoming}</b> · Outgoing: <b>${f.outgoing}</b>${f.routed ? html` · Routed: <b>${f.routed}</b>` : ''}</p>`}
      ${(f.kind === 'none' || f.kind === 'ufw-inactive') && html`<p class="small ink2">Every port listening on a public address is reachable. Turn on ufw from the terminal (allow SSH first: <span class="mono">ufw allow OpenSSH && ufw enable</span>).</p>`}
      ${n.docker && f.kind === 'ufw' && html`<div class="row"><span class="small ink2 grow">Docker ports: ${n.ufwDocker === 'active' ? html`<b>controlled by ufw</b> (ufw-docker). Open one with "Allow" on its port.` : html`<b>bypass ufw.</b> Docker's NAT rules come before ufw's.`}</span>
        ${onUfwDocker && html`<${Btn} size="sm" onClick=${onUfwDocker}>Let ufw control Docker</${Btn}>`}</div>`}
      ${f.kind === 'ufw' && (f.rules.length ? html`<table class="t"><thead><tr><th>To</th><th>Action</th><th>From</th></tr></thead>
        <tbody>${f.rules.map((r) => html`<tr><td class="mono small">${r[0]}</td><td class="small">${r[1]}</td><td class="mono small ink2">${r[2]}</td></tr>`)}</tbody></table>` : html`<p class="small muted">No rules: only the defaults apply.</p>`)}
      ${f.kind === 'firewalld' && html`<pre class="out short">${f.raw}</pre>`}
    </div></section>`;
}

// Installs the ufw-docker rules (github.com/chaifeng/ufw-docker). Afterwards published ports are closed to the internet
// unless allowed, so let the user pick which ones stay open; 80/443 are pre-ticked.
function UfwDockerDialog({ s, ports, onClose }) {
  const opts = [...new Set(ports.map((p) => `${p.containerPort}/${p.proto}`))];
  const [keep, setKeep] = useState(() => new Set(opts.filter((o) => /^(80|443)\//.test(o))));
  const flip = (o) => setKeep((k) => { const x = new Set(k); x.has(o) ? x.delete(o) : x.add(o); return x; });
  const run = async () => { onClose(); await startJob(s, 'ufw-docker', { KEEP_OPEN: [...keep].join(' ') }); };
  return html`<${Modal} title="Let ufw control Docker ports" onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${run}>Apply</${Btn}>`}>
    <p class="ink2">Adds the <a href="https://github.com/chaifeng/ufw-docker" target="_blank" rel="noopener noreferrer">ufw-docker</a> rules to <span class="mono">/etc/ufw/after.rules</span> (a backup is kept) and reloads ufw.</p>
    <p class="ink2">Afterwards, published container ports are <b>closed to the internet</b> unless you allow them. Private networks (10.x, 172.16–31.x, 192.168.x) can still reach them.</p>
    ${opts.length > 0 && html`<div class="field"><span>Keep these open to everyone</span>
      ${opts.map((o) => html`<label class="check"><input type="checkbox" checked=${keep.has(o)} onChange=${() => flip(o)} /><span class="mono">${o}</span>
        <span class="small muted">${ports.filter((p) => `${p.containerPort}/${p.proto}` === o).map((p) => `host ${p.port}, ${p.published.map((x) => x.container).join(', ')}`).join('; ')}</span></label>`)}
      <span class="hint">These are ports inside the containers, which is what ufw-docker matches on.</span></div>`}
  </${Modal}>`;
}

// ---------- terminal ----------
function TerminalTab({ s }) {
  const ref = useRef();
  const [state, setState] = useState('connecting');
  const [session, setSession] = useState(0);
  // Fill the viewport below wherever the terminal starts (alert bars above it vary).
  useEffect(() => {
    const fit = () => { const el = ref.current; if (el && !document.fullscreenElement) el.style.height = `${Math.max(320, innerHeight - (el.getBoundingClientRect().top + scrollY) - 24)}px`; };
    fit();
    addEventListener('resize', fit);
    return () => removeEventListener('resize', fit);
  }, []);
  useEffect(() => {
    let ws;
    let term;
    let ro;
    let disposed = false;
    setState('connecting');
    (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import('/vendor/xterm.js'), import('/vendor/addon-fit.js')]);
      if (disposed) return;
      term = new Terminal({ fontFamily: "'JetBrains Mono', ui-monospace, monospace", fontSize: 13, cursorBlink: true, scrollback: 5000,
        theme: { background: '#11161D', foreground: '#E4E9F0', cursor: '#4A94D6', selectionBackground: '#2E3846' } });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(ref.current);
      fit.fit();
      ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/servers/${s.id}/terminal?cols=${term.cols}&rows=${term.rows}`);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => { setState('open'); term.focus(); };
      ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
      ws.onclose = () => { if (!disposed) { setState('closed'); term.write('\r\n\x1b[90m[session closed]\x1b[0m\r\n'); } };
      term.onData((d) => ws.readyState === 1 && ws.send(JSON.stringify({ t: 'i', d })));
      term.onResize(({ cols, rows }) => ws.readyState === 1 && ws.send(JSON.stringify({ t: 'r', c: cols, r: rows })));
      ro = new ResizeObserver(() => { try { fit.fit(); } catch { /* hidden */ } });
      ro.observe(ref.current);
    })().catch((e) => setState(`error: ${e.message}`));
    return () => { disposed = true; ro?.disconnect(); ws?.close(); term?.dispose(); };
  }, [s.id, session]);
  const full = () => ref.current.requestFullscreen?.().then(() => ref.current.querySelector('textarea')?.focus()).catch((e) => toast(e.message, 'bad'));
  return html`<div class="stack sm">
    <div class="row"><span class=${`chip ${state === 'open' ? 'ok' : state === 'closed' ? '' : state.startsWith('error') ? 'bad' : 'info'}`}>${state === 'open' ? `Connected as ${s.user}` : state === 'closed' ? 'Disconnected' : state}</span>
      <span class="small muted hide-sm">Sessions are recorded in the audit log (opened, not keystrokes).</span>
      <div class="row nw right">
        ${state === 'closed' && html`<button class="btn sm" onClick=${() => setSession((n) => n + 1)}>Reconnect</button>`}
        <button class="btn sm ghost" onClick=${full} title="Full screen (Esc leaves)"><${Icon} name="full" size=${14} />Full screen</button></div></div>
    <div class="term" ref=${ref} />
  </div>`;
}

// ---------- settings ----------
function SettingsTab({ s }) {
  const keys = useLoad(() => api('/api/keys'));
  const [v, setV] = useState({ name: s.name, host: s.host, port: s.port, user: s.user, tags: s.tags.join(', '), notes: s.notes, key_id: s.key_id || '', sudo_password: '' });
  const [test, setTest] = useState(null);
  const save = async () => {
    let key_id = v.key_id;
    const fresh = key_id === '__new';
    if (fresh) key_id = (await api('/api/keys', { method: 'POST', body: { name: v.name, generate: true } })).id;
    try {
      await api(`/api/servers/${s.id}`, { method: 'PUT', body: serverBody({ ...v, key_id }) });
    } catch (e) {
      if (fresh) await api(`/api/keys/${key_id}`, { method: 'DELETE' }).catch(() => {}); // no orphan key per failed save
      throw e;
    }
    if (fresh) { setV((x) => ({ ...x, key_id })); keys.reload(true); }
    setV((x) => ({ ...x, sudo_password: '' }));
    await refreshServers();
    toast('Server saved', 'ok');
  };
  const clearSudo = async () => {
    if (!await confirmBox({ title: 'Remove the saved sudo password?', body: 'Updates and admin actions will fail unless this user has passwordless sudo.', confirm: 'Remove password', danger: true })) return;
    await api(`/api/servers/${s.id}`, { method: 'PUT', body: { clear_sudo_password: true } });
    await refreshServers();
    toast('Sudo password removed');
  };
  const pause = async () => {
    await api(`/api/servers/${s.id}`, { method: 'PUT', body: { paused: !s.paused } });
    await refreshServers();
    toast(s.paused ? 'Monitoring resumed' : 'Monitoring paused', 'ok');
  };
  const remove = async () => {
    if (!await confirmBox({ title: `Remove ${s.name}?`, body: 'Fleetdeck forgets this server, its metrics and alert state. Nothing changes on the server itself.', confirm: 'Remove server', danger: true, typeToConfirm: s.name })) return;
    await api(`/api/servers/${s.id}`, { method: 'DELETE' });
    await refreshServers();
    go('/');
    toast(`${s.name} removed`);
  };
  const key = keys.data?.find((k) => k.id === v.key_id);
  return html`<div class="grid-2">
    <section class="panel"><div class="panel-h"><h2>Connection</h2></div><div class="panel-b stack">
      ${keys.loading ? html`<${Loading} />` : html`<${ServerForm} value=${v} onChange=${setV} keys=${keys.data || []} hasSudo=${s.has_sudo_password} />`}
      ${s.has_sudo_password && html`<div><button class="btn sm ghost" onClick=${clearSudo}>Remove saved sudo password</button></div>`}
      ${key && html`<div class="field"><span>Public key to authorize on the server</span><${Copy} text=${key.public_key} /></div>`}
      <div class="row"><${Btn} kind="primary" onClick=${save}>Save changes</${Btn}>
        <${Btn} icon="term" onClick=${async () => setTest(await api(`/api/servers/${s.id}/test`, { method: 'POST' }))}>Test connection</${Btn}></div>
      ${test && (test.ok ? html`<p class="small"><span class="chip ok">Connected as ${test.user}</span> ${PRIV_TEXT[test.privilege]}</p>`
        : html`<p class="small err">${test.error}</p>`)}
    </div></section>
    <div class="stack">
      <section class="panel"><div class="panel-h"><h2>Host key</h2></div><div class="panel-b stack">
        <p class="ink2 small">Pinned on the first connection. If it changes, Fleetdeck refuses to connect and raises an alert.</p>
        <span class="mono small wrap-any">${s.host_key || 'Not pinned yet'}</span>
      </div></section>
      <section class="panel"><div class="panel-h"><h2>Monitoring</h2></div><div class="panel-b stack">
        <label class="check"><input type="checkbox" class="switch" role="switch" checked=${!s.paused} onChange=${(e) => { e.target.checked = !s.paused; pause().catch((x) => toast(x.message, 'bad')); }} />Monitor this server</label>
        <p class="ink2 small">${s.paused ? 'Paused: no checks, no alerts. Jobs and the terminal still work.' : 'Checked every minute. Pause during maintenance to silence alerts.'}</p>
        ${can('admin') && html`<p class="ink2 small">Planned work? Keep monitoring but hold back notifications for a while. Anything still firing afterwards is sent then.</p>
        <div class="row">${[1, 4].map((h) => html`<${Btn} size="sm" onClick=${async () => { await muteFor(s.id, h, `Maintenance on ${s.name}`); toast(`Notifications for ${s.name} muted for ${h} h`, 'ok'); }}>Mute for ${h} h</${Btn}>`)}
          <a class="btn sm ghost" href="#/alerts">Quiet hours</a></div>`}
      </div></section>
      <${AccessPanel} s=${s} />
      <section class="panel danger-zone"><div class="panel-h"><h2>Remove server</h2></div><div class="panel-b stack">
        <p class="ink2 small">Deletes its history from Fleetdeck. The server itself is not touched.</p>
        <div><${Btn} kind="danger" icon="trash" onClick=${remove}>Remove ${s.name}</${Btn}></div>
      </div></section>
    </div>
  </div>`;
}

// Who besides the admins sees this server (owners, operators and viewers of the organization).
function AccessPanel({ s }) {
  const m = useLoad(() => api(`/api/servers/${s.id}/members`), [s.id]);
  const [sel, setSel] = useState(null);
  useEffect(() => { if (m.data) setSel(new Set(m.data.filter((u) => u.assigned).map((u) => u.id))); }, [m.data]);
  const changed = m.data && sel && m.data.some((u) => !!u.assigned !== sel.has(u.id));
  const flip = (id) => setSel((x) => { const n = new Set(x); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const save = async () => { await api(`/api/servers/${s.id}/members`, { method: 'PUT', body: { user_ids: [...sel] } }); toast('Access saved', 'ok'); m.reload(true); };
  return html`<section class="panel"><div class="panel-h"><h2>Access</h2></div><div class="panel-b stack">
    <p class="ink2 small">Admins always see every server. Pick who else sees ${s.name}; what they can do depends on their role.</p>
    ${m.error ? html`<${ErrorNote} error=${m.error} retry=${m.reload} />` : !sel ? html`<${Loading} />`
      : !m.data.length ? html`<p class="small muted">Nobody else is in this organization yet.${can('admin') ? html` Invite people under <a href="#/settings/org">Settings → Organization</a>.` : ''}</p>`
      : html`<div>${m.data.map((u) => html`<label class="check list"><input type="checkbox" checked=${sel.has(u.id)} onChange=${() => flip(u.id)} />
          <span>${u.name || u.email}</span><span class="small muted">${u.role}${u.name ? `, ${u.email}` : ''}</span></label>`)}</div>
        <div><${Btn} kind=${changed ? 'primary' : ''} disabled=${!changed} onClick=${save}>Save access</${Btn}></div>`}
  </div></section>`;
}
