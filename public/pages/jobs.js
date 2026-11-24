import {
  html, useState, useEffect, useMemo, api, go, store, useStore, useLoad, serversStore, toast, confirmBox,
  Btn, Th, useSort, Lamp, JobChip, Empty, Field, Modal, Loading, ErrorNote, Output, ago, dur, when,
} from '../ui.js';

// ---------- target picker (servers, tags, or all) ----------
export function resolveTargets(t, servers) {
  if (t.all) return servers;
  const ids = new Set(t.servers || []);
  const tags = new Set(t.tags || []);
  return servers.filter((s) => ids.has(s.id) || s.tags.some((x) => tags.has(x)));
}

export function TargetPicker({ value, onChange }) {
  const servers = useStore(serversStore) || [];
  const tags = [...new Set(servers.flatMap((s) => s.tags))].sort();
  const t = { all: false, servers: [], tags: [], ...value };
  const flip = (list, x) => (list.includes(x) ? list.filter((y) => y !== x) : [...list, x]);
  const n = resolveTargets(t, servers).length;
  return html`<div class="field">
    <span>Run on <span class="muted">(${n} server${n === 1 ? '' : 's'})</span></span>
    <label class="check"><input type="checkbox" checked=${t.all} onChange=${(e) => onChange({ ...t, all: e.target.checked })} />All servers, including ones added later</label>
    ${!t.all && html`
      ${tags.length > 0 && html`<div class="row"><span class="small muted">Tags</span>${tags.map((x) => html`<button type="button" class="tag" aria-pressed=${t.tags.includes(x)} onClick=${() => onChange({ ...t, tags: flip(t.tags, x) })}>${x}</button>`)}</div>`}
      <div class="panel" style=${{ maxHeight: '190px', overflowY: 'auto', padding: '6px 10px' }}>
        ${servers.map((s) => html`<label class="check" style=${{ padding: '3px 0' }}>
          <input type="checkbox" checked=${t.servers.includes(s.id) || s.tags.some((x) => t.tags.includes(x))} disabled=${s.tags.some((x) => t.tags.includes(x))}
            onChange=${() => onChange({ ...t, servers: flip(t.servers, s.id) })} />
          <${Lamp} status=${s.status} />${s.name}<span class="mono muted small">${s.host}</span></label>`)}
        ${!servers.length && html`<span class="muted small">No servers yet.</span>`}
      </div>`}
  </div>`;
}

// ---------- run dialog ----------
const runStore = store(null);
export const openRun = (opts = {}) => runStore.set(opts);
export function RunHost() {
  const o = useStore(runStore);
  return o && html`<${RunDialog} ...${o} onClose=${() => runStore.set(null)} />`;
}

function RunDialog({ targets: initTargets, scriptId: initScript, onClose }) {
  const scripts = useLoad(() => api('/api/scripts'));
  const [mode, setMode] = useState('script');
  const [scriptId, setScriptId] = useState(initScript || 'update');
  const [params, setParams] = useState({});
  const [command, setCommand] = useState('');
  const [asRoot, setAsRoot] = useState(true);
  const [check, setCheck] = useState(false);
  const [targets, setTargets] = useState(initTargets || { servers: [], tags: [] });
  const [err, setErr] = useState('');
  const servers = useStore(serversStore) || [];
  const script = scripts.data?.find((s) => s.id === scriptId);
  useEffect(() => { if (script) setParams(Object.fromEntries(script.params.map((p) => [p.name, p.default]))); }, [script?.id]);
  const n = resolveTargets(targets, servers).length;

  const run = async () => {
    setErr('');
    const destructive = mode === 'script' && ['reboot', 'shutdown'].includes(scriptId);
    if (destructive && !await confirmBox({ title: `${script.name} ${n} server${n === 1 ? '' : 's'}?`, body: 'Services on these servers will be unavailable until they come back.', confirm: script.name, danger: true, typeToConfirm: n > 1 ? `${n} servers` : undefined })) return;
    try {
      const body = mode === 'script' ? { script_id: scriptId, params, targets, options: { check } } : { body: command, as_root: asRoot, targets };
      const { id } = await api('/api/jobs', { method: 'POST', body });
      onClose();
      go(`/jobs/${id}`);
    } catch (e) { setErr(e.message); }
  };

  return html`<${Modal} title="Run a script" wide onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" icon="play" disabled=${!n} onClick=${run}>Run on ${n} server${n === 1 ? '' : 's'}</${Btn}>`}>
    <div class="seg" role="group" aria-label="What to run">
      <button aria-pressed=${mode === 'script'} onClick=${() => setMode('script')}>Saved script</button>
      <button aria-pressed=${mode === 'command'} onClick=${() => setMode('command')}>One-off command</button>
    </div>
    ${mode === 'script' ? (scripts.loading ? html`<${Loading} />` : html`
      <${Field} label="Script" hint=${script?.description}>
        <select value=${scriptId} onChange=${(e) => setScriptId(e.target.value)}>${(scripts.data || []).map((s) => html`<option value=${s.id}>${s.name}</option>`)}</select>
      </${Field}>
      ${script?.params.map((p) => html`<${Field} label=${p.label} hint=${script.kind === 'ansible' ? html`Extra variable <span class="mono">{{ ${p.name} }}</span>` : html`Available as <span class="mono">$${p.name}</span>`}>
        <input class="mono" value=${params[p.name] ?? ''} required=${p.required} onInput=${(e) => setParams({ ...params, [p.name]: e.target.value })} /></${Field}>`)}
      ${script?.kind === 'ansible' ? html`<p class="small ink2">Runs <span class="mono">ansible-playbook ${script.playbook}</span> from the latest commit of its repository.</p>
        <label class="check"><input type="checkbox" checked=${check} onChange=${(e) => setCheck(e.target.checked)} />Dry run: show what would change (--check --diff)</label>`
      : script && html`<details><summary class="small ink2" style=${{ cursor: 'pointer' }}>Show script</summary><pre class="out" style=${{ marginTop: '8px', maxHeight: '200px' }}>${script.body}</pre></details>`}`)
    : html`
      <${Field} label="Command" hint="Runs with sh. Multiple lines are fine."><textarea autofocus value=${command} onInput=${(e) => setCommand(e.target.value)} placeholder="df -h /" /></${Field}>
      <label class="check"><input type="checkbox" checked=${asRoot} onChange=${(e) => setAsRoot(e.target.checked)} />Run as root (via sudo when the SSH user isn't root)</label>`}
    <${TargetPicker} value=${targets} onChange=${setTargets} />
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}

