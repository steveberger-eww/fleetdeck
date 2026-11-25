import {
  html, useState, useEffect, useMemo, api, go, store, useStore, useLoad, useQuery, serversStore, toast, confirmBox,
  Btn, Th, useSort, Lamp, JobChip, Empty, Field, ParamInput, Modal, Loading, ErrorNote, Output, ago, dur, when, plural, can,
} from '../ui.js';

// ---------- the one guard for destructive runs ----------
// Every way of starting a job (Run dialog, re-run, schedule "Run now", fleet update) goes through here.
const DANGER = new Set(['reboot', 'shutdown']);
export async function confirmRun({ scriptId, name, count, names = [] }) {
  const danger = DANGER.has(scriptId);
  const fleetUpdate = ['update', 'upgrade-packages'].includes(scriptId) && count > 1;
  if (!danger && !fleetUpdate) return true;
  const where = count === 1 && names[0] ? names[0] : plural(count, 'server');
  return confirmBox({
    title: `${name} on ${where}?`,
    body: danger ? 'Everything running there will be unavailable until it is back up.' : 'Installs the pending package upgrades. Services can restart while it runs.',
    confirm: danger ? name : 'Install updates', danger,
    typeToConfirm: count > 1 ? plural(count, 'server') : danger ? names[0] : undefined,
  });
}

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
    <span>Run on <span class="muted">(${plural(n, 'server')})</span></span>
    <label class="check"><input type="checkbox" checked=${t.all} onChange=${(e) => onChange({ ...t, all: e.target.checked })} />All servers, including ones added later</label>
    ${!t.all && html`
      ${tags.length > 0 && html`<div class="row"><span class="small muted">Tags</span>${tags.map((x) => html`<button type="button" class="pill" aria-pressed=${t.tags.includes(x)} onClick=${() => onChange({ ...t, tags: flip(t.tags, x) })}>${x}</button>`)}</div>`}
      <div class="panel pick">
        ${servers.map((s) => html`<label class="check list">
          <input type="checkbox" checked=${t.servers.includes(s.id) || s.tags.some((x) => t.tags.includes(x))} disabled=${s.tags.some((x) => t.tags.includes(x))}
            onChange=${() => onChange({ ...t, servers: flip(t.servers, s.id) })} />
          <${Lamp} status=${s.status} label />${s.name}<span class="mono muted small">${s.host}</span></label>`)}
        ${!servers.length && html`<span class="muted small">No servers yet.</span>`}
      </div>`}
  </div>`;
}

// ---------- run dialog ----------
const runStore = store(null);
export const openRun = (opts = {}) => runStore.set(opts);
export const closeRun = () => runStore.set(null);
export function RunHost() {
  const o = useStore(runStore);
  return o && html`<${RunDialog} ...${o} onClose=${() => runStore.set(null)} />`;
}

const lastScript = () => { try { return sessionStorage.getItem('fd-last-script') || 'update'; } catch { return 'update'; } };

function RunDialog({ targets: initTargets, scriptId: initScript, onClose }) {
  const scripts = useLoad(() => api('/api/scripts'));
  const [mode, setMode] = useState('script');
  const [scriptId, setScriptId] = useState(initScript || lastScript());
  const [params, setParams] = useState({});
  const [command, setCommand] = useState('');
  const [asRoot, setAsRoot] = useState(true);
  const [check, setCheck] = useState(false);
  const [targets, setTargets] = useState(initTargets || { servers: [], tags: [] });
  const [err, setErr] = useState('');
  const servers = useStore(serversStore) || [];
  const script = scripts.data?.find((s) => s.id === scriptId);
  useEffect(() => { if (script) setParams(Object.fromEntries(script.params.map((p) => [p.name, p.default]))); }, [script?.id]);
  const picked = resolveTargets(targets, servers);
  const n = picked.length;

  const run = async () => {
    setErr('');
    if (mode === 'script' && !await confirmRun({ scriptId, name: script?.name || scriptId, count: n, names: picked.map((s) => s.name) })) return;
    try {
      const body = mode === 'script' ? { script_id: scriptId, params, targets, options: { check } } : { body: command, as_root: asRoot, targets };
      const { id } = await api('/api/jobs', { method: 'POST', body });
      if (mode === 'script') try { sessionStorage.setItem('fd-last-script', scriptId); } catch { /* private mode */ }
      onClose();
      go(`/jobs/${id}`);
    } catch (e) { setErr(e.message); }
  };

  const where = n === 0 ? 'Pick at least one server.' : `${picked.slice(0, 3).map((s) => s.name).join(', ')}${n > 3 ? ` and ${n - 3} more` : ''}`;
  return html`<${Modal} title="Run a script" wide onClose=${onClose}
    footer=${html`<span class="small muted grow trunc" title=${picked.map((s) => s.name).join(', ')}>${where}</span>
      <button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" icon="play" disabled=${!n} onClick=${run}>Run on ${plural(n, 'server')}</${Btn}>`}>
    <div class="seg" role="group" aria-label="What to run">
      <button aria-pressed=${mode === 'script'} onClick=${() => setMode('script')}>Saved script</button>
      <button aria-pressed=${mode === 'command'} onClick=${() => setMode('command')}>One-off command</button>
    </div>
    ${mode === 'script' ? (scripts.loading ? html`<${Loading} />` : html`
      <${Field} label="Script" hint=${script?.description}>
        <select value=${scriptId} onChange=${(e) => setScriptId(e.target.value)}>${(scripts.data || []).map((s) => html`<option value=${s.id}>${s.name}</option>`)}</select>
      </${Field}>
      ${script?.params.map((p) => html`<${ParamInput} p=${p} value=${params[p.name] ?? ''} onChange=${(x) => setParams({ ...params, [p.name]: x })}
        hint=${script.kind === 'ansible' ? html`Extra variable <span class="mono">{{ ${p.name} }}</span>` : html`Available as <span class="mono">$${p.name}</span>`} />`)}
      ${script?.kind === 'ansible' ? html`<p class="small ink2">Runs <span class="mono">ansible-playbook ${script.playbook}</span> from the latest commit of its repository.</p>
        <label class="check"><input type="checkbox" checked=${check} onChange=${(e) => setCheck(e.target.checked)} />Dry run: show what would change (--check --diff)</label>`
      : script && html`<details><summary class="small ink2">Show script</summary><pre class="out short">${script.body}</pre></details>`}`)
    : html`
      <${Field} label="Command" hint="Runs with sh. Multiple lines are fine."><textarea autofocus value=${command} onInput=${(e) => setCommand(e.target.value)} placeholder="df -h /" /></${Field}>
      <label class="check"><input type="checkbox" checked=${asRoot} onChange=${(e) => setAsRoot(e.target.checked)} />Run as root (via sudo when the SSH user isn't root)</label>`}
    <${TargetPicker} value=${targets} onChange=${setTargets} />
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}

