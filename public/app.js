import {
  html, render, useState, useEffect, useRef, useMemo, api, useRoute, go, Icon, Logo, ToastHost, ConfirmHost, useLoad, Btn,
  Lamp, useStore, serversStore, runningStore, refreshServers, refreshRunning, Field, byAttention, attention, isMac, dismissConfirm,
  meStore, refreshMe, can, ROLES, Empty, toast, switchOrg, passkeysSupported, getPasskey,
} from './ui.js';
import { getTheme, setTheme } from './theme.js';
import { Overview } from './pages/overview.js';
import { ServerPage, TAB_LABEL } from './pages/server.js';
import { JobsPage, JobPage, RunHost, openRun, closeRun } from './pages/jobs.js';
import { ScriptsPage, SchedulesPage } from './pages/scripts.js';
import { AlertsPage } from './pages/alerts.js';
import { KeysPage, VariablesPage } from './pages/keys.js';
import { ReposPage } from './pages/repos.js';
import { SettingsPage, SsoPage, OrgSsoPage, AuditPage } from './pages/settings.js';

// ---------- sign in / first-run setup / invites ----------
function AuthFrame({ title, sub, children }) {
  return html`<main class="auth"><div class="panel">
    <div class="brand"><${Logo} size=${26} />Fleetdeck</div>
    <form onSubmit=${(e) => e.preventDefault()}>
      <div><h1>${title}</h1>${sub && html`<p class="muted small">${sub}</p>`}</div>
      ${children}
    </form></div></main>`;
}

// ?org=<slug> (the organization's own sign-in link) adds its single sign-on button. `ssoHref` points the instance button elsewhere (invites).
function Login({ auth, onDone, title, sub, ssoHref = '/api/auth/oidc/start' }) {
  const [f, setF] = useState({ email: '', password: '', code: '' });
  const [needCode, setNeedCode] = useState(false);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const submit = async () => {
    if (!needCode && (!f.email || !f.password)) return setErr('Enter your email and password.');
    setBusy(true); setErr('');
    try {
      const r = await api('/api/auth/login', { method: 'POST', body: { email: f.email, password: f.password, code: needCode ? f.code : undefined } });
      if (r.totp_required) setNeedCode(true); else onDone();
    } catch (e) { setErr(e.message); } finally { setBusy(false); }
  };
  const enter = (e) => e.key === 'Enter' && submit();
  const passkey = async () => {
    setBusy(true); setErr('');
    try {
      const { token, options } = await api('/api/auth/passkey/begin', { method: 'POST' });
      await api('/api/auth/passkey/finish', { method: 'POST', body: { token, credential: await getPasskey(options) } });
      onDone();
    } catch (e) { if (e.name !== 'NotAllowedError') setErr(e.message); } finally { setBusy(false); } // NotAllowedError: the user closed the prompt
  };
  const org = auth.org;
  const orgBtn = org?.sso && !needCode && html`<a class="btn primary" href=${`/api/auth/oidc/start?org=${encodeURIComponent(org.slug)}`}>Sign in with ${org.name}</a>`;
  const sso = auth.sso && !needCode && html`<a class=${`btn ${orgBtn ? '' : 'primary'}`} href=${ssoHref}>Sign in with ${auth.sso}</a>`;
  return html`<${AuthFrame} title=${title || (org ? `Sign in to ${org.name}` : 'Sign in')} sub=${needCode ? 'Enter the 6-digit code from your authenticator app.' : sub}>
    ${(orgBtn || sso) && html`${orgBtn}${sso}<div class="or">or use your password</div>`}
    ${!needCode && html`<${Field} label="Email"><input type="text" autocomplete="username" autofocus value=${f.email} onInput=${set('email')} /></${Field}>
      <${Field} label="Password"><input type="password" autocomplete="current-password" value=${f.password} onInput=${set('password')} onKeyDown=${enter} /></${Field}>`}
    ${needCode && html`<${Field} label="Authentication code"><input inputmode="numeric" autocomplete="one-time-code" autofocus maxlength="6" value=${f.code} onInput=${set('code')} onKeyDown=${enter} /></${Field}>`}
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <button class=${`btn ${orgBtn || sso ? '' : 'primary'}`} type="submit" disabled=${busy} onClick=${submit}>${busy ? 'Signing in…' : 'Sign in'}</button>
    ${!needCode && passkeysSupported() && html`<button class="btn" type="button" disabled=${busy} onClick=${passkey}>Sign in with a passkey</button>`}
  </${AuthFrame}>`;
}

