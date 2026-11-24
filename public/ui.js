// Shared UI kit: API client, stores, router, formatters, components. Preact + htm, no build step.
import { html, render, useState, useEffect, useRef, useMemo, useCallback } from '/vendor/htm.js';
export { html, render, useState, useEffect, useRef, useMemo, useCallback };

// ---------- API ----------
export async function api(path, { method = 'GET', body } = {}) {
  const r = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = (r.headers.get('content-type') || '').includes('json') ? await r.json() : null;
  if (r.status === 401 && !path.startsWith('/api/auth')) dispatchEvent(new Event('fd-signed-out'));
  if (!r.ok) throw Object.assign(new Error(data?.error || `Request failed (${r.status})`), { status: r.status });
  return data;
}

// ---------- tiny stores ----------
export function store(value) {
  const subs = new Set();
  return { get: () => value, set: (v) => { value = typeof v === 'function' ? v(value) : v; subs.forEach((f) => f(value)); }, sub: (f) => (subs.add(f), () => subs.delete(f)) };
}
export function useStore(s) {
  const [v, setV] = useState(s.get());
  useEffect(() => { setV(s.get()); return s.sub(setV); }, [s]);
  return v;
}

export function useLoad(fn, deps = [], { poll } = {}) {
  const [state, setState] = useState({ loading: true, data: null, error: null });
  const reload = useCallback(async (quiet) => {
    if (!quiet) setState((s) => ({ ...s, loading: true }));
    try { setState({ loading: false, data: await fn(), error: null }); } catch (e) { setState((s) => ({ ...s, loading: false, error: e.message })); }
  }, deps);
  useEffect(() => {
    reload();
    if (!poll) return;
    const t = setInterval(() => document.visibilityState === 'visible' && reload(true), poll);
    return () => clearInterval(t);
  }, [reload, poll]); // poll in deps: toggling it (e.g. "Follow" in logs) must start/stop the timer
  return { ...state, reload };
}

// ---------- router (hash based) ----------
export function useRoute() {
  const [h, setH] = useState(location.hash.slice(1) || '/');
  useEffect(() => {
    const f = () => setH(location.hash.slice(1) || '/');
    addEventListener('hashchange', f);
    return () => removeEventListener('hashchange', f);
  }, []);
  return h;
}
export const go = (p) => { location.hash = p; };

// ---------- formatters ----------
export const pct = (v, d = 0) => (v == null || Number.isNaN(v) ? '–' : `${(v * 100).toFixed(d)}%`);
export function bytes(n, d = 1) {
  if (n == null) return '–';
  if (n < 1024) return `${Math.round(n)} B`;
  const u = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return `${n.toFixed(n >= 100 ? 0 : d)} ${u[i]}`;
}
export const rate = (n) => (n == null ? '–' : `${bytes(n)}/s`);
export function ago(t) {
  if (!t) return 'never';
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 0) return `in ${dur(-s * 1000)}`;
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}
export function dur(ms) {
  if (ms == null) return '–';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}
