import {
  html, useState, api, go, useStore, useLoad, useDirty, serversStore, toast, confirmBox,
  Btn, Th, useSort, Empty, Field, Modal, Footer, Loading, ErrorNote, ago, when,
} from '../ui.js';

const NEEDS_THRESHOLD = (m) => !['down', 'reboot', 'check', 'process'].includes(m);
const HAS_DURATION = (m) => ['down', 'check', 'cpu', 'mem', 'swap', 'load', 'temp', 'net', 'gpu', 'custom'].includes(m);
const FOR_AT_LEAST = (m) => ['down', 'check'].includes(m); // minutes are a duration, not an average
const unitOf = (u) => (!u ? '' : /^[%°]/.test(u) ? u : ` ${u}`);
const HAS_MOUNT = (m) => ['disk', 'inodes'].includes(m);
const inScope = (r, servers) => servers.filter((s) => (r.server_id ? s.id === r.server_id : !r.tag || s.tags.includes(r.tag)));
// Servers in the rule's scope that report disks but not the rule's mount.
const mountMissing = (r, servers) => (r.mount ? inScope(r, servers).filter((s) => s.snapshot?.disks && !s.snapshot.disks.some((d) => d.mount === r.mount)) : []);

function ruleText(r, metrics) {
  const m = metrics[r.metric];
  if (r.metric === 'down') return `Unreachable for ${r.minutes} min`;
  if (r.metric === 'check') return `Endpoint check failing for ${r.minutes} min`;
  if (r.metric === 'reboot') return 'Reboot required';
  if (r.metric === 'process') return `Process ${r.process} not running`;
  if (r.metric === 'custom') return `${r.custom_name} above ${r.threshold}${unitOf(r.custom_unit)}${r.minutes ? ` for ${r.minutes} min` : ''}`;
  const label = r.mount ? `${m.label.replace(' (fullest mount)', '')} on ${r.mount}` : m.label;
  return `${label} ${m.below ? 'below' : 'above'} ${r.threshold}${m.unit}${HAS_DURATION(r.metric) && r.minutes ? ` for ${r.minutes} min` : ''}`;
}

