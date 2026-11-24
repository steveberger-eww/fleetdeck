import {
  html, useState, useMemo, api, go, useStore, useLoad, serversStore, refreshServers, toast, confirmBox,
  Btn, Lamp, Gauge, Spark, Empty, Field, Modal, Copy, Loading, JobChip, bytes, rate, ago, dur, uptime,
} from '../ui.js';
import { openRun } from './jobs.js';

export function Flags({ s }) {
  const n = s.snapshot;
  const out = [];
  if (s.status === 'down') out.push(html`<span class="chip bad" title=${s.error?.message}>Unreachable${s.error ? ` for ${dur(Date.now() - s.error.since)}` : ''}</span>`);
  if (s.status === 'paused') out.push(html`<span class="chip">Paused</span>`);
  if (s.ssh?.hostKeyMismatch) out.push(html`<span class="chip bad">Host key changed</span>`);
  if (s.alerts) out.push(html`<span class="chip bad">${s.alerts} alert${s.alerts > 1 ? 's' : ''}</span>`);
  if (n?.security) out.push(html`<span class="chip warn">${n.security} security</span>`);
  if (n?.updates) out.push(html`<span class="chip warn">${n.updates} update${n.updates > 1 ? 's' : ''}</span>`);
  if (n?.reboot) out.push(html`<span class="chip warn">Reboot needed</span>`);
  if (n?.failed) out.push(html`<span class="chip bad">${n.failed} failed unit${n.failed > 1 ? 's' : ''}</span>`);
  if (n?.unhealthy) out.push(html`<span class="chip bad">${n.unhealthy} unhealthy container${n.unhealthy > 1 ? 's' : ''}</span>`);
  if (n?.smart?.disks?.some((d) => d.problem)) out.push(html`<span class="chip bad">Disk health</span>`);
  if (!out.length && s.status === 'up') out.push(html`<span class="chip ok">Healthy</span>`);
  return out;
}

function Unit({ s, selected, onSelect }) {
  const n = s.snapshot;
  const root = n?.disks?.find((d) => d.mount === '/') || n?.disks?.[0];
  return html`<div class=${`unit ${s.status}`} role="listitem" onClick=${() => go(`/servers/${s.id}`)}>
    <input type="checkbox" checked=${selected} aria-label=${`Select ${s.name}`} onClick=${(e) => e.stopPropagation()} onChange=${onSelect} />
    <div class="name">
      <div class="row nw"><${Lamp} status=${s.status} /><b class="trunc">${s.name}</b>${s.tags.map((t) => html`<span class="tag hide-sm">${t}</span>`)}</div>
      <span class="mono trunc">${s.user}@${s.host}</span>
      <span class="small muted trunc">${n ? `${n.os || n.osId}, up ${uptime(n.uptime)}` : s.error ? s.error.message : 'Waiting for the first check'}</span>
    </div>
    <div class="gauge-col"><${Gauge} label="CPU" value=${n?.cpu} /><${Spark} points=${s.spark} idx=${1} /></div>
    <div><${Gauge} label="Memory" value=${n?.mem} sub=${n ? `${bytes(n.memUsed)} of ${bytes(n.memTotal)}` : ''} /></div>
    <div class="hide-sm"><${Gauge} label="Disk" value=${root?.pct} sub=${root ? `${bytes(root.used)} of ${bytes(root.size)}` : ''} /></div>
    <div class="hide-sm small ink2"><div>In ${rate(n?.rx)}</div><div>Out ${rate(n?.tx)}</div></div>
    <div class="flags"><${Flags} s=${s} /></div>
  </div>`;
}