export const uptime = (s) => (s == null ? '–' : dur(s * 1000));
const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
export const when = (t) => (t ? dtf.format(new Date(t)) : '–');
const tf = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const df = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
const lf = new Intl.DateTimeFormat(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
export const logTime = (t) => lf.format(new Date(t));
export const level = (v, warn = 0.75, bad = 0.9) => (v == null ? '' : v >= bad ? 'bad' : v >= warn ? 'warn' : '');

// ---------- toasts & confirm ----------
export const toasts = store([]);
export function toast(msg, kind = '') {
  const id = Math.random();
  toasts.set((l) => [...l, { id, msg, kind }]);
  setTimeout(() => toasts.set((l) => l.filter((t) => t.id !== id)), kind === 'bad' ? 7000 : 3500);
}
export function ToastHost() {
  const list = useStore(toasts);
  return html`<div class="toasts" role="status" aria-live="polite">${list.map((t) => html`<div class=${`toast ${t.kind}`} key=${t.id}>${t.msg}</div>`)}</div>`;
}

const confirms = store(null);
export const confirmBox = (opts) => new Promise((resolve) => confirms.set({ ...opts, resolve }));
export function ConfirmHost() {
  const c = useStore(confirms);
  const [typed, setTyped] = useState('');
  useEffect(() => setTyped(''), [c]);
  if (!c) return null;
  const done = (v) => { confirms.set(null); c.resolve(v); };
  const ok = !c.typeToConfirm || typed === c.typeToConfirm;
  return html`<${Modal} title=${c.title} onClose=${() => done(false)}
    footer=${html`<button class="btn" onClick=${() => done(false)}>Cancel</button>
      <button class=${`btn ${c.danger ? 'danger solid' : 'primary'}`} disabled=${!ok} onClick=${() => done(true)}>${c.confirm || 'Confirm'}</button>`}>
    ${c.body && html`<p class="ink2">${c.body}</p>`}
    ${c.typeToConfirm && html`<label class="field"><span>Type <b>${c.typeToConfirm}</b> to confirm</span>
      <input autofocus value=${typed} onInput=${(e) => setTyped(e.target.value)} onKeyDown=${(e) => e.key === 'Enter' && ok && done(true)} /></label>`}
  </${Modal}>`;
}

// ---------- components ----------
const ICONS = {
  menu: 'M3 6h18M3 12h18M3 18h18', search: 'M11 19a8 8 0 1 1 0-16 8 8 0 0 1 0 16zM21 21l-4.3-4.3', plus: 'M12 5v14M5 12h14',
  play: 'M7 4.5v15l12-7.5z', refresh: 'M20 12a8 8 0 1 1-2.3-5.7M20 4v5h-5', x: 'M6 6l12 12M18 6 6 18',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1', ext: 'M14 4h6v6M20 4l-9 9M18 14v6H4V6h6', term: 'M4 6l6 6-6 6M12 18h8',
  moon: 'M20 14.5A8.5 8.5 0 1 1 9.5 4a7 7 0 0 0 10.5 10.5z', sun: 'M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
  out: 'M15 4h4a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-4M10 8l-4 4 4 4M6 12h11', overview: 'M4 5h16v4H4zM4 10h16v4H4zM4 15h16v4H4z',
  jobs: 'M9 6h11M9 12h11M9 18h11M4.5 6h.01M4.5 12h.01M4.5 18h.01', scripts: 'M8 7l-5 5 5 5M16 7l5 5-5 5',
  clock: 'M12 21a9 9 0 1 1 0-18 9 9 0 0 1 0 18zM12 7v5l3 2', bell: 'M6 16v-5a6 6 0 1 1 12 0v5l2 2H4zM10 21h4',
  key: 'M8 15a4 4 0 1 1 3.5-6M11.5 9 21 9M17 9v4M20 9v3', audit: 'M6 3h9l4 4v14H6zM9 11h7M9 15h7M9 19h4', gear: 'M4 7h9M17 7h3M4 17h3M11 17h9M15 5v4M9 15v4',
  vars: 'M8 4H7a2 2 0 0 0-2 2v4l-2 2 2 2v4a2 2 0 0 0 2 2h1M16 4h1a2 2 0 0 1 2 2v4l2 2-2 2v4a2 2 0 0 1-2 2h-1',
  stop: 'M7 7h10v10H7z', git: 'M6 3v12M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 9a9 9 0 0 1-9 9', trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3', edit: 'M4 20h4L19 9l-4-4L4 16zM13 7l4 4',
};
export const Icon = ({ name, size = 16 }) => html`<svg width=${size} height=${size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d=${ICONS[name]} /></svg>`;

export const Logo = ({ size = 22 }) => html`<svg width=${size} height=${size} viewBox="0 0 24 24" aria-hidden="true">
  <rect x="2" y="3" width="20" height="5" rx="1.5" fill="var(--ink)" /><rect x="2" y="9.5" width="20" height="5" rx="1.5" fill="var(--ink)" /><rect x="2" y="16" width="20" height="5" rx="1.5" fill="var(--ink)" />
  <circle cx="5.5" cy="5.5" r="1.2" fill="var(--ok)" /><circle cx="5.5" cy="12" r="1.2" fill="var(--ok)" /><circle cx="5.5" cy="18.5" r="1.2" fill="var(--warn)" /></svg>`;

// A button that shows a spinner while its async onClick runs and toasts errors.
export function Btn({ onClick, kind = '', size = '', icon, children, title, disabled, type = 'button', class: extra = '', ...rest }) {
  const [busy, setBusy] = useState(false);
  const click = async (e) => {
    if (!onClick) return;
    const r = onClick(e);
    if (!r?.then) return;
    setBusy(true);
    try { await r; } catch (err) { toast(err.message, 'bad'); } finally { setBusy(false); }
  };
  const cls = ['btn', kind, size, icon && !children ? 'icon' : '', extra].filter(Boolean).join(' ');
  return html`<button type=${type} class=${cls} onClick=${click} disabled=${disabled || busy} title=${title} aria-label=${!children ? title : undefined} ...${rest}>
    ${busy ? html`<span class="spin" />` : icon && html`<${Icon} name=${icon} size=${size === 'sm' ? 14 : 16} />`}${children}</button>`;
}

// ---------- sortable tables ----------
// const sort = useSort(rows, 'cpu', -1); then <${Th} sort=${sort} k="cpu" desc>CPU</${Th}> and render sort.rows.
// `k` is a field name; pass get=${(row) => value} for computed columns. Nulls always sort last.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
const blank = (v) => v == null || v === '' || Number.isNaN(v);
export function compare(a, b) {
  if (blank(a)) return blank(b) ? 0 : 1;
  if (blank(b)) return -1;
  return typeof a === 'number' && typeof b === 'number' ? a - b : collator.compare(String(a), String(b));
}
export function useSort(rows, key = null, dir = 1) {
  const [s, setS] = useState({ key, dir, get: null });
  const sorted = useMemo(() => {
    if (!s.key || !rows) return rows || [];
    const get = s.get || ((r) => r[s.key]);
    return rows.slice().sort((a, b) => {
      const va = get(a), vb = get(b);
      return blank(va) || blank(vb) ? compare(va, vb) : s.dir * compare(va, vb); // blanks last either way
    });
  }, [rows, s]);
  const toggle = (k, get, desc) => setS((o) => ({ key: k, get, dir: o.key === k ? -o.dir : desc ? -1 : 1 }));
  return { rows: sorted, key: s.key, dir: s.dir, toggle };
}
export function Th({ sort, k, get, desc, class: cls = '', children }) {
  const on = sort.key === k;
  return html`<th class=${`sortable ${cls}`} aria-sort=${on ? (sort.dir > 0 ? 'ascending' : 'descending') : 'none'}>
    <button type="button" onClick=${() => sort.toggle(k, get, desc)}>${children}<span class="arrow" aria-hidden="true">${on ? (sort.dir > 0 ? '▲' : '▼') : '↕'}</span></button></th>`;
}

export const Lamp = ({ status }) => html`<span class=${`lamp ${status}`} aria-hidden="true" />`;
const STATUS_TEXT = { up: 'Online', down: 'Unreachable', paused: 'Paused', unknown: 'Waiting' };
export const StatusChip = ({ status }) => html`<span class=${`chip ${status === 'up' ? 'ok' : status === 'down' ? 'bad' : ''}`}><${Lamp} status=${status} />${STATUS_TEXT[status] || status}</span>`;
const JOB = { success: ['ok', 'Succeeded'], failed: ['bad', 'Failed'], running: ['info', 'Running'], queued: ['', 'Queued'], cancelled: ['warn', 'Cancelled'], interrupted: ['warn', 'Interrupted'], skipped: ['', 'Skipped'] };
export const JobChip = ({ status }) => html`<span class=${`chip ${JOB[status]?.[0] || ''}`}>${status === 'running' && html`<${Lamp} status="running" />`}${JOB[status]?.[1] || status}</span>`;

export function Gauge({ label, value, sub }) {
  return html`<div class="gauge">
    <div class="lbl"><span>${label}</span><b>${pct(value)}</b></div>
    <div class="bar" role="meter" aria-label=${label} aria-valuemin="0" aria-valuemax="100" aria-valuenow=${value == null ? undefined : Math.round(value * 100)}><i class=${level(value)} style=${{ width: `${Math.round((value || 0) * 100)}%` }} /></div>
    ${sub && html`<div class="small muted trunc">${sub}</div>`}
  </div>`;
}

export function Spark({ points, idx = 1 }) {
  const pts = (points || []).filter((p) => p[idx] != null);
  if (pts.length < 2) return html`<svg class="spark" />`;
  const t0 = pts[0][0];
  const span = pts[pts.length - 1][0] - t0 || 1;
  const d = pts.map((p, i) => `${i ? 'L' : 'M'}${(((p[0] - t0) / span) * 100).toFixed(2)},${(20 - p[idx] * 20).toFixed(2)}`).join('');
  return html`<svg class="spark" viewBox="0 0 100 20" preserveAspectRatio="none" aria-hidden="true"><path class="area" d=${`${d}L100,20L0,20Z`} /><path d=${d} /></svg>`;
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const e = 10 ** Math.floor(Math.log10(v));
  const m = v / e;
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * e;
}

// Line chart with recessive grid, one y-axis, crosshair + tooltip. Gaps in data break the line.
export function LineChart({ title, series, data, fmt = (v) => v.toFixed(1), yMax, current }) {
  const ref = useRef();
  const [w, setW] = useState(420);
  const [hover, setHover] = useState(null);
  useEffect(() => {
    const ro = new ResizeObserver(([e]) => setW(Math.max(200, e.contentRect.width)));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  const H = 150, pl = 46, pr = 8, pt = 8, pb = 22;
  const pts = data?.points || [];
  const from = data?.from || Date.now() - 3600e3;
  const to = data?.to || Date.now();
  const max = yMax ?? niceMax(Math.max(0, ...pts.flatMap((p) => series.map((s) => p[s.key] ?? 0))));
  const x = (t) => pl + ((t - from) / (to - from)) * (w - pl - pr);
  const y = (v) => pt + (1 - Math.min(v, max) / max) * (H - pt - pb);
  const gap = (data?.bucket || 60e3) * 2.5;
  const path = (key) => {
    let d = '';
    let prev = null;
    for (const p of pts) {
      if (p[key] == null) { prev = null; continue; }
      d += `${prev && p.t - prev.t <= gap ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p[key]).toFixed(1)}`;
      prev = p;
    }
    return d;
  };
  const span = to - from;
  const tfmt = (t) => (span <= 864e5 ? tf : df).format(new Date(t));
  const xticks = [0.02, 0.35, 0.68, 0.98].map((f) => from + f * span);
  const move = (e) => {
    if (!pts.length) return;
    const r = e.currentTarget.getBoundingClientRect();
    const t = from + ((e.clientX - r.left - pl) / (w - pl - pr)) * span;
    let best = pts[0];
    for (const p of pts) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    setHover(best);
  };
  const legend = series.length > 1;
  return html`<div class="panel chart">
    <div class="chart-h"><h3>${title}</h3>
      ${legend && html`<div class="legend">${series.map((s) => html`<span><i style=${{ background: s.color }} />${s.label}</span>`)}</div>`}
      ${current && html`<span class="now">${current}</span>`}</div>
    <div class="plot" ref=${ref}>
      <svg viewBox=${`0 0 ${w} ${H}`} onPointerMove=${move} onPointerLeave=${() => setHover(null)} role="img" aria-label=${`${title} chart`}>
        <g class="grid">${[0, 0.5, 1].map((f) => html`<line x1=${pl} x2=${w - pr} y1=${y(max * f)} y2=${y(max * f)} />`)}</g>
        <g class="axis">
          ${[0, 0.5, 1].map((f) => html`<text x=${pl - 6} y=${y(max * f) + 4} text-anchor="end">${fmt(max * f)}</text>`)}
          ${xticks.map((t, i) => html`<text x=${x(t)} y=${H - 4} text-anchor=${i === 0 ? 'start' : i === 3 ? 'end' : 'middle'}>${tfmt(t)}</text>`)}
        </g>
        ${series.map((s) => html`<path class="line" d=${path(s.key)} stroke=${s.color} />`)}
        ${!pts.length && html`<text x=${(w + pl) / 2} y=${H / 2} text-anchor="middle" fill="var(--muted)" font-size="13">No data for this range yet</text>`}
        ${hover && html`<g>
          <line class="cross" x1=${x(hover.t)} x2=${x(hover.t)} y1=${pt} y2=${H - pb} />
          ${series.map((s) => hover[s.key] != null && html`<circle class="dot" cx=${x(hover.t)} cy=${y(hover[s.key])} r="4" fill=${s.color} />`)}
        </g>`}
      </svg>
      ${hover && html`<div class="tip" style=${{ left: `${Math.min(Math.max(x(hover.t) + 10, 0), w - 170)}px` }}>
        <div class="k">${when(hover.t)}</div>
        ${series.map((s) => html`<div><i style=${{ background: s.color }} />${s.label}: <b>${hover[s.key] == null ? '–' : fmt(hover[s.key])}</b></div>`)}
      </div>`}
    </div>
  </div>`;
}

export function Modal({ title, onClose, children, footer, wide }) {
  const ref = useRef();
  useEffect(() => {
    const d = ref.current;
    if (!d.open) d.showModal();
    const cancel = (e) => { e.preventDefault(); onClose?.(); };
    d.addEventListener('cancel', cancel);
    return () => d.removeEventListener('cancel', cancel);
  }, []);
  return html`<dialog class=${`modal ${wide ? 'wide' : ''}`} ref=${ref} aria-label=${title}>
    <div class="modal-h"><h2 class="grow">${title}</h2><button class="btn ghost icon sm" onClick=${onClose} aria-label="Close"><${Icon} name="x" /></button></div>
    <div class="modal-b">${children}</div>
    ${footer && html`<div class="modal-f">${footer}</div>`}
  </dialog>`;
}

export const Empty = ({ title, children, action }) => html`<div class="empty"><h3>${title}</h3>${children && html`<p>${children}</p>`}${action}</div>`;
export const Field = ({ label, hint, children }) => html`<label class="field"><span>${label}</span>${children}${hint && html`<span class="hint">${hint}</span>`}</label>`;
export const Loading = () => html`<div class="stack" style=${{ padding: '16px' }}><div class="skel" style=${{ width: '60%' }} /><div class="skel" /><div class="skel" style=${{ width: '80%' }} /></div>`;
export const ErrorNote = ({ error, retry }) => html`<div class="alert-bar"><span class="grow">${error}</span>${retry && html`<button class="btn sm" onClick=${() => retry()}>Try again</button>`}</div>`;

export function Copy({ text, label = 'Copy' }) {
  return html`<div class="copy"><code>${text}</code><${Btn} size="sm" icon="copy" onClick=${async () => { await navigator.clipboard.writeText(text); toast('Copied to clipboard', 'ok'); }}>${label}</${Btn}></div>`;
}

// Auto-scrolling output pane; stays pinned to the bottom unless the user scrolls up.
export function Output({ text, style }) {
  const ref = useRef();
  const pinned = useRef(true);
  useEffect(() => { if (pinned.current && ref.current) ref.current.scrollTop = ref.current.scrollHeight; }, [text]);
  return html`<pre class="out" ref=${ref} style=${style} onScroll=${(e) => { const el = e.target; pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 30; }}>${text || ' '}</pre>`;
}

// Servers list, shared by the shell, overview and pickers.
export const serversStore = store(null);
export async function refreshServers() {
  try { serversStore.set(await api('/api/servers')); } catch { /* shell shows stale data */ }
}
