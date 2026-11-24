import { html, useState, useEffect, useMemo, api, useLoad, useQuery, toast, confirmBox, Btn, Th, useSort, Empty, Field, Loading, ErrorNote, ago, when, getClock, setClock } from '../ui.js';
import { getTheme, setTheme } from '../theme.js';

export function SettingsPage() {
  const acct = useLoad(() => api('/api/account'));
  const settings = useLoad(() => api('/api/settings'));
  return html`<div class="page">
    <div class="page-h"><div><h1>Settings</h1></div></div>
    <div class="grid-2">
      <div class="stack">
        <${PasswordPanel} />
        ${acct.data && html`<${TotpPanel} enabled=${acct.data.totp} onChange=${() => acct.reload(true)} />`}
        <${ThemePanel} />
      </div>
      <div class="stack">
        ${acct.data && html`<${SessionsPanel} sessions=${acct.data.sessions} onChange=${() => acct.reload(true)} />`}
        ${settings.data && html`<${RetentionPanel} value=${settings.data} />`}
        ${settings.data && html`<section class="panel"><div class="panel-h"><h2>Encryption</h2></div><div class="panel-b"><p class="small ink2">
          ${settings.data.secret_from_env
            ? 'Keys and notification tokens are encrypted with FLEETDECK_SECRET from the environment.'
            : html`Keys and notification tokens are encrypted with <span class="mono">data/secret.key</span>, stored next to the database. Set <span class="mono">FLEETDECK_SECRET</span> to keep the key out of backups of the data folder.`}</p></div></section>`}
      </div>
    </div>
  </div>`;
}

function PasswordPanel() {
  const [v, setV] = useState({ current: '', next: '', confirm: '' });
  const [err, setErr] = useState('');
  const save = async () => {
    setErr('');
    if (v.next !== v.confirm) return setErr('New passwords do not match');
    try {
      await api('/api/account/password', { method: 'POST', body: { current: v.current, next: v.next } });
      setV({ current: '', next: '', confirm: '' });
      toast('Password changed. Other sessions were signed out.', 'ok');
    } catch (e) { setErr(e.message); }
  };
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  return html`<section class="panel"><div class="panel-h"><h2>Password</h2></div><div class="panel-b form">
    <${Field} label="Current password"><input type="password" autocomplete="current-password" value=${v.current} onInput=${set('current')} /></${Field}>
    <div class="cols"><${Field} label="New password"><input type="password" autocomplete="new-password" value=${v.next} onInput=${set('next')} /></${Field}>
      <${Field} label="Repeat new password"><input type="password" autocomplete="new-password" value=${v.confirm} onInput=${set('confirm')} /></${Field}></div>
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <div><${Btn} kind="primary" onClick=${save} disabled=${!v.current || v.next.length < 10}>Change password</${Btn}></div>
  </div></section>`;
}

