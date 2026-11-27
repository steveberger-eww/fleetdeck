// Alert rule helpers for the Alerts page and its rule dialog.
export const NEEDS_THRESHOLD = (m) => !['down', 'reboot', 'check', 'process', 'container'].includes(m);
export const NAMED = (m) => ['process', 'container'].includes(m); // the name goes in the rule's process field
export const HAS_DURATION = (m) => ['down', 'check', 'cpu', 'mem', 'swap', 'load', 'temp', 'net', 'gpu', 'custom'].includes(m);
export const FOR_AT_LEAST = (m) => ['down', 'check'].includes(m); // minutes are a duration, not an average
export const HAS_MOUNT = (m) => ['disk', 'inodes'].includes(m);
const unitOf = (u) => (!u ? '' : /^[%°]/.test(u) ? u : ` ${u}`);
export const inScope = (r, servers) => servers.filter((s) => (r.server_id ? s.id === r.server_id : !r.tag || s.tags.includes(r.tag)));
// Servers in the rule's scope that report disks but not the rule's mount.
export const mountMissing = (r, servers) => (r.mount ? inScope(r, servers).filter((s) => s.snapshot?.disks && !s.snapshot.disks.some((d) => d.mount === r.mount)) : []);

export function ruleText(r, metrics) {
  const m = metrics[r.metric];
  if (r.metric === 'down') return `Unreachable for ${r.minutes} min`;
  if (r.metric === 'check') return `Endpoint check failing for ${r.minutes} min`;
  if (r.metric === 'reboot') return 'Reboot required';
  if (r.metric === 'process') return `Process ${r.process} not running`;
  if (r.metric === 'container') return `Container ${r.process} not running`;
  if (r.metric === 'custom') return `${r.custom_name} above ${r.threshold}${unitOf(r.custom_unit)}${r.minutes ? ` for ${r.minutes} min` : ''}`;
  const label = r.mount ? `${m.label.replace(' (fullest mount)', '')} on ${r.mount}` : m.label;
  return `${label} ${m.below ? 'below' : 'above'} ${r.threshold}${m.unit}${HAS_DURATION(r.metric) && r.minutes ? ` for ${r.minutes} min` : ''}`;
}

// A ScopeSelect value as the API's { tag, server_id }.
export const scopeBody = (scope) => {
  const tag = scope.startsWith('tag:') ? scope.slice(4) : null;
  return { tag, server_id: tag ? null : scope || null };
};