const MIN_PW = 10;
// Password + repeat with the length hint, shared by setup and invites.
function NewPassword({ f, set, onEnter }) {
  const short = f.password.length > 0 && f.password.length < MIN_PW;
  const left = MIN_PW - f.password.length;
  return html`<label class="field"><span>Password</span><input type="password" autocomplete="new-password" value=${f.password} onInput=${set('password')} />
      <span class=${`hint ${short ? 'err' : ''}`}>${short ? `${left} more character${left === 1 ? '' : 's'}. A passphrase works well.` : `At least ${MIN_PW} characters. A passphrase works well.`}</span></label>
    <label class="field"><span>Repeat password</span><input type="password" autocomplete="new-password" value=${f.confirm} onInput=${set('confirm')} onKeyDown=${(e) => e.key === 'Enter' && onEnter()} />
      ${f.confirm.length > 0 && f.confirm !== f.password && html`<span class="hint err">Doesn't match yet.</span>`}</label>`;
}
const pwOk = (f) => f.password.length >= MIN_PW && f.password === f.confirm;

function Setup({ onDone }) {
  const [f, setF] = useState({ token: '', name: '', email: '', organization: '', password: '', confirm: '' });
  const [err, setErr] = useState('');
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const ok = pwOk(f) && f.token && f.name && f.email && f.organization;
  const submit = async () => {
    if (!ok) return;
    try { await api('/api/auth/setup', { method: 'POST', body: { token: f.token, name: f.name, email: f.email, organization: f.organization, password: f.password } }); onDone(); } catch (e) { setErr(e.message); }
  };
  return html`<${AuthFrame} title="Set up Fleetdeck" sub="Create the first account. It can manage every organization on this Fleetdeck. The setup token is printed in the server console (or docker logs).">
    <${Field} label="Setup token"><input class="mono" autofocus value=${f.token} onInput=${set('token')} /></${Field}>
    <${Field} label="Your name"><input autocomplete="name" value=${f.name} onInput=${set('name')} /></${Field}>
    <${Field} label="Email"><input type="email" autocomplete="username" value=${f.email} onInput=${set('email')} /></${Field}>
    <${Field} label="Organization" hint="Your company or team. You can add more organizations later."><input autocomplete="organization" value=${f.organization} onInput=${set('organization')} /></${Field}>
    <${NewPassword} f=${f} set=${set} onEnter=${submit} />
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <button class="btn primary" type="submit" disabled=${!ok} onClick=${submit}>Create account and sign in</button>
  </${AuthFrame}>`;
}

const ROLE_TEXT = { admin: 'admin', owner: 'owner', operator: 'operator', viewer: 'viewer' };
const intoApp = () => { location.hash = '#/'; location.reload(); };

// #/invite/<token>: works signed in (join with this account) and signed out (create an account, or sign in first).
function Invite({ token, auth }) {
  const inv = useLoad(() => api(`/api/invites/${encodeURIComponent(token)}`), [token]);
  const [f, setF] = useState({ name: '', password: '', confirm: '' });
  const [err, setErr] = useState('');
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const accept = async (body) => {
    setErr('');
    try { await api(`/api/invites/${encodeURIComponent(token)}/accept`, { method: 'POST', body }); intoApp(); } catch (e) { setErr(e.message); }
  };
  if (inv.loading) return null;
  if (inv.error) return html`<${AuthFrame} title="Invite"><p class="err" role="alert">${inv.error}</p><a class="btn" href="/">Go to Fleetdeck</a></${AuthFrame}>`;
  const d = inv.data;
  const sub = `You're invited to ${d.org} as ${ROLE_TEXT[d.role] || d.role}.`;
  const ssoHref = `/api/auth/oidc/start?invite=${encodeURIComponent(token)}`;
  if (d.user) {
    return html`<${AuthFrame} title=${`Join ${d.org}`} sub=${sub}>
      <p class="small ink2">You're signed in as <b>${d.user}</b>${d.user !== d.email ? html`. The invite was sent to <b>${d.email}</b>` : ''}.</p>
      ${err && html`<p class="err" role="alert">${err}</p>`}
      <${Btn} kind="primary" type="submit" onClick=${() => accept({})}>Join ${d.org}</${Btn}>
      <a class="btn ghost" href="#/">Not now</a>
    </${AuthFrame}>`;
  }
  if (d.exists) return html`<${Login} auth=${auth} onDone=${() => location.reload()} title=${`Sign in to join ${d.org}`} sub=${`${sub} There's already an account for ${d.email}.`} ssoHref=${ssoHref} />`;
  const submit = () => pwOk(f) && f.name && accept({ name: f.name, password: f.password });
  return html`<${AuthFrame} title=${`Join ${d.org}`} sub=${sub}>
    ${d.sso && html`<a class="btn primary" href=${ssoHref}>Accept with ${d.sso}</a><div class="or">or create a password</div>`}
    <${Field} label="Email" hint="The invite is for this address."><input value=${d.email} disabled /></${Field}>
    <${Field} label="Your name"><input autocomplete="name" autofocus value=${f.name} onInput=${set('name')} /></${Field}>
    <${NewPassword} f=${f} set=${set} onEnter=${submit} />
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <button class=${`btn ${d.sso ? '' : 'primary'}`} type="submit" disabled=${!pwOk(f) || !f.name} onClick=${submit}>Create account and join</button>
  </${AuthFrame}>`;
}

