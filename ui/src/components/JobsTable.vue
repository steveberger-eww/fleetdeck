<script setup>
// Job history, newest first, polled every 5 s. Used by the Jobs page and a server's Jobs tab.
import { ref, computed, watch } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { can, openRun } from '@/lib/store.js';
import { ago, dur, when, plural } from '@/lib/format.js';
import Btn from './Btn.vue';
import SortTh from './SortTh.vue';
import JobChip from './JobChip.vue';
import Empty from './Empty.vue';
import Loading from './Loading.vue';
import ErrorNote from './ErrorNote.vue';

const props = defineProps({ serverId: String, limit: { type: Number, default: 30 }, status: { type: String, default: '' } });
const router = useRouter();
const qs = computed(() => `limit=${props.limit}${props.serverId ? `&server=${props.serverId}` : ''}${props.status ? `&status=${props.status}` : ''}`);
const jobs = useLoad(() => api(`/api/jobs?${qs.value}`), { key: qs, poll: 5000 });
const extra = ref([]);
watch(qs, () => { extra.value = []; });
const rows = computed(() => [...(jobs.data || []), ...extra.value.filter((e) => !(jobs.data || []).some((j) => j.id === e.id))]);
const sort = useSort(rows);
const more = async () => { extra.value = [...extra.value, ...await api(`/api/jobs?${qs.value}&before=${rows.value[rows.value.length - 1].id}`)]; };
const hosts = (j) => j.runs.filter((r) => r.server_id !== '__ansible__');
const failed = (j) => hosts(j).filter((r) => ['failed', 'interrupted'].includes(r.status)).length;
const open = (e, j) => { if (!e.target.closest('a, button')) router.push(`/jobs/${j.id}`); };
</script>
<template>
  <Loading v-if="jobs.loading && !jobs.data" />
  <div v-else-if="jobs.error" class="panel-b"><ErrorNote :error="jobs.error" :retry="jobs.reload" /></div>
  <Empty v-else-if="!rows.length && status" :title="`No ${status} jobs`" icon="jobs" />
  <Empty v-else-if="!rows.length" title="No jobs yet" icon="jobs">
    Every script you run is kept here with its full output.
    <template #action><Btn v-if="can('operator')" icon="play" @click="openRun(serverId ? { targets: { servers: [serverId] } } : {})">Run a script</Btn></template>
  </Empty>
  <div v-else class="table-wrap">
    <table class="t">
      <thead><tr>
        <SortTh :sort="sort" k="name">Job</SortTh><SortTh :sort="sort" k="status">Status</SortTh>
        <SortTh :sort="sort" k="servers" :get="(j) => j.runs.length" desc>Servers</SortTh>
        <SortTh :sort="sort" k="actor" class="hide-sm">Started by</SortTh><SortTh :sort="sort" k="created_at" desc>Started</SortTh>
        <SortTh :sort="sort" k="took" :get="(j) => j.finished_at && j.finished_at - j.created_at" class="num hide-sm" desc>Duration</SortTh>
      </tr></thead>
      <tbody>
        <tr v-for="j in sort.rows" :key="j.id" class="click" @click="open($event, j)">
          <td><RouterLink class="rowlink" :to="`/jobs/${j.id}`"><span class="muted">#{{ j.id }}</span> {{ j.name }}</RouterLink> <span v-if="j.kind === 'ansible'" class="tag">Ansible</span></td>
          <td><JobChip :status="j.status" /></td>
          <td><div class="row nw"><span class="trunc" style="max-width: 280px">{{ hosts(j).length <= 3 ? hosts(j).map((r) => r.server_name).join(', ') : plural(hosts(j).length, 'server') }}</span><span v-if="failed(j)" class="chip bad">{{ failed(j) }} failed</span></div></td>
          <td class="ink2 hide-sm">{{ j.actor }}</td>
          <td class="ink2 nowrap" :title="when(j.created_at)">{{ ago(j.created_at) }}</td>
          <td class="num ink2 nowrap hide-sm">{{ j.finished_at ? dur(j.finished_at - j.created_at) : '…' }}</td>
        </tr>
      </tbody>
    </table>
    <div v-if="rows.length >= limit && rows.length % limit === 0" class="more-row"><Btn size="sm" @click="more">Load older jobs</Btn></div>
  </div>
</template>
