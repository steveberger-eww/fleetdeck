// The server list's filter, shared by the list column and the fleet page's tiles. Kept per tab (sessionStorage).
import { reactive, watch } from 'vue';
import { attention, byAttention } from './status.js';

const KEY = 'fd-fleet-filter';
let saved = {};
try { saved = JSON.parse(sessionStorage.getItem(KEY)) || {}; } catch { /* private mode */ }
// status: '' | 'problems' | 'warnings' | 'updates'
export const fleetFilter = reactive({ q: '', status: '', tag: '', ...saved });
watch(fleetFilter, (f) => { try { sessionStorage.setItem(KEY, JSON.stringify(f)); } catch { /* private mode */ } });

export const FILTERS = {
  problems: (s) => attention(s) <= 2, // red: unreachable, host key, alerts firing, disk health
  warnings: (s) => attention(s) === 3, // amber: failed units, unhealthy containers, updates, reboot
  updates: (s) => s.snapshot?.updates > 0,
};
export function filterServers(list, f = fleetFilter) {
  const n = f.q.toLowerCase().trim();
  return (list || []).filter((s) => (!f.tag || s.tags.includes(f.tag)) && (!FILTERS[f.status] || FILTERS[f.status](s))
    && (!n || `${s.name} ${s.host} ${s.snapshot?.os || ''} ${s.tags.join(' ')}`.toLowerCase().includes(n)))
    .sort(byAttention);
}