// ---------- navigation: one source for the sidebar and the palette ----------
// [label, path, icon, lowest role that may open it]
const NAV = [
  [null, [['Overview', '/', 'overview'], ['Alerts', '/alerts', 'bell', 'admin']]],
  ['Automation', [['Jobs', '/jobs', 'jobs'], ['Scripts', '/scripts', 'scripts'], ['Schedules', '/schedules', 'clock', 'admin'], ['Repositories', '/repos', 'git', 'admin'], ['Variables', '/variables', 'vars', 'admin']]],
  ['Access', [['SSH keys', '/keys', 'key', 'owner']]],
  ['System', [['Audit log', '/audit', 'audit', 'admin'], ['Settings', '/settings', 'gear']]],
];
const ALL_PAGES = NAV.flatMap(([, items]) => items);
// The sidebar for the current role (empty groups dropped).
const navFor = () => NAV.map(([g, items]) => [g, items.filter(([, , , r]) => !r || can(r))]).filter(([, items]) => items.length);

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
      can('operator') && { group: 'Actions', label: 'Run a script…', icon: 'play', run: () => openRun() },
      can('owner') && { group: 'Actions', label: 'Add server…', icon: 'plus', run: () => go('/?add=1') },
      // per-server actions only once you type, so the empty list stays short
      ...(n ? sorted.flatMap((s) => [
        can('operator') && { group: 'Actions', label: `Terminal: ${s.name}`, hint: s.host, icon: 'term', run: () => go(`/servers/${s.id}/terminal`) },
        { group: 'Actions', label: `Logs: ${s.name}`, hint: s.host, icon: 'audit', run: () => go(`/servers/${s.id}/logs`) },
        can('operator') && { group: 'Actions', label: `Update: ${s.name}`, hint: s.host, icon: 'refresh', run: () => openRun({ targets: { servers: [s.id] }, scriptId: 'update' }) },
      ]) : []),
      ...navFor().flatMap(([, items]) => items).map(([label, path, icon]) => ({ group: 'Pages', label, icon, run: () => go(path) })),
    ].filter(Boolean);
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
  if (parts[0] === 'settings' && parts[1]) {
    const sub = { sso: 'Single sign-on', org: 'Organization', instance: 'Instance' }[parts[1]] || parts[1];
    return html`<nav class="crumbs" aria-label="Breadcrumb"><a href="#/settings">Settings</a><span>/</span>
      ${parts[2] === 'sso' ? html`<a href=${`#/settings/${parts[1]}`}>${sub}</a><span>/</span><b>Single sign-on</b>` : html`<b>${sub}</b>`}</nav>`;
  }
  if (parts[0] === 'jobs' && parts[1]) return html`<nav class="crumbs" aria-label="Breadcrumb"><a href="#/jobs">Jobs</a><span>/</span><b>#${parts[1]}</b></nav>`;
  return html`<span class="crumbs" />`;
}

const roleName = (r) => ROLES.find(([k]) => k === r)?.[1] || r;
const signOut = async () => { await api('/api/auth/logout', { method: 'POST' }); location.hash = ''; location.reload(); };

// Sidebar footer: the organization (switchable when there are several), who you are, theme, sign out.
function SideMe({ me }) {
  const many = !me.locked && me.tenants.length > 1;
  return html`<div class="side-foot">
    <div class="side-me">
      ${many ? html`<select value=${me.tenant.id} aria-label="Organization" onChange=${(e) => switchOrg(e.target.value).catch((x) => toast(x.message, 'bad'))}>
          ${me.tenants.map((t) => html`<option value=${t.id}>${t.name}</option>`)}</select>`
        : html`<b class="trunc">${me.tenant.name}</b>`}
      <span class="small muted trunc" title=${me.user.email}>${me.user.name || me.user.email} · ${roleName(me.role)}${me.user.super_admin ? ' · super admin' : ''}</span>
    </div>
    <${ThemeButton} /><button class="btn ghost sm right" onClick=${signOut}><${Icon} name="out" />Sign out</button>
  </div>`;
}