// ---------- job list ----------
const STATUS_FILTERS = [['', 'All'], ['running', 'Running'], ['failed', 'Failed']];

export function JobsTable({ serverId, limit = 30, status = '' }) {
  const [extra, setExtra] = useState([]);
  const qs = `limit=${limit}${serverId ? `&server=${serverId}` : ''}${status ? `&status=${status}` : ''}`;
  const jobs = useLoad(() => api(`/api/jobs?${qs}`), [qs], { poll: 5000 });
  useEffect(() => setExtra([]), [qs]);
  const rows = useMemo(() => [...(jobs.data || []), ...extra.filter((e) => !(jobs.data || []).some((j) => j.id === e.id))], [jobs.data, extra]);
  const sort = useSort(rows);
  const more = async () => setExtra([...extra, ...await api(`/api/jobs?${qs}&before=${rows[rows.length - 1].id}`)]);
  if (jobs.loading && !jobs.data) return html`<${Loading} />`;
  if (jobs.error) return html`<div class="panel-b"><${ErrorNote} error=${jobs.error} retry=${jobs.reload} /></div>`;
  if (!rows.length) {
    return status ? html`<${Empty} title=${`No ${status} jobs`} />`
      : html`<${Empty} title="No jobs yet" action=${can('operator') && html`<${Btn} icon="play" onClick=${() => openRun(serverId ? { targets: { servers: [serverId] } } : {})}>Run a script</${Btn}>`}>Every script you run is kept here with its full output.</${Empty}>`;
  }
  return html`<div class="table-wrap"><table class="t">
    <thead><tr><${Th} sort=${sort} k="name">Job</${Th}><${Th} sort=${sort} k="status">Status</${Th}><${Th} sort=${sort} k="servers" get=${(j) => j.runs.length} desc>Servers</${Th}><${Th} sort=${sort} k="actor" class="hide-sm">Started by</${Th}><${Th} sort=${sort} k="created_at" desc>Started</${Th}><${Th} sort=${sort} k="took" get=${(j) => j.finished_at && j.finished_at - j.created_at} class="num hide-sm" desc>Took</${Th}></tr></thead>
    <tbody>${sort.rows.map((j) => {
      const runs = j.runs.filter((r) => r.server_id !== '__ansible__');
      const failed = runs.filter((r) => ['failed', 'interrupted'].includes(r.status)).length;
      return html`<tr class="click" onClick=${(e) => !e.target.closest('a') && go(`/jobs/${j.id}`)}>
        <td><a class="rowlink" href=${`#/jobs/${j.id}`}><span class="muted">#${j.id}</span> ${j.name}</a>${j.kind === 'ansible' && html` <span class="tag">ansible</span>`}</td>
        <td><${JobChip} status=${j.status} /></td>
        <td class="trunc max-cell">${runs.length <= 3 ? runs.map((r) => r.server_name).join(', ') : plural(runs.length, 'server')}${failed ? html` <span class="chip bad">${failed} failed</span>` : ''}</td>
        <td class="ink2 hide-sm">${j.actor}</td><td class="ink2 nowrap" title=${when(j.created_at)}>${ago(j.created_at)}</td>
        <td class="num ink2 hide-sm">${j.finished_at ? dur(j.finished_at - j.created_at) : '…'}</td></tr>`;
    })}</tbody></table>
    ${rows.length >= limit && rows.length % limit === 0 && html`<div class="pad"><${Btn} size="sm" onClick=${more}>Load older jobs</${Btn}></div>`}
  </div>`;
}

