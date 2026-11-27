<script setup>
// Fleet health: the detail pane when no server is open. The server list column is the fleet table;
// this page answers "what needs me?" — triage tiles (they filter the list), alerts, a to-do list with fixes, load and jobs.
import { ref, computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Btn, Icon, Lamp, CellBar, Empty, Loading, Flags, Callout, JobChip } from '@/components';
import AddServerDialog from './AddServerDialog.vue';
import FleetList from '@/layout/FleetList.vue';
import { api } from '@/lib/api.js';
import { useLoad, setQuery } from '@/lib/use.js';
import { servers, running, can, openRun } from '@/lib/store.js';
import { fleetFilter, FILTERS } from '@/lib/fleet.js';
import { attention, byAttention, rootDisk } from '@/lib/status.js';
import { ago, plural, compare, dur } from '@/lib/format.js';
import { confirmRun } from '@/lib/actions.js';

const route = useRoute();
const router = useRouter();
const adding = computed(() => route.query.add && can('owner'));
const admin = can('admin');
// The firing list is admins'; others see counts per server.
const alerts = useLoad(() => (admin ? api('/api/alerts') : null), { poll: admin ? 30_000 : 0 });
const jobs = useLoad(() => api('/api/jobs?limit=8'), { poll: 10_000 });
const failedJobs = useLoad(() => api('/api/jobs?status=failed&limit=50'), { poll: 30_000 });

const all = computed(() => servers.value || []);
const stats = computed(() => {
  const s = all.value;
  const withUpdates = s.filter(FILTERS.updates);
  return {
    up: s.filter((x) => x.status === 'up').length,
    paused: s.filter((x) => x.status === 'paused').length,
    problems: s.filter(FILTERS.problems).length,
    warnings: s.filter(FILTERS.warnings).length,
    withUpdates,
    updates: withUpdates.reduce((a, x) => a + x.snapshot.updates, 0),
    security: s.reduce((a, x) => a + (x.snapshot?.security || 0), 0),
    failed24: (failedJobs.data || []).filter((j) => j.created_at > Date.now() - 864e5).length,
  };
});
const firing = computed(() => alerts.data?.firing || []);
const filterBy = (k) => { fleetFilter.status = fleetFilter.status === k ? '' : k; };

// One row per server that needs something, with the fix that fits best.
const todo = computed(() => all.value.filter((s) => attention(s) <= 3).sort(byAttention));
function fix(s) {
  const n = s.snapshot;
  if (s.ssh?.hostKeyMismatch) return can('owner') && { label: 'Review host key', to: `/servers/${s.id}` };
  if (s.status === 'down') return can('owner') && { label: 'Connection', to: `/servers/${s.id}/settings` };
  if (n?.failed) return { label: 'Failed services', to: { path: `/servers/${s.id}/services`, query: { show: 'failed' } } };
  if (n?.unhealthy) return { label: 'Containers', to: `/servers/${s.id}/containers` };
  if (n?.updates && can('operator')) return { label: 'Update', run: () => openRun({ targets: { servers: [s.id] }, scriptId: 'update' }) };
  return null;
}

// Busiest servers by one resource.
const METRICS = [['cpu', 'CPU', (s) => s.snapshot?.cpu], ['mem', 'Memory', (s) => s.snapshot?.mem], ['disk', 'Disk', (s) => rootDisk(s)?.pct]];
const metric = ref('cpu');
const busiest = computed(() => {
  const get = METRICS.find(([k]) => k === metric.value)[2];
  return all.value.filter((s) => s.status === 'up' && get(s) != null).sort((a, b) => compare(get(b), get(a))).slice(0, 6).map((s) => ({ s, v: get(s) }));
});

