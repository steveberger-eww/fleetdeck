<script setup>
import { ref, computed } from 'vue';
import { useRouter } from 'vue-router';
import { Btn, SortTh, Empty, Loading, ErrorNote } from '@/components';
import ScheduleDialog from './ScheduleDialog.vue';
import WebhooksPanel from './WebhooksPanel.vue';
import Switch from '@/components/Switch.vue';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { servers, toast, confirmBox } from '@/lib/store.js';
import { confirmRun, resolveTargets } from '@/lib/actions.js';
import { ago, when } from '@/lib/format.js';
import { cronLabel, targetText } from './util.js';

const router = useRouter();
const list = useLoad(() => api('/api/schedules'), { poll: 30_000 });
const all = computed(() => servers.value || []);
const sort = useSort(() => list.data);
const edit = ref(null);
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
const NIGHTLY = { name: 'Nightly updates', script_id: 'update', cron: '0 4 * * *', targets: { all: true } };

async function toggle(s, e) {
  try { await api(`/api/schedules/${s.id}`, { method: 'PUT', body: { enabled: !s.enabled } }); } catch (x) { e.target.checked = s.enabled; toast(x.message, 'bad'); }
  list.reload(true);
}
async function runNow(s) {
  const t = resolveTargets(s.targets, all.value);
  if (!await confirmRun({ scriptId: s.script_id, name: s.script_name, count: t.length, names: t.map((x) => x.name) })) return;
  const { id } = await api(`/api/schedules/${s.id}/run`, { method: 'POST' });
  router.push(`/jobs/${id}`);
}
async function del(s) {
  if (!await confirmBox({ title: `Delete schedule "${s.name}"?`, confirm: 'Delete schedule', danger: true })) return;
  await api(`/api/schedules/${s.id}`, { method: 'DELETE' });
  list.reload(true);
}
const saved = () => { edit.value = null; list.reload(true); };
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Schedules &amp; webhooks</h1><p class="sub">Run scripts automatically or from outside. Times use the Fleetdeck host's time zone ({{ tz }} in this browser).</p></div>
      <div class="actions"><Btn kind="primary" icon="plus" @click="edit = {}">New schedule</Btn></div>
    </div>

    <section class="panel">
      <Loading v-if="list.loading" />
      <div v-else-if="list.error" class="panel-b"><ErrorNote :error="list.error" :retry="list.reload" /></div>
      <Empty v-else-if="!list.data?.length" title="No schedules yet" icon="clock">
        For example: install updates every night at 04:00 on all servers.
        <template #action><Btn icon="plus" @click="edit = { ...NIGHTLY }">Schedule nightly updates</Btn></template>
      </Empty>
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <SortTh :sort="sort" k="enabled" class="on" desc>On</SortTh>
            <SortTh :sort="sort" k="name">Schedule</SortTh>
            <SortTh :sort="sort" k="cron">When</SortTh>
            <SortTh :sort="sort" k="targets" :get="(s) => targetText(s.targets, all)" class="hide-sm">Targets</SortTh>
            <SortTh :sort="sort" k="next" :get="(s) => (s.enabled ? s.next : null)">Next run</SortTh>
            <SortTh :sort="sort" k="last_run" desc>Last run</SortTh>
            <th><span class="sr">Actions</span></th>
          </tr></thead>
          <tbody>
            <tr v-for="s in sort.rows" :key="s.id" :class="!s.enabled && 'off'">
              <td class="on"><Switch :checked="s.enabled" :label="`Enable ${s.name}`" @change="toggle(s, $event)" /></td>
              <td><span class="strong">{{ s.name }}</span><span class="sub">{{ s.script_name }}</span></td>
              <td>
                <template v-if="s.once || cronLabel(s.cron)">{{ s.once ? 'Once' : cronLabel(s.cron) }}<span class="sub">{{ s.cron }}</span></template>
                <template v-else>{{ s.cron }}</template>
              </td>
              <td class="ink2 hide-sm"><div class="trunc targets" :title="targetText(s.targets, all)">{{ targetText(s.targets, all) }}</div></td>
              <td class="ink2 nowrap">{{ s.enabled && s.next ? when(s.next) : '–' }}</td>
              <td class="nowrap"><RouterLink v-if="s.last_job" :to="`/jobs/${s.last_job}`" :title="when(s.last_run)">{{ ago(s.last_run) }}</RouterLink><span v-else class="muted">never</span></td>
              <td class="actions">
                <div class="row">
                  <Btn size="sm" icon="play" @click="runNow(s)">Run now</Btn>
                  <Btn size="sm" kind="ghost" icon="edit" title="Edit" @click="edit = s" />
                  <Btn size="sm" kind="ghost" icon="trash" title="Delete" @click="del(s)" />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <WebhooksPanel />
    <ScheduleDialog v-if="edit" :schedule="edit" @close="edit = null" @saved="saved" />
  </div>
</template>

<style scoped>
.t .on { width: 1%; }
.t td.on { padding-right: 0; }
.targets { max-width: 220px; }
tr.off td:not(.on, .actions) { opacity: .6; }
</style>
