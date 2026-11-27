<script setup>
// Top processes, refreshed every 10 s; the refresh holds while the pointer or focus is in the table.
import { computed } from 'vue';
import { Icon, Menu, Loading, ErrorNote, Empty, SortTh } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useQuery, useSort, useHold } from '@/lib/use.js';
import { can, toast, confirmBox } from '@/lib/store.js';
import { bytes, dur } from '@/lib/format.js';
import { attempt } from './util.js';

const props = defineProps({ s: Object });
const [held, bind] = useHold();
const ps = useLoad(() => api(`/api/servers/${props.s.id}/processes`), { poll: 10_000, paused: held });
const q = useQuery('q');
const filtered = computed(() => (ps.data || []).filter((p) => !q.value || `${p.args} ${p.user} ${p.pid}`.toLowerCase().includes(q.value.toLowerCase())));
const sort = useSort(filtered, 'cpu', -1);
const list = computed(() => sort.rows.slice(0, 80));
const kill = (p, signal) => attempt(async () => {
  if (!await confirmBox({ title: `Send ${signal} to ${p.comm} (${p.pid})?`, body: p.args, confirm: signal === 'KILL' ? 'Force kill' : 'Terminate', danger: true })) return;
  await api(`/api/servers/${props.s.id}/processes/kill`, { method: 'POST', body: { pid: p.pid, signal } });
  toast(`Sent ${signal} to ${p.pid}`, 'ok');
  ps.reload(true);
})();
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <label class="search w-md"><Icon name="search" /><input v-model="q" type="search" placeholder="Filter processes" aria-label="Filter processes" /></label>
      <span class="sub right" aria-live="polite">{{ held ? 'Paused while you point at the table' : 'Updates every 10 seconds' }}</span>
    </div>
    <Loading v-if="ps.loading && !ps.data" :rows="6" />
    <div v-else-if="ps.error" class="panel-b"><ErrorNote :error="ps.error" :retry="ps.reload" /></div>
    <Empty v-else-if="!list.length" title="No processes match" icon="filter" />
    <div v-else class="table-wrap" v-bind="bind">
      <table class="t compact">
        <thead><tr>
          <SortTh :sort="sort" k="pid" class="num">PID</SortTh><SortTh :sort="sort" k="user" class="hide-sm">User</SortTh>
          <SortTh :sort="sort" k="cpu" class="num" desc>CPU</SortTh><SortTh :sort="sort" k="mem" class="num" desc>Memory</SortTh>
          <SortTh :sort="sort" k="rss" class="num hide-sm" desc>RSS</SortTh><SortTh :sort="sort" k="elapsed" desc class="hide-sm">Running</SortTh>
          <SortTh :sort="sort" k="args">Command</SortTh><th v-if="can('operator')"><span class="sr">Actions</span></th>
        </tr></thead>
        <tbody>
          <tr v-for="p in list" :key="p.pid">
            <td class="num">{{ p.pid }}</td><td class="ink2 hide-sm">{{ p.user }}</td>
            <td class="num">{{ p.cpu.toFixed(1) }}%</td><td class="num">{{ p.mem.toFixed(1) }}%</td>
            <td class="num ink2 hide-sm">{{ bytes(p.rss) }}</td><td class="ink2 nowrap hide-sm">{{ dur(p.elapsed * 1000) }}</td>
            <td><span class="trunc cmd" :title="p.args">{{ p.args }}</span></td>
            <td v-if="can('operator')" class="actions">
              <Menu size="sm" :label="`Actions for ${p.comm} (${p.pid})`">
                <button type="button" class="mi" @click="kill(p, 'TERM')"><Icon name="stop" />Terminate</button>
                <button type="button" class="mi" @click="kill(p, 'KILL')"><Icon name="x" />Force kill</button>
              </Menu>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.cmd { display: block; max-width: 520px; }
</style>
