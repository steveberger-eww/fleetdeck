<script setup>
// Every server's containers from the per-minute probe (the servers list already carries them): find one, jump to its server.
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Icon, Lamp, SortTh, CellBar, Empty, Loading } from '@/components';
import { useQuery, setQuery, useSort, stored } from '@/lib/use.js';
import { servers } from '@/lib/store.js';
import { containerProblem } from '@/lib/status.js';
import { bytes, plural } from '@/lib/format.js';

const route = useRoute();
const router = useRouter();
// Stopped cleanly (exit code 0) or never started; the server's Containers tab hides these the same way.
const quiet = (c) => c.state === 'created' || (c.state === 'exited' && /^Exited \(0\)/.test(c.status));
const FILTERS = { '': (c) => !quiet(c), problems: containerProblem, stopped: quiet };
// Group by: [label, key of a container]. The grouped column is hidden.
const GROUPS = { server: ['Server', (c) => c.s.id], project: ['Stack', (c) => c.project || ''], image: ['Image', (c) => c.image] };

const q = useQuery('q');
const tag = useQuery('tag');
const filter = useQuery('show');
// The URL wins (links keep working); without one, the last choice.
const lastGroup = stored('fd-docker-group', 'none');
const groupQ = useQuery('group', lastGroup.value);
const group = computed({ get: () => groupQ.value, set: (g) => { lastGroup.value = g; groupQ.value = g; } });
const closedList = stored('fd-docker-closed', []);
const closed = computed(() => new Set(closedList.value));
const setOpen = (keys, open) => {
  const s = new Set(closedList.value);
  for (const k of keys) open ? s.delete(k) : s.add(k);
  closedList.value = [...s];
};

const hosts = computed(() => (servers.value || []).filter((s) => s.snapshot?.containers));
const all = computed(() => hosts.value.flatMap((s) => s.snapshot.containers.map((c) => ({ ...c, s, server: s.name }))));
const tags = computed(() => [...new Set(hosts.value.flatMap((s) => s.tags))].sort());
const list = computed(() => {
  const n = q.value.toLowerCase().trim();
  const f = FILTERS[filter.value] || FILTERS[''];
  return all.value.filter((c) => f(c) && (!tag.value || c.s.tags.includes(tag.value))
    && (!n || `${c.name} ${c.image} ${c.project || ''} ${c.server} ${c.s.tags.join(' ')}`.toLowerCase().includes(n)));
});
const sort = useSort(() => list.value, 'server');
const counts = computed(() => ({
  running: all.value.filter((c) => c.state === 'running').length,
  problems: all.value.filter(containerProblem).length,
  stopped: all.value.filter(quiet).length,
}));

// Groups in name order (the server's name for server groups), keeping the rows' sort order; the empty key last.
const grouped = computed(() => {
  const g = GROUPS[group.value];
  if (!g) return null;
  const m = new Map();
  for (const c of sort.rows) {
    const k = g[1](c);
    if (!m.has(k)) m.set(k, { key: k, id: `${group.value}:${k}`, rows: [] });
    m.get(k).rows.push(c);
  }
  const name = (x) => (group.value === 'server' ? x.rows[0].server : x.key);
  return [...m.values()].sort((a, b) => (!a.key) - (!b.key) || name(a).localeCompare(name(b)));
});
const anyClosed = computed(() => grouped.value?.some((g) => closed.value.has(g.id)));
const show = (col) => group.value !== col;
const filtered = computed(() => q.value || tag.value || filter.value);
const clear = () => setQuery(router, route, { q: null, tag: null, show: null });
const toggleFilter = (k) => { filter.value = filter.value === k ? '' : k; };

