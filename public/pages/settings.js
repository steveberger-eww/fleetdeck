import {
  html, useState, useEffect, useMemo, api, go, useLoad, useQuery, useStore, useDirty, toast, confirmBox, Btn, Th, useSort, Empty, Field, Modal, Footer, Loading, ErrorNote, Copy,
  ago, when, getClock, setClock, meStore, refreshMe, can, isSuper, switchOrg, ROLES, passkeysSupported, createPasskey,
} from '../ui.js';
import { getTheme, setTheme } from '../theme.js';

// #/settings (your account), #/settings/org (admins), #/settings/instance (super admins)
export function SettingsPage({ tab }) {
  const tabs = [['', 'Account'], can('admin') && ['org', 'Organization'], isSuper() && ['instance', 'Instance']].filter(Boolean);
  const cur = tabs.some(([k]) => k === tab) ? tab : '';
  return html`<div class="page">
    <div class="page-h"><div><h1>Settings</h1></div>
      ${tabs.length > 1 && html`<div class="seg" role="group" aria-label="Settings">${tabs.map(([k, l]) => html`<button aria-pressed=${cur === k} onClick=${() => go(`/settings${k ? `/${k}` : ''}`)}>${l}</button>`)}</div>`}</div>
    ${cur === 'org' ? html`<${OrgSettings} />` : cur === 'instance' ? html`<${InstanceSettings} />` : html`<${AccountSettings} />`}
  </div>`;
}

// ---------- your account ----------
function AccountSettings() {
  const acct = useLoad(() => api('/api/account'));
  const me = useStore(meStore);
  if (acct.error) return html`<${ErrorNote} error=${acct.error} retry=${acct.reload} />`;
  if (!acct.data) return html`<${Loading} />`;
  const a = acct.data;
  const reload = () => acct.reload(true);
  return html`<div class="grid-2">
    <div class="stack">
      ${a.locked ? html`<section class="panel"><div class="panel-h"><h2>${a.name || a.email}</h2></div><div class="panel-b stack sm">
          <p class="small">${a.email}</p>
          <p class="small ink2">You signed in through the single sign-on of ${me.tenant.name}, so this session can't change your profile, password or two-factor sign-in, or open other organizations. Sign in with your password to do that.</p></div></section>`
        : html`<${ProfilePanel} a=${a} onChange=${reload} />
          <${PasswordPanel} has=${a.has_password} onChange=${reload} />
          <${TotpPanel} enabled=${a.totp} onChange=${reload} />
          <${PasskeysPanel} list=${a.passkeys} onChange=${reload} />
          <${SsoSummary} />`}
      <${ThemePanel} />
    </div>
    <div class="stack"><${SessionsPanel} sessions=${a.sessions} onChange=${reload} /></div>
  </div>`;
}

function ProfilePanel({ a, onChange }) {
  const [v, setV] = useState({ name: a.name, email: a.email });
  const changed = v.name !== a.name || v.email !== a.email;
  const save = async () => { await api('/api/account', { method: 'PUT', body: v }); toast('Profile saved', 'ok'); onChange(); refreshMe(); };
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  return html`<section class="panel"><div class="panel-h"><h2>Profile</h2></div><div class="panel-b form">
    <div class="cols"><${Field} label="Name"><input autocomplete="name" value=${v.name} onInput=${set('name')} /></${Field}>
      <${Field} label="Email" hint="You sign in with it."><input type="email" autocomplete="email" value=${v.email} onInput=${set('email')} /></${Field}></div>
    <div><${Btn} kind=${changed ? 'primary' : ''} disabled=${!changed || !v.name.trim() || !v.email.trim()} onClick=${save}>Save</${Btn}></div></div></section>`;
}

function PasswordPanel({ has, onChange }) {
  const [v, setV] = useState({ current: '', next: '', confirm: '' });
  const [err, setErr] = useState('');
  const save = async () => {
    setErr('');
    if (v.next !== v.confirm) return setErr('New passwords do not match');
    try {
      await api('/api/account/password', { method: 'POST', body: { current: has ? v.current : undefined, next: v.next } });
      setV({ current: '', next: '', confirm: '' });
      toast(has ? 'Password changed. Other sessions were signed out.' : 'Password set. Other sessions were signed out.', 'ok');
      onChange();
    } catch (e) { setErr(e.message); }
  };
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  return html`<section class="panel"><div class="panel-h"><h2>Password</h2>${!has && html`<span class="chip">None yet</span>`}</div><div class="panel-b form">
    ${has ? html`<${Field} label="Current password"><input type="password" autocomplete="current-password" value=${v.current} onInput=${set('current')} /></${Field}>`
      : html`<p class="small ink2">Your account was created through single sign-on. Add a password to also sign in with your email.</p>`}
    <div class="cols"><${Field} label="New password" hint="At least 10 characters."><input type="password" autocomplete="new-password" value=${v.next} onInput=${set('next')} /></${Field}>
      <${Field} label="Repeat new password"><input type="password" autocomplete="new-password" value=${v.confirm} onInput=${set('confirm')} /></${Field}></div>
    ${err && html`<p class="err" role="alert">${err}</p>`}
    <div><${Btn} kind="primary" onClick=${save} disabled=${(has && !v.current) || v.next.length < 10}>${has ? 'Change password' : 'Set password'}</${Btn}></div>
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

