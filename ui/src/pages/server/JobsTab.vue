<script setup>
// Schedules that reach this server (directly, by tag or "all"), one-off runs cancellable here; then the job history.
import { ref, computed } from 'vue';
import { Btn, ErrorNote, JobsTable } from '@/components';
import ScheduleRun from './ScheduleRun.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { can, confirmBox } from '@/lib/store.js';
import { when } from '@/lib/format.js';

const props = defineProps({ s: Object });
const admin = can('admin');
const list = useLoad(() => (admin ? api('/api/schedules') : null), { poll: admin ? 60_000 : 0 });
const add = ref(false);
const mine = computed(() => (list.data || [])
  .filter((x) => x.enabled && (x.targets.all || x.targets.servers?.includes(props.s.id) || x.targets.tags?.some((t) => props.s.tags.includes(t))))
  .sort((a, b) => (a.next || Infinity) - (b.next || Infinity)));
async function cancel(x) {
  if (!await confirmBox({ title: `Cancel "${x.name}"?`, confirm: 'Cancel run', danger: true })) return;
  await api(`/api/schedules/${x.id}`, { method: 'DELETE' });
  list.reload(true);
}
const closed = (saved) => { add.value = false; if (saved) list.reload(true); };
</script>

<template>
  <div class="stack">
    <section v-if="admin" class="panel">
      <div class="panel-h">
        <h2>Scheduled</h2><RouterLink class="small" to="/schedules">All schedules</RouterLink>
        <Btn size="sm" icon="clock" class="right" @click="add = true">Run later…</Btn>
      </div>
      <div v-if="list.error" class="panel-b"><ErrorNote :error="list.error" :retry="list.reload" /></div>
      <div v-else-if="!list.loading && !mine.length" class="panel-b small muted">Nothing scheduled on this server.</div>
      <div v-else-if="mine.length" class="table-wrap">
        <table class="t">
          <tbody>
            <tr v-for="x in mine" :key="x.id">
              <td><span class="strong">{{ x.name }}</span><span class="sub">{{ x.script_name }}</span></td>
              <td class="ink2">{{ x.once ? 'Once' : x.cron }}</td>
              <td class="nowrap">{{ x.next ? when(x.next) : '–' }}</td>
              <td class="actions"><Btn v-if="x.once" size="sm" @click="cancel(x)">Cancel</Btn></td>
            </tr>
          </tbody>
        </table>
      </div>
      <ScheduleRun v-if="add" :s="s" @close="closed" />
    </section>
    <section class="panel"><JobsTable :server-id="s.id" /></section>
  </div>
</template>
