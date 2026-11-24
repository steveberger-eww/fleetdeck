import {
  html, useState, useEffect, api, go, useStore, useLoad, serversStore, toast, confirmBox,
  Btn, Empty, Field, Modal, Loading, ErrorNote, ago, when,
} from '../ui.js';
import { openRun, TargetPicker } from './jobs.js';

// ---------- scripts ----------
export function ScriptsPage() {
  const scripts = useLoad(() => api('/api/scripts'));
  const [edit, setEdit] = useState(null);
  const del = async (s) => {
    if (!await confirmBox({ title: `Delete "${s.name}"?`, body: 'Schedules that use it are deleted too. Past jobs keep their output.', confirm: 'Delete script', danger: true })) return;
    await api(`/api/scripts/${s.id}`, { method: 'DELETE' });
    toast('Script deleted');
    scripts.reload(true);
  };
  return html`<div class="page">
    <div class="page-h"><div><h1>Scripts</h1><p class="sub">Reusable shell scripts. Parameters become environment variables.</p></div>
      <div class="right"><${Btn} kind="primary" icon="plus" onClick=${() => setEdit({})}>New script</${Btn}></div></div>
    <section class="panel">
      ${scripts.loading ? html`<${Loading} />` : scripts.error ? html`<${ErrorNote} error=${scripts.error} retry=${scripts.reload} />` : html`<div class="table-wrap"><table class="t">
        <thead><tr><th>Script</th><th>Parameters</th><th>Runs as</th><th class="act"></th></tr></thead>
        <tbody>${scripts.data.map((s) => html`<tr>
          <td><div class="row nw"><b>${s.name}</b>${s.builtin && html`<span class="tag">built-in</span>`}${s.kind === 'ansible' && html`<span class="tag">ansible</span>`}</div><div class="small muted">${s.kind === 'ansible' ? html`<span class="mono">${s.playbook}</span> ` : ''}${s.description}</div></td>
          <td class="mono small ink2">${s.params.map((p) => (s.kind === 'ansible' ? p.name : `$${p.name}`)).join(' ') || '–'}</td>
          <td class="ink2">${s.kind === 'ansible' ? (s.as_root ? 'become root' : 'SSH user') : s.as_root ? 'root' : 'SSH user'}</td>
          <td class="act"><div class="row nw">
            <${Btn} size="sm" icon="play" onClick=${() => openRun({ scriptId: s.id })}>Run</${Btn}>
            <${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setEdit(s)} />
            ${!s.builtin && html`<${Btn} size="sm" kind="ghost" icon="trash" title="Delete" onClick=${() => del(s)} />`}
          </div></td></tr>`)}</tbody></table></div>`}
    </section>
    ${edit && html`<${ScriptDialog} script=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); scripts.reload(true); }} />`}
  </div>`;
}