const to = (c) => `/servers/${c.s.id}/containers${quiet(c) ? '?ctr=all' : ''}`;
const open = (e, c) => { if (!e.target.closest('a, button')) router.push(to(c)); };
const lamp = (c) => (c.state === 'running' ? 'ok' : c.state === 'dead' ? 'bad' : ['exited', 'created'].includes(c.state) ? 'unknown' : 'warn');
const groupInfo = (g) => {
  const up = g.rows.filter((x) => x.state === 'running').length;
  const mem = g.rows.reduce((a, x) => a + (x.mem || 0), 0);
  const on = new Set(g.rows.map((x) => x.s.id)).size;
  return `${up} of ${plural(g.rows.length, 'container')} running${group.value !== 'server' && on > 1 ? ` on ${on} servers` : ''}${mem ? `, ${bytes(mem)} memory` : ''}`;
};
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Docker</h1><p class="sub">{{ servers ? `${plural(all.length, 'container')} on ${plural(hosts.length, 'server')}, checked every minute` : 'Loading…' }}</p></div>
    </div>

    <Loading v-if="!servers" :rows="5" />
    <div v-else-if="!hosts.length" class="panel"><Empty title="No Docker hosts" icon="box">Servers with Docker list their containers here.</Empty></div>

    <template v-else>
      <div class="stats" role="group" aria-label="Filter by state">
        <button type="button" class="stat ok" :aria-pressed="!filter" title="Everything but containers that stopped cleanly" @click="filter = ''">
          <b>{{ counts.running }}<small> / {{ all.length }}</small></b><span>Running</span></button>
        <button type="button" :class="['stat', counts.problems ? 'warn' : 'zero']" :disabled="!counts.problems && filter !== 'problems'" :aria-pressed="filter === 'problems'"
          title="Unhealthy, restarting or dead" @click="toggleFilter('problems')"><b>{{ counts.problems }}</b><span>Problems</span></button>
        <button type="button" :class="['stat', !counts.stopped && 'zero']" :disabled="!counts.stopped && filter !== 'stopped'" :aria-pressed="filter === 'stopped'"
          title="Stopped cleanly (exit code 0) or never started" @click="toggleFilter('stopped')"><b>{{ counts.stopped }}</b><span>Stopped</span></button>
      </div>

      <section class="panel">
        <div class="panel-h">
          <label class="search w-sm"><Icon name="search" /><input v-model="q" type="search" placeholder="Container, image, stack or server" aria-label="Search containers" /></label>
          <select v-if="tags.length" v-model="tag" class="auto" aria-label="Tag"><option value="">All tags</option><option v-for="t in tags" :key="t" :value="t">{{ t }}</option></select>
          <button v-if="filtered" type="button" class="btn ghost sm" @click="clear"><Icon name="x" :size="14" />Clear</button>
          <div class="row nw right">
            <button v-if="grouped" type="button" class="btn ghost sm" @click="setOpen(grouped.map((g) => g.id), anyClosed)">{{ anyClosed ? 'Expand all' : 'Collapse all' }}</button>
            <label class="row nw small muted">Group by
              <select v-model="group" class="auto sm" aria-label="Group by">
                <option value="none">Nothing</option><option v-for="(g, k) in GROUPS" :key="k" :value="k">{{ g[0] }}</option>
              </select></label>
          </div>
        </div>

        <Empty v-if="!list.length" title="No matching containers" icon="filter">
          {{ filter ? 'Try another filter.' : 'Clear the search to see them all.' }}
          <template v-if="filtered" #action><button type="button" class="btn" @click="clear">Clear filters</button></template>
        </Empty>
        <div v-else class="table-wrap">
          <table class="t">
            <thead><tr>
              <SortTh :sort="sort" k="state">State</SortTh>
              <SortTh :sort="sort" k="name">Name</SortTh>
              <SortTh v-if="show('project')" :sort="sort" k="project" class="hide-sm">Stack</SortTh>
              <SortTh v-if="show('server')" :sort="sort" k="server">Server</SortTh>
              <SortTh v-if="show('image')" :sort="sort" k="image" class="hide-sm">Image</SortTh>
              <SortTh :sort="sort" k="status" class="hide-sm">Status</SortTh>
              <SortTh :sort="sort" k="cpu" desc>CPU</SortTh>
              <SortTh :sort="sort" k="mem" desc>Memory</SortTh>
            </tr></thead>
            <tbody>
              <template v-for="g in grouped || [{ id: '', rows: sort.rows }]" :key="g.id">
                <tr v-if="grouped" class="grp click" @click="!$event.target.closest('a, button') && setOpen([g.id], closed.has(g.id))">
                  <td colspan="7">
                    <div class="row nw">
                      <button type="button" class="grp-t" :aria-expanded="!closed.has(g.id)" :aria-label="closed.has(g.id) ? 'Expand' : 'Collapse'" @click="setOpen([g.id], closed.has(g.id))"><Icon name="chev" :size="14" /></button>
                      <template v-if="group === 'server'">
                        <RouterLink class="strong" :to="`/servers/${g.rows[0].s.id}`">{{ g.rows[0].server }}</RouterLink>
                        <span v-if="g.rows[0].s.status !== 'up'" class="chip warn" title="The server isn't answering: this is its last known state">Stale</span>
                      </template>
                      <span v-else class="strong trunc" :title="g.key">{{ g.key || (group === 'project' ? 'No stack' : '') }}</span>
                      <span class="small muted">{{ groupInfo(g) }}</span>
                    </div>
                  </td>
                </tr>
                <template v-if="!grouped || !closed.has(g.id)">
                  <tr v-for="c in g.rows" :key="`${c.s.id}/${c.name}`" class="click" @click="open($event, c)">
                    <td><div class="row nw">
                      <Lamp :status="lamp(c)" /><span class="ink2">{{ c.state }}</span>
                      <span v-if="c.health === 'unhealthy'" class="chip warn">Unhealthy</span>
                      <span v-else-if="c.health === 'healthy'" class="xs muted">healthy</span>
                    </div></td>
                    <td><RouterLink class="rowlink" :to="to(c)">{{ c.name }}</RouterLink></td>
                    <td v-if="show('project')" class="ink2 hide-sm">{{ c.project || '–' }}</td>
                    <td v-if="show('server')" class="nowrap">
                      <RouterLink :to="`/servers/${c.s.id}`">{{ c.server }}</RouterLink>
                      <span v-if="c.s.status !== 'up'" class="chip warn stale" title="The server isn't answering: this is its last known state">Stale</span>
                    </td>
                    <td v-if="show('image')" class="ink2 hide-sm"><div class="trunc image" :title="c.image">{{ c.image }}</div></td>
                    <td class="ink2 nowrap hide-sm">{{ c.status }}</td>
                    <td><CellBar v-if="c.cpu != null" :value="c.cpu / 100 / (c.s.snapshot.cores || 1)">{{ c.cpu.toFixed(1) }}%</CellBar><span v-else class="muted">–</span></td>
                    <td class="nowrap"><CellBar v-if="c.mem != null" :value="c.mem / (c.s.snapshot.memTotal || Infinity)">{{ bytes(c.mem) }}</CellBar><span v-else class="muted">–</span></td>
                  </tr>
                </template>
              </template>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.image { max-width: 240px; }
.stale { margin-left: 6px; }
.t tr.grp td { background: var(--panel-2); padding-top: 7px; padding-bottom: 7px; }
.grp-t { all: unset; display: inline-flex; padding: 3px; border-radius: 4px; color: var(--muted); cursor: pointer; }
.grp-t:hover { color: var(--ink); background: var(--hover); }
.grp-t:focus-visible { outline: 2px solid var(--accent); }
.grp-t svg { transition: transform .12s; }
.grp-t[aria-expanded="false"] svg { transform: rotate(-90deg); }
</style>
