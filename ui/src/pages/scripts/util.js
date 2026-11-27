// Shared by the scripts and schedules pages.
export const NEXT_WHEN = { success: 'when it succeeds', failure: 'when it fails', always: 'always' };

export const PRESETS = [['Every hour', '0 * * * *'], ['Daily at 04:00', '0 4 * * *'], ['Sundays at 03:00', '0 3 * * 0'], ['Weekdays at 09:00', '0 9 * * 1-5'], ['1st of the month', '0 5 1 * *']];
export const cronLabel = (c) => PRESETS.find(([, x]) => x === c.trim())?.[0];

export function targetText(t, servers) {
  if (t.all) return 'All servers';
  const names = (t.servers || []).map((id) => servers.find((s) => s.id === id)?.name || 'removed server');
  return [...names, ...(t.tags || []).map((x) => `tag ${x}`)].join(', ');
}