function ScriptDialog({ script, onClose, onSaved }) {
  const [v, setV] = useState({ name: '', description: '', body: '#!/bin/sh\nset -e\n\n', as_root: true, params: [], kind: 'shell', repo_id: '', playbook: '', ...script });
  const repos = useLoad(() => api('/api/repos'));
  const files = useLoad(() => (v.kind === 'ansible' && v.repo_id ? api(`/api/repos/${v.repo_id}/files`) : Promise.resolve([])), [v.kind, v.repo_id]);
  const ansible = v.kind === 'ansible';
  const [err, setErr] = useState('');
  const set = (k) => (e) => setV({ ...v, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  const setParam = (i, k, val) => setV({ ...v, params: v.params.map((p, j) => (j === i ? { ...p, [k]: val } : p)) });
  const save = async () => {
    setErr('');
    try {
      await api(script.id ? `/api/scripts/${script.id}` : '/api/scripts', { method: script.id ? 'PUT' : 'POST', body: v });
      toast('Script saved', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} wide title=${script.id ? `Edit ${script.name}` : 'New script'} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${save}>Save script</${Btn}>`}>
    <div class="cols"><${Field} label="Name"><input value=${v.name} onInput=${set('name')} autofocus /></${Field}>
      <${Field} label="Description"><input value=${v.description} onInput=${set('description')} /></${Field}></div>
    ${!script.id && html`<div class="seg" role="group" aria-label="Script type">
      <button aria-pressed=${!ansible} onClick=${() => setV({ ...v, kind: 'shell' })}>Shell script</button>
      <button aria-pressed=${ansible} onClick=${() => setV({ ...v, kind: 'ansible' })}>Ansible playbook</button></div>`}
    ${ansible ? html`
      ${repos.data && !repos.data.length ? html`<div class="alert-bar info"><span class="grow">Add a Git repository first.</span><a class="btn sm" href="#/repos" onClick=${onClose}>Repositories</a></div>` : html`
      <div class="cols">
        <${Field} label="Repository"><select value=${v.repo_id} onChange=${(e) => setV({ ...v, repo_id: e.target.value })}>
          <option value="">Choose a repository</option>${(repos.data || []).map((r) => html`<option value=${r.id}>${r.name} (${r.branch})</option>`)}</select></${Field}>
        <${Field} label="Playbook" hint=${files.error ? files.error : files.loading && v.repo_id ? 'Loading files…' : 'Path inside the repository'}>
          <input class="mono" list="fd-playbooks" value=${v.playbook} onInput=${set('playbook')} placeholder="site.yml" />
          <datalist id="fd-playbooks">${(files.data || []).map((f) => html`<option value=${f} />`)}</datalist></${Field}>
      </div>`}
      <label class="check"><input type="checkbox" checked=${v.as_root} onChange=${set('as_root')} />Become root (ansible_become)</label>
      <p class="small ink2">Fleetdeck builds the inventory from the servers you pick: hosts use the server names, tags become groups.</p>`
    : html`
    <${Field} label="Script" hint="Runs with sh on each server. Non-zero exit marks that server as failed.">
      <textarea style=${{ minHeight: '220px' }} spellcheck="false" value=${v.body} onInput=${set('body')} /></${Field}>
    <label class="check"><input type="checkbox" checked=${v.as_root} onChange=${set('as_root')} />Run as root (via sudo when the SSH user isn't root)</label>`}
    <div class="field"><span>${ansible ? 'Extra variables' : 'Parameters'}</span>
      ${v.params.map((p, i) => html`<div class="row nw">
        <input class="mono" placeholder=${ansible ? 'name' : 'NAME'} value=${p.name} onInput=${(e) => setParam(i, 'name', ansible ? e.target.value : e.target.value.toUpperCase())} style=${{ width: '140px' }} aria-label="Variable name" />
        <input placeholder="Label" value=${p.label} onInput=${(e) => setParam(i, 'label', e.target.value)} class="grow" aria-label="Label" />
        <input placeholder="Default" value=${p.default} onInput=${(e) => setParam(i, 'default', e.target.value)} class="grow" aria-label="Default value" />
        <label class="check small nowrap"><input type="checkbox" checked=${p.required} onChange=${(e) => setParam(i, 'required', e.target.checked)} />Required</label>
        <${Btn} size="sm" kind="ghost" icon="x" title="Remove parameter" onClick=${() => setV({ ...v, params: v.params.filter((_, j) => j !== i) })} />
      </div>`)}
      <div><${Btn} size="sm" icon="plus" onClick=${() => setV({ ...v, params: [...v.params, { name: '', label: '', default: '', required: false }] })}>Add parameter</${Btn}></div>
    </div>
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}

// ---------- schedules ----------
function targetText(t, servers) {
  if (t.all) return 'All servers';
  const names = (t.servers || []).map((id) => servers.find((s) => s.id === id)?.name || 'removed server');
  return [...names, ...(t.tags || []).map((x) => `tag ${x}`)].join(', ');
}

export function SchedulesPage() {
  const list = useLoad(() => api('/api/schedules'), [], { poll: 30_000 });
  const servers = useStore(serversStore) || [];
  const [edit, setEdit] = useState(null);
  const toggle = async (s) => { await api(`/api/schedules/${s.id}`, { method: 'PUT', body: { enabled: !s.enabled } }); list.reload(true); };
  const runNow = async (s) => { const { id } = await api(`/api/schedules/${s.id}/run`, { method: 'POST' }); go(`/jobs/${id}`); };
  const del = async (s) => {
    if (!await confirmBox({ title: `Delete schedule "${s.name}"?`, confirm: 'Delete schedule', danger: true })) return;
    await api(`/api/schedules/${s.id}`, { method: 'DELETE' });
    list.reload(true);
  };
  return html`<div class="page">
    <div class="page-h"><div><h1>Schedules</h1><p class="sub">Run scripts automatically. Times use the Fleetdeck host's time zone (${Intl.DateTimeFormat().resolvedOptions().timeZone} in this browser).</p></div>
      <div class="right"><${Btn} kind="primary" icon="plus" onClick=${() => setEdit({})}>New schedule</${Btn}></div></div>
    <section class="panel">
      ${list.loading ? html`<${Loading} />` : !list.data?.length
        ? html`<${Empty} title="No schedules yet" action=${html`<${Btn} icon="plus" onClick=${() => setEdit({ name: 'Nightly updates', script_id: 'update', cron: '0 4 * * *', targets: { all: true } })}>Schedule nightly updates</${Btn}>`}>For example: install updates every night at 04:00 on all servers.</${Empty}>`
        : html`<div class="table-wrap"><table class="t">
        <thead><tr><th>On</th><th>Schedule</th><th>When</th><th>Targets</th><th>Next run</th><th>Last run</th><th class="act"></th></tr></thead>
        <tbody>${list.data.map((s) => html`<tr>
          <td><input type="checkbox" checked=${s.enabled} onChange=${() => toggle(s)} aria-label=${`Enable ${s.name}`} /></td>
          <td><b>${s.name}</b><div class="small muted">${s.script_name}</div></td>
          <td class="mono">${s.cron}</td>
          <td class="ink2 trunc" style=${{ maxWidth: '220px' }}>${targetText(s.targets, servers)}</td>
          <td class="ink2 nowrap">${s.enabled && s.next ? when(s.next) : '–'}</td>
          <td class="nowrap">${s.last_job ? html`<a href=${`#/jobs/${s.last_job}`}>${ago(s.last_run)}</a>` : html`<span class="muted">never</span>`}</td>
          <td class="act"><div class="row nw">
            <${Btn} size="sm" icon="play" onClick=${() => runNow(s)}>Run now</${Btn}>
            <${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setEdit(s)} />
            <${Btn} size="sm" kind="ghost" icon="trash" title="Delete" onClick=${() => del(s)} />
          </div></td></tr>`)}</tbody></table></div>`}
    </section>
    ${edit && html`<${ScheduleDialog} schedule=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(true); }} />`}
  </div>`;
}

const PRESETS = [['Every hour', '0 * * * *'], ['Daily at 04:00', '0 4 * * *'], ['Sundays at 03:00', '0 3 * * 0'], ['Weekdays at 09:00', '0 9 * * 1-5'], ['1st of the month', '0 5 1 * *']];

function ScheduleDialog({ schedule, onClose, onSaved }) {
  const scripts = useLoad(() => api('/api/scripts'));
  const [v, setV] = useState({ name: '', script_id: 'update', cron: '0 4 * * *', targets: { all: true }, params: {}, enabled: true, ...schedule });
  const [preview, setPreview] = useState({ next: [], error: '' });
  const [err, setErr] = useState('');
  const script = scripts.data?.find((s) => s.id === v.script_id);
  useEffect(() => {
    const t = setTimeout(() => api('/api/cron/preview', { method: 'POST', body: { cron: v.cron } })
      .then((r) => setPreview({ next: r.next, error: '' }), (e) => setPreview({ next: [], error: e.message })), 250);
    return () => clearTimeout(t);
  }, [v.cron]);
  const save = async () => {
    setErr('');
    try {
      await api(schedule.id ? `/api/schedules/${schedule.id}` : '/api/schedules', { method: schedule.id ? 'PUT' : 'POST', body: v });
      toast('Schedule saved', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} wide title=${schedule.id ? `Edit ${schedule.name}` : 'New schedule'} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${save}>Save schedule</${Btn}>`}>
    <div class="cols">
      <${Field} label="Name"><input value=${v.name} onInput=${(e) => setV({ ...v, name: e.target.value })} autofocus /></${Field}>
      <${Field} label="Script">${scripts.loading ? html`<${Loading} />` : html`<select value=${v.script_id} onChange=${(e) => setV({ ...v, script_id: e.target.value, params: {} })}>${scripts.data.map((s) => html`<option value=${s.id}>${s.name}</option>`)}</select>`}</${Field}>
    </div>
    ${script?.params.map((p) => html`<${Field} label=${p.label}><input class="mono" value=${v.params[p.name] ?? p.default} onInput=${(e) => setV({ ...v, params: { ...v.params, [p.name]: e.target.value } })} /></${Field}>`)}
    <${Field} label="When" hint="Cron format: minute hour day-of-month month day-of-week">
      <input class="mono" value=${v.cron} onInput=${(e) => setV({ ...v, cron: e.target.value })} />
    </${Field}>
    <div class="row">${PRESETS.map(([l, c]) => html`<button type="button" class="tag" aria-pressed=${v.cron === c} onClick=${() => setV({ ...v, cron: c })}>${l}</button>`)}</div>
    ${preview.error ? html`<p class="err">${preview.error}</p>` : html`<p class="small ink2">Next runs: ${preview.next.map(when).join('; ') || '–'}</p>`}
    <${TargetPicker} value=${v.targets} onChange=${(targets) => setV({ ...v, targets })} />
    <label class="check"><input type="checkbox" checked=${v.enabled} onChange=${(e) => setV({ ...v, enabled: e.target.checked })} />Enabled</label>
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}