export function AlertsPage() {
  const a = useLoad(() => api('/api/alerts'), [], { poll: 30_000 });
  const n = useLoad(() => api('/api/notifiers'));
  const [rule, setRule] = useState(null);
  const [channel, setChannel] = useState(null);
  const sort = useSort(a.data?.rules);
  const servers = useStore(serversStore) || [];
  if (a.loading || n.loading) return html`<div class="page"><${Loading} /></div>`;
  if (a.error) return html`<div class="page"><${ErrorNote} error=${a.error} retry=${a.reload} /></div>`;
  const { metrics, rules, firing, history, custom } = a.data;

  const toggleRule = async (r) => { await api(`/api/alerts/rules/${r.id}`, { method: 'PUT', body: { enabled: !r.enabled } }); a.reload(true); };
  const delRule = async (r) => {
    if (!await confirmBox({ title: 'Delete this rule?', body: ruleText(r, metrics), confirm: 'Delete rule', danger: true })) return;
    await api(`/api/alerts/rules/${r.id}`, { method: 'DELETE' });
    a.reload(true);
  };
  const toggleChannel = async (c) => { await api(`/api/notifiers/${c.id}`, { method: 'PUT', body: { enabled: !c.enabled } }); n.reload(true); };
  const testChannel = async (c) => { await api(`/api/notifiers/${c.id}/test`, { method: 'POST' }); toast(`Test sent via ${c.name}`, 'ok'); };
  const delChannel = async (c) => {
    if (!await confirmBox({ title: `Delete "${c.name}"?`, confirm: 'Delete channel', danger: true })) return;
    await api(`/api/notifiers/${c.id}`, { method: 'DELETE' });
    n.reload(true);
  };
  const setJobMode = async (mode) => { await api('/api/settings', { method: 'PUT', body: { notify_jobs: mode } }); n.reload(true); toast('Saved', 'ok'); };

  const channels = n.data.notifiers.filter((c) => c.enabled).length;
  const firingBy = (r) => firing.filter((f) => f.rule_id === r.id).length;
  return html`<div class="page">
    <div class="page-h"><div><h1>Alerts</h1><p class="sub">Rules are checked after every probe. You get one message when a rule fires and one when it resolves.</p></div></div>

    ${firing.length > 0 && html`<section class="panel"><div class="panel-h"><h2>Firing now</h2><span class="count">${firing.length}</span></div>
      <div class="table-wrap"><table class="t"><tbody>${firing.map((f) => html`<tr class="click" onClick=${(e) => !e.target.closest('a') && go(`/servers/${f.server_id}`)}>
        <td class="shrink"><span class="chip bad">Firing</span></td><td><a class="rowlink" href=${`#/servers/${f.server_id}`}><b>${f.server_name}</b></a> ${f.message}</td><td class="muted nowrap num">since ${ago(f.since)}</td></tr>`)}</tbody></table></div></section>`}

    <section class="panel">
      <div class="panel-h"><h2>Rules</h2><${Btn} size="sm" icon="plus" class="right" onClick=${() => setRule({})}>Add rule</${Btn}></div>
      ${rules.length > 0 && !channels && html`<div class="panel-b b-bottom"><div class="alert-bar warn"><span class="grow">Alerts only show in Fleetdeck. Add a notification channel to get them pushed to your phone or chat.</span><a class="btn sm" href="#/alerts" onClick=${(e) => { e.preventDefault(); document.getElementById('channels')?.scrollIntoView({ behavior: 'smooth' }); }}>Notification channels</a></div></div>`}
      ${!rules.length ? html`<${Empty} title="No rules">Add a rule to get notified when a server goes down or fills up.</${Empty}>` : html`<div class="table-wrap"><table class="t">
        <thead><tr><${Th} sort=${sort} k="enabled" desc class="shrink">On</${Th}><${Th} sort=${sort} k="metric" get=${(r) => ruleText(r, metrics)}>Condition</${Th}><${Th} sort=${sort} k="scope" get=${(r) => r.server_name || r.tag || ''}>Applies to</${Th}><${Th} sort=${sort} k="firing" get=${firingBy} desc class="hide-sm">Now</${Th}><th class="act"></th></tr></thead>
        <tbody>${sort.rows.map((r) => html`<tr>
          <td><input type="checkbox" class="switch" role="switch" checked=${r.enabled} onChange=${() => toggleRule(r)} aria-label=${`Enabled: ${ruleText(r, metrics)}`} /></td>
          <td>${ruleText(r, metrics)}</td><td class="ink2">${r.server_name || (r.tag ? html`<span class="tag">${r.tag}</span>` : 'All servers')}</td>
          <td class="hide-sm">${firingBy(r) ? html`<span class="chip bad">Firing on ${firingBy(r)}</span>` : mountMissing(r, servers).length ? html`<span class="chip warn" title=${mountMissing(r, servers).map((s) => s.name).join(', ')}>Mount not found</span>` : html`<span class="muted small">${r.enabled ? 'Quiet' : 'Off'}</span>`}</td>
          <td class="act"><div class="row nw"><${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setRule(r)} /><${Btn} size="sm" kind="quiet-danger" icon="trash" title="Delete" onClick=${() => delRule(r)} /></div></td>
        </tr>`)}</tbody></table></div>`}
    </section>

    <section class="panel" id="channels">
      <div class="panel-h"><h2>Notification channels</h2><${Btn} size="sm" icon="plus" class="right" onClick=${() => setChannel({})}>Add channel</${Btn}></div>
      ${!n.data.notifiers.length ? html`<${Empty} title="Nowhere to send alerts yet">Alerts still show up here and in the sidebar. Add ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook to get pushed.</${Empty}>`
        : html`<div class="table-wrap"><table class="t"><tbody>${n.data.notifiers.map((c) => html`<tr>
          <td class="shrink"><input type="checkbox" class="switch" role="switch" checked=${c.enabled} onChange=${() => toggleChannel(c)} aria-label=${`Enabled: ${c.name}`} /></td>
          <td><b>${c.name}</b></td><td class="ink2">${n.data.types[c.type].label}</td>
          <td class="act"><div class="row nw"><${Btn} size="sm" kind="ghost" onClick=${() => testChannel(c)}>Send test</${Btn}>
            <${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setChannel(c)} /><${Btn} size="sm" kind="quiet-danger" icon="trash" title="Delete" onClick=${() => delChannel(c)} /></div></td></tr>`)}</tbody></table></div>`}
      <div class="panel-b row b-top">
        <span class="ink2">Notify about failed jobs</span>
        <div class="seg" role="group" aria-label="Failed job notifications">${[['off', 'Never'], ['scheduled', 'Scheduled and webhook jobs'], ['all', 'All jobs']].map(([k, l]) => html`<button aria-pressed=${n.data.notify_jobs === k} onClick=${() => setJobMode(k)}>${l}</button>`)}</div>
      </div>
    </section>

    <${QuietPanel} />

    <section class="panel"><div class="panel-h"><h2>History</h2><span class="small muted">Last 100 events</span></div>
      ${!history.length ? html`<${Empty} title="Nothing has fired yet" />` : html`<div class="table-wrap scroll-y"><table class="t"><tbody>${history.map((e) => html`<tr>
        <td class="nowrap ink2 shrink" title=${when(e.t)}>${ago(e.t)}</td><td class="shrink"><span class=${`chip ${e.level === 'error' ? 'bad' : 'ok'}`}>${e.level === 'error' ? 'Fired' : 'Resolved'}</span></td><td>${e.message.replace(/^(Firing|Resolved): /, '')}</td></tr>`)}</tbody></table></div>`}
    </section>

    ${rule && html`<${RuleDialog} rule=${rule} metrics=${metrics} custom=${custom} onClose=${() => setRule(null)} onSaved=${() => { setRule(null); a.reload(true); }} />`}
    ${channel && html`<${ChannelDialog} channel=${channel} types=${n.data.types} onClose=${() => setChannel(null)} onSaved=${() => { setChannel(null); n.reload(true); }} />`}
  </div>`;
}

// ---------- quiet hours ----------
const hhmm = (m) => `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
const scopeText = (w) => w.server_name || (w.tag ? `tag ${w.tag}` : 'All servers');
export const muteFor = (serverId, hours, name) => api('/api/mutes', { method: 'POST', body: { kind: 'once', server_id: serverId, name, start: Date.now(), end: Date.now() + hours * 3600e3 } });

function QuietPanel() {
  const m = useLoad(() => api('/api/mutes'), [], { poll: 60_000 });
  const [add, setAdd] = useState(false);
  const del = async (w) => { await api(`/api/mutes/${w.id}`, { method: 'DELETE' }); m.reload(true); };
  return html`<section class="panel">
    <div class="panel-h"><h2>Quiet hours</h2><span class="small muted">No notifications during these windows. Alerts still show here, and anything still firing afterwards is sent then.</span>
      <${Btn} size="sm" icon="plus" class="right" onClick=${() => setAdd(true)}>Add quiet hours</${Btn}></div>
    ${m.loading ? html`<${Loading} />` : !m.data?.windows.length ? html`<${Empty} title="Always notify">Add nightly quiet hours, or a one-time maintenance window before planned work.</${Empty}>`
      : html`<div class="table-wrap"><table class="t"><tbody>${m.data.windows.map((w) => html`<tr>
        <td class="shrink">${w.active ? html`<span class="chip warn">Active</span>` : html`<span class="chip">${w.kind === 'daily' ? 'Daily' : 'Planned'}</span>`}</td>
        <td><b>${w.kind === 'daily' ? `${hhmm(w.start_at)}–${hhmm(w.end_at)} every day` : `${when(w.start_at)} – ${when(w.end_at)}`}</b>${w.name && html`<div class="small muted">${w.name}</div>`}</td>
        <td class="ink2">${scopeText(w)}</td>
        <td class="act"><${Btn} size="sm" kind="quiet-danger" icon="trash" title="Remove" onClick=${() => del(w)} /></td></tr>`)}</tbody></table></div>`}
    ${add && html`<${QuietDialog} tz=${m.data?.tz} onClose=${() => setAdd(false)} onSaved=${() => { setAdd(false); m.reload(true); }} />`}
  </section>`;
}

const localInput = (t) => new Date(t - new Date(t).getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

function QuietDialog({ tz, onClose, onSaved }) {
  const servers = useStore(serversStore) || [];
  const tags = [...new Set(servers.flatMap((s) => s.tags))].sort();
  const [v, setV] = useState({ kind: 'daily', name: '', start: '22:00', end: '07:00', from: localInput(Date.now()), to: localInput(Date.now() + 2 * 3600e3), scope: '' });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  const save = async () => {
    setErr('');
    try {
      const tag = v.scope.startsWith('tag:') ? v.scope.slice(4) : null;
      const times = v.kind === 'daily' ? { start: v.start, end: v.end } : { start: new Date(v.from).getTime(), end: new Date(v.to).getTime() };
      await api('/api/mutes', { method: 'POST', body: { kind: v.kind, name: v.name, ...times, tag, server_id: tag ? null : v.scope || null } });
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title="Add quiet hours" onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Save" />`}>
    <div class="seg" role="group" aria-label="Kind"><button aria-pressed=${v.kind === 'daily'} onClick=${() => setV({ ...v, kind: 'daily' })}>Every day</button><button aria-pressed=${v.kind === 'once'} onClick=${() => setV({ ...v, kind: 'once' })}>One time</button></div>
    ${v.kind === 'daily'
      ? html`<div class="cols"><${Field} label="From"><input type="time" value=${v.start} onInput=${set('start')} /></${Field}><${Field} label="Until"><input type="time" value=${v.end} onInput=${set('end')} /></${Field}></div>
        <p class="small ink2">In the Fleetdeck server's time zone${tz ? ` (${tz})` : ''}. A window past midnight, like 22:00–07:00, works.</p>`
      : html`<div class="cols"><${Field} label="From"><input type="datetime-local" value=${v.from} onInput=${set('from')} /></${Field}><${Field} label="Until"><input type="datetime-local" value=${v.to} onInput=${set('to')} /></${Field}></div>`}
    <${Field} label="Applies to"><select value=${v.scope} onChange=${set('scope')}>
      <option value="">All servers</option>
      ${tags.length > 0 && html`<optgroup label="Servers tagged">${tags.map((t) => html`<option value=${`tag:${t}`}>${t}</option>`)}</optgroup>`}
      <optgroup label="One server">${servers.map((s) => html`<option value=${s.id}>${s.name}</option>`)}</optgroup></select></${Field}>
    <${Field} label="Note (optional)"><input value=${v.name} onInput=${set('name')} placeholder="Datacenter maintenance" /></${Field}>
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}

function RuleDialog({ rule, metrics, custom, onClose, onSaved }) {
  const servers = useStore(serversStore) || [];
  const tags = [...new Set(servers.flatMap((s) => s.tags))].sort();
  // scope: '' = all servers, 'tag:<name>', or a server id
  const [v, setV] = useState({ metric: 'cpu', threshold: 90, minutes: 5, enabled: true, ...rule, mount: rule.mount || '', process: rule.process || '', custom_id: rule.custom_id || custom[0]?.id || '', scope: rule.server_id || (rule.tag ? `tag:${rule.tag}` : '') });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const m = v.metric === 'custom' ? { ...metrics.custom, unit: custom.find((c) => c.id === v.custom_id)?.unit || '' } : metrics[v.metric];
  const tagOf = v.scope.startsWith('tag:') && v.scope.slice(4);
  const mounts = [...new Set(inScope({ server_id: tagOf ? null : v.scope, tag: tagOf }, servers).flatMap((s) => (s.snapshot?.disks || []).map((d) => d.mount)))].sort();
  const save = async () => {
    setErr('');
    try {
      const tag = v.scope.startsWith('tag:') ? v.scope.slice(4) : null;
      const body = { metric: v.metric, enabled: v.enabled, threshold: Number(v.threshold), minutes: Number(v.minutes), tag, server_id: tag ? null : v.scope || null, mount: HAS_MOUNT(v.metric) ? v.mount : null, custom_id: v.metric === 'custom' ? v.custom_id : null, process: v.metric === 'process' ? v.process.trim() : null };
      await api(rule.id ? `/api/alerts/rules/${rule.id}` : '/api/alerts/rules', { method: rule.id ? 'PUT' : 'POST', body });
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${rule.id ? 'Edit rule' : 'Add rule'} onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Save rule" />`}>
    <${Field} label="Alert when"><select value=${v.metric} onChange=${(e) => setV({ ...v, metric: e.target.value, threshold: metrics[e.target.value].def ?? v.threshold })}>${Object.entries(metrics).map(([k, x]) => html`<option value=${k}>${x.label}</option>`)}</select></${Field}>
    ${v.metric === 'custom' && html`<${Field} label="Custom metric" hint=${!custom.length ? "Add one in a server's Metrics panel first." : null}><select value=${v.custom_id} onChange=${(e) => setV({ ...v, custom_id: e.target.value })}>
      ${custom.map((c) => html`<option value=${c.id}>${c.name}${c.unit ? ` (${c.unit})` : ''}</option>`)}</select></${Field}>`}
    ${v.metric === 'process' && html`<${Field} label="Process name" hint="As ps -e shows it (the program name, not the full command line)."><input class="mono" value=${v.process} onInput=${(e) => setV({ ...v, process: e.target.value })} placeholder="nginx" /></${Field}>`}
    <div class="cols">
      ${NEEDS_THRESHOLD(v.metric) && html`<${Field} label=${`Is ${m.below ? 'below' : 'above'}${m.unit ? ` (${m.unit.trim()})` : ''}`}><input type="number" min="0" step="any" value=${v.threshold} onInput=${(e) => setV({ ...v, threshold: e.target.value })} /></${Field}>`}
      ${HAS_DURATION(v.metric) && html`<${Field} label=${FOR_AT_LEAST(v.metric) ? 'For at least (minutes)' : 'Averaged over (minutes)'} hint=${!FOR_AT_LEAST(v.metric) ? '0 uses the latest value' : null}><input type="number" min="0" max="1440" value=${v.minutes} onInput=${(e) => setV({ ...v, minutes: e.target.value })} /></${Field}>`}
    </div>
    <${Field} label="Applies to" hint=${v.scope.startsWith('tag:') ? 'Includes servers that get this tag later.' : null}><select value=${v.scope} onChange=${(e) => setV({ ...v, scope: e.target.value })}>
      <option value="">All servers</option>
      ${tags.length > 0 && html`<optgroup label="Servers tagged">${tags.map((t) => html`<option value=${`tag:${t}`}>${t}</option>`)}</optgroup>`}
      <optgroup label="One server">${servers.map((s) => html`<option value=${s.id}>${s.name}</option>`)}</optgroup></select></${Field}>
    ${HAS_MOUNT(v.metric) && html`<${Field} label="Mount (optional)" hint="Empty watches the fullest mount."><input class="mono" list="rule-mounts" value=${v.mount} onInput=${(e) => setV({ ...v, mount: e.target.value })} placeholder="/data" />
      <datalist id="rule-mounts">${mounts.map((x) => html`<option value=${x} />`)}</datalist></${Field}>`}
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}

function ChannelDialog({ channel, types, onClose, onSaved }) {
  const [v, setV] = useState({ name: '', type: 'ntfy', enabled: true, ...channel, config: Object.fromEntries(Object.entries(channel.config || {}).map(([k, x]) => [k, x === '••••••' ? '' : x])) });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const t = types[v.type];
  const save = async () => {
    setErr('');
    try {
      await api(channel.id ? `/api/notifiers/${channel.id}` : '/api/notifiers', { method: channel.id ? 'PUT' : 'POST', body: v });
      toast('Channel saved', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${channel.id ? `Edit ${channel.name}` : 'Add notification channel'} onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label="Save channel" disabled=${!v.name} />`}>
    <div class="cols">
      <${Field} label="Type"><select value=${v.type} disabled=${!!channel.id} onChange=${(e) => setV({ ...v, type: e.target.value, config: {} })}>${Object.entries(types).map(([k, x]) => html`<option value=${k}>${x.label}</option>`)}</select></${Field}>
      <${Field} label="Name"><input value=${v.name} onInput=${(e) => setV({ ...v, name: e.target.value })} placeholder="My phone" /></${Field}>
    </div>
    ${t.fields.map((f) => html`<${Field} label=${f.label} hint=${f.secret && channel.id && channel.config?.[f.key] ? 'Saved. Leave empty to keep it.' : f.secret ? 'Stored encrypted.' : null}>
      <input class="mono" type=${f.secret ? 'password' : 'text'} autocomplete="off" value=${v.config[f.key] || ''} onInput=${(e) => setV({ ...v, config: { ...v.config, [f.key]: e.target.value } })} /></${Field}>`)}
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}