function TotpPanel({ enabled, onChange }) {
  const [setup, setSetup] = useState(null);
  const [code, setCode] = useState('');
  const [pw, setPw] = useState('');
  const [err, setErr] = useState('');
  const begin = async () => { setSetup(await api('/api/account/totp/begin', { method: 'POST' })); setErr(''); };
  const enable = async () => {
    try { await api('/api/account/totp/enable', { method: 'POST', body: { code } }); setSetup(null); setCode(''); toast('Two-factor sign-in is on', 'ok'); onChange(); } catch (e) { setErr(e.message); }
  };
  const disable = async () => {
    try { await api('/api/account/totp/disable', { method: 'POST', body: { password: pw, code } }); setPw(''); setCode(''); toast('Two-factor sign-in is off'); onChange(); } catch (e) { setErr(e.message); }
  };
  return html`<section class="panel"><div class="panel-h"><h2>Two-factor sign-in</h2>${enabled ? html`<span class="chip ok">On</span>` : html`<span class="chip warn">Off</span>`}</div>
    <div class="panel-b form">
      ${enabled ? html`<p class="small ink2">Turning it off needs your password and a current code.</p>
        <div class="cols"><${Field} label="Password"><input type="password" value=${pw} onInput=${(e) => setPw(e.target.value)} /></${Field}>
          <${Field} label="Code"><input inputmode="numeric" maxlength="6" value=${code} onInput=${(e) => setCode(e.target.value)} /></${Field}></div>
        <div><${Btn} kind="danger" onClick=${disable}>Turn off two-factor sign-in</${Btn}></div>`
      : !setup ? html`<p class="small ink2">Ask for a code from an authenticator app (1Password, Aegis, Google Authenticator…) at every sign-in.</p><div><${Btn} kind="primary" onClick=${begin}>Set up two-factor sign-in</${Btn}></div>`
      : html`<div class="row top">
          <img class="qr" src=${setup.qr} width="168" height="168" alt="QR code for your authenticator app" />
          <div class="stack sm grow"><p class="small">Scan the code, or enter this secret manually:</p>
            <span class="mono wrap-any">${setup.secret.match(/.{1,4}/g).join(' ')}</span>
            <${Field} label="Code from the app"><input inputmode="numeric" maxlength="6" autofocus value=${code} onInput=${(e) => setCode(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && enable()} /></${Field}>
            <div class="row"><${Btn} kind="primary" onClick=${enable}>Turn on</${Btn}><button class="btn ghost" onClick=${() => setSetup(null)}>Cancel</button></div></div></div>`}
      ${err && html`<p class="err" role="alert">${err}</p>`}
    </div></section>`;
}

function ThemePanel() {
  const [t, setT] = useState(getTheme());
  return html`<section class="panel"><div class="panel-h"><h2>Appearance</h2></div><div class="panel-b">
    <div class="seg" role="group" aria-label="Theme">${[['system', 'Match system'], ['light', 'Light'], ['dark', 'Dark']].map(([k, l]) => html`<button aria-pressed=${t === k} onClick=${() => { setTheme(k); setT(k); }}>${l}</button>`)}</div>
    <div class="seg" role="group" aria-label="Clock">${[['', 'Clock of this browser'], ['24', '24-hour'], ['12', '12-hour']].map(([k, l]) => html`<button aria-pressed=${getClock() === k} onClick=${() => setClock(k)}>${l}</button>`)}</div>
  </div></section>`;
}

function browserName(ua = '') {
  const b = /Firefox\//.test(ua) ? 'Firefox' : /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl/.test(ua) ? 'curl' : 'Browser';
  const os = /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${b} on ${os}` : b;
}

function SessionsPanel({ sessions, onChange }) {
  const revoke = async (s) => { await api(`/api/account/sessions/${s.id}`, { method: 'DELETE' }); onChange(); };
  const others = async () => {
    if (!await confirmBox({ title: 'Sign out everywhere else?', confirm: 'Sign out other sessions', danger: true })) return;
    await api('/api/account/sessions/revoke-others', { method: 'POST' });
    onChange();
  };
  return html`<section class="panel"><div class="panel-h"><h2>Signed-in sessions</h2>${sessions.length > 1 && html`<${Btn} size="sm" class="right" onClick=${others}>Sign out others</${Btn}>`}</div>
    <table class="t"><tbody>${sessions.map((s) => html`<tr>
      <td><b>${browserName(s.ua)}</b>${s.current && html` <span class="chip ok">This session</span>`}<div class="small muted">${s.ip}, signed in ${when(s.created_at)}</div></td>
      <td class="ink2 nowrap">active ${ago(s.seen_at)}</td>
      <td class="act">${!s.current && html`<${Btn} size="sm" kind="quiet-danger" onClick=${() => revoke(s)}>Sign out</${Btn}>`}</td></tr>`)}</tbody></table></section>`;
}

const RETENTION = ['retention_metrics_days', 'retention_jobs_days', 'retention_events_days'];
function RetentionPanel({ value }) {
  const [v, setV] = useState(value);
  const [saved, setSaved] = useState(value);
  const changed = RETENTION.some((k) => v[k] !== saved[k]);
  const save = async () => { await api('/api/settings', { method: 'PUT', body: v }); setSaved(v); toast('Retention saved', 'ok'); };
  const f = (k, label, max) => html`<${Field} label=${label}><input type="number" min="1" max=${max} value=${v[k]} onInput=${(e) => setV({ ...v, [k]: Number(e.target.value) })} /></${Field}>`;
  return html`<section class="panel"><div class="panel-h"><h2>Keep data for (days)</h2></div><div class="panel-b form">
    <div class="cols">${f('retention_metrics_days', 'Metrics', 365)}${f('retention_jobs_days', 'Job output', 3650)}${f('retention_events_days', 'Audit log', 3650)}</div>
    <div><${Btn} kind=${changed ? 'primary' : ''} disabled=${!changed} onClick=${save}>Save</${Btn}></div></div></section>`;
}

// ---------- audit log ----------
// "Job #12 …" in job events links to the job.
const jobLinks = (msg) => msg.split(/(Job #\d+)/).map((part) => (/^Job #\d+$/.test(part) ? html`<a href=${`#/jobs/${part.slice(5)}`}>${part}</a>` : part));
const KINDS = [['', 'Everything'], ['audit', 'Changes'], ['security', 'Security'], ['alert', 'Alerts'], ['job', 'Jobs'], ['schedule', 'Schedules'], ['notify', 'Notifications']];

export function AuditPage() {
  const [kind, setKind] = useQuery('kind');
  const [query, setQuery] = useQuery('q');
  const [q, setQ] = useState(query);
  const [older, setOlder] = useState([]);
  useEffect(() => { const t = setTimeout(() => setQuery(q), 300); return () => clearTimeout(t); }, [q]);
  useEffect(() => setOlder([]), [kind, query]);
  const qs = new URLSearchParams(Object.entries({ kind, q: query, limit: '100' }).filter(([, x]) => x)).toString();
  const ev = useLoad(() => api(`/api/events?${qs}`), [qs], { poll: 15_000 });
  const rows = useMemo(() => [...(ev.data || []), ...older], [ev.data, older]);
  const sort = useSort(rows);
  const more = async () => setOlder([...older, ...await api(`/api/events?${qs}&before=${rows[rows.length - 1].id}`)]);
  const lvl = { error: 'bad', warn: 'warn', success: 'ok' };
  return html`<div class="page">
    <div class="page-h"><div><h1>Audit log</h1><p class="sub">Who changed what, every remote action, sign-ins and alerts.</p></div></div>
    <section class="panel">
      <div class="panel-h">
        <div class="seg hide-sm" role="group" aria-label="Type">${KINDS.map(([k, l]) => html`<button aria-pressed=${kind === k} onClick=${() => setKind(k)}>${l}</button>`)}</div>
        <select class="show-sm" value=${kind} onChange=${(e) => setKind(e.target.value)} aria-label="Type">${KINDS.map(([k, l]) => html`<option value=${k}>${l}</option>`)}</select>
        <input type="search" placeholder="Search" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Search events" class="right" />
      </div>
      ${ev.loading && !ev.data ? html`<${Loading} />` : ev.error ? html`<div class="panel-b"><${ErrorNote} error=${ev.error} retry=${ev.reload} /></div>`
        : !rows.length ? html`<${Empty} title="No events match" />`
        : html`<div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="t" desc>Time</${Th}><${Th} sort=${sort} k="kind">Type</${Th}><${Th} sort=${sort} k="message">Event</${Th}><${Th} sort=${sort} k="server_name">Server</${Th}><${Th} sort=${sort} k="ip" class="hide-sm">From</${Th}></tr></thead>
          <tbody>${sort.rows.map((e) => html`<tr>
            <td class="nowrap ink2" title=${when(e.t)}>${ago(e.t)}</td>
            <td><span class=${`chip ${lvl[e.level] || ''}`}>${KINDS.find(([k]) => k === e.kind)?.[1] || e.kind}</span></td>
            <td>${e.kind === 'job' ? jobLinks(e.message) : e.message}</td>
            <td>${e.server_id && e.server_name ? html`<a href=${`#/servers/${e.server_id}`}>${e.server_name}</a>` : ''}</td>
            <td class="mono small ink2 hide-sm">${e.ip || ''}</td></tr>`)}</tbody></table>
          ${rows.length >= 100 && html`<div class="pad"><${Btn} size="sm" onClick=${more}>Load older events</${Btn}></div>`}</div>`}
    </section>
  </div>`;
}
