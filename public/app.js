import {
  html, render, useState, useEffect, useRef, useMemo, api, useRoute, go, Icon, Logo, ToastHost, ConfirmHost,
  Lamp, useStore, serversStore, refreshServers, Field,
} from './ui.js';
import { getTheme, setTheme } from './theme.js';
import { Overview } from './pages/overview.js';
import { ServerPage } from './pages/server.js';
import { JobsPage, JobPage, RunHost, openRun } from './pages/jobs.js';
import { ScriptsPage, SchedulesPage } from './pages/scripts.js';
import { AlertsPage } from './pages/alerts.js';
import { KeysPage } from './pages/keys.js';
import { ReposPage } from './pages/repos.js';
import { SettingsPage, AuditPage } from './pages/settings.js';

// ---------- sign in / first-run setup ----------
function AuthFrame({ title, sub, children }) {
  return html`<main class="auth"><div class="panel">
    <div class="brand"><${Logo} size=${26} />Fleetdeck</div>
    <form onSubmit=${(e) => e.preventDefault()} style=${{ paddingTop: '6px' }}>
      <div><h1 style=${{ fontSize: '19px' }}>${title}</h1>${sub && html`<p class="muted small" style=${{ marginTop: '4px' }}>${sub}</p>`}</div>
      ${children}
    </form></div></main>`;
}

