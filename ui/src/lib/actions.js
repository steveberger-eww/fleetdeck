// Actions shared by several pages.
import { api } from './api.js';
import { confirmBox, toast } from './store.js';
import { plural } from './format.js';

// The one guard for destructive runs. Every way of starting a job (Run dialog, re-run, schedule "Run now",
// fleet update, server Reboot) goes through here. Resolves true when it may go ahead.
const DANGER = new Set(['reboot', 'shutdown']);
export async function confirmRun({ scriptId, name, count, names = [] }) {
  const danger = DANGER.has(scriptId);
  const fleetUpdate = ['update', 'upgrade-packages'].includes(scriptId) && count > 1;
  if (!danger && !fleetUpdate) return true;
  const where = count === 1 && names[0] ? names[0] : plural(count, 'server');
  return confirmBox({
    title: `${name} on ${where}?`,
    body: danger ? 'Everything running there will be unavailable until it is back up.' : 'Installs the pending package upgrades. Services can restart while it runs.',
    confirm: danger ? name : 'Install updates', danger,
    typeToConfirm: count > 1 ? plural(count, 'server') : danger ? names[0] : undefined,
  });
}

// Targets { all, servers: [id], tags: [tag] } → the server objects they cover.
export function resolveTargets(t, servers) {
  if (t.all) return servers;
  const ids = new Set(t.servers || []);
  const tags = new Set(t.tags || []);
  return servers.filter((s) => ids.has(s.id) || s.tags.some((x) => tags.has(x)));
}

// Start a script on one server; resolves the job id.
export async function startJob(serverId, scriptId, params) {
  const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: scriptId, params, targets: { servers: [serverId] } } });
  return id;
}

// Mute a server's notifications for `hours` (a one-off quiet window).
export const muteFor = (serverId, hours, name) => api('/api/mutes', { method: 'POST', body: { kind: 'once', server_id: serverId, name, start: Date.now(), end: Date.now() + hours * 3600e3 } });

// Copy to the clipboard and say so.
export async function copyText(text, what = 'Copied to clipboard') {
  await navigator.clipboard.writeText(text);
  toast(what, 'ok');
}

// Offer text as a file download.
export function download(text, name, type = 'text/plain') {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([text], { type }));
  a.download = name.replace(/[^\w.-]+/g, '-');
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
