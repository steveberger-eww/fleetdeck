// Applies the saved theme before the app renders (separate file: CSP forbids inline scripts).
export function getTheme() {
  try { return localStorage.getItem('fd-theme') || 'system'; } catch { return 'system'; }
}
export function setTheme(t) {
  try { localStorage.setItem('fd-theme', t); } catch { /* private mode */ }
  applyTheme(t);
}
function applyTheme(t) {
  if (t === 'system') delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
}
applyTheme(getTheme());