async function installUpdates() {
  const w = stats.value.withUpdates;
  if (!await confirmRun({ scriptId: 'update', name: 'Update packages', count: w.length, names: w.map((s) => s.name) })) return;
  const { id } = await api('/api/jobs', { method: 'POST', body: { script_id: 'update', targets: { servers: w.map((s) => s.id) } } });
  router.push(`/jobs/${id}`);
}
const hosts = (j) => j.runs.filter((r) => r.server_id !== '__ansible__');
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Fleet health</h1><p class="sub">{{ servers ? `${plural(all.length, 'server')}, checked every minute` : 'Loading…' }}</p></div>
      <div v-if="can('owner') && all.length" class="actions"><Btn icon="plus" @click="setQuery(router, route, { add: '1' })">Add server</Btn></div>
    </div>

    <Loading v-if="!servers" :rows="5" />

    <div v-else-if="!all.length" class="panel">
      <Empty v-if="can('owner')" title="Add your first server" icon="server">
        Fleetdeck connects over SSH, so nothing gets installed on the server. You need its address and a user that can log in with a key.
        <template #action><Btn kind="primary" icon="plus" @click="setQuery(router, route, { add: '1' })">Add server</Btn></template>
      </Empty>
      <Empty v-else title="No servers for you yet" icon="server">An admin or a server's owner can give you access on the server's Settings tab.</Empty>
    </div>

    <template v-else>
      <div class="stats" role="group" aria-label="Filter the server list">
        <button type="button" :class="['stat', 'all', stats.up + stats.paused < all.length ? 'bad' : 'ok']" :aria-pressed="!fleetFilter.status" title="Show all servers" @click="fleetFilter.status = ''">
          <b>{{ stats.up }}<small> / {{ all.length }}</small></b><span>Online{{ stats.paused ? `, ${stats.paused} paused` : '' }}</span></button>
        <button type="button" :class="['stat', stats.problems ? 'bad' : 'zero']" :disabled="!stats.problems && fleetFilter.status !== 'problems'" :aria-pressed="fleetFilter.status === 'problems'"
          title="Unreachable, host key changed, alerts firing or failing disks" @click="filterBy('problems')"><b>{{ stats.problems }}</b><span>Critical</span></button>
        <button type="button" :class="['stat', stats.warnings ? 'warn' : 'zero']" :disabled="!stats.warnings && fleetFilter.status !== 'warnings'" :aria-pressed="fleetFilter.status === 'warnings'"
          title="Failed units, unhealthy containers, pending updates or a reboot" @click="filterBy('warnings')"><b>{{ stats.warnings }}</b><span>Warnings</span></button>
        <button type="button" :class="['stat', stats.updates ? (stats.security ? 'warn' : 'info') : 'zero']" :disabled="!stats.updates && fleetFilter.status !== 'updates'" :aria-pressed="fleetFilter.status === 'updates'"
          @click="filterBy('updates')"><b>{{ stats.updates }}</b><span>Updates{{ stats.security ? `, ${stats.security} security` : '' }}</span></button>
        <RouterLink :class="['stat', stats.failed24 ? 'bad' : running ? 'info' : 'zero']" :to="stats.failed24 || !running ? '/jobs?status=failed' : '/jobs?status=running'">
          <b>{{ stats.failed24 || running }}</b><span>{{ stats.failed24 ? 'Failed jobs, 24 h' : running ? 'Jobs running' : 'Failed jobs, 24 h' }}</span></RouterLink>
      </div>

      <Callout v-if="firing.length" kind="bad" :title="`${plural(firing.length, 'alert')} firing`">
        <ul><li v-for="a in firing.slice(0, 4)" :key="`${a.server_id}-${a.message}`"><RouterLink :to="`/servers/${a.server_id}`">{{ a.server_name }}</RouterLink>: {{ a.message }} <span class="muted">({{ ago(a.since) }})</span></li>
          <li v-if="firing.length > 4" class="muted">and {{ firing.length - 4 }} more</li></ul>
        <template v-if="admin" #actions><RouterLink class="btn sm" to="/alerts">Alerts</RouterLink></template>
      </Callout>

      <FleetList class="fl-inline" />

      <div class="dash">
        <section class="panel">
          <div class="panel-h"><h2>Needs attention</h2><span v-if="todo.length" class="count muted">{{ todo.length }}</span>
            <Btn v-if="stats.withUpdates.length && can('operator')" class="right" size="sm" icon="refresh" @click="installUpdates">Install updates on {{ plural(stats.withUpdates.length, 'server') }}</Btn></div>
          <ul v-if="todo.length" class="todo">
            <li v-for="s in todo" :key="s.id" :class="attention(s) <= 2 ? 'is-bad' : 'is-warn'">
              <Lamp :status="s.status === 'up' ? (attention(s) <= 2 ? 'bad' : 'warn') : s.status" label />
              <div class="grow">
                <RouterLink class="nm" :to="`/servers/${s.id}`">{{ s.name }}</RouterLink>
                <div class="why"><Flags :s="s" /></div>
                <p v-if="s.status === 'down' && s.error" class="xs muted trunc" :title="s.error.message">{{ s.error.message }}</p>
              </div>
              <template v-if="fix(s)">
                <Btn v-if="fix(s).run" size="sm" @click="fix(s).run">{{ fix(s).label }}</Btn>
                <RouterLink v-else class="btn sm" :to="fix(s).to">{{ fix(s).label }}</RouterLink>
              </template>
              <RouterLink class="btn ghost icon sm" :to="`/servers/${s.id}`" :aria-label="`Open ${s.name}`" :title="`Open ${s.name}`"><Icon name="chevr" :size="14" /></RouterLink>
            </li>
          </ul>
          <Empty v-else title="Nothing needs attention" icon="check">{{ plural(stats.up, 'server') }} online and healthy.</Empty>
        </section>

        <div class="stack">
          <section class="panel">
            <div class="panel-h"><h2>Busiest</h2>
              <div class="seg right" role="group" aria-label="Resource"><button v-for="[k, l] in METRICS" :key="k" type="button" :aria-pressed="metric === k" @click="metric = k">{{ l }}</button></div></div>
            <ul v-if="busiest.length" class="busy">
              <li v-for="{ s, v } in busiest" :key="s.id"><RouterLink class="trunc" :to="`/servers/${s.id}`">{{ s.name }}</RouterLink><CellBar :value="v" /></li>
            </ul>
            <p v-else class="panel-b small muted">No live metrics yet.</p>
          </section>

          <section class="panel">
            <div class="panel-h"><h2>Recent jobs</h2><RouterLink class="right small" to="/jobs">All jobs</RouterLink></div>
            <ul v-if="jobs.data?.length" class="jobs">
              <li v-for="j in jobs.data" :key="j.id">
                <RouterLink class="trunc" :to="`/jobs/${j.id}`">{{ j.name }}</RouterLink>
                <span class="xs muted nowrap">{{ hosts(j).length === 1 ? hosts(j)[0].server_name : plural(hosts(j).length, 'server') }} · {{ j.finished_at ? dur(j.finished_at - j.created_at) : ago(j.created_at) }}</span>
                <JobChip :status="j.status" />
              </li>
            </ul>
            <p v-else-if="!jobs.loading" class="panel-b small muted">No jobs yet.</p>
          </section>
        </div>
      </div>
    </template>

    <AddServerDialog v-if="adding" @close="setQuery(router, route, { add: null })" />
  </div>