function Login({ onDone }) {
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
  return html`<${AuthFrame} title="Sign in" sub=${needCode ? 'Enter the 6-digit code from your authenticator app.' : null}>
    ${!needCode && html`<${Field} label="Password"><input type="password" autocomplete="current-password" autofocus value=${pw} onInput=${(e) => setPw(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && submit()} /></${Field}>`}
    ${needCode && html`<${Field} label="Authentication code"><input inputmode="numeric" autocomplete="one-time-code" autofocus maxlength="6" value=${code} onInput=${(e) => setCode(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && submit()} /></${Field}>`}
    ${err && html`<p class="err">${err}</p>`}
    <button class="btn primary" type="submit" disabled=${busy} onClick=${submit}>${busy ? 'Signing in…' : 'Sign in'}</button>
  </${AuthFrame}>`;
}

function Setup({ onDone }) {
  const [f, setF] = useState({ token: '', password: '', confirm: '' });
  const [err, setErr] = useState('');
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const submit = async () => {
    if (f.password !== f.confirm) return setErr('Passwords do not match');
    try { await api('/api/auth/setup', { method: 'POST', body: { token: f.token, password: f.password } }); onDone(); } catch (e) { setErr(e.message); }
  };
  return html`<${AuthFrame} title="Set up Fleetdeck" sub="Choose the password that protects every server you add. The setup token is printed in the server console (or docker logs).">
    <${Field} label="Setup token"><input class="mono" autofocus value=${f.token} onInput=${set('token')} /></${Field}>
    <${Field} label="Password" hint="At least 10 characters. A passphrase works well."><input type="password" autocomplete="new-password" value=${f.password} onInput=${set('password')} /></${Field}>
    <${Field} label="Repeat password"><input type="password" autocomplete="new-password" value=${f.confirm} onInput=${set('confirm')} onKeyDown=${(e) => e.key === 'Enter' && submit()} /></${Field}>
    ${err && html`<p class="err">${err}</p>`}
    <button class="btn primary" type="submit" onClick=${submit}>Create password and sign in</button>
  </${AuthFrame}>`;
}

// ---------- command palette (⌘K) ----------
const PAGES = [
  ['Fleet overview', '/', 'overview'], ['Jobs', '/jobs', 'jobs'], ['Scripts', '/scripts', 'scripts'], ['Schedules', '/schedules', 'clock'],
  ['Alerts', '/alerts', 'bell'], ['Repositories', '/repos', 'git'], ['SSH keys', '/keys', 'key'], ['Audit log', '/audit', 'audit'], ['Settings', '/settings', 'gear'],
];

function Palette({ onClose }) {
  const servers = useStore(serversStore) || [];
  const [q, setQ] = useState('');
  const [i, setI] = useState(0);
  const items = useMemo(() => {
    const all = [
      ...servers.map((s) => ({ label: s.name, hint: s.host, status: s.status, run: () => go(`/servers/${s.id}`) })),
      ...servers.map((s) => ({ label: `Terminal: ${s.name}`, hint: s.host, icon: 'term', run: () => go(`/servers/${s.id}/terminal`) })),
      { label: 'Run a script…', icon: 'play', run: () => openRun() },
      ...PAGES.map(([label, path, icon]) => ({ label, icon, run: () => go(path) })),
    ];
    const n = q.toLowerCase().trim();
    return (n ? all.filter((x) => `${x.label} ${x.hint || ''}`.toLowerCase().includes(n)) : all.filter((x) => !x.label.startsWith('Terminal:'))).slice(0, 30);
  }, [q, servers]);
  const pick = (x) => { onClose(); x?.run(); };
  const key = (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setI((v) => Math.min(v + 1, items.length - 1)); }
    if (e.key === 'ArrowUp') { e.preventDefault(); setI((v) => Math.max(v - 1, 0)); }
    if (e.key === 'Enter') pick(items[i]);
    if (e.key === 'Escape') onClose();
  };
  return html`<div class="palette" onClick=${(e) => e.target === e.currentTarget && onClose()}>
    <div role="dialog" aria-label="Search">
      <input autofocus placeholder="Jump to a server, page or action…" value=${q} onInput=${(e) => { setQ(e.target.value); setI(0); }} onKeyDown=${key} aria-label="Search" />
      <ul role="listbox">${items.map((x, n) => html`<li role="option" aria-selected=${n === i} onMouseEnter=${() => setI(n)} onClick=${() => pick(x)}>
        ${x.status ? html`<${Lamp} status=${x.status} />` : html`<${Icon} name=${x.icon} />`}<span class="grow trunc">${x.label}</span>${x.hint && html`<span class="mono muted small">${x.hint}</span>`}</li>`)}
        ${!items.length && html`<li class="muted">Nothing matches “${q}”</li>`}</ul>
    </div></div>`;
}

// ---------- shell ----------
const NAV = [['Overview', '/', 'overview'], ['Jobs', '/jobs', 'jobs'], ['Scripts', '/scripts', 'scripts'], ['Schedules', '/schedules', 'clock'], ['Repositories', '/repos', 'git'], ['Alerts', '/alerts', 'bell'], ['SSH keys', '/keys', 'key'], ['Audit log', '/audit', 'audit'], ['Settings', '/settings', 'gear']];

function ThemeButton() {
  const [t, setT] = useState(getTheme());
  const next = { system: 'light', light: 'dark', dark: 'system' }[t];
  return html`<button class="btn ghost icon sm" title=${`Theme: ${t} (switch to ${next})`} aria-label=${`Theme: ${t}`} onClick=${() => { setTheme(next); setT(next); }}><${Icon} name=${t === 'dark' ? 'moon' : 'sun'} /></button>`;
}

function Shell() {
  const route = useRoute();
  const servers = useStore(serversStore);
  const [open, setOpen] = useState(false);
  const [palette, setPalette] = useState(false);
  useEffect(() => {
    refreshServers();
    const t = setInterval(() => document.visibilityState === 'visible' && refreshServers(), 15_000);
    const key = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette((v) => !v); }
    };
    addEventListener('keydown', key);
    return () => { clearInterval(t); removeEventListener('keydown', key); };
  }, []);
  useEffect(() => { setOpen(false); scrollTo(0, 0); }, [route]);

  const parts = route.split('/').filter(Boolean);
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
  else if (section === 'repos') page = html`<${ReposPage} />`;
  else if (section === 'audit') page = html`<${AuditPage} />`;
  else if (section === 'settings') page = html`<${SettingsPage} />`;
  else page = html`<div class="page"><h1>Page not found</h1><a href="#/">Back to the fleet</a></div>`;

  const signOut = async () => { await api('/api/auth/logout', { method: 'POST' }); location.reload(); };
  return html`<div class=${`app ${open ? 'open' : ''}`}>
    <div class="scrim" onClick=${() => setOpen(false)} />
    <nav class="side" aria-label="Main">
      <a class="brand" href="#/"><${Logo} />Fleetdeck</a>
      <div class="nav">${NAV.map(([label, path, icon]) => html`<a href=${`#${path}`} aria-current=${(path === '/' ? !section : `/${section}` === path) ? 'page' : undefined}>
        <${Icon} name=${icon} />${label}${label === 'Alerts' && firing > 0 && html`<span class="count" aria-label=${`${firing} firing`}>${firing}</span>`}</a>`)}</div>
      <div class="side-h">Servers</div>
      <div class="side-servers">${(servers || []).map((s) => html`<a href=${`#/servers/${s.id}`} aria-current=${parts[1] === s.id ? 'page' : undefined} title=${s.host}>
        <${Lamp} status=${s.status} /><span class="trunc">${s.name}</span></a>`)}
        ${servers && !servers.length && html`<span class="muted small" style=${{ padding: '4px 10px' }}>None yet</span>`}</div>
      <div class="side-foot"><${ThemeButton} /><button class="btn ghost sm right" onClick=${signOut}><${Icon} name="out" />Sign out</button></div>
    </nav>
    <div class="main">
      <header class="top">
        <button class="btn ghost icon menu-btn" aria-label="Open menu" onClick=${() => setOpen(true)}><${Icon} name="menu" /></button>
        <button class="search-btn" onClick=${() => setPalette(true)}><${Icon} name="search" /><span>Search servers and pages</span><span class="kbd">⌘K</span></button>
        <button class="btn primary right" onClick=${() => openRun()}><${Icon} name="play" />Run script</button>
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
  if (auth.error) return html`<main class="auth"><p>Fleetdeck's server is not reachable. Reload once it is running.</p></main>`;
  if (auth.setup) return html`<${Setup} onDone=${check} />`;
  if (!auth.authed) return html`<${Login} onDone=${check} />`;
  return html`<${Shell} />`;
}

render(html`<${App} />`, document.getElementById('root'));
