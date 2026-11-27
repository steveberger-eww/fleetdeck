// Helpers shared by the server page's tabs.
import { router } from '@/router.js';
import { startJob } from '@/lib/actions.js';
import { toast } from '@/lib/store.js';

// Start a script on this server and open its job.
export async function runOn(s, scriptId, params) {
  router.push(`/jobs/${await startJob(s.id, scriptId, params)}`);
}

// For plain buttons (menu items): run an async action and toast its error. <Btn> does this by itself.
export const attempt = (fn) => () => fn().catch((e) => toast(e.message, 'bad'));

export const logsTo = (s, unit) => ({ path: `/servers/${s.id}/logs`, query: { unit } });

// Hosts in URLs: IPv6 needs brackets.
export const urlHost = (h) => (h.includes(':') ? `[${h}]` : h);

export const RANGES = ['1h', '6h', '24h', '7d', '30d'];