export function Overview() {
  const servers = useStore(serversStore);
  const [q, setQ] = useState('');
  const [tag, setTag] = useState(null);
  const [sel, setSel] = useState(() => new Set());
  const [adding, setAdding] = useState(false);
  const alerts = useLoad(() => api('/api/alerts'), [], { poll: 30_000 });
  const jobs = useLoad(() => api('/api/jobs?limit=6'), [], { poll: 10_000 });
  const runningJobs = useLoad(() => api('/api/jobs?status=running&limit=200'), [], { poll: 10_000 });

  const tags = useMemo(() => [...new Set((servers || []).flatMap((s) => s.tags))].sort(), [servers]);
  const list = useMemo(() => {
    const n = q.toLowerCase().trim();
    return (servers || []).filter((s) => (!tag || s.tags.includes(tag)) &&
      (!n || `${s.name} ${s.host} ${s.snapshot?.os || ''} ${s.tags.join(' ')}`.toLowerCase().includes(n)));
  }, [servers, q, tag]);

  if (!servers) return html`<div class="page"><${Loading} /></div>`;

  const up = servers.filter((s) => s.status === 'up').length;
  const down = servers.filter((s) => s.status === 'down').length;
  const updates = servers.reduce((a, s) => a + (s.snapshot?.updates || 0), 0);
  const security = servers.reduce((a, s) => a + (s.snapshot?.security || 0), 0);
  const firing = alerts.data?.firing || [];
  const running = runningJobs.data?.length || 0;
  const toggle = (id) => setSel((p) => { const n = new Set(p); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const allSel = list.length > 0 && list.every((s) => sel.has(s.id));

  const updateAll = async () => {
    if (!await confirmBox({ title: 'Update all servers?', body: `Installs all pending package upgrades on ${servers.length} servers.`, confirm: 'Update all' })) return;
    const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: 'update', targets: { all: true } } });
    go(`/jobs/${id}`);
  };

  return html`<div class="page">
    <div class="page-h">
      <div><h1>Fleet</h1><p class="sub">${servers.length} server${servers.length === 1 ? '' : 's'}, checked every minute</p></div>
      <div class="row right">
        ${servers.length > 0 && html`<${Btn} icon="refresh" onClick=${updateAll}>Update all</${Btn}>`}
        <${Btn} kind="primary" icon="plus" onClick=${() => setAdding(true)}>Add server</${Btn}>
      </div>
    </div>

    ${servers.length === 0 ? html`<div class="panel"><${Empty} title="Add your first server"
        action=${html`<${Btn} kind="primary" icon="plus" onClick=${() => setAdding(true)}>Add server</${Btn}>`}>
        Fleetdeck connects over SSH, so nothing gets installed on the server. You need its address and a user that can log in with a key.</${Empty}></div>`
    : html`
      <div class="strip">
        <div><b>${up}<span class="muted" style=${{ fontSize: '15px' }}> / ${servers.length}</span></b><span>Online</span></div>
        <div class=${down ? 'bad' : ''}><b>${down}</b><span>Unreachable</span></div>
        <div class=${updates ? 'warn' : ''}><b>${updates}</b><span>Pending updates${security ? `, ${security} security` : ''}</span></div>
        <div class=${firing.length ? 'bad' : ''}><b>${firing.length}</b><span>Alerts firing</span></div>
        <div><b>${running}</b><span>Jobs running</span></div>
      </div>

      <div class="row">
        <input type="search" placeholder="Filter servers" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter servers" style=${{ width: '220px' }} />
        ${tags.map((t) => html`<button class="tag" aria-pressed=${tag === t} onClick=${() => setTag(tag === t ? null : t)}>${t}</button>`)}
        ${sel.size > 0 && html`<div class="row right">
          <span class="small ink2">${sel.size} selected</span>
          <${Btn} size="sm" onClick=${() => openRun({ targets: { servers: [...sel] } })} icon="play">Run script</${Btn}>
          <${Btn} size="sm" onClick=${() => openRun({ targets: { servers: [...sel] }, scriptId: 'update' })}>Update</${Btn}>
          <${Btn} size="sm" kind="ghost" onClick=${() => setSel(new Set())}>Clear</${Btn}>
        </div>`}
      </div>

      <div class="rack" role="list" aria-label="Servers">
        <div class="rack-h">
          <input type="checkbox" checked=${allSel} aria-label="Select all" onChange=${() => setSel(allSel ? new Set() : new Set(list.map((s) => s.id)))} />
          <span>Server</span><span>CPU, last hour</span><span>Memory</span><span>Disk</span><span>Network</span><span style=${{ textAlign: 'right' }}>Status</span>
        </div>
        ${list.map((s) => html`<${Unit} key=${s.id} s=${s} selected=${sel.has(s.id)} onSelect=${() => toggle(s.id)} />`)}
        ${!list.length && html`<${Empty} title="No servers match">Clear the filter to see all ${servers.length}.</${Empty}>`}
      </div>

      <div class="charts">
        <section class="panel">
          <div class="panel-h"><h2>Firing alerts</h2><a class="right small" href="#/alerts">Alert rules</a></div>
          ${firing.length ? html`<table class="t"><tbody>${firing.map((a) => html`<tr class="click" onClick=${() => go(`/servers/${a.server_id}`)}>
            <td><span class="chip bad">Firing</span></td><td>${a.message}</td><td class="muted nowrap">${ago(a.since)}</td></tr>`)}</tbody></table>`
          : html`<${Empty} title="All clear">No alert rule is firing right now.</${Empty}>`}
        </section>
        <section class="panel">
          <div class="panel-h"><h2>Recent jobs</h2><a class="right small" href="#/jobs">All jobs</a></div>
          ${jobs.data?.length ? html`<table class="t"><tbody>${jobs.data.map((j) => html`<tr class="click" onClick=${() => go(`/jobs/${j.id}`)}>
            <td><${JobChip} status=${j.status} /></td><td class="trunc">${j.name}</td><td class="muted nowrap">${j.runs.length} host${j.runs.length === 1 ? '' : 's'}</td><td class="muted nowrap">${ago(j.created_at)}</td></tr>`)}</tbody></table>`
          : html`<${Empty} title="No jobs yet">Run a script on one or more servers and its output shows up here.</${Empty}>`}
        </section>
      </div>`}

    ${adding && html`<${ServerDialog} onClose=${() => setAdding(false)} />`}
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
    <div class="cols" style=${{ gridTemplateColumns: 'minmax(0, 3fr) minmax(80px, 1fr) minmax(0, 2fr)' }}>
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
    <${Field} label="Notes"><textarea style=${{ minHeight: '60px', fontFamily: 'var(--font)', fontSize: '14px' }} value=${value.notes} onInput=${set('notes')} /></${Field}>
  </div>`;
}

