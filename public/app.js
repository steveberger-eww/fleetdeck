import {
  html, render, useState, useEffect, useRef, useMemo, api, useRoute, go, Icon, Logo, ToastHost, ConfirmHost,
  Lamp, useStore, serversStore, runningStore, refreshServers, refreshRunning, Field, byAttention, attention, isMac, dismissConfirm,
} from './ui.js';
import { getTheme, setTheme } from './theme.js';
import { Overview } from './pages/overview.js';
import { ServerPage, TAB_LABEL } from './pages/server.js';
import { JobsPage, JobPage, RunHost, openRun, closeRun } from './pages/jobs.js';
import { ScriptsPage, SchedulesPage } from './pages/scripts.js';
import { AlertsPage } from './pages/alerts.js';
import { KeysPage, VariablesPage } from './pages/keys.js';
import { ReposPage } from './pages/repos.js';
import { SettingsPage, SsoPage, AuditPage } from './pages/settings.js';

// ---------- sign in / first-run setup ----------
function AuthFrame({ title, sub, children }) {
  return html`<main class="auth"><div class="panel">
    <div class="brand"><${Logo} size=${26} />Fleetdeck</div>
    <form onSubmit=${(e) => e.preventDefault()}>
      <div><h1>${title}</h1>${sub && html`<p class="muted small">${sub}</p>`}</div>
      ${children}
    </form></div></main>`;
}

