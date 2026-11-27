import { dur, plural, compare } from './format.js';

export const STATUS_TEXT = { up: 'Online', down: 'Unreachable', paused: 'Paused', unknown: 'Waiting', running: 'Running', warn: 'Online, has warnings', bad: 'Online, has problems' };

// What needs attention on a server, most urgent first: [kind, text, title]. kind: bad | warn | quiet | ''.
export function flagList(s) {
  const n = s.snapshot;
  const f = [];
  if (s.status === 'down') f.push(['bad', `Unreachable${s.error ? ` for ${dur(Date.now() - s.error.since)}` : ''}`, s.error?.message]);
  if (s.ssh?.hostKeyMismatch) f.push(['bad', 'Host key changed']);
  if (s.alerts) f.push(['bad', plural(s.alerts, 'alert')]);
  if (n?.smart?.disks?.some((d) => d.problem)) f.push(['bad', 'Disk health']);
  // Red is for "down or about to lose data"; a failed unit or container is worth a look, not an alarm.
  if (n?.failed) f.push(['warn', plural(n.failed, 'failed unit')]);
  if (n?.unhealthy) f.push(['warn', plural(n.unhealthy, 'unhealthy container')]);
  if (n?.security) f.push(['warn', `${n.security} security`]);
  if (n?.reboot) f.push(['warn', 'Reboot needed']);
  if (n?.updates) f.push(['warn', plural(n.updates, 'update')]);
  if (s.status === 'paused') f.push(['', 'Paused']);
  if (!f.length && s.status === 'up') f.push(['quiet', 'Healthy']);
  return f;
}
// Sort key: unreachable/host key 0, alerts 1, disk health 2, warnings 3, healthy 4, paused 5.
export function attention(s) {
  if (s.status === 'paused') return 5;
  if (s.status === 'down' || s.ssh?.hostKeyMismatch) return 0;
  if (s.alerts) return 1;
  const f = flagList(s);
  return f.some(([k]) => k === 'bad') ? 2 : f.some(([k]) => k === 'warn') ? 3 : 4;
}
export const byAttention = (a, b) => attention(a) - attention(b) || compare(a.name, b.name);
// The lamp color for a server row: its attention level, so a server with a failed unit doesn't look healthy.
export const lampOf = (s) => (s.status !== 'up' ? s.status : ['bad', 'bad', 'bad', 'warn'][attention(s)] || 'up');
export const rootDisk = (s) => s.snapshot?.disks?.find((d) => d.mount === '/') || s.snapshot?.disks?.[0];

// Docker: same as the server's Containers tab badge. Non-zero exits don't count: docker stop often ends in 137 or 143.
export const containerProblem = (c) => c.health === 'unhealthy' || c.state === 'restarting' || c.state === 'dead';

// Job status → [chip kind, label]. Success is the normal case: quiet, so failed rows stand out.
export const JOB_STATUS = { success: ['quiet', 'Succeeded'], failed: ['bad', 'Failed'], running: ['info', 'Running'], queued: ['', 'Queued'], cancelled: ['warn', 'Cancelled'], interrupted: ['warn', 'Interrupted'], skipped: ['', 'Skipped'] };
