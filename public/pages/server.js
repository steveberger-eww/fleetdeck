import {
  html, useState, useEffect, useRef, useMemo, api, go, useStore, useLoad, serversStore, refreshServers, toast, confirmBox,
  Btn, Lamp, StatusChip, Gauge, LineChart, Empty, Field, Modal, Loading, ErrorNote, Output, Icon, Copy,
  bytes, rate, pct, ago, dur, uptime, when, logTime, level,
} from '../ui.js';
import { Flags, ServerForm, serverBody } from './overview.js';
import { JobsTable, openRun } from './jobs.js';

const TABS = [['overview', 'Overview'], ['updates', 'Updates'], ['services', 'Services'], ['logs', 'Logs'], ['processes', 'Processes'], ['containers', 'Containers'], ['terminal', 'Terminal'], ['jobs', 'Jobs'], ['settings', 'Settings']];

async function startJob(server, scriptId, params) {
  const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: scriptId, params, targets: { servers: [server.id] } } });
  go(`/jobs/${id}`);
}

export function ServerPage({ id, tab }) {
  const servers = useStore(serversStore);
  const s = servers?.find((x) => x.id === id);
  if (!servers) return html`<div class="page"><${Loading} /></div>`;
  if (!s) return html`<div class="page"><${Empty} title="Server not found" action=${html`<a class="btn" href="#/">Back to the fleet</a>`}>It may have been removed.</${Empty}></div>`;
  const n = s.snapshot;

  const reboot = async () => {
    if (!await confirmBox({ title: `Reboot ${s.name}?`, body: 'Everything running on it will be unavailable until it is back up.', confirm: 'Reboot', danger: true, typeToConfirm: s.name })) return;
    await startJob(s, 'reboot');
  };
  const acceptKey = async () => {
    if (!await confirmBox({ title: 'Trust the new host key?', body: `Only do this if you know why the key changed (reinstall, new server at the same address). Offered key: ${s.ssh.hostKeyMismatch}`, confirm: 'Trust new key', danger: true })) return;
    await api(`/api/servers/${s.id}/hostkey/accept`, { method: 'POST' });
    toast('New host key trusted', 'ok');
    refreshServers();
  };

  let body;
  if (tab === 'updates') body = html`<${UpdatesTab} s=${s} />`;
  else if (tab === 'services') body = html`<${ServicesTab} s=${s} />`;
  else if (tab === 'logs') body = html`<${LogsTab} s=${s} />`;
  else if (tab === 'processes') body = html`<${ProcessesTab} s=${s} />`;
  else if (tab === 'containers') body = html`<${ContainersTab} s=${s} />`;
  else if (tab === 'terminal') body = html`<${TerminalTab} s=${s} />`;
  else if (tab === 'jobs') body = html`<section class="panel"><${JobsTable} serverId=${s.id} /></section>`;
  else if (tab === 'settings') body = html`<${SettingsTab} s=${s} />`;
  else body = html`<${OverviewTab} s=${s} />`;

  return html`<div class="page">
    <div class="srv-h">
      <div class="id grow">
        <div class="row"><h1>${s.name}</h1><${StatusChip} status=${s.status} />${s.tags.map((t) => html`<span class="tag">${t}</span>`)}</div>
        <div class="meta"><span class="mono">${s.user}@${s.host}${s.port !== 22 ? `:${s.port}` : ''}</span>
          ${n && html`<span>${n.os}</span><span>Up ${uptime(n.uptime)}</span><span>Checked ${ago(n.t)}</span>`}</div>
      </div>
      <div class="row">
        <${Btn} icon="refresh" title="Check now" onClick=${async () => { await api(`/api/servers/${s.id}/probe`, { method: 'POST' }); await refreshServers(); }} />
        ${n?.cockpit
          ? html`<a class="btn" href=${`https://${s.host.includes(':') ? `[${s.host}]` : s.host}:9090`} target="_blank" rel="noopener noreferrer"><${Icon} name="ext" />Cockpit</a>`
          : n && html`<${Btn} onClick=${() => startJob(s, 'cockpit')} title="Install Cockpit for storage, networking and firewall management">Install Cockpit</${Btn}>`}
        <${Btn} icon="play" onClick=${() => openRun({ targets: { servers: [s.id] } })}>Run script</${Btn}>
        <${Btn} kind="danger" onClick=${reboot}>Reboot</${Btn}>
      </div>
    </div>

    ${s.ssh?.hostKeyMismatch && html`<div class="alert-bar"><span class="grow"><b>The host key changed.</b> Fleetdeck refuses to connect, because this can mean someone is intercepting the connection. Pinned: <span class="mono">${s.host_key}</span>, offered: <span class="mono">${s.ssh.hostKeyMismatch}</span></span><${Btn} kind="danger" onClick=${acceptKey}>Trust new key</${Btn}></div>`}
    ${s.ssh?.sudoRejected && html`<div class="alert-bar warn"><span class="grow"><b>The server rejected the stored sudo password.</b> Fleetdeck stopped using it so the account doesn't get locked. Update it to resume admin actions.</span><a class="btn sm" href=${`#/servers/${s.id}/settings`}>Update password</a></div>`}
    ${s.status === 'down' && !s.ssh?.hostKeyMismatch && html`<div class="alert-bar"><span class="grow"><b>Unreachable for ${dur(Date.now() - s.error.since)}.</b> ${s.error.message}</span><a class="btn sm" href=${`#/servers/${s.id}/settings`}>Connection settings</a></div>`}
    ${(s.firing || []).map((a) => html`<div class="alert-bar"><span class="grow"><b>Alert firing ${ago(a.since)}:</b> ${a.message}</span><a class="btn sm" href="#/alerts">Alert rules</a></div>`)}
    ${n && (n.updates || n.reboot || n.failed) ? html`<div class="row"><${Flags} s=${{ ...s, alerts: 0, status: '' }} />
      ${n.updates > 0 && html`<${Btn} size="sm" onClick=${() => startJob(s, 'update')}>Install ${n.updates} update${n.updates > 1 ? 's' : ''}</${Btn}>`}</div>` : ''}

    <nav class="tabs" aria-label="Server sections">${TABS.map(([k, label]) => html`<a href=${`#/servers/${s.id}${k === 'overview' ? '' : `/${k}`}`} aria-current=${tab === k ? 'page' : undefined}>${label}</a>`)}</nav>
    ${body}
  </div>`;
}

// ---------- overview: charts + system info ----------
const RANGES = ['1h', '6h', '24h', '7d', '30d'];
const P = (v) => `${Math.round(v * 100)}%`;

function OverviewTab({ s }) {
  const [range, setRange] = useState('1h');
  const m = useLoad(() => api(`/api/servers/${s.id}/metrics?range=${range}`), [s.id, range], { poll: 60_000 });
  const n = s.snapshot;
  const hasTemp = m.data?.points.some((p) => p.temp != null);
  return html`<div class="stack">
    <div class="row"><h2>Metrics</h2><div class="seg right" role="group" aria-label="Time range">${RANGES.map((r) => html`<button aria-pressed=${r === range} onClick=${() => setRange(r)}>${r}</button>`)}</div></div>
    ${m.error && html`<${ErrorNote} error=${m.error} retry=${m.reload} />`}
    <div class="charts">
      <${LineChart} title="CPU" data=${m.data} yMax=${1} fmt=${P} series=${[{ key: 'cpu', label: 'CPU', color: 'var(--s1)' }]} current=${n && `${pct(n.cpu)} of ${n.cores} cores`} />
      <${LineChart} title="Memory" data=${m.data} yMax=${1} fmt=${P} series=${[{ key: 'mem', label: 'Memory', color: 'var(--s1)' }, { key: 'swap', label: 'Swap', color: 'var(--s2)' }]} current=${n && `${bytes(n.memUsed)} of ${bytes(n.memTotal)}`} />
      <${LineChart} title="Network" data=${m.data} fmt=${(v) => rate(v)} series=${[{ key: 'rx', label: 'In', color: 'var(--s1)' }, { key: 'tx', label: 'Out', color: 'var(--s2)' }]} />
      <${LineChart} title="Disk I/O" data=${m.data} fmt=${(v) => rate(v)} series=${[{ key: 'dr', label: 'Read', color: 'var(--s1)' }, { key: 'dw', label: 'Write', color: 'var(--s2)' }]} />
      <${LineChart} title="Load (1 min)" data=${m.data} fmt=${(v) => v.toFixed(v < 10 ? 1 : 0)} series=${[{ key: 'load1', label: 'Load', color: 'var(--s1)' }]} current=${n && `${n.load.map((l) => l.toFixed(2)).join(', ')}`} />
      ${hasTemp && html`<${LineChart} title="Temperature" data=${m.data} fmt=${(v) => `${Math.round(v)}°`} series=${[{ key: 'temp', label: 'Temperature', color: 'var(--s1)' }]} current=${n?.temp != null && `${Math.round(n.temp)} °C`} />`}
    </div>
    ${n && html`<div class="charts">
      <section class="panel"><div class="panel-h"><h2>Filesystems</h2></div>
        <div class="table-wrap"><table class="t"><thead><tr><th>Mount</th><th>Type</th><th style=${{ width: '40%' }}>Used</th><th class="num">Size</th></tr></thead>
        <tbody>${n.disks.map((d) => html`<tr><td class="mono">${d.mount}</td><td class="ink2">${d.type}</td>
          <td><div class="row nw"><div class="bar grow"><i class=${level(d.pct, 0.8, 0.9)} style=${{ width: `${d.pct * 100}%` }} /></div><span class="small nowrap">${pct(d.pct)}</span></div></td>
          <td class="num ink2 nowrap">${bytes(d.used)} / ${bytes(d.size)}</td></tr>`)}</tbody></table></div></section>
      <section class="panel"><div class="panel-h"><h2>System</h2></div><div class="panel-b">
        <dl class="kv">
          <dt>Hostname</dt><dd class="mono">${n.hostname}</dd>
          <dt>OS</dt><dd>${n.os}</dd>
          <dt>Kernel</dt><dd class="mono">${n.kernel} (${n.arch})</dd>
          <dt>CPU</dt><dd>${n.cpuModel || '–'}, ${n.cores} cores</dd>
          ${n.virt && html`<dt>Virtualization</dt><dd>${n.virt}</dd>`}
          <dt>Memory</dt><dd>${bytes(n.memTotal)}${n.swapTotal ? `, ${bytes(n.swapTotal)} swap` : ', no swap'}</dd>
          <dt>Addresses</dt><dd class="mono">${n.ips.join(', ') || '–'}</dd>
          <dt>Booted</dt><dd>${when(n.t - n.uptime * 1000)}</dd>
          <dt>Processes</dt><dd>${n.procs}</dd>
          <dt>Packages</dt><dd>${n.pkg || 'unknown'}${n.updates != null ? `, ${n.updates} pending` : ''}</dd>
          ${n.docker && html`<dt>Docker</dt><dd>${n.docker.running} of ${n.docker.total} containers running</dd>`}
          <dt>Checks run as</dt><dd>${n.probeUser}${n.probeUser !== 'root' ? ' (no passwordless sudo)' : ''}</dd>
        </dl></div></section>
    </div>`}
    ${s.notes && html`<section class="panel"><div class="panel-h"><h2>Notes</h2></div><div class="panel-b" style=${{ whiteSpace: 'pre-wrap' }}>${s.notes}</div></section>`}
  </div>`;
}

// ---------- updates ----------
function UpdatesTab({ s }) {
  const [refresh, setRefresh] = useState(0);
  const p = useLoad(() => api(`/api/servers/${s.id}/packages${refresh ? '?refresh=1' : ''}`), [s.id, refresh]);
  const [sel, setSel] = useState(() => new Set());
  const list = p.data?.packages || [];
  const toggle = (name) => setSel((x) => { const n = new Set(x); n.has(name) ? n.delete(name) : n.add(name); return n; });
  return html`<section class="panel">
    <div class="panel-h"><h2>Available updates</h2>${p.data && html`<span class="small muted">${p.data.manager || 'unknown package manager'}</span>`}
      <div class="row right">
        <${Btn} size="sm" icon="refresh" onClick=${() => setRefresh((r) => r + 1)} disabled=${p.loading}>Refresh package lists</${Btn}>
        ${sel.size > 0 && html`<${Btn} size="sm" onClick=${() => startJob(s, 'upgrade-packages', { PACKAGES: [...sel].join(' ') })}>Upgrade ${sel.size} selected</${Btn}>`}
        ${list.length > 0 && html`<${Btn} size="sm" kind="primary" onClick=${() => startJob(s, 'update')}>Upgrade all</${Btn}>`}
      </div></div>
    ${p.loading ? html`<${Loading} />` : p.error ? html`<div class="panel-b"><${ErrorNote} error=${p.error} retry=${p.reload} /></div>`
      : !list.length ? html`<${Empty} title="Everything is up to date">Fleetdeck checks the package cache every minute. Refresh the package lists to look for new releases now.</${Empty}>`
      : html`<div class="table-wrap"><table class="t"><thead><tr><th><input type="checkbox" aria-label="Select all" checked=${sel.size === list.length} onChange=${() => setSel(sel.size === list.length ? new Set() : new Set(list.map((x) => x.name)))} /></th><th>Package</th><th>Installed</th><th>Available</th><th>Source</th></tr></thead>
        <tbody>${list.map((x) => html`<tr><td><input type="checkbox" checked=${sel.has(x.name)} onChange=${() => toggle(x.name)} aria-label=${`Select ${x.name}`} /></td>
          <td class="mono">${x.name} ${x.security && html`<span class="chip warn">security</span>`}</td><td class="mono ink2">${x.from || '–'}</td><td class="mono">${x.version}</td><td class="ink2">${x.source}</td></tr>`)}</tbody></table></div>`}
  </section>`;
}

// ---------- services ----------
function ServicesTab({ s }) {
  const svc = useLoad(() => api(`/api/servers/${s.id}/services`), [s.id]);
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState('running');
  const [status, setStatus] = useState(null);
  const list = useMemo(() => (svc.data || []).filter((u) => {
    if (filter === 'running' && u.active !== 'active') return false;
    if (filter === 'failed' && u.active !== 'failed') return false;
    if (filter === 'inactive' && u.active !== 'inactive') return false;
    return !q || `${u.unit} ${u.description}`.toLowerCase().includes(q.toLowerCase());
  }), [svc.data, q, filter]);
  const act = async (u, action) => {
    if (['stop', 'disable'].includes(action) && !await confirmBox({ title: `${action[0].toUpperCase() + action.slice(1)} ${u.unit}?`, confirm: action[0].toUpperCase() + action.slice(1), danger: true })) return;
    await api(`/api/servers/${s.id}/services`, { method: 'POST', body: { unit: u.unit, action } });
    toast(`${u.unit}: ${action} done`, 'ok');
    svc.reload(true);
  };
  const showStatus = async (u) => setStatus({ unit: u.unit, text: (await api(`/api/servers/${s.id}/services/status?unit=${encodeURIComponent(u.unit)}`)).text });
  const counts = { failed: (svc.data || []).filter((u) => u.active === 'failed').length };
  return html`<section class="panel">
    <div class="panel-h">
      <div class="seg" role="group" aria-label="Filter">${[['running', 'Running'], ['failed', `Failed${counts.failed ? ` (${counts.failed})` : ''}`], ['inactive', 'Stopped'], ['all', 'All']].map(([k, l]) => html`<button aria-pressed=${filter === k} onClick=${() => setFilter(k)}>${l}</button>`)}</div>
      <input type="search" placeholder="Filter services" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter services" />
      <${Btn} size="sm" kind="ghost" icon="refresh" class="right" onClick=${() => svc.reload()}>Refresh</${Btn}>
    </div>
    ${svc.loading && !svc.data ? html`<${Loading} />` : svc.error ? html`<div class="panel-b"><${ErrorNote} error=${svc.error} retry=${svc.reload} /></div>`
      : !list.length ? html`<${Empty} title="No services match" />`
      : html`<div class="table-wrap"><table class="t"><thead><tr><th>State</th><th>Service</th><th>Description</th><th>Start at boot</th><th class="act"></th></tr></thead>
      <tbody>${list.map((u) => html`<tr class="click" onClick=${() => showStatus(u)}>
        <td><span class=${`chip ${u.active === 'active' ? 'ok' : u.active === 'failed' ? 'bad' : ''}`}>${u.active === 'active' ? u.sub : u.active}</span></td>
        <td class="mono">${u.unit.replace(/\.service$/, '')}</td><td class="ink2 trunc" style=${{ maxWidth: '320px' }}>${u.description}</td>
        <td class="ink2">${u.enabled}</td>
        <td class="act" onClick=${(e) => e.stopPropagation()}><div class="row nw">
          ${u.active === 'active' ? html`<${Btn} size="sm" onClick=${() => act(u, 'restart')}>Restart</${Btn}><${Btn} size="sm" kind="danger" onClick=${() => act(u, 'stop')}>Stop</${Btn}>`
            : html`<${Btn} size="sm" onClick=${() => act(u, 'start')}>Start</${Btn}>`}
          ${u.enabled === 'enabled' ? html`<${Btn} size="sm" kind="ghost" onClick=${() => act(u, 'disable')}>Disable</${Btn}>`
            : u.enabled === 'disabled' ? html`<${Btn} size="sm" kind="ghost" onClick=${() => act(u, 'enable')}>Enable</${Btn}>` : ''}
        </div></td></tr>`)}</tbody></table></div>`}
    ${status && html`<${Modal} wide title=${status.unit} onClose=${() => setStatus(null)}>
      <div class="row"><a class="btn sm" href=${`#/servers/${s.id}/logs`} onClick=${() => sessionStorage.setItem('fd-log-unit', status.unit)}>Open logs</a></div>
      <${Output} text=${status.text} /></${Modal}>`}
  </section>`;
}

// ---------- logs ----------
const PRIOS = [['', 'All levels'], ['3', 'Errors'], ['4', 'Warnings and worse'], ['6', 'Info and worse']];
const PRIO_NAME = ['emerg', 'alert', 'crit', 'error', 'warn', 'notice', 'info', 'debug'];
function LogsTab({ s }) {
  const [f, setF] = useState(() => {
    let unit = '';
    try { unit = sessionStorage.getItem('fd-log-unit') || ''; sessionStorage.removeItem('fd-log-unit'); } catch { /* ignore */ }
    return { unit, priority: '', since: '1h', lines: '500' };
  });
  const [q, setQ] = useState('');
  const [follow, setFollow] = useState(false);
  const qs = new URLSearchParams(Object.entries(f).filter(([, v]) => v)).toString();
  const logs = useLoad(() => api(`/api/servers/${s.id}/logs?${qs}`), [s.id, qs], { poll: follow ? 5000 : 0 });
  const list = useMemo(() => {
    const n = q.toLowerCase();
    return (logs.data || []).filter((e) => !n || `${e.id} ${e.msg}`.toLowerCase().includes(n)).reverse();
  }, [logs.data, q]);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  return html`<section class="panel">
    <div class="panel-h">
      <input class="mono" placeholder="Unit, e.g. nginx" value=${f.unit} onChange=${set('unit')} aria-label="Unit" style=${{ width: '170px' }} />
      <select value=${f.priority} onChange=${set('priority')} aria-label="Level">${PRIOS.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>
      <select value=${f.since} onChange=${set('since')} aria-label="Time range">${[['15m', 'Last 15 min'], ['1h', 'Last hour'], ['6h', 'Last 6 hours'], ['24h', 'Last day'], ['7d', 'Last week']].map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>
      <select value=${f.lines} onChange=${set('lines')} aria-label="Lines">${['200', '500', '2000', '5000'].map((v) => html`<option value=${v}>${v} lines</option>`)}</select>
      <input type="search" placeholder="Search" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Search messages" />
      <label class="check small"><input type="checkbox" checked=${follow} onChange=${(e) => setFollow(e.target.checked)} />Follow</label>
      <${Btn} size="sm" kind="ghost" icon="refresh" class="right" onClick=${() => logs.reload()}>Refresh</${Btn}>
    </div>
    ${logs.loading && !logs.data ? html`<${Loading} />` : logs.error ? html`<div class="panel-b"><${ErrorNote} error=${logs.error} retry=${logs.reload} /></div>`
      : !list.length ? html`<${Empty} title="No log entries">Nothing matches these filters. Widen the time range or clear the search.</${Empty}>`
      : html`<div class="logs" style=${{ maxHeight: '65vh', overflowY: 'auto' }}>${list.map((e) => html`<div class=${`e p${e.p}`}>
          <span class="muted">${logTime(e.t)}</span><span class=${`p${e.p}`}>${PRIO_NAME[e.p]}</span><span class="trunc ink2" title=${e.unit}>${e.id}</span><span class="m">${e.msg}</span></div>`)}</div>`}
  </section>`;
}

// ---------- processes ----------
function ProcessesTab({ s }) {
  const ps = useLoad(() => api(`/api/servers/${s.id}/processes`), [s.id], { poll: 10_000 });
  const [sort, setSort] = useState('cpu');
  const [q, setQ] = useState('');
  const list = useMemo(() => (ps.data || []).filter((p) => !q || `${p.args} ${p.user} ${p.pid}`.toLowerCase().includes(q.toLowerCase())).sort((a, b) => b[sort] - a[sort]).slice(0, 80), [ps.data, sort, q]);
  const kill = async (p, signal) => {
    if (!await confirmBox({ title: `Send ${signal} to ${p.comm} (${p.pid})?`, body: p.args, confirm: signal === 'KILL' ? 'Force kill' : 'Terminate', danger: true })) return;
    await api(`/api/servers/${s.id}/processes/kill`, { method: 'POST', body: { pid: p.pid, signal } });
    toast(`Sent ${signal} to ${p.pid}`, 'ok');
    ps.reload(true);
  };
  return html`<section class="panel">
    <div class="panel-h">
      <div class="seg" role="group" aria-label="Sort by"><button aria-pressed=${sort === 'cpu'} onClick=${() => setSort('cpu')}>Top CPU</button><button aria-pressed=${sort === 'mem'} onClick=${() => setSort('mem')}>Top memory</button></div>
      <input type="search" placeholder="Filter processes" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter processes" />
      <span class="small muted right">Updates every 10 seconds</span>
    </div>
    ${ps.loading && !ps.data ? html`<${Loading} />` : ps.error ? html`<div class="panel-b"><${ErrorNote} error=${ps.error} retry=${ps.reload} /></div>`
      : html`<div class="table-wrap"><table class="t"><thead><tr><th class="num">PID</th><th>User</th><th class="num">CPU</th><th class="num">Memory</th><th class="num">RSS</th><th>Running</th><th>Command</th><th class="act"></th></tr></thead>
      <tbody>${list.map((p) => html`<tr><td class="num mono">${p.pid}</td><td class="ink2">${p.user}</td><td class="num">${p.cpu.toFixed(1)}%</td><td class="num">${p.mem.toFixed(1)}%</td>
        <td class="num ink2">${bytes(p.rss)}</td><td class="ink2 nowrap">${dur(p.elapsed * 1000)}</td><td class="mono trunc" style=${{ maxWidth: '420px' }} title=${p.args}>${p.args}</td>
        <td class="act"><div class="row nw"><${Btn} size="sm" kind="ghost" onClick=${() => kill(p, 'TERM')}>Stop</${Btn}><${Btn} size="sm" kind="danger" onClick=${() => kill(p, 'KILL')}>Kill</${Btn}></div></td></tr>`)}</tbody></table></div>`}
  </section>`;
}

// ---------- containers ----------
function ContainersTab({ s }) {
  const c = useLoad(() => api(`/api/servers/${s.id}/containers`), [s.id], { poll: 15_000 });
  const [logs, setLogs] = useState(null);
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
  return html`<section class="panel">
    <div class="panel-h"><h2>Containers</h2><span class="small muted">${c.data.containers.filter((x) => x.state === 'running').length} of ${c.data.containers.length} running</span>
      <${Btn} size="sm" kind="ghost" class="right" onClick=${() => startJob(s, 'docker-prune')}>Clean up unused data</${Btn}></div>
    ${!c.data.containers.length ? html`<${Empty} title="No containers" />` : html`<div class="table-wrap"><table class="t">
      <thead><tr><th>State</th><th>Name</th><th>Image</th><th>Status</th><th class="num">CPU</th><th>Memory</th><th>Ports</th><th class="act"></th></tr></thead>
      <tbody>${c.data.containers.map((x) => html`<tr>
        <td><span class=${`chip ${x.state === 'running' ? 'ok' : ['exited', 'dead'].includes(x.state) ? '' : 'warn'}`}>${x.state}</span></td>
        <td class="mono">${x.name}</td><td class="mono ink2 trunc" style=${{ maxWidth: '220px' }} title=${x.image}>${x.image}</td><td class="ink2 nowrap">${x.status}</td>
        <td class="num">${x.cpu}</td><td class="ink2 nowrap">${x.mem}</td><td class="mono small ink2 trunc" style=${{ maxWidth: '200px' }} title=${x.ports}>${x.ports}</td>
        <td class="act"><div class="row nw">
          <${Btn} size="sm" kind="ghost" onClick=${() => showLogs(x)}>Logs</${Btn}>
          ${x.state === 'running' ? html`<${Btn} size="sm" onClick=${() => act(x, 'restart')}>Restart</${Btn}><${Btn} size="sm" kind="danger" onClick=${() => act(x, 'stop')}>Stop</${Btn}>`
            : html`<${Btn} size="sm" onClick=${() => act(x, 'start')}>Start</${Btn}><${Btn} size="sm" kind="danger" onClick=${() => act(x, 'rm')}>Remove</${Btn}>`}
        </div></td></tr>`)}</tbody></table></div>`}
    ${logs && html`<${Modal} wide title=${`Logs: ${logs.name}`} onClose=${() => setLogs(null)}><${Output} text=${logs.text} /></${Modal}>`}
  </section>`;
}

// ---------- terminal ----------
function TerminalTab({ s }) {
  const ref = useRef();
  const [state, setState] = useState('connecting');
  const [session, setSession] = useState(0);
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
  return html`<div class="stack" style=${{ gap: '10px' }}>
    <div class="row"><span class=${`chip ${state === 'open' ? 'ok' : state === 'closed' ? '' : state.startsWith('error') ? 'bad' : 'info'}`}>${state === 'open' ? `Connected as ${s.user}` : state === 'closed' ? 'Disconnected' : state}</span>
      <span class="small muted">Sessions are recorded in the audit log (opened, not keystrokes).</span>
      ${state === 'closed' && html`<button class="btn sm right" onClick=${() => setSession((n) => n + 1)}>Reconnect</button>`}</div>
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
    if (key_id === '__new') {
      const k = await api('/api/keys', { method: 'POST', body: { name: v.name, generate: true } });
      key_id = k.id;
      setV({ ...v, key_id });
      keys.reload(true);
    }
    await api(`/api/servers/${s.id}`, { method: 'PUT', body: serverBody({ ...v, key_id }) });
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
  return html`<div class="charts" style=${{ alignItems: 'start' }}>
    <section class="panel"><div class="panel-h"><h2>Connection</h2></div><div class="panel-b stack">
      ${keys.loading ? html`<${Loading} />` : html`<${ServerForm} value=${v} onChange=${setV} keys=${keys.data || []} hasSudo=${s.has_sudo_password} />`}
      ${s.has_sudo_password && html`<div><button class="btn sm ghost" onClick=${clearSudo}>Remove saved sudo password</button></div>`}
      ${key && html`<div class="field"><span>Public key to authorize on the server</span><${Copy} text=${key.public_key} /></div>`}
      <div class="row"><${Btn} kind="primary" onClick=${save}>Save changes</${Btn}>
        <${Btn} icon="term" onClick=${async () => setTest(await api(`/api/servers/${s.id}/test`, { method: 'POST' }))}>Test connection</${Btn}></div>
      ${test && (test.ok ? html`<p class="small"><span class="chip ok">Connected as ${test.user}</span> ${{ none: 'No sudo: admin actions will fail. Add the sudo password above.', sudo: 'Passwordless sudo works.', password: 'sudo works with the stored password.', 'wrong-password': 'The server rejected the sudo password.', root: 'Logged in as root.' }[test.privilege]}</p>`
        : html`<p class="small err">${test.error}</p>`)}
    </div></section>
    <div class="stack">
      <section class="panel"><div class="panel-h"><h2>Host key</h2></div><div class="panel-b stack">
        <p class="ink2 small">Pinned on the first connection. If it changes, Fleetdeck refuses to connect and raises an alert.</p>
        <span class="mono small" style=${{ overflowWrap: 'anywhere' }}>${s.host_key || 'Not pinned yet'}</span>
      </div></section>
      <section class="panel"><div class="panel-h"><h2>Monitoring</h2></div><div class="panel-b stack">
        <p class="ink2 small">${s.paused ? 'Paused: no checks, no alerts. Jobs and the terminal still work.' : 'Checked every minute. Pause during maintenance to silence alerts.'}</p>
        <div><${Btn} onClick=${pause}>${s.paused ? 'Resume monitoring' : 'Pause monitoring'}</${Btn}></div>
      </div></section>
      <section class="panel"><div class="panel-h"><h2>Remove server</h2></div><div class="panel-b stack">
        <p class="ink2 small">Deletes its history from Fleetdeck. The server itself is not touched.</p>
        <div><${Btn} kind="danger" icon="trash" onClick=${remove}>Remove ${s.name}</${Btn}></div>
      </div></section>
    </div>
  </div>`;
}