function PasskeysPanel({ list, onChange }) {
  const [name, setName] = useState('');
  const [err, setErr] = useState('');
  const add = async () => {
    setErr('');
    try {
      const options = await api('/api/account/passkeys/begin', { method: 'POST', body: { name } });
      await api('/api/account/passkeys/finish', { method: 'POST', body: { credential: await createPasskey(options) } });
      setName('');
      toast('Passkey added', 'ok');
      onChange();
    } catch (e) { if (e.name !== 'NotAllowedError') setErr(e.message); }
  };
  const remove = async (p) => {
    if (!await confirmBox({ title: `Remove the passkey "${p.name}"?`, body: 'You can no longer sign in with it. Also remove it from the device or password manager.', confirm: 'Remove passkey', danger: true })) return;
    await api(`/api/account/passkeys/${encodeURIComponent(p.id)}`, { method: 'DELETE' });
    toast('Passkey removed');
    onChange();
  };
  return html`<section class="panel"><div class="panel-h"><h2>Passkeys</h2>${list.length > 0 && html`<span class="chip ok">${list.length}</span>`}</div>
    <div class="panel-b form">
      <p class="small ink2">Sign in with your fingerprint, face or device PIN instead of a password and code. A passkey counts as both factors.</p>
      ${list.length > 0 && html`<table class="t"><tbody>${list.map((p) => html`<tr><td><b>${p.name}</b><div class="small muted">Added ${ago(p.created_at)}${p.last_used ? `, last used ${ago(p.last_used)}` : ', never used'}</div></td>
        <td class="act"><${Btn} size="sm" kind="ghost" icon="trash" title="Remove" onClick=${() => remove(p)} /></td></tr>`)}</tbody></table>`}
      ${passkeysSupported() ? html`<div class="row nw"><input placeholder="Name, e.g. MacBook or YubiKey" value=${name} onInput=${(e) => setName(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && name.trim() && add()} class="grow" aria-label="Passkey name" />
          <${Btn} kind="primary" disabled=${!name.trim()} onClick=${add}>Add passkey</${Btn}></div>`
        : html`<p class="small ink2">Passkeys need HTTPS (or localhost) and a browser that supports them.</p>`}
      ${err && html`<p class="err" role="alert">${err}</p>`}
    </div></section>`;
}

function SsoSummary() {
  const sso = useLoad(() => api('/api/sso'));
  const d = sso.data;
  if (!d || (!d.configured && !d.can_configure)) return null; // nothing to connect to, and not yours to set up
  const on = d.linked;
  return html`<section class="panel"><div class="panel-h"><h2>Single sign-on</h2>${on ? html`<span class="chip ok">On</span>` : html`<span class="chip">Off</span>`}</div>
    <div class="panel-b form">
      <p class="small ink2">${on ? html`Connected as <b>${d.account}</b>. Password sign-in is ${d.password_login ? 'still on as a fallback' : 'off'}.`
        : d.configured ? 'Connect your account to sign in with single sign-on instead of a password.'
        : 'Let everyone on this Fleetdeck sign in with authentik, Keycloak or another OpenID Connect provider.'}</p>
      <div><a class=${`btn ${on ? '' : 'primary'}`} href="#/settings/sso">${on ? 'Manage' : d.configured ? 'Connect account' : 'Set up single sign-on'}</a></div>
    </div></section>`;
}

// ---------- single sign-on (#/settings/sso for the instance, #/settings/org/sso for an organization) ----------
const Step = ({ n, title, done, off, children }) => html`<section class=${`panel step ${off ? 'off' : ''}`}>
  <div class="panel-h"><h2><span class=${`step-n ${done ? 'ok' : ''}`}>${done ? '✓' : n}</span>${title}</h2></div>
  <div class="panel-b form">${children}</div></section>`;

// Steps 1-2, the same for every provider: create it, then enter its details. `f` holds the form.
function ProviderSteps({ d, f, setF, err, onSave, configured, off }) {
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const insecure = d.redirect_url.startsWith('http://') && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])[:/]/.test(d.redirect_url);
  return html`<${Step} n="1" title="Create a provider in authentik" done=${configured} off=${off}>
      <p class="small">In the <b>Admin interface</b>, open <b>Applications → Applications</b> and click <b>Create with provider</b>. Choose <b>OAuth2/OpenID Provider</b>.</p>
      <div class="field"><span>Redirect URI (paste into authentik, matching mode <b>Strict</b>)</span><${Copy} text=${d.redirect_url} /></div>
      ${insecure && html`<p class="hint err">This starts with http://. If Fleetdeck runs behind a reverse proxy with HTTPS, start it with <span class="mono">FLEETDECK_TRUST_PROXY=1</span> or set the public URL under Settings → Instance, and reload this page.</p>`}
      <ul class="small ink2 checklist">
        <li>Client type: <b>Confidential</b></li>
        <li>Signing Key: pick one, e.g. <b>authentik Self-signed Certificate</b>. Without it, sign-in fails.</li>
      </ul>
    </${Step}>
    <${Step} n="2" title="Enter the provider details" done=${configured} off=${off}>
      <${Field} label="Issuer URL" hint="On the provider's page: “OpenID Configuration Issuer”, like https://auth.example.com/application/o/fleetdeck/">
        <input class="mono" placeholder="https://" value=${f.issuer} disabled=${off} onInput=${set('issuer')} /></${Field}>
      <div class="cols">
        <${Field} label="Client ID" hint="On the provider's page."><input class="mono" value=${f.client_id} disabled=${off} onInput=${set('client_id')} /></${Field}>
        <${Field} label="Client secret" hint=${d.has_secret ? 'Saved. Leave empty to keep it.' : 'Click Edit on the provider to see it.'}>
          <input type="password" autocomplete="off" value=${f.client_secret} disabled=${off} onInput=${set('client_secret')} /></${Field}>
      </div>
      ${err && html`<p class="err" role="alert">${err}</p>`}
      <div><${Btn} kind=${configured ? '' : 'primary'} onClick=${onSave} disabled=${off || !f.issuer || !f.client_id || (!d.has_secret && !f.client_secret)}>Save and test</${Btn}></div>
    </${Step}>`;
}

// Loads a provider view and keeps the form for steps 1-2.
function useProvider(path) {
  const sso = useLoad(() => api(path));
  const [f, setF] = useState(null);
  const [err, setErr] = useState('');
  useEffect(() => { if (sso.data && !f) setF({ issuer: sso.data.issuer, client_id: sso.data.client_id, client_secret: '' }); }, [sso.data]);
  const save = async () => {
    setErr('');
    try {
      await api(path, { method: 'PUT', body: f });
      setF(null); sso.reload(true);
      toast('Saved. Fleetdeck can reach your provider.', 'ok');
    } catch (e) { setErr(e.message); }
  };
  const remove = async (body) => {
    if (!await confirmBox({ title: 'Remove single sign-on?', body, confirm: 'Remove', danger: true })) return;
    await api(path, { method: 'DELETE' }); setF(null); sso.reload(true); toast('Single sign-on removed');
  };
  return { sso, f, setF, err, save, remove };
}

export function SsoPage() {
  const { sso, f, setF, err, save, remove } = useProvider('/api/sso');
  if (sso.error) return html`<div class="page"><${ErrorNote} error=${sso.error} retry=${sso.reload} /></div>`;
  if (!sso.data || !f) return html`<div class="page"><${Loading} /></div>`;
  const d = sso.data;
  const admin = d.can_configure;
  const configured = d.configured;
  if (!admin && !configured) return html`<div class="page narrow"><${Empty} title="Single sign-on isn't set up" action=${html`<a class="btn" href="#/settings">Back to settings</a>`}>A super admin can set it up for this Fleetdeck.</${Empty}></div>`;
  const setPw = async (enabled) => { await api('/api/sso/password-login', { method: 'PUT', body: { enabled } }); sso.reload(true); };
  const n = admin ? 2 : 0; // people who can't configure it only see the steps for their own account
  return html`<div class="page narrow">
    <div class="page-h"><div><h1>Single sign-on</h1>
      <p class="sub">${admin ? 'Sign in to this Fleetdeck with authentik or any OpenID Connect provider (Keycloak, Authelia, Zitadel…). The steps use authentik\'s names.'
        : 'Sign in with your company account instead of a password.'}</p></div></div>
    <div class="stack">
      ${admin && html`<${ProviderSteps} d=${d} f=${f} setF=${setF} err=${err} onSave=${save} configured=${configured} />`}

      <${Step} n=${n + 1} title="Connect your account" done=${d.linked} off=${!configured}>
        ${d.linked ? html`<p class="small">Connected as <b>${d.account}</b>.</p>`
          : html`<p class="small">Sign in at your provider once to connect it to your Fleetdeck account. Accounts with the same email address connect by themselves on their first sign-in.</p>`}
        <p class="small ink2">Sign-ins this way skip Fleetdeck's own 2FA, so turn on MFA in your provider.</p>
        <div>${configured ? html`<a class=${`btn ${d.linked ? '' : 'primary'}`} href="/api/auth/oidc/start?link=1">${d.linked ? 'Connect a different account' : 'Connect account'}</a>`
          : html`<button class="btn" disabled>Connect account</button>`}</div>
      </${Step}>

      <${Step} n=${n + 2} title="Password sign-in (optional)" done=${d.linked && !d.password_login} off=${!d.linked}>
        <p class="small">Keep your password as a fallback, or turn it off so only single sign-on works for your account.</p>
        ${d.password_forced ? html`<p class="small ink2"><span class="mono">FLEETDECK_PASSWORD_LOGIN=1</span> is set, so passwords always work right now.</p>`
          : html`<p class="small ink2">Locked out? Start Fleetdeck with <span class="mono">FLEETDECK_PASSWORD_LOGIN=1</span> to use passwords again.</p>`}
        <div class="seg" role="group" aria-label="Password sign-in">
          <button aria-pressed=${d.password_login} disabled=${!d.linked} onClick=${() => setPw(true)}>On</button>
          <button aria-pressed=${!d.password_login} disabled=${!d.linked || d.password_forced} onClick=${() => setPw(false)}>Off</button></div>
      </${Step}>

      ${admin && (d.issuer || d.client_id) && html`<div><${Btn} kind="quiet-danger" onClick=${() => remove('Everyone signs in with their password again. Connected accounts are forgotten.')}>Remove single sign-on</${Btn}></div>`}
    </div>
  </div>`;
}

const JOIN_ROLES = [['', 'Only people who are already members'], ['viewer', 'Anyone from the provider, as viewer'], ['operator', 'Anyone from the provider, as operator'], ['owner', 'Anyone from the provider, as owner']];

export function OrgSsoPage() {
  const me = useStore(meStore);
  const { sso, f, setF, err, save, remove } = useProvider('/api/tenant/sso');
  if (!can('admin')) return html`<div class="page"><${Empty} title="Not available with your role" /></div>`;
  if (sso.error) return html`<div class="page"><${ErrorNote} error=${sso.error} retry=${sso.reload} /></div>`;
  if (!sso.data || !f) return html`<div class="page"><${Loading} /></div>`;
  const d = sso.data;
  const configured = !!(d.issuer && d.client_id && d.has_secret);
  const off = !d.licensed;
  const setRole = async (role) => { await api('/api/tenant/sso', { method: 'PUT', body: { role } }); sso.reload(true); toast('Saved', 'ok'); };
  return html`<div class="page narrow">
    <div class="page-h"><div><h1>Single sign-on for ${me.tenant.name}</h1>
      <p class="sub">Your people sign in with your organization's own authentik or other OpenID Connect provider (Keycloak, Authelia, Zitadel…). The steps use authentik's names.</p></div></div>
    <div class="stack">
      ${off && html`<div class="alert-bar info"><span class="grow">An organization's own single sign-on needs a <b>business license</b>.</span></div>`}
      <${ProviderSteps} d=${d} f=${f} setF=${setF} err=${err} onSave=${save} configured=${configured} off=${off} />

      <${Step} n="3" title="Who can sign in" done=${configured} off=${off || !configured}>
        <${Field} label="People without a Fleetdeck account" hint="Accounts are matched by email. Super admins can't sign in this way.">
          <select value=${d.role} disabled=${off || !configured} onChange=${(e) => setRole(e.target.value).catch((x) => toast(x.message, 'bad'))}>
            ${JOIN_ROLES.map(([k, l]) => html`<option value=${k}>${l}</option>`)}</select></${Field}>
        <p class="small ink2">Sign-ins this way skip Fleetdeck's own 2FA, so turn on MFA in your provider.</p>
      </${Step}>

      <${Step} n="4" title="Share the sign-in link" done=${configured} off=${off || !configured}>
        <${Copy} text=${d.login_url} />
        <p class="small ink2">Sign-ins through this link open ${me.tenant.name} only. Switching organizations or changing a password or 2FA needs a normal sign-in.</p>
      </${Step}>

      ${(d.issuer || d.client_id) && html`<div><${Btn} kind="quiet-danger" onClick=${() => remove(`People of ${me.tenant.name} sign in with their password or invite again.`)}>Remove single sign-on</${Btn}></div>`}
    </div>
  </div>`;
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
  const save = async () => { await api('/api/instance', { method: 'PUT', body: Object.fromEntries(RETENTION.map((k) => [k, v[k]])) }); setSaved(v); toast('Retention saved', 'ok'); };
  const f = (k, label, max) => html`<${Field} label=${label}><input type="number" min="1" max=${max} value=${v[k]} onInput=${(e) => setV({ ...v, [k]: Number(e.target.value) })} /></${Field}>`;
  return html`<section class="panel"><div class="panel-h"><h2>Keep data for (days)</h2></div><div class="panel-b form">
    <div class="cols">${f('retention_metrics_days', 'Metrics', 365)}${f('retention_jobs_days', 'Job output', 3650)}${f('retention_events_days', 'Audit log', 3650)}</div>
    <div><${Btn} kind=${changed ? 'primary' : ''} disabled=${!changed} onClick=${save}>Save</${Btn}></div></div></section>`;
}

// ---------- the organization (admins) ----------
const roleName = (r) => ROLES.find(([k]) => k === r)?.[1] || r;
const RoleSelect = ({ value, onChange, label = 'Role' }) => html`<select value=${value} aria-label=${label} onChange=${(e) => onChange(e.target.value, e.target)}>${ROLES.map(([k, l]) => html`<option value=${k}>${l}</option>`)}</select>`;

function OrgSettings() {
  return html`<div class="stack">
    <div class="grid-2"><${OrgPanel} /><${OrgSsoSummary} /></div>
    <${PeoplePanel} />
  </div>`;
}

function OrgPanel() {
  const me = useStore(meStore);
  const [v, setV] = useState({ name: me.tenant.name, slug: me.tenant.slug });
  const changed = v.name !== me.tenant.name || v.slug !== me.tenant.slug;
  const save = async () => { const t = await api('/api/tenant', { method: 'PUT', body: v }); setV({ name: t.name, slug: t.slug }); await refreshMe(); toast('Organization saved', 'ok'); };
  return html`<section class="panel"><div class="panel-h"><h2>Organization</h2></div><div class="panel-b form">
    <${Field} label="Name"><input value=${v.name} onInput=${(e) => setV({ ...v, name: e.target.value })} /></${Field}>
    <${Field} label="Short name" hint="Lowercase letters, digits and dashes. Part of your single sign-on link (…/?org=${v.slug || 'short-name'})."><input class="mono" value=${v.slug} onInput=${(e) => setV({ ...v, slug: e.target.value })} /></${Field}>
    <div><${Btn} kind=${changed ? 'primary' : ''} disabled=${!changed || !v.name.trim()} onClick=${save}>Save</${Btn}></div></div></section>`;
}

function OrgSsoSummary() {
  const sso = useLoad(() => api('/api/tenant/sso'));
  const d = sso.data;
  const on = d && d.issuer && d.client_id && d.has_secret;
  return html`<section class="panel"><div class="panel-h"><h2>Single sign-on</h2>${d && (!d.licensed ? html`<span class="chip">Business license</span>` : on ? html`<span class="chip ok">On</span>` : html`<span class="chip">Off</span>`)}</div>
    <div class="panel-b form">
      ${sso.error ? html`<${ErrorNote} error=${sso.error} retry=${sso.reload} />` : html`<p class="small ink2">${d && !d.licensed ? 'Your own provider for this organization needs a business license.' : on ? html`Your people sign in at <span class="mono wrap-any">${d.login_url}</span>${d.role ? `; newcomers join as ${d.role}` : ''}.`
        : 'Let your people sign in with your own authentik, Keycloak or other OpenID Connect provider.'}</p>`}
      <div><a class=${`btn ${on || !d?.licensed ? '' : 'primary'}`} href="#/settings/org/sso">${on ? 'Manage' : 'Set up single sign-on'}</a></div>
    </div></section>`;
}

function PeoplePanel() {
  const m = useLoad(() => api('/api/members'));
  const [inv, setInv] = useState({ email: '', role: 'operator' });
  const [link, setLink] = useState(null); // { email, url } right after creating an invite
  const sort = useSort(m.data?.members, 'name', 1, (u) => u.name || u.email);
  const invite = async () => {
    const r = await api('/api/members/invites', { method: 'POST', body: inv });
    setLink({ email: inv.email, url: r.url });
    setInv({ ...inv, email: '' });
    m.reload(true);
  };
  const setRole = async (u, role, el) => {
    const self = u.id === m.data.me;
    if (self && !await confirmBox({ title: 'Change your own role?', body: `You become ${roleName(role).toLowerCase()} and lose admin access to this organization.`, confirm: 'Change role', danger: true })) { el.value = u.role; return; }
    try { await api(`/api/members/${u.id}`, { method: 'PUT', body: { role } }); toast(`${u.name || u.email} is now ${roleName(role).toLowerCase()}`, 'ok'); } catch (e) { el.value = u.role; toast(e.message, 'bad'); }
    m.reload(true);
    if (self) refreshMe();
  };
  const remove = async (u) => {
    if (!await confirmBox({ title: `Remove ${u.name || u.email}?`, body: 'They lose access to this organization and its servers. Their account stays.', confirm: 'Remove', danger: true })) return;
    await api(`/api/members/${u.id}`, { method: 'DELETE' });
    toast(`${u.email} removed`);
    m.reload(true);
  };
  const withdraw = async (x) => { await api(`/api/members/invites/${x.id}`, { method: 'DELETE' }); m.reload(true); };
  return html`<section class="panel">
    <div class="panel-h"><h2>People</h2><span class="small muted">Admins see every server. Everyone else sees the servers assigned to them on the server's Settings tab.</span></div>
    ${m.loading && !m.data ? html`<${Loading} />` : m.error ? html`<div class="panel-b"><${ErrorNote} error=${m.error} retry=${m.reload} /></div>` : html`
      <div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="name" get=${(u) => u.name || u.email}>Person</${Th}><${Th} sort=${sort} k="role">Role</${Th}><${Th} sort=${sort} k="servers" desc class="num">Servers</${Th}><th class="hide-sm">Sign-in</th><${Th} sort=${sort} k="created_at" desc class="hide-sm">Joined</${Th}><th class="act"></th></tr></thead>
        <tbody>${sort.rows.map((u) => html`<tr>
          <td><b>${u.name || u.email}</b>${u.id === m.data.me && html` <span class="chip">You</span>`}<div class="small muted">${u.email}</div></td>
          <td><${RoleSelect} value=${u.role} label=${`Role of ${u.email}`} onChange=${(r, el) => setRole(u, r, el)} /></td>
          <td class="num ink2">${u.role === 'admin' ? 'All' : u.servers}</td>
          <td class="hide-sm"><div class="row nw">${u.sso && html`<span class="tag">SSO</span>`}${u.totp && html`<span class="tag">2FA</span>`}</div></td>
          <td class="ink2 nowrap hide-sm" title=${when(u.created_at)}>${ago(u.created_at)}</td>
          <td class="act">${u.id !== m.data.me && html`<${Btn} size="sm" kind="quiet-danger" icon="trash" title=${`Remove ${u.email}`} onClick=${() => remove(u)} />`}</td></tr>`)}</tbody></table></div>
      <div class="panel-b form b-top">
        <div class="row">
          <input type="email" class="grow" placeholder="Email address" aria-label="Email address to invite" value=${inv.email} onInput=${(e) => setInv({ ...inv, email: e.target.value })} />
          <${RoleSelect} value=${inv.role} onChange=${(role) => setInv({ ...inv, role })} />
          <${Btn} kind="primary" icon="plus" disabled=${!inv.email.trim()} onClick=${invite}>Invite</${Btn}></div>
        <p class="small ink2">Admins manage everything. Owners add servers and manage the ones they have. Operators run jobs, services and the terminal on their servers. Viewers only look.</p>
        ${link && html`<div class="field"><span>Send this link to ${link.email}. It works once.</span><${Copy} text=${link.url} /></div>`}
      </div>
      ${m.data.invites.length > 0 && html`<div class="table-wrap b-top"><table class="t"><thead><tr><th>Pending invite</th><th>Role</th><th class="hide-sm">Expires</th><th class="act"></th></tr></thead>
        <tbody>${m.data.invites.map((x) => html`<tr><td>${x.email}<div class="small muted">by ${x.created_by}</div></td><td class="ink2">${roleName(x.role)}</td>
          <td class="ink2 nowrap hide-sm">${when(x.expires_at)}</td>
          <td class="act"><${Btn} size="sm" kind="quiet-danger" onClick=${() => withdraw(x)}>Withdraw</${Btn}></td></tr>`)}</tbody></table></div>`}`}
  </section>`;
}

// ---------- the instance (super admins) ----------
function InstanceSettings() {
  const d = useLoad(() => api('/api/instance'));
  if (d.error) return html`<${ErrorNote} error=${d.error} retry=${d.reload} />`;
  if (!d.data) return html`<${Loading} />`;
  const x = d.data;
  return html`<div class="stack">
    <div class="grid-2">
      <div class="stack"><${PublicUrlPanel} d=${x} /><${InstanceSsoPanel} /></div>
      <div class="stack"><${RetentionPanel} value=${x} />
        <section class="panel"><div class="panel-h"><h2>Encryption</h2></div><div class="panel-b"><p class="small ink2">
          ${x.secret_from_env
            ? 'Keys and notification tokens are encrypted with FLEETDECK_SECRET from the environment.'
            : html`Keys and notification tokens are encrypted with <span class="mono">data/secret.key</span>, stored next to the database. Set <span class="mono">FLEETDECK_SECRET</span> to keep the key out of backups of the data folder.`}</p></div></section></div>
    </div>
    <${OrgsPanel} list=${x.tenants} onChange=${() => d.reload(true)} />
    <${UsersPanel} list=${x.users} me=${x.me} onChange=${() => d.reload(true)} />
  </div>`;
}

function PublicUrlPanel({ d }) {
  const [v, setV] = useState(d.public_url || '');
  const [saved, setSaved] = useState(d.public_url || '');
  const save = async () => { await api('/api/instance', { method: 'PUT', body: { public_url: v } }); setSaved(v); toast('Public URL saved', 'ok'); };
  return html`<section class="panel"><div class="panel-h"><h2>Public URL</h2></div><div class="panel-b form">
    <p class="small ink2">The address people open Fleetdeck at. Single sign-on redirects and invite links use it. Empty: taken from each request, which is fine on localhost for development.</p>
    ${d.public_url_env ? html`<div class="field"><span>Set by <span class="mono">FLEETDECK_PUBLIC_URL</span></span><span class="mono">${d.public_url_env}</span></div>`
      : html`<${Field} label="URL"><input class="mono" placeholder="https://fleetdeck.example.com" value=${v} onInput=${(e) => setV(e.target.value)} /></${Field}>
        <div><${Btn} kind=${v !== saved ? 'primary' : ''} disabled=${v === saved} onClick=${save}>Save</${Btn}></div>`}
  </div></section>`;
}

function InstanceSsoPanel() {
  const sso = useLoad(() => api('/api/sso'));
  const on = sso.data?.configured;
  return html`<section class="panel"><div class="panel-h"><h2>Single sign-on for everyone</h2>${sso.data && (on ? html`<span class="chip ok">On</span>` : html`<span class="chip">Off</span>`)}</div>
    <div class="panel-b form"><p class="small ink2">One provider for every organization. Accounts are matched by email; new people still need an invite. Organizations can add their own provider under Settings → Organization.</p>
      <div><a class=${`btn ${on ? '' : 'primary'}`} href="#/settings/sso">${on ? 'Manage' : 'Set up single sign-on'}</a></div></div></section>`;
}

function OrgsPanel({ list, onChange }) {
  const me = useStore(meStore);
  const sort = useSort(list, 'name');
  const [edit, setEdit] = useState(null);
  const del = async (t) => {
    if (!await confirmBox({ title: `Delete ${t.name}?`, body: 'Its scripts, keys, schedules, alert rules and audit log are deleted too.', confirm: 'Delete organization', danger: true, typeToConfirm: t.slug })) return;
    await api(`/api/instance/tenants/${t.id}`, { method: 'DELETE' });
    toast(`${t.name} deleted`);
    onChange();
  };
  return html`<section class="panel">
    <div class="panel-h"><h2>Organizations</h2><span class="small muted">Each has its own servers, people, scripts and alerts.</span>
      <${Btn} size="sm" icon="plus" class="right" onClick=${() => setEdit({})}>New organization</${Btn}></div>
    <div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="name">Organization</${Th}><${Th} sort=${sort} k="servers" desc class="num">Servers</${Th}><${Th} sort=${sort} k="members" desc class="num">People</${Th}><${Th} sort=${sort} k="created_at" desc class="hide-sm">Created</${Th}><th class="act"></th></tr></thead>
      <tbody>${sort.rows.map((t) => html`<tr>
        <td><b>${t.name}</b>${t.id === me.tenant.id && html` <span class="chip">Current</span>`}<div class="mono small muted">${t.slug}</div></td>
        <td class="num">${t.servers}</td><td class="num">${t.members}</td>
        <td class="ink2 nowrap hide-sm" title=${when(t.created_at)}>${ago(t.created_at)}</td>
        <td class="act"><div class="row nw">
          ${t.id !== me.tenant.id && !me.locked && html`<${Btn} size="sm" kind="ghost" onClick=${() => switchOrg(t.id)}>Open</${Btn}>`}
          <${Btn} size="sm" kind="ghost" icon="edit" title=${`Rename ${t.name}`} onClick=${() => setEdit(t)} />
          <${Btn} size="sm" kind="quiet-danger" icon="trash" title=${t.servers ? 'Remove its servers first' : `Delete ${t.name}`} disabled=${t.servers > 0 || t.id === me.tenant.id} onClick=${() => del(t)} />
        </div></td></tr>`)}</tbody></table></div>
    ${edit && html`<${OrgDialog} org=${edit} onClose=${() => setEdit(null)} onSaved=${() => { onChange(); if (edit.id === me.tenant.id) refreshMe(); }} />`}
  </section>`;
}

function OrgDialog({ org, onClose, onSaved }) {
  const [v, setV] = useState({ name: org.name || '', slug: org.slug || '', admin_email: '' });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const [link, setLink] = useState(null);
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  const save = async () => {
    setErr('');
    try {
      if (org.id) { await api(`/api/instance/tenants/${org.id}`, { method: 'PUT', body: { name: v.name, slug: v.slug } }); onSaved(); onClose(); return; }
      const r = await api('/api/instance/tenants', { method: 'POST', body: v });
      onSaved();
      if (r.invite_url) setLink(r.invite_url); else { toast(`${r.tenant.name} created`, 'ok'); onClose(); }
    } catch (e) { setErr(e.message); }
  };
  if (link) {
    return html`<${Modal} title="Organization created" onClose=${onClose} footer=${html`<button class="btn primary" onClick=${onClose}>Done</button>`}>
      <p class="ink2">Send this link to ${v.admin_email}. Opening it makes them the organization's admin. It works once.</p><${Copy} text=${link} /></${Modal}>`;
  }
  return html`<${Modal} title=${org.id ? `Rename ${org.name}` : 'New organization'} onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label=${org.id ? 'Save' : 'Create organization'} disabled=${!v.name.trim()} />`}>
    <${Field} label="Name"><input value=${v.name} onInput=${set('name')} autofocus /></${Field}>
    <${Field} label="Short name" hint=${org.id ? 'Part of its single sign-on link.' : 'Optional. Made from the name when empty.'}><input class="mono" value=${v.slug} onInput=${set('slug')} /></${Field}>
    ${!org.id && html`<${Field} label="Admin email (optional)" hint="Creates an invite link that makes this person its admin. You can also open the organization and invite people yourself.">
      <input type="email" value=${v.admin_email} onInput=${set('admin_email')} /></${Field}>`}
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}