export function JobsPage() {
  const [status, setStatus] = useQuery('status');
  return html`<div class="page">
    <div class="page-h"><div><h1>Jobs</h1><p class="sub">Every script run, with per-server output. Kept for 90 days by default.</p></div></div>
    <section class="panel">
      <div class="panel-h"><div class="seg" role="group" aria-label="Status">${STATUS_FILTERS.map(([k, l]) => html`<button aria-pressed=${status === k} onClick=${() => setStatus(k)}>${l}</button>`)}</div></div>
      <${JobsTable} limit=${40} status=${status} />
    </section>
  </div>`;
}

// ---------- job detail with live output ----------
function useNow(on) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { if (!on) return; const t = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(t); }, [on]);
  return now;
}

export function JobPage({ id }) {
  const [job, setJob] = useState(null);
  const [error, setError] = useState('');
  const [sel, setSel] = useQuery('host');
  const servers = useStore(serversStore) || [];
  useEffect(() => {
    const es = new EventSource(`/api/jobs/${id}/stream`);
    const patch = (server, f) => setJob((j) => j && ({ ...j, runs: j.runs.map((r) => (r.server_id === server ? f(r) : r)) }));
    es.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.type === 'snapshot') setJob(m.job);
      else if (m.type === 'data') patch(m.server, (r) => ({ ...r, output: r.output + m.data }));
      else if (m.type === 'run') patch(m.server, (r) => ({ ...r, status: m.status, exit_code: m.code ?? r.exit_code }));
      else if (m.type === 'job') {
        setJob((j) => ({ ...j, status: m.status, finished_at: Date.now() }));
        es.close();
        setTimeout(() => api(`/api/jobs/${id}`).then(setJob, () => {}), 1000); // a chained script starts right after
      }
    };
    es.onerror = () => { es.close(); api(`/api/jobs/${id}`).then(setJob, (e) => setError(e.message)); };
    return () => es.close();
  }, [id]);
  const now = useNow(job?.status === 'running');

  const runs = (job?.runs || []).slice().sort((a, b) => (b.server_id === '__ansible__') - (a.server_id === '__ansible__'));
  const hosts = runs.filter((r) => r.server_id !== '__ansible__');
  const current = runs.find((r) => r.server_id === sel) || (job?.kind === 'ansible' ? runs[0] : runs.find((r) => r.status === 'failed') || runs.find((r) => r.status === 'running') || runs[0]);
  const failed = hosts.filter((r) => !['success', 'skipped', 'running', 'queued'].includes(r.status));
  if (error) return html`<div class="page"><${ErrorNote} error=${error} /></div>`;
  if (!job) return html`<div class="page"><${Loading} /></div>`;

  const counts = {};
  for (const r of hosts) counts[r.status] = (counts[r.status] || 0) + 1;
  const rerun = async (failedOnly) => {
    const list = failedOnly ? failed : hosts;
    if (job.script_id && !await confirmRun({ scriptId: job.script_id, name: job.name, count: list.length, names: list.map((r) => r.server_name) })) return;
    const r = await api(`/api/jobs/${id}/rerun`, { method: 'POST', body: { failed_only: failedOnly } });
    go(`/jobs/${r.id}`);
  };
  const cancel = async () => {
    if (!await confirmBox({ title: 'Cancel this job?', body: 'Output stops immediately. A command already running on a server may still finish there.', confirm: 'Cancel job', danger: true })) return;
    await api(`/api/jobs/${id}/cancel`, { method: 'POST' });
    toast('Job cancelled');
  };
  const took = (job.finished_at || now) - job.created_at;
  const SUMMARY = [['success', 'ok', 'succeeded'], ['failed', 'bad', 'failed'], ['interrupted', 'warn', 'interrupted'], ['cancelled', 'warn', 'cancelled'], ['running', 'info', 'running'], ['queued', '', 'queued'], ['skipped', '', 'skipped']];
  return html`<div class="page">
    <div class="page-h">
      <div><div class="row"><h1>${job.name}</h1><${JobChip} status=${job.status} /></div>
        <p class="sub">Job #${job.id}, started by ${job.parent_job ? html`<a href=${`#/jobs/${job.parent_job}`}>#${job.parent_job}</a>` : job.actor} ${ago(job.created_at)}, ${job.finished_at ? 'took' : 'running for'} ${dur(took)}${job.options?.check ? '. Dry run, nothing was changed' : ''}${job.next_job && html` · Then ran <a href=${`#/jobs/${job.next_job}`}>#${job.next_job}</a>`}</p></div>
      ${can('operator') && html`<div class="row right">
        ${job.status === 'running' && html`<${Btn} kind="danger" icon="stop" onClick=${cancel}>Cancel</${Btn}>`}
        ${job.status !== 'running' && failed.length > 0 && failed.length < hosts.length && html`<${Btn} onClick=${() => rerun(true)}>Re-run failed</${Btn}>`}
        ${job.status !== 'running' && html`<${Btn} icon="refresh" onClick=${() => rerun(false)}>Re-run</${Btn}>`}
      </div>`}
    </div>
    ${hosts.length > 1 && html`<div class="row">${SUMMARY.filter(([k]) => counts[k]).map(([k, cls, l]) => html`<span class=${`chip ${cls}`}>${counts[k]} ${l}</span>`)}</div>`}
    <div class="job-grid">
      <div class="panel"><div class="hostlist">${runs.map((r) => html`<button aria-pressed=${r === current} onClick=${() => setSel(r.server_id)}>
        <span class=${`grow trunc ${r.server_id === '__ansible__' ? 'mono' : ''}`}>${r.server_name}</span><${JobChip} status=${r.status} /></button>`)}</div></div>
      <section class="panel">
        <div class="panel-h"><h2>${current?.server_name}</h2>
          ${current?.exit_code != null && html`<span class="small muted">exit code ${current.exit_code}</span>`}
          ${current?.server_id !== '__ansible__' && servers.some((s) => s.id === current?.server_id) && html`<a class="right small" href=${`#/servers/${current.server_id}`}>Open server</a>`}</div>
        <div class="panel-b"><${Output} name=${`job-${job.id}-${current?.server_name || ''}`} text=${current?.output || (current?.status === 'queued' ? 'Waiting for a free slot…' : '')} /></div>
      </section>
    </div>
    <details class="panel"><summary class="panel-h"><h2>Script</h2></summary>
      <div class="panel-b stack">
        ${Object.keys(job.params || {}).length > 0 && html`<dl class="kv">${Object.entries(job.params).map(([k, v]) => html`<dt class="mono">$${k}</dt><dd class="mono">${v}</dd>`)}</dl>`}
        ${job.kind === 'ansible' ? html`<dl class="kv"><dt>Playbook</dt><dd class="mono">${job.body}</dd><dt>Commit</dt><dd class="mono">${job.commit_sha || '–'}</dd>
          <dt>Mode</dt><dd>${job.options?.check ? 'Dry run (--check --diff)' : 'Apply'}${job.as_root ? ', become root' : ''}</dd></dl>`
        : html`<pre class="out">${job.body}</pre><p class="small muted">Ran ${job.as_root ? 'as root' : 'as the SSH user'}.</p>`}
      </div></details>
  </div>`;
}