// ---------- job list ----------
export function JobsTable({ serverId, limit = 30 }) {
  const [extra, setExtra] = useState([]);
  const jobs = useLoad(() => api(`/api/jobs?limit=${limit}${serverId ? `&server=${serverId}` : ''}`), [serverId], { poll: 5000 });
  const rows = useMemo(() => [...(jobs.data || []), ...extra.filter((e) => !(jobs.data || []).some((j) => j.id === e.id))], [jobs.data, extra]);
  const sort = useSort(rows);
  const more = async () => {
    const last = rows[rows.length - 1];
    setExtra([...extra, ...await api(`/api/jobs?limit=${limit}&before=${last.id}${serverId ? `&server=${serverId}` : ''}`)]);
  };
  if (jobs.loading && !jobs.data) return html`<${Loading} />`;
  if (jobs.error) return html`<${ErrorNote} error=${jobs.error} retry=${jobs.reload} />`;
  if (!rows.length) return html`<${Empty} title="No jobs yet" action=${html`<${Btn} icon="play" onClick=${() => openRun(serverId ? { targets: { servers: [serverId] } } : {})}>Run a script</${Btn}>`}>Every script you run is kept here with its full output.</${Empty}>`;
  return html`<div class="table-wrap"><table class="t">
    <thead><tr><${Th} sort=${sort} k="name">Job</${Th}><${Th} sort=${sort} k="status">Status</${Th}><${Th} sort=${sort} k="servers" get=${(j) => j.runs.length} desc>Servers</${Th}><${Th} sort=${sort} k="actor">Started by</${Th}><${Th} sort=${sort} k="created_at" desc>Started</${Th}><${Th} sort=${sort} k="took" get=${(j) => j.finished_at && j.finished_at - j.created_at} class="num" desc>Took</${Th}></tr></thead>
    <tbody>${sort.rows.map((j) => {
      j = { ...j, runs: j.runs.filter((r) => r.server_id !== '__ansible__') };
      const failed = j.runs.filter((r) => ['failed', 'interrupted'].includes(r.status)).length;
      return html`<tr class="click" onClick=${() => go(`/jobs/${j.id}`)}>
        <td><span class="muted">#${j.id}</span> ${j.name}${j.kind === 'ansible' && html` <span class="tag">ansible</span>`}</td>
        <td><${JobChip} status=${j.status} /></td>
        <td class="trunc" style=${{ maxWidth: '260px' }}>${j.runs.length <= 3 ? j.runs.map((r) => r.server_name).join(', ') : `${j.runs.length} servers`}${failed ? html` <span class="chip bad">${failed} failed</span>` : ''}</td>
        <td class="ink2">${j.actor}</td><td class="ink2 nowrap" title=${when(j.created_at)}>${ago(j.created_at)}</td>
        <td class="num ink2">${j.finished_at ? dur(j.finished_at - j.created_at) : '…'}</td></tr>`;
    })}</tbody></table>
    ${rows.length >= limit && rows.length % limit === 0 && html`<div style=${{ padding: '10px' }}><${Btn} size="sm" onClick=${more}>Load older jobs</${Btn}></div>`}
  </div>`;
}

export function JobsPage() {
  return html`<div class="page">
    <div class="page-h"><div><h1>Jobs</h1><p class="sub">Every script run, with per-server output. Kept for 90 days by default.</p></div>
      <div class="right"><${Btn} kind="primary" icon="play" onClick=${() => openRun()}>Run script</${Btn}></div></div>
    <section class="panel"><${JobsTable} limit=${40} /></section>
  </div>`;
}

