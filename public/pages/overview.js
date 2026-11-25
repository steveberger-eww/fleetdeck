import {
  html, useState, useMemo, api, go, useStore, useLoad, useQuery, setQuery, useDirty, serversStore, runningStore, refreshServers,
  Btn, Lamp, Gauge, Spark, Empty, Field, Modal, Footer, Copy, Loading, JobChip, Icon, bytes, rate, traffic, ago, uptime, plural, eol,
  flagList, attention, byAttention, compare, can,
} from '../ui.js';
import { openRun, confirmRun } from './jobs.js';

const MAX_FLAGS = 2;
export function Flags({ s, max = Infinity }) {
  const f = flagList(s);
  const shown = f.slice(0, max);
  const rest = f.slice(max);
  return html`${shown.map(([kind, text, title]) => html`<span class=${`chip ${kind}`} title=${title}>${text}</span>`)}
    ${rest.length > 0 && html`<span class="chip" title=${rest.map(([, t]) => t).join(', ')}>+${rest.length}</span>`}`;
}

const rootDisk = (s) => s.snapshot?.disks?.find((d) => d.mount === '/') || s.snapshot?.disks?.[0];

function Unit({ s, selected, onSelect }) {
  const n = s.snapshot;
  const root = rootDisk(s);
  const info = n ? `${n.os || n.osId}, up ${uptime(n.uptime)}` : s.error ? s.error.message : 'Waiting for the first check';
  const end = eol(n);
  const a = attention(s);
  return html`<div class=${`unit ${s.status} ${a <= 2 ? 'bad' : a === 3 ? 'warn' : ''}`} role="listitem" onClick=${(e) => !e.target.closest('a,input') && go(`/servers/${s.id}`)}>
    <input type="checkbox" checked=${selected} aria-label=${`Select ${s.name}`} onChange=${onSelect} />
    <div class="name">
      <div class="row nw"><${Lamp} status=${s.status} label /><a class="trunc" href=${`#/servers/${s.id}`} title=${info}>${s.name}</a>${end && html`<span class=${`eol ${end.kind}`} title=${`${n.os}: ${end.text}`} role="img" aria-label=${end.text}><${Icon} name="warn" size=${14} /></span>`}${s.tags.length > 0 && html`<span class="tags hide-md">${s.tags.map((t) => html`<span class="tag">${t}</span>`)}</span>`}</div>
      <span class="mono trunc" title=${info}>${s.user}@${s.host}</span>
    </div>
    <div class="gauge-col"><${Gauge} label="CPU" value=${n?.cpu} /><${Spark} points=${s.spark} idx=${1} /></div>
    <div><${Gauge} label="Memory" value=${n?.mem} sub=${n ? `${bytes(n.memUsed)} of ${bytes(n.memTotal)}` : ''} /></div>
    <div class="hide-md"><${Gauge} label="Disk" value=${root?.pct} sub=${root ? `${bytes(root.used)} of ${bytes(root.size)}` : ''} /></div>
    <div class="hide-md small ink2" title=${traffic(s.traffic) || null}><div>In ${rate(n?.rx)}</div><div>Out ${rate(n?.tx)}</div>${s.traffic && html`<div class="muted nowrap">${bytes(s.traffic.rx + s.traffic.tx)} / ${s.traffic.days} d</div>`}</div>
    <div class="flags"><${Flags} s=${s} max=${MAX_FLAGS} /></div>
  </div>`;
}

// Stat-card filters; each is a predicate over a server.
const FILTERS = {
  problems: (s) => attention(s) <= 2, // the red rows: unreachable, alerts, failed units, unhealthy containers, disk health
  down: (s) => s.status === 'down' || s.ssh?.hostKeyMismatch,
  updates: (s) => s.snapshot?.updates > 0,
  alerts: (s) => s.alerts > 0,
};
const SORTS = {
  attention: byAttention,
  name: (a, b) => compare(a.name, b.name),
  cpu: (a, b) => compare(b.snapshot?.cpu, a.snapshot?.cpu),
  mem: (a, b) => compare(b.snapshot?.mem, a.snapshot?.mem),
  disk: (a, b) => compare(rootDisk(b)?.pct, rootDisk(a)?.pct),
};

export function Overview() {
  const servers = useStore(serversStore);
  const running = useStore(runningStore);
  const [q, setQ] = useQuery('q');
  const [tag, setTag] = useQuery('tag');
  const [filter, setFilter] = useQuery('status');
  const [sortBy, setSort] = useQuery('sort', 'attention');
  const [adding] = useQuery('add');
  const [sel, setSel] = useState(() => new Set());
  const admin = can('admin');
  const alerts = useLoad(() => (admin ? api('/api/alerts') : null), [admin], { poll: admin ? 30_000 : 0 }); // the firing list is admins'; others see counts per server
  const jobs = useLoad(() => api('/api/jobs?limit=6'), [], { poll: 10_000 });

  const tags = useMemo(() => [...new Set((servers || []).flatMap((s) => s.tags))].sort(), [servers]);
  const list = useMemo(() => {
    const n = q.toLowerCase().trim();
    return (servers || []).filter((s) => (!tag || s.tags.includes(tag)) && (!FILTERS[filter] || FILTERS[filter](s)) &&
      (!n || `${s.name} ${s.host} ${s.snapshot?.os || ''} ${s.tags.join(' ')}`.toLowerCase().includes(n)))
      .sort(SORTS[sortBy] || byAttention);
  }, [servers, q, tag, filter, sortBy]);

  if (!servers) return html`<div class="page"><${Loading} /></div>`;

  const up = servers.filter((s) => s.status === 'up').length;
  const problems = servers.filter(FILTERS.problems).length;
  const down = servers.filter(FILTERS.down).length;
  const withUpdates = servers.filter(FILTERS.updates);
  const updates = withUpdates.reduce((a, s) => a + s.snapshot.updates, 0);
  const security = servers.reduce((a, s) => a + (s.snapshot?.security || 0), 0);
  const alerting = servers.reduce((a, s) => a + s.alerts, 0); // same source as the sidebar badge
  const firing = alerts.data?.firing || [];
  const toggle = (id) => setSel((p) => { const n = new Set(p); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const allSel = list.length > 0 && list.every((s) => sel.has(s.id));
  const card = (key) => ({ 'aria-pressed': filter === key, onClick: () => setFilter(filter === key ? '' : key) });

  const installUpdates = async () => {
    const ids = withUpdates.map((s) => s.id);
    if (!await confirmRun({ scriptId: 'update', name: 'Update packages', count: ids.length, names: withUpdates.map((s) => s.name) })) return;
    const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: 'update', targets: { servers: ids } } });
    go(`/jobs/${id}`);
  };
  const closeAdd = () => setQuery({ add: null });

  return html`<div class="page">
    <div class="page-h">
      <div><h1>Fleet</h1><p class="sub">${plural(servers.length, 'server')}, checked every minute</p></div>
      ${can('owner') && html`<div class="row right"><${Btn} kind="primary" icon="plus" onClick=${() => setQuery({ add: '1' })}>Add server</${Btn}></div>`}
    </div>

    ${servers.length === 0 ? (can('owner') ? html`<div class="panel"><${Empty} title="Add your first server"
        action=${html`<${Btn} kind="primary" icon="plus" onClick=${() => setQuery({ add: '1' })}>Add server</${Btn}>`}>
        Fleetdeck connects over SSH, so nothing gets installed on the server. You need its address and a user that can log in with a key.</${Empty}></div>`
      : html`<div class="panel"><${Empty} title="No servers for you yet">An admin or a server's owner can give you access on the server's Settings tab.</${Empty}></div>`)
    : html`
      <div class="strip" role="group" aria-label="Filter by state">
        <button onClick=${() => setFilter('')} title="Show all servers"><b>${up}<small> / ${servers.length}</small></b><span>Online</span></button>
        <button class=${problems ? 'bad' : ''} ...${card('problems')}><b>${problems}</b><span>Need attention</span></button>
        <button class=${down ? 'bad' : ''} ...${card('down')}><b>${down}</b><span>Unreachable</span></button>
        <button class=${updates ? 'warn' : ''} ...${card('updates')}><b>${updates}</b><span>Pending updates${security ? `, ${security} security` : ''}</span></button>
        <button class=${alerting ? 'bad' : ''} ...${card('alerts')}><b>${alerting}</b><span>Alerts firing</span></button>
        <a href="#/jobs?status=running"><b>${running}</b><span>Jobs running</span></a>
      </div>

      ${firing.length > 0 && html`<div class="alert-bar" role="status"><span class="grow"><b>${plural(firing.length, 'alert')} firing</b>
        <ul>${firing.slice(0, 3).map((a) => html`<li><a href=${`#/servers/${a.server_id}`}>${a.server_name}</a>: ${a.message} <span class="muted">(${ago(a.since)})</span></li>`)}
          ${firing.length > 3 && html`<li class="muted">and ${firing.length - 3} more</li>`}</ul></span>
        ${admin && html`<a class="btn sm" href="#/alerts">Alerts</a>`}</div>`}

      ${filter === 'updates' && withUpdates.length > 0 && html`<div class="alert-bar info"><span class="grow">${plural(withUpdates.length, 'server')} with ${plural(updates, 'pending update')}.</span>
        ${can('operator') && html`<${Btn} size="sm" kind="primary" icon="refresh" onClick=${installUpdates}>Install updates on ${plural(withUpdates.length, 'server')}</${Btn}>`}</div>`}

      <div class="row">
        <input type="search" class="w-sm" placeholder="Filter servers" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter servers" />
        ${tags.map((t) => html`<button class="pill hide-sm" aria-pressed=${tag === t} onClick=${() => setTag(tag === t ? '' : t)}>${t}</button>`)}
        <label class="row nw right small muted">Sort
          <select value=${sortBy} onChange=${(e) => setSort(e.target.value)} aria-label="Sort servers">
            <option value="attention">Needs attention</option><option value="name">Name</option><option value="cpu">CPU</option><option value="mem">Memory</option><option value="disk">Disk</option>
          </select></label>
      </div>

      <div class="rack" role="list" aria-label="Servers">
        <div class="rack-h">
          <input type="checkbox" checked=${allSel} aria-label="Select all" onChange=${() => setSel(allSel ? new Set() : new Set(list.map((s) => s.id)))} />
          <span>Server</span><span>CPU, last hour</span><span>Memory</span><span>Disk</span><span>Network</span><span class="r">Status</span>
        </div>
        ${list.map((s) => html`<${Unit} key=${s.id} s=${s} selected=${sel.has(s.id)} onSelect=${() => toggle(s.id)} />`)}
        ${!list.length && html`<${Empty} title="No servers match" action=${html`<button class="btn" onClick=${() => setQuery({ q: null, tag: null, status: null })}>Clear filters</button>`}>
          ${plural(servers.length, 'server')} in total.</${Empty}>`}
      </div>

      ${sel.size > 0 && html`<div class="bulkbar" role="region" aria-label="Selection">
        <b>${sel.size} selected</b>
        <div class="row right">
          ${can('operator') && html`<${Btn} size="sm" kind="primary" icon="play" onClick=${() => openRun({ targets: { servers: [...sel] } })}>Run script</${Btn}>
          <${Btn} size="sm" icon="refresh" onClick=${() => openRun({ targets: { servers: [...sel] }, scriptId: 'update' })}>Update</${Btn}>`}
          <${Btn} size="sm" kind="ghost" onClick=${() => setSel(new Set())}>Clear</${Btn}>
        </div></div>`}

      <section class="panel">
        <div class="panel-h"><h2>Recent jobs</h2><a class="right small" href="#/jobs">All jobs</a></div>
        ${jobs.data?.length ? html`<div class="table-wrap"><table class="t"><tbody>${jobs.data.map((j) => html`<tr class="click" onClick=${(e) => !e.target.closest('a') && go(`/jobs/${j.id}`)}>
          <td class="shrink"><${JobChip} status=${j.status} /></td><td class="trunc"><a class="rowlink" href=${`#/jobs/${j.id}`}>${j.name}</a></td>
          <td class="muted nowrap hide-sm">${plural(j.runs.filter((r) => r.server_id !== '__ansible__').length, 'host')}</td><td class="muted nowrap num">${ago(j.created_at)}</td></tr>`)}</tbody></table></div>`
        : html`<${Empty} title="No jobs yet">Run a script on one or more servers and its output shows up here.</${Empty}>`}
      </section>`}

    ${adding && can('owner') && html`<${ServerDialog} onClose=${closeAdd} />`}
  </div>`;
}

// ---------- add / edit server ----------
export function ServerForm({ value, onChange, keys, hasSudo }) {
  const set = (k) => (e) => onChange({ ...value, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  return html`<div class="form">
    <div class="cols">
      <${Field} label="Name"><input value=${value.name} onInput=${set('name')} placeholder="web-1" autofocus /></${Field}>
      <${Field} label="Tags" hint="Comma separated, e.g. prod, web"><input value=${value.tags} onInput=${set('tags')} /></${Field}>
    </div>
    <div class="cols host">
      <${Field} label="Host"><input class="mono" value=${value.host} onInput=${set('host')} placeholder="203.0.113.10 or web1.example.com" /></${Field}>
      <${Field} label="Port"><input type="number" min="1" max="65535" value=${value.port} onInput=${set('port')} /></${Field}>
      <${Field} label="User"><input class="mono" value=${value.user} onInput=${set('user')} /></${Field}>
    </div>
    <${Field} label="SSH key" hint=${value.user !== 'root' ? 'Non-root users need passwordless sudo for updates and admin actions.' : null}>
      <select value=${value.key_id} onChange=${set('key_id')}>
        <option value="__new">Generate a new key for this server</option>
        ${keys.map((k) => html`<option value=${k.id}>${k.name}</option>`)}
        <option value="">Use the ssh-agent or ~/.ssh key of the Fleetdeck host</option>
      </select>
    </${Field}>
    ${value.user !== 'root' && html`<${Field} label="Sudo password (optional)"
        hint=${hasSudo ? 'Saved. Leave empty to keep it.' : 'Only if this user needs a password for sudo. Stored encrypted and never shown again; prefer passwordless sudo if you can.'}>
      <input type="password" autocomplete="new-password" value=${value.sudo_password || ''} onInput=${set('sudo_password')} placeholder=${hasSudo ? '••••••••' : ''} /></${Field}>`}
    <${Field} label="Notes"><textarea class="prose" value=${value.notes} onInput=${set('notes')} /></${Field}>
  </div>`;
}

export const serverBody = (v) => ({ name: v.name, host: v.host, port: Number(v.port), user: v.user, tags: v.tags, notes: v.notes, key_id: v.key_id || null,
  ...(v.sudo_password ? { sudo_password: v.sudo_password } : {}) });

export const PRIV_TEXT = { root: 'Logged in as root.', sudo: 'Passwordless sudo works.', password: 'sudo works with the stored password.', 'wrong-password': 'The server rejected the sudo password. Fix it in the server settings.', none: 'No sudo: monitoring works, updates and admin actions will fail. Add a sudo password in the server settings or allow passwordless sudo.' };

function ServerDialog({ onClose }) {
  const keys = useLoad(() => api('/api/keys'));
  const [v, setV] = useState({ name: '', host: '', port: 22, user: 'root', tags: '', notes: '', key_id: '__new' });
  const dirty = useDirty(v);
  const [created, setCreated] = useState(null); // { server, key }
  const [test, setTest] = useState(null);
  const [err, setErr] = useState('');

  const save = async () => {
    setErr('');
    try {
      let key = keys.data?.find((k) => k.id === v.key_id) || null;
      if (v.key_id === '__new') key = await api('/api/keys', { method: 'POST', body: { name: v.name || v.host, generate: true } });
      try {
        const server = await api('/api/servers', { method: 'POST', body: serverBody({ ...v, key_id: key?.id || null }) });
        refreshServers();
        setCreated({ server, key });
      } catch (e) {
        if (v.key_id === '__new') await api(`/api/keys/${key.id}`, { method: 'DELETE' }).catch(() => {}); // no orphan key per failed attempt
        throw e;
      }
    } catch (e) { setErr(e.message); }
  };
  const runTest = async () => setTest(await api(`/api/servers/${created.server.id}/test`, { method: 'POST' }));

  if (!created) {
    return html`<${Modal} title="Add server" onClose=${onClose} dirty=${dirty} footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Add server" disabled=${!v.host} />`}>
      ${keys.loading ? html`<${Loading} />` : html`<${ServerForm} value=${v} onChange=${setV} keys=${keys.data || []} />`}
      ${err && html`<p class="err" role="alert">${err}</p>`}
    </${Modal}>`;
  }
  const { server, key } = created;
  const cmd = key && `mkdir -p ~/.ssh && echo '${key.public_key}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`;
  return html`<${Modal} title=${`Connect ${server.name}`} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Close</button><button class="btn primary" onClick=${() => { onClose(); go(`/servers/${server.id}`); }}>Open server</button>`}>
    ${key && html`<div class="stack sm">
      <p>Authorize the key <b>${key.name}</b> on the server. Run this as <span class="mono">${server.user}</span> on ${server.host}:</p>
      <${Copy} text=${cmd} />
      <p class="small muted">Or add the public key through your cloud provider's console.</p>
    </div>`}
    <div class="row"><${Btn} icon="term" onClick=${runTest}>Test connection</${Btn}>
      ${test && (test.ok
        ? html`<span class="chip ok">Connected as ${test.user}</span><span class="small ink2">${PRIV_TEXT[test.privilege]}</span>`
        : html`<span class="chip bad">Failed</span><span class="small err">${test.error}</span>`)}</div>
    ${test?.ok && test.host_key && html`<p class="small muted">Host key pinned: <span class="mono">${test.host_key}</span></p>`}
  </${Modal}>`;
}