export const serverBody = (v) => ({ name: v.name, host: v.host, port: Number(v.port), user: v.user, tags: v.tags, notes: v.notes, key_id: v.key_id || null,
  ...(v.sudo_password ? { sudo_password: v.sudo_password } : {}) });

function ServerDialog({ onClose }) {
  const keys = useLoad(() => api('/api/keys'));
  const [v, setV] = useState({ name: '', host: '', port: 22, user: 'root', tags: '', notes: '', key_id: '__new' });
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
    return html`<${Modal} title="Add server" onClose=${onClose} footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${save}>Add server</${Btn}>`}>
      ${keys.loading ? html`<${Loading} />` : html`<${ServerForm} value=${v} onChange=${setV} keys=${keys.data || []} />`}
      ${err && html`<p class="err">${err}</p>`}
    </${Modal}>`;
  }
  const { server, key } = created;
  const cmd = key && `mkdir -p ~/.ssh && echo '${key.public_key}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`;
  const privText = { root: 'Logged in as root.', sudo: 'Passwordless sudo works.', password: 'sudo works with the stored password.', 'wrong-password': 'The server rejected the sudo password. Fix it in the server settings.', none: 'No sudo: monitoring works, updates and admin actions will fail. Add a sudo password in the server settings or allow passwordless sudo.' };
  return html`<${Modal} title=${`Connect ${server.name}`} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Close</button><button class="btn primary" onClick=${() => { onClose(); go(`/servers/${server.id}`); }}>Open server</button>`}>
    ${key && html`<div class="stack" style=${{ gap: '8px' }}>
      <p>Authorize the key <b>${key.name}</b> on the server. Run this as <span class="mono">${server.user}</span> on ${server.host}:</p>
      <${Copy} text=${cmd} />
      <p class="small muted">Or add the public key through your cloud provider's console.</p>
    </div>`}
    <div class="row"><${Btn} icon="term" onClick=${runTest}>Test connection</${Btn}>
      ${test && (test.ok
        ? html`<span class="chip ok">Connected as ${test.user}</span><span class="small ink2">${privText[test.privilege]}</span>`
        : html`<span class="chip bad">Failed</span><span class="small err">${test.error}</span>`)}</div>
    ${test?.ok && test.host_key && html`<p class="small muted">Host key pinned: <span class="mono">${test.host_key}</span></p>`}
  </${Modal}>`;
}