</template>

<style scoped>
.dash { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(280px, 1fr); gap: 14px; align-items: start; }
.stat.all[aria-pressed="true"] { border-color: var(--line); box-shadow: var(--shadow-sm); }
.todo, .busy, .jobs { list-style: none; margin: 0; padding: 0; }
.todo li { display: flex; align-items: flex-start; gap: 10px; padding: 9px 12px; border-bottom: 1px solid var(--line); }
.todo li:last-child { border-bottom: 0; }
.todo li.is-bad { box-shadow: inset 2px 0 0 var(--bad); }
.todo li.is-warn { box-shadow: inset 2px 0 0 var(--warn); }
.todo .lamp { margin-top: 6px; }
.todo .nm { font-weight: 600; color: var(--ink); }
.todo .why { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px; }
.todo .btn { margin-top: 1px; }
.busy li { display: grid; grid-template-columns: minmax(0, 1fr) 140px; align-items: center; gap: 12px; padding: 7px 12px; border-bottom: 1px solid var(--line); }
.busy li:last-child, .jobs li:last-child { border-bottom: 0; }
.busy a, .jobs a { color: var(--ink); font-weight: 500; }
.jobs li { display: grid; grid-template-columns: minmax(0, 1fr) auto; column-gap: 10px; padding: 7px 12px; border-bottom: 1px solid var(--line); }
.jobs li .chip { grid-row: 1 / span 2; grid-column: 2; align-self: center; }
.jobs li .xs { grid-column: 1; }
.fl-inline { display: none; }
@media (max-width: 1100px) { .dash { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 900px) {
  .fl-inline { display: flex; position: static; width: auto; height: auto; border: 1px solid var(--line); border-radius: var(--r-lg); }
  .fl-inline :deep(.fl-body) { overflow: visible; }
}
</style>