function Login({ auth, onDone }) {
  const [pw, setPw] = useState('');
  const [code, setCode] = useState('');
  const [needCode, setNeedCode] = useState(false);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true); setErr('');
    try {
      const r = await api('/api/auth/login', { method: 'POST', body: { password: pw, code: needCode ? code : undefined } });
      if (r.totp_required) setNeedCode(true); else onDone();
    } catch (e) { setErr(e.message); } finally { setBusy(false); }
  };
  const sso = auth.sso && !needCode && html`<a class="btn primary" href="/api/auth/oidc/start">Sign in with ${auth.sso}</a>`;
  if (!auth.password) return html`<${AuthFrame} title="Sign in">${sso}</${AuthFrame}>`;
  return html`<${AuthFrame} title="Sign in" sub=${needCode ? 'Enter the 6-digit code from your authenticator app.' : null}>
    ${sso && html`${sso}<div class="or">or use your password</div>`}
    ${!needCode && html`<${Field} label="Password"><input type="password" autocomplete="current-password" autofocus value=${pw} onInput=${(e) => setPw(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && submit()} /></${Field}>`}
    ${needCode && html`<${Field} label="Authentication code"><input inputmode="numeric" autocomplete="one-time-code" autofocus maxlength="6" value=${code} onInput=${(e) => setCode(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && submit()} /></${Field}>`}
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <button class=${`btn ${sso ? '' : 'primary'}`} type="submit" disabled=${busy} onClick=${submit}>${busy ? 'Signing in…' : 'Sign in'}</button>
  </${AuthFrame}>`;
}

const MIN_PW = 10;
function Setup({ onDone }) {
  const [f, setF] = useState({ token: '', password: '', confirm: '' });
  const [err, setErr] = useState('');
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const short = f.password.length > 0 && f.password.length < MIN_PW;
  const mismatch = f.confirm.length > 0 && f.confirm !== f.password;
  const submit = async () => {
    if (f.password !== f.confirm) return setErr('Passwords do not match');
    try { await api('/api/auth/setup', { method: 'POST', body: { token: f.token, password: f.password } }); onDone(); } catch (e) { setErr(e.message); }
  };
  return html`<${AuthFrame} title="Set up Fleetdeck" sub="Choose the password that protects every server you add. The setup token is printed in the server console (or docker logs).">
    <${Field} label="Setup token"><input class="mono" autofocus value=${f.token} onInput=${set('token')} /></${Field}>
    <label class="field"><span>Password</span><input type="password" autocomplete="new-password" value=${f.password} onInput=${set('password')} />
      <span class=${`hint ${short ? 'err' : ''}`}>${short ? `${MIN_PW - f.password.length} more character${MIN_PW - f.password.length === 1 ? '' : 's'}. A passphrase works well.` : `At least ${MIN_PW} characters. A passphrase works well.`}</span></label>
    <label class="field"><span>Repeat password</span><input type="password" autocomplete="new-password" value=${f.confirm} onInput=${set('confirm')} onKeyDown=${(e) => e.key === 'Enter' && submit()} />
      ${mismatch && html`<span class="hint err">Doesn't match yet.</span>`}</label>
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <button class="btn primary" type="submit" disabled=${f.password.length < MIN_PW || mismatch || !f.token} onClick=${submit}>Create password and sign in</button>
  </${AuthFrame}>`;
}

// ---------- navigation: one source for the sidebar and the palette ----------
const NAV = [
  [null, [['Overview', '/', 'overview'], ['Alerts', '/alerts', 'bell']]],
  ['Automation', [['Jobs', '/jobs', 'jobs'], ['Scripts', '/scripts', 'scripts'], ['Schedules', '/schedules', 'clock'], ['Repositories', '/repos', 'git'], ['Variables', '/variables', 'vars']]],
  ['Access', [['SSH keys', '/keys', 'key']]],
  ['System', [['Audit log', '/audit', 'audit'], ['Settings', '/settings', 'gear']]],
];
const PAGES = NAV.flatMap(([, items]) => items);

// ---------- command palette (⌘K / Ctrl K) ----------
function Palette({ onClose }) {
  const servers = useStore(serversStore) || [];
  const [q, setQ] = useState('');
  const [i, setI] = useState(0);
  const ref = useRef();
  useEffect(() => {
    const d = ref.current;
    d.showModal();
    const cancel = (e) => { e.preventDefault(); onClose(); };
    d.addEventListener('cancel', cancel);
    return () => d.removeEventListener('cancel', cancel);
  }, []);
  const items = useMemo(() => {
    const n = q.toLowerCase().trim();
    const sorted = servers.slice().sort(byAttention);
    const all = [
      ...sorted.map((s) => ({ group: 'Servers', label: s.name, hint: s.host, status: s.status, run: () => go(`/servers/${s.id}`) })),
      { group: 'Actions', label: 'Run a script…', icon: 'play', run: () => openRun() },
      { group: 'Actions', label: 'Add server…', icon: 'plus', run: () => go('/?add=1') },
      // per-server actions only once you type, so the empty list stays short
      ...(n ? sorted.flatMap((s) => [
        { group: 'Actions', label: `Terminal: ${s.name}`, hint: s.host, icon: 'term', run: () => go(`/servers/${s.id}/terminal`) },
        { group: 'Actions', label: `Logs: ${s.name}`, hint: s.host, icon: 'audit', run: () => go(`/servers/${s.id}/logs`) },
        { group: 'Actions', label: `Update: ${s.name}`, hint: s.host, icon: 'refresh', run: () => openRun({ targets: { servers: [s.id] }, scriptId: 'update' }) },
      ]) : []),
      ...PAGES.map(([label, path, icon]) => ({ group: 'Pages', label, icon, run: () => go(path) })),
    ];
    return (n ? all.filter((x) => `${x.label} ${x.hint || ''}`.toLowerCase().includes(n)) : all).slice(0, 40);
  }, [q, servers]);
  const pick = (x) => { onClose(); x?.run(); };
  const key = (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setI((v) => Math.min(v + 1, items.length - 1)); }
    if (e.key === 'ArrowUp') { e.preventDefault(); setI((v) => Math.max(v - 1, 0)); }
    if (e.key === 'Enter') pick(items[i]);
  };
  useEffect(() => document.getElementById(`pal-${i}`)?.scrollIntoView({ block: 'nearest' }), [i]);
  return html`<dialog class="palette" ref=${ref} aria-label="Search" onClick=${(e) => e.target === e.currentTarget && onClose()}>
    <input autofocus placeholder="Jump to a server, page or action…" value=${q} onInput=${(e) => { setQ(e.target.value); setI(0); }} onKeyDown=${key}
      role="combobox" aria-expanded="true" aria-controls="pal-list" aria-activedescendant=${items.length ? `pal-${i}` : undefined} aria-label="Search" />
    <ul role="listbox" id="pal-list">${items.map((x, n) => html`
      ${(n === 0 || items[n - 1].group !== x.group) && html`<li class="grp" role="presentation">${x.group}</li>`}
      <li role="option" id=${`pal-${n}`} aria-selected=${n === i} onMouseEnter=${() => setI(n)} onClick=${() => pick(x)}>
        ${x.status ? html`<${Lamp} status=${x.status} label />` : html`<${Icon} name=${x.icon} />`}<span class="grow trunc">${x.label}</span>${x.hint && html`<span class="mono muted small">${x.hint}</span>`}</li>`)}
      ${!items.length && html`<li class="muted">Nothing matches “${q}”</li>`}</ul>
  </dialog>`;
}

// ---------- shell ----------
function ThemeButton() {
  const [t, setT] = useState(getTheme());
  const next = { system: 'light', light: 'dark', dark: 'system' }[t];
  return html`<button class="btn ghost icon sm" title=${`Theme: ${t} (switch to ${next})`} aria-label=${`Theme: ${t}`} onClick=${() => { setTheme(next); setT(next); }}><${Icon} name=${t === 'dark' ? 'moon' : 'sun'} /></button>`;
}

const SHOW_ALL_UNDER = 12;
function SideServers({ servers, current }) {
  const [all, setAll] = useState(false);
  if (!servers) return null;
  if (!servers.length) return html`<div class="side-servers"><span class="muted small side-more">None yet</span></div>`;
  const sorted = servers.slice().sort(byAttention);
  // Long lists fold the quiet ones (healthy, paused) away; the open server always stays visible.
  const fold = !all && sorted.length > SHOW_ALL_UNDER;
  const shown = fold ? sorted.filter((s) => attention(s) < 4 || s.id === current) : sorted;
  const hidden = sorted.length - shown.length;
  return html`<div class="side-servers">${shown.map((s) => html`<a href=${`#/servers/${s.id}`} aria-current=${current === s.id ? 'page' : undefined} title=${s.host}>
      <${Lamp} status=${s.status} label /><span class="trunc">${s.name}</span>${s.alerts > 0 && html`<span class="count soft" aria-label=${`${s.alerts} alerts`}>${s.alerts}</span>`}</a>`)}
    ${hidden > 0 && html`<button class="side-more" onClick=${() => setAll(true)}><${Icon} name="chev" size=${14} />Show ${hidden} healthy</button>`}
    ${all && sorted.length > SHOW_ALL_UNDER && html`<button class="side-more" onClick=${() => setAll(false)}>Show fewer</button>`}</div>`;
}

function Crumbs({ parts, servers }) {
  if (parts[0] === 'servers' && parts[1]) {
    const s = servers?.find((x) => x.id === parts[1]);
    return html`<nav class="crumbs" aria-label="Breadcrumb"><a href="#/">Fleet</a><span>/</span>
      ${parts[2] ? html`<a href=${`#/servers/${parts[1]}`}>${s?.name || 'Server'}</a><span>/</span><b>${TAB_LABEL[parts[2]] || parts[2]}</b>` : html`<b>${s?.name || 'Server'}</b>`}</nav>`;
  }
  if (parts[0] === 'settings' && parts[1] === 'sso') return html`<nav class="crumbs" aria-label="Breadcrumb"><a href="#/settings">Settings</a><span>/</span><b>Single sign-on</b></nav>`;
  if (parts[0] === 'jobs' && parts[1]) return html`<nav class="crumbs" aria-label="Breadcrumb"><a href="#/jobs">Jobs</a><span>/</span><b>#${parts[1]}</b></nav>`;
  return html`<span class="crumbs" />`;
}

function Shell() {
  const { path } = useRoute();
  const servers = useStore(serversStore);
  const running = useStore(runningStore);
  const [open, setOpen] = useState(false);
  const [palette, setPalette] = useState(false);
  useEffect(() => {
    const refresh = () => { refreshServers(); refreshRunning(); };
    refresh();
    const t = setInterval(() => document.visibilityState === 'visible' && refresh(), 15_000);
    const key = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette((v) => !v); }
    };
    addEventListener('keydown', key);
    return () => { clearInterval(t); removeEventListener('keydown', key); };
  }, []);
  useEffect(() => { setOpen(false); closeRun(); dismissConfirm(); scrollTo(0, 0); }, [path]); // path only: typing a filter must not scroll

  const parts = path.split('/').filter(Boolean);
  const section = parts[0] || '';
  const firing = (servers || []).reduce((n, s) => n + s.alerts, 0);
  let page;
  if (!section) page = html`<${Overview} />`;
  else if (section === 'servers' && parts[1]) page = html`<${ServerPage} key=${parts[1]} id=${parts[1]} tab=${parts[2] || 'overview'} />`;
  else if (section === 'jobs' && parts[1]) page = html`<${JobPage} key=${parts[1]} id=${parts[1]} />`;
  else if (section === 'jobs') page = html`<${JobsPage} />`;
  else if (section === 'scripts') page = html`<${ScriptsPage} />`;
  else if (section === 'schedules') page = html`<${SchedulesPage} />`;
  else if (section === 'alerts') page = html`<${AlertsPage} />`;
  else if (section === 'keys') page = html`<${KeysPage} />`;
  else if (section === 'variables') page = html`<${VariablesPage} />`;
  else if (section === 'repos') page = html`<${ReposPage} />`;
  else if (section === 'audit') page = html`<${AuditPage} />`;
  else if (section === 'settings' && parts[1] === 'sso') page = html`<${SsoPage} />`;
  else if (section === 'settings') page = html`<${SettingsPage} />`;
  else page = html`<div class="page"><h1>Page not found</h1><a href="#/">Back to the fleet</a></div>`;

  const badge = (label) => {
    if (label === 'Alerts' && firing > 0) return html`<span class="count" aria-label=${`${firing} firing`}>${firing}</span>`;
    if (label === 'Jobs' && running > 0) return html`<span class="count info" aria-label=${`${running} running`}>${running}</span>`;
    return null;
  };
  const signOut = async () => { await api('/api/auth/logout', { method: 'POST' }); location.reload(); };
  return html`<div class=${`app ${open ? 'open' : ''}`}>
    <div class="scrim" onClick=${() => setOpen(false)} />
    <nav class="side" aria-label="Main">
      <a class="brand" href="#/"><${Logo} />Fleetdeck</a>
      ${NAV.map(([group, items]) => html`${group && html`<div class="side-h">${group}</div>`}
        <div class="nav">${items.map(([label, p, icon]) => html`<a href=${`#${p}`} aria-current=${(p === '/' ? !section : `/${section}` === p) ? 'page' : undefined}>
          <${Icon} name=${icon} />${label}${badge(label)}</a>`)}</div>`)}
      <div class="side-h">Servers${servers?.length > 0 && html`<span class="n">${servers.length}</span>`}</div>
      <${SideServers} servers=${servers} current=${section === 'servers' ? parts[1] : null} />
      <div class="side-foot"><${ThemeButton} /><button class="btn ghost sm right" onClick=${signOut}><${Icon} name="out" />Sign out</button></div>
    </nav>
    <div class="main">
      <header class="top">
        <button class="btn ghost icon menu-btn" aria-label="Open menu" onClick=${() => setOpen(true)}><${Icon} name="menu" /></button>
        <${Crumbs} parts=${parts} servers=${servers} />
        <button class="search-btn right" onClick=${() => setPalette(true)}><${Icon} name="search" /><span>Search servers, pages, actions</span><span class="kbd">${isMac ? '⌘K' : 'Ctrl K'}</span></button>
        <button class="btn primary" onClick=${() => openRun()} aria-label="Run script"><${Icon} name="play" /><span class="hide-sm">Run script</span></button>
      </header>
      <main>${page}</main>
    </div>
    ${palette && html`<${Palette} onClose=${() => setPalette(false)} />`}
    <${RunHost} /><${ConfirmHost} /><${ToastHost} />
  </div>`;
}

function App() {
  const [auth, setAuth] = useState(null);
  const check = () => api('/api/auth/state').then(setAuth).catch(() => setAuth({ error: true }));
  useEffect(() => {
    check();
    const out = () => setAuth((a) => ({ ...a, authed: false }));
    addEventListener('fd-signed-out', out);
    return () => removeEventListener('fd-signed-out', out);
  }, []);
  if (!auth) return null;
  if (auth.error) {
    return html`<main class="auth"><div class="panel"><div class="empty"><h3>Fleetdeck's server isn't reachable</h3><p>Check that it is running, then try again.</p>
      <button class="btn primary" onClick=${() => { setAuth(null); check(); }}>Try again</button></div></div></main>`;
  }
  if (auth.setup) return html`<${Setup} onDone=${check} />`;
  if (!auth.authed) return html`<${Login} auth=${auth} onDone=${check} />`;
  return html`<${Shell} />`;
}

render(html`<${App} />`, document.getElementById('root'));
