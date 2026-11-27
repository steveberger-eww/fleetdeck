// Theme: dark by default; 'light' pins light, 'system' follows the OS. Loaded as its own module before the app so the first paint is right.
export function getTheme() {
  try { return localStorage.getItem('fd-theme') || 'dark'; } catch { return 'dark'; }
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
