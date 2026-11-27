<script setup>
// Webhooks: a secret URL that runs one script on fixed servers.
import { ref, computed } from 'vue';
import { Btn, Modal, Copy, Empty, Loading, ErrorNote } from '@/components';
import ScheduleDialog from './ScheduleDialog.vue';
import Switch from '@/components/Switch.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { servers, toast, confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';
import { targetText } from './util.js';

const list = useLoad(() => api('/api/triggers'), { poll: 30_000 });
const edit = ref(null);
const shown = ref(null); // { name, token } right after create/rotate
const all = computed(() => servers.value || []);
const hookUrl = (token) => `${location.origin}/api/hooks/${token}`;

async function toggle(t, e) {
  try { await api(`/api/triggers/${t.id}`, { method: 'PUT', body: { enabled: !t.enabled } }); } catch (x) { e.target.checked = t.enabled; toast(x.message, 'bad'); }
  list.reload(true);
}
async function rotate(t) {
  if (!await confirmBox({ title: `New URL for "${t.name}"?`, body: 'The current URL stops working immediately. Update it wherever it is used.', confirm: 'Generate new URL', danger: true })) return;
  shown.value = { name: t.name, token: (await api(`/api/triggers/${t.id}/rotate`, { method: 'POST' })).token };
}
async function del(t) {
  if (!await confirmBox({ title: `Delete webhook "${t.name}"?`, body: 'Its URL stops working.', confirm: 'Delete webhook', danger: true })) return;
  await api(`/api/triggers/${t.id}`, { method: 'DELETE' });
  list.reload(true);
}
function saved(r) {
  edit.value = null;
  list.reload(true);
  if (r?.token) shown.value = { name: r.name, token: r.token };
}
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <div class="grow"><h2>Webhooks</h2><p class="sub">Start a script from CI, a Git push or anything that can send a POST request.</p></div>
      <Btn size="sm" icon="plus" @click="edit = { name: '', script_id: 'update', targets: { servers: [], tags: [] } }">New webhook</Btn>
    </div>
    <Loading v-if="list.loading" />
    <div v-else-if="list.error" class="panel-b"><ErrorNote :error="list.error" :retry="list.reload" /></div>
    <Empty v-else-if="!list.data?.length" title="No webhooks" icon="link">Each webhook gets a secret URL that runs one script on fixed servers. It can't do anything else.</Empty>
    <div v-else class="table-wrap">
      <table class="t">
        <thead><tr><th class="on">On</th><th>Webhook</th><th class="hide-sm">Targets</th><th>Last run</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr v-for="t in list.data" :key="t.id" :class="!t.enabled && 'off'">
            <td class="on"><Switch :checked="t.enabled" :label="`Enable ${t.name}`" @change="toggle(t, $event)" /></td>
            <td><span class="strong">{{ t.name }}</span><span class="sub">{{ t.script_name }}</span></td>
            <td class="ink2 hide-sm"><div class="trunc targets">{{ targetText(t.targets, all) }}</div></td>
            <td class="nowrap"><RouterLink v-if="t.last_job" :to="`/jobs/${t.last_job}`" :title="when(t.last_run)">{{ ago(t.last_run) }}</RouterLink><span v-else class="muted">never</span></td>
            <td class="actions">
              <div class="row">
                <Btn size="sm" kind="ghost" @click="rotate(t)">New URL</Btn>
                <Btn size="sm" kind="ghost" icon="edit" title="Edit" @click="edit = t" />
                <Btn size="sm" kind="ghost" icon="trash" title="Delete" @click="del(t)" />
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <ScheduleDialog v-if="edit" webhook :schedule="edit" @close="edit = null" @saved="saved" />
    <Modal v-if="shown" :title="`URL for &quot;${shown.name}&quot;`" @close="shown = null">
      <p class="ink2">Copy it now: it is shown only once. Anyone with this URL can start the job, so keep it secret like a password.</p>
      <Copy :text="hookUrl(shown.token)" label="Copy URL" />
      <p class="small ink2">Example: <code>curl -X POST {{ hookUrl('…') }}</code>. The request body is ignored. A call while the previous run is still going gets HTTP 409 and starts nothing.</p>
      <template #footer><button type="button" class="btn primary" @click="shown = null">Done</button></template>
    </Modal>
  </section>
</template>

<style scoped>
.panel-h .sub { margin-top: 1px; }
.t th.on, .t td.on { width: 1%; padding-right: 0; }
.targets { max-width: 260px; }
tr.off td:not(.on, .actions) { opacity: .6; }
</style>