function Shell() {
  const { path } = useRoute();
  const me = useStore(meStore);
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
  const need = section && ALL_PAGES.find(([, p]) => p === `/${section}`)?.[3];
  let page;
  if (need && !can(need)) page = html`<div class="page"><${Empty} title="Not available with your role" action=${html`<a class="btn" href="#/">Back to the fleet</a>`}>An admin of ${me.tenant.name} can change your role.</${Empty}></div>`;
  else if (!section) page = html`<${Overview} />`;
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
  else if (section === 'settings' && parts[1] === 'org' && parts[2] === 'sso') page = html`<${OrgSsoPage} />`;
  else if (section === 'settings') page = html`<${SettingsPage} key=${parts[1] || ''} tab=${parts[1] || ''} />`;
  else page = html`<div class="page"><h1>Page not found</h1><a href="#/">Back to the fleet</a></div>`;

  const badge = (label) => {
    if (label === 'Alerts' && firing > 0) return html`<span class="count" aria-label=${`${firing} firing`}>${firing}</span>`;
    if (label === 'Jobs' && running > 0) return html`<span class="count info" aria-label=${`${running} running`}>${running}</span>`;
    return null;
  };
  return html`<div class=${`app ${open ? 'open' : ''}`}>
    <div class="scrim" onClick=${() => setOpen(false)} />
    <nav class="side" aria-label="Main">
      <a class="brand" href="#/"><${Logo} />Fleetdeck</a>
      ${navFor().map(([group, items]) => html`${group && html`<div class="side-h">${group}</div>`}
        <div class="nav">${items.map(([label, p, icon]) => html`<a href=${`#${p}`} aria-current=${(p === '/' ? !section : `/${section}` === p) ? 'page' : undefined}>
          <${Icon} name=${icon} />${label}${badge(label)}</a>`)}</div>`)}
      <div class="side-h">Servers${servers?.length > 0 && html`<span class="n">${servers.length}</span>`}</div>
      <${SideServers} servers=${servers} current=${section === 'servers' ? parts[1] : null} />
      <${SideMe} me=${me} />
    </nav>
    <div class="main">
      <header class="top">
        <button class="btn ghost icon menu-btn" aria-label="Open menu" onClick=${() => setOpen(true)}><${Icon} name="menu" /></button>
        <${Crumbs} parts=${parts} servers=${servers} />
        <button class="search-btn right" onClick=${() => setPalette(true)}><${Icon} name="search" /><span>Search servers, pages, actions</span><span class="kbd">${isMac ? '⌘K' : 'Ctrl K'}</span></button>
        ${can('operator') && html`<button class="btn primary" onClick=${() => openRun()} aria-label="Run script"><${Icon} name="play" /><span class="hide-sm">Run script</span></button>`}
      </header>
      <main>${page}</main>
    </div>
    ${palette && html`<${Palette} onClose=${() => setPalette(false)} />`}
    <${RunHost} /><${ConfirmHost} /><${ToastHost} />
  </div>`;
}

// Signed in, but in no organization (yet).
function NoOrg({ me }) {
  return html`<${AuthFrame} title="You're not in an organization yet" sub=${`Signed in as ${me.user.email}.`}>
    ${me.tenants.length ? html`<p class="small ink2">Open one of these:</p>${me.tenants.map((t) => html`<${Btn} onClick=${() => switchOrg(t.id)}>${t.name}</${Btn}>`)}`
      : html`<p class="small ink2">Ask an admin of your organization for an invite link. Opening it adds this account.</p>`}
    <button class="btn ghost" onClick=${signOut}><${Icon} name="out" />Sign out</button>
  </${AuthFrame}>`;
}

function App() {
  const { path } = useRoute();
  const me = useStore(meStore);
  const [auth, setAuth] = useState(null);
  const org = new URLSearchParams(location.search).get('org');
  const check = () => api(`/api/auth/state${org ? `?org=${encodeURIComponent(org)}` : ''}`)
    .then(async (a) => { if (a.authed) await refreshMe(); setAuth(a); })
    .catch(() => setAuth({ error: true }));
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
  if (path.startsWith('/invite/')) return html`<${Invite} key=${path} token=${path.slice(8)} auth=${auth} /><${ToastHost} />`;
  if (!auth.authed) return html`<${Login} auth=${auth} onDone=${check} />`;
  if (!me) return null;
  if (!me.tenant) return html`<${NoOrg} me=${me} /><${ToastHost} />`;
  return html`<${Shell} />`;
}

render(html`<${App} />`, document.getElementById('root'));
