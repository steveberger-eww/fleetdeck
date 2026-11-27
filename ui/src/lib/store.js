// App-wide state: who is signed in, the servers list, running jobs, toasts, confirms and the Run dialog.
import { reactive, ref, shallowRef } from 'vue';
import { api } from './api.js';

// ---------- session ----------
// me: { user, tenant, tenants, role, locked }. Loaded once by the app; switching organizations reloads the page.
export const me = shallowRef(null);
export async function refreshMe() { me.value = await api('/api/me'); }
const LEVEL = { viewer: 1, operator: 2, owner: 3, admin: 4 };
// can('owner'): the role in this organization is owner or higher. The backend enforces it; this only hides what would fail.
export const can = (role) => (LEVEL[me.value?.role] || 0) >= LEVEL[role];
export const isSuper = () => !!me.value?.user?.super_admin;
export const ROLES = [['admin', 'Admin'], ['owner', 'Owner'], ['operator', 'Operator'], ['viewer', 'Viewer']];
export const roleName = (r) => ROLES.find(([k]) => k === r)?.[1] || r;
export async function switchOrg(id) {
  await api('/api/me/tenant', { method: 'POST', body: { id } });
  location.hash = '#/';
  location.reload();
}
// A personal organization: only its creator sees it. Creating one switches to it.
export async function createPersonal() {
  await api('/api/me/personal', { method: 'POST' });
  location.hash = '#/';
  location.reload();
}
export async function signOut() {
  await api('/api/auth/logout', { method: 'POST' });
  location.hash = '';
  location.reload();
}

// ---------- fleet ----------
// servers: null until the first load. Polled by the shell every 15 s.
export const servers = shallowRef(null);
export const running = ref(0);
export async function refreshServers() {
  try { servers.value = await api('/api/servers'); } catch { /* keep stale data */ }
}
export async function refreshRunning() {
  try { running.value = (await api('/api/jobs?status=running&limit=200')).length; } catch { /* keep last */ }
}

// ---------- toasts ----------
// toast('Saved', 'ok'), toast(err.message, 'bad'), toast('Muted', 'ok', { label: 'Quiet hours', to: '/alerts' })
export const toasts = reactive([]);
export const dropToast = (id) => { const i = toasts.findIndex((t) => t.id === id); if (i >= 0) toasts.splice(i, 1); };
export function toast(msg, kind = '', action = null) {
  const id = Math.random();
  toasts.push({ id, msg, kind, action });
  setTimeout(() => dropToast(id), kind === 'bad' ? 10_000 : 4000);
}

// ---------- confirm ----------
// await confirmBox({ title, body, confirm: 'Delete', danger: true, typeToConfirm: 'web-1' }) → true/false
export const confirmState = shallowRef(null);
export const confirmBox = (opts) => new Promise((resolve) => { confirmState.value = { ...opts, resolve }; });
export function answerConfirm(v) {
  const c = confirmState.value;
  if (!c) return;
  confirmState.value = null;
  c.resolve(v);
}
// Leaving the page answers an open confirm with "no".
export const dismissConfirm = () => answerConfirm(false);

// ---------- Run dialog (hosted by the shell) ----------
// openRun({ targets: { servers: [id] }, scriptId: 'update' })
export const runRequest = shallowRef(null);
export const openRun = (opts = {}) => { runRequest.value = opts; };
export const closeRun = () => { runRequest.value = null; };