function UsersPanel({ list, me, onChange }) {
  const sort = useSort(list, 'email');
  const setSuper = async (u, on) => {
    if (!on && !await confirmBox({ title: `Remove super admin rights from ${u.email}?`, body: u.id === me ? 'You lose access to this page and to organizations you are not a member of.' : null, confirm: 'Remove rights', danger: true })) return;
    try { await api(`/api/instance/users/${u.id}`, { method: 'PUT', body: { super_admin: on } }); } catch (e) { toast(e.message, 'bad'); }
    if (u.id === me) { await refreshMe(); go('/settings'); } else onChange();
  };
  const del = async (u) => {
    if (!await confirmBox({ title: `Delete the account ${u.email}?`, body: 'They are removed from every organization and signed out.', confirm: 'Delete account', danger: true })) return;
    await api(`/api/instance/users/${u.id}`, { method: 'DELETE' });
    toast(`${u.email} deleted`);
    onChange();
  };
  return html`<section class="panel">
    <div class="panel-h"><h2>Accounts</h2><span class="small muted">Everyone who can sign in. Super admins manage this Fleetdeck and act as admin in every organization.</span></div>
    <div class="table-wrap"><table class="t"><thead><tr><${Th} sort=${sort} k="email">Account</${Th}><${Th} sort=${sort} k="orgs" class="hide-sm">Organizations</${Th}><${Th} sort=${sort} k="super_admin" desc>Super admin</${Th}><${Th} sort=${sort} k="created_at" desc class="hide-sm">Created</${Th}><th class="act"></th></tr></thead>
      <tbody>${sort.rows.map((u) => html`<tr>
        <td><b>${u.name || u.email}</b>${u.id === me && html` <span class="chip">You</span>`}<div class="small muted">${u.email}</div></td>
        <td class="small ink2 hide-sm">${u.orgs || '–'}</td>
        <td><input type="checkbox" class="switch" role="switch" checked=${!!u.super_admin} aria-label=${`Super admin: ${u.email}`} onChange=${(e) => { e.target.checked = !!u.super_admin; setSuper(u, !u.super_admin); }} /></td>
        <td class="ink2 nowrap hide-sm" title=${when(u.created_at)}>${ago(u.created_at)}</td>
        <td class="act">${u.id !== me && html`<${Btn} size="sm" kind="quiet-danger" icon="trash" title=${`Delete ${u.email}`} onClick=${() => del(u)} />`}</td></tr>`)}</tbody></table></div>
  </section>`;
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