// ---------- job detail with live output ----------
export function JobPage({ id }) {
  const [job, setJob] = useState(null);
  const [error, setError] = useState('');
  const [sel, setSel] = useState(null);
  useEffect(() => {
    const es = new EventSource(`/api/jobs/${id}/stream`);
    const patch = (server, f) => setJob((j) => j && ({ ...j, runs: j.runs.map((r) => (r.server_id === server ? f(r) : r)) }));
    es.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.type === 'snapshot') setJob(m.job);
      else if (m.type === 'data') patch(m.server, (r) => ({ ...r, output: r.output + m.data }));
      else if (m.type === 'run') patch(m.server, (r) => ({ ...r, status: m.status, exit_code: m.code ?? r.exit_code }));
      else if (m.type === 'job') { setJob((j) => ({ ...j, status: m.status, finished_at: Date.now() })); es.close(); }
    };
    es.onerror = () => { es.close(); api(`/api/jobs/${id}`).then(setJob, (e) => setError(e.message)); };
    return () => es.close();
  }, [id]);

  const runs = (job?.runs || []).slice().sort((a, b) => (b.server_id === '__ansible__') - (a.server_id === '__ansible__'));
  const hosts = runs.filter((r) => r.server_id !== '__ansible__');
  const current = runs.find((r) => r.server_id === sel) || (job?.kind === 'ansible' ? runs[0] : runs.find((r) => r.status === 'failed') || runs.find((r) => r.status === 'running') || runs[0]);
  const failed = hosts.filter((r) => !['success', 'skipped'].includes(r.status)).length;
  if (error) return html`<div class="page"><${ErrorNote} error=${error} /></div>`;
  if (!job) return html`<div class="page"><${Loading} /></div>`;

  const rerun = async (failedOnly) => {
    const r = await api(`/api/jobs/${id}/rerun`, { method: 'POST', body: { failed_only: failedOnly } });
    go(`/jobs/${r.id}`);
  };
  const cancel = async () => {
    if (!await confirmBox({ title: 'Cancel this job?', body: 'Output stops immediately. A command already running on a server may still finish there.', confirm: 'Cancel job', danger: true })) return;
    await api(`/api/jobs/${id}/cancel`, { method: 'POST' });
    toast('Job cancelled');
  };
  return html`<div class="page">
    <div class="page-h">
      <div><div class="row"><h1>${job.name}</h1><${JobChip} status=${job.status} /></div>
        <p class="sub">Job #${job.id}, started by ${job.actor} ${ago(job.created_at)}${job.finished_at ? `, took ${dur(job.finished_at - job.created_at)}` : ''}${job.options?.check ? '. Dry run, nothing was changed' : ''}</p></div>
      <div class="row right">
        ${job.status === 'running' && html`<${Btn} kind="danger" icon="stop" onClick=${cancel}>Cancel</${Btn}>`}
        ${job.status !== 'running' && failed > 0 && failed < hosts.length && html`<${Btn} onClick=${() => rerun(true)}>Re-run failed</${Btn}>`}
        ${job.status !== 'running' && html`<${Btn} icon="refresh" onClick=${() => rerun(false)}>Re-run</${Btn}>`}
      </div>
    </div>
    <div class="job-grid">
      <div class="panel" style=${{ padding: '6px' }}><div class="hostlist">${runs.map((r) => html`<button aria-pressed=${r === current} onClick=${() => setSel(r.server_id)}>
        <${Lamp} status=${r.status === 'success' ? 'up' : r.status === 'running' ? 'running' : ['failed', 'interrupted'].includes(r.status) ? 'down' : ''} />
        <span class=${`grow trunc ${r.server_id === '__ansible__' ? 'mono' : ''}`}>${r.server_name}</span><${JobChip} status=${r.status} /></button>`)}</div></div>
      <section class="panel">
        <div class="panel-h"><h2>${current?.server_name}</h2>
          ${current?.exit_code != null && html`<span class="small muted">exit code ${current.exit_code}</span>`}
          ${current?.server_id !== '__ansible__' && html`<a class="right small" href=${`#/servers/${current?.server_id}`}>Open server</a>`}</div>
        <div class="panel-b"><${Output} text=${current?.output || (current?.status === 'queued' ? 'Waiting for a free slot…' : '')} /></div>
      </section>
    </div>
    <details class="panel"><summary class="panel-h" style=${{ cursor: 'pointer' }}><h2>Script</h2></summary>
      <div class="panel-b stack">
        ${Object.keys(job.params || {}).length > 0 && html`<dl class="kv">${Object.entries(job.params).map(([k, v]) => html`<dt class="mono">$${k}</dt><dd class="mono">${v}</dd>`)}</dl>`}
        ${job.kind === 'ansible' ? html`<dl class="kv"><dt>Playbook</dt><dd class="mono">${job.body}</dd><dt>Commit</dt><dd class="mono">${job.commit_sha || '–'}</dd>
          <dt>Mode</dt><dd>${job.options?.check ? 'Dry run (--check --diff)' : 'Apply'}${job.as_root ? ', become root' : ''}</dd></dl>`
        : html`<pre class="out">${job.body}</pre><p class="small muted">Ran ${job.as_root ? 'as root' : 'as the SSH user'}.</p>`}
      </div></details>
  </div>`;
}
