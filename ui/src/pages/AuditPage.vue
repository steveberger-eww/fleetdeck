<script setup>
import { ref, computed, watch, onBeforeUnmount } from 'vue';
import { Btn, Icon, SortTh, Empty, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useQuery, useSort } from '@/lib/use.js';
import { ago, when } from '@/lib/format.js';

const KINDS = [['', 'Everything'], ['audit', 'Changes'], ['security', 'Security'], ['alert', 'Alerts'], ['job', 'Jobs'], ['schedule', 'Schedules'], ['notify', 'Notifications']];
const LEVEL = { error: 'bad', warn: 'warn', success: 'ok' };
const PAGE = 100;
const kind = useQuery('kind');
const query = useQuery('q');
// The search box writes to the URL 300 ms after the last keystroke.
const q = ref(query.value);
let timer;
watch(q, (v) => { clearTimeout(timer); timer = setTimeout(() => { query.value = v; }, 300); });
onBeforeUnmount(() => clearTimeout(timer));

const qs = computed(() => new URLSearchParams(Object.entries({ kind: kind.value, q: query.value, limit: String(PAGE) }).filter(([, x]) => x)).toString());
const ev = useLoad(() => api(`/api/events?${qs.value}`), { key: qs, poll: 15_000 });
const older = ref([]);
watch(qs, () => { older.value = []; });
const rows = computed(() => [...(ev.data || []), ...older.value]);
const sort = useSort(rows);
const more = async () => { older.value = [...older.value, ...await api(`/api/events?${qs.value}&before=${rows.value[rows.value.length - 1].id}`)]; };
const kindName = (k) => KINDS.find(([x]) => x === k)?.[1] || k;
// "Job #12 …" in job events links to the job.
const parts = (e) => (e.kind === 'job' ? e.message.split(/(Job #\d+)/) : [e.message]);
const isJob = (p) => /^Job #\d+$/.test(p);
</script>
<template>
  <div class="page">
    <div class="page-h"><div><h1>Audit log</h1><p class="sub">Who changed what, every remote action, sign-ins and alerts.</p></div></div>
    <section class="panel">
      <div class="panel-h">
        <div class="seg hide-sm" role="group" aria-label="Type">
          <button v-for="[k, l] in KINDS" :key="k" type="button" :aria-pressed="kind === k" @click="kind = k">{{ l }}</button>
        </div>
        <select v-model="kind" class="show-sm auto" aria-label="Type"><option v-for="[k, l] in KINDS" :key="k" :value="k">{{ l }}</option></select>
        <label class="search w-sm right"><Icon name="search" /><input v-model="q" type="search" placeholder="Search" aria-label="Search events" /></label>
      </div>
      <Loading v-if="ev.loading && !ev.data" :rows="6" />
      <div v-else-if="ev.error" class="panel-b"><ErrorNote :error="ev.error" :retry="ev.reload" /></div>
      <Empty v-else-if="!rows.length && (kind || query)" title="No events match" icon="filter" />
      <Empty v-else-if="!rows.length" title="No events yet" icon="audit" />
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <SortTh :sort="sort" k="t" desc>Time</SortTh><SortTh :sort="sort" k="kind">Type</SortTh><SortTh :sort="sort" k="message">Event</SortTh>
            <SortTh :sort="sort" k="server_name">Server</SortTh><SortTh :sort="sort" k="ip" class="hide-sm">From</SortTh>
          </tr></thead>
          <tbody>
            <tr v-for="e in sort.rows" :key="e.id">
              <td class="nowrap ink2" :title="when(e.t)">{{ ago(e.t) }}</td>
              <td><span :class="['chip', LEVEL[e.level]]">{{ kindName(e.kind) }}</span></td>
              <td class="msg"><template v-for="(p, i) in parts(e)" :key="i"><RouterLink v-if="isJob(p)" :to="`/jobs/${p.slice(5)}`">{{ p }}</RouterLink><template v-else>{{ p }}</template></template></td>
              <td class="nowrap"><RouterLink v-if="e.server_id && e.server_name" :to="`/servers/${e.server_id}`">{{ e.server_name }}</RouterLink></td>
              <td class="small ink2 hide-sm">{{ e.ip }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="rows.length >= PAGE && rows.length % PAGE === 0" class="more-row"><Btn size="sm" @click="more">Load older events</Btn></div>
      </div>
    </section>
  </div>
</template>
<style scoped>
.msg { min-width: 260px; overflow-wrap: anywhere; }
</style>
