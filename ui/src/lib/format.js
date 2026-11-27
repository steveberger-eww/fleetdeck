// Formatters. All return '–' for missing values.
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
export const traffic = (t) => t && `↓ ${bytes(t.rx)} ↑ ${bytes(t.tx)}, last ${t.days} days`;
export function dur(ms) {
  if (ms == null) return '–';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}
export function ago(t) {
  if (!t) return 'never';
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 0) return `in ${dur(-s * 1000)}`;
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}
export const uptime = (s) => (s == null ? '–' : dur(s * 1000));
// OS end of life from the snapshot's osEol (YYYY-MM-DD): nothing while more than ~6 months away.
export function eol(n) {
  if (!n?.osEol) return null;
  const t = new Date(`${n.osEol}T00:00:00`);
  const month = t.toLocaleDateString(undefined, { month: 'short', year: 'numeric' });
  if (t <= Date.now()) return { kind: 'bad', text: `End of life since ${month}` };
  if (t - Date.now() < 183 * 864e5) return { kind: 'warn', text: `Support ends ${month}` };
  return null;
}
// Clock: '' = the locale's default, '24' or '12'. Read once at load; changing it reloads the page.
export function getClock() {
  try { return localStorage.getItem('fd-clock') || ''; } catch { return ''; }
}
export function setClock(c) {
  try { localStorage.setItem('fd-clock', c); } catch { /* private mode */ }
  location.reload();
}
const hc = { 24: { hourCycle: 'h23' }, 12: { hourCycle: 'h12' } }[getClock()];
const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', ...hc });
export const when = (t) => (t ? dtf.format(new Date(t)) : '–');
export const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', ...hc });
export const dayFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
const lf = new Intl.DateTimeFormat(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', ...hc });
export const logTime = (t) => lf.format(new Date(t));
// '', 'warn' or 'bad' for a 0..1 usage value.
export const level = (v, warn = 0.75, bad = 0.9) => (v == null ? '' : v >= bad ? 'bad' : v >= warn ? 'warn' : '');
export const plural = (n, word, many = `${word}s`) => `${n} ${n === 1 ? word : many}`;
export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

// Terminal colors and cursor moves mean nothing in a <pre>; drop them.
// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;
export const stripAnsi = (s) => (s || '').replace(ANSI, '');

// Sort compare: numbers numerically, strings naturally; blanks always last.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
export const blank = (v) => v == null || v === '' || Number.isNaN(v);
export function compare(a, b) {
  if (blank(a)) return blank(b) ? 0 : 1;
  if (blank(b)) return -1;
  return typeof a === 'number' && typeof b === 'number' ? a - b : collator.compare(String(a), String(b));
}
