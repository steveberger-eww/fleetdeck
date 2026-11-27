<script setup>
// Endpoint checks: HTTP(S) and TCP, run from the Fleetdeck host (not over SSH) every minute.
import { ref, computed } from 'vue';
import { Btn, Empty, Loading, ErrorNote } from '@/components';
import CheckDialog from './CheckDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { can, confirmBox } from '@/lib/store.js';
import { pct, when } from '@/lib/format.js';

const props = defineProps({ s: Object });
const id = props.s.id;
const list = useLoad(() => api(`/api/servers/${id}/checks`), { poll: 60_000 });
const edit = ref(null);
const owner = computed(() => can('owner'));
const runNow = async (c) => { await api(`/api/servers/${id}/checks/${c.id}/run`, { method: 'POST' }); list.reload(true); };
async function del(c) {
  if (!await confirmBox({ title: `Remove the check "${c.name}"?`, body: 'Its history goes with it.', confirm: 'Remove check', danger: true })) return;
  await api(`/api/servers/${id}/checks/${c.id}`, { method: 'DELETE' });
  list.reload(true);
}
const state = (c) => (!c.enabled ? ['', 'off'] : c.last_ok == null ? ['', 'pending'] : c.last_ok ? ['ok', 'up'] : ['bad', 'failing']);
const tlsKind = (d) => (d < 7 ? 'bad' : d < 21 ? 'warn' : '');
const saved = () => { edit.value = null; list.reload(true); };
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <h2>Endpoint checks</h2><span class="sub">HTTP(S) and TCP checks from the Fleetdeck host, every minute.</span>
      <Btn v-if="owner" size="sm" icon="plus" class="right" @click="edit = {}">Add check</Btn>
    </div>
    <Loading v-if="list.loading && !list.data" />
    <div v-else-if="list.error" class="panel-b"><ErrorNote :error="list.error" :retry="list.reload" /></div>
    <Empty v-else-if="!list.data.length" title="No checks" icon="net">Is the website up, and when does its certificate expire? Add a check, then an "Endpoint check failing" or "TLS certificate" rule under Alerts.</Empty>
    <div v-else class="table-wrap">
      <table class="t">
        <thead><tr><th>Status</th><th>Check</th><th class="num">Response</th><th>TLS</th><th class="num hide-sm">24 h</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr v-for="c in list.data" :key="c.id">
            <td><span :class="['chip', state(c)[0]]" :title="!c.last_ok && c.failing_since ? `Since ${when(c.failing_since)}` : undefined">{{ state(c)[1] }}</span></td>
            <td>
              <span class="strong">{{ c.name }}</span><template v-if="c.name !== c.target"> <span class="small ink2 wrap-any">{{ c.target }}</span></template>
              <div v-if="c.last_error" class="small bad-ink">{{ c.last_error }}</div>
            </td>
            <td class="num ink2 nowrap">{{ c.last_ms != null ? `${c.last_ms} ms` : '–' }}</td>
            <td>
              <template v-if="c.kind === 'http' && c.target.startsWith('https:') && c.tls_days != null"><span :class="['chip', tlsKind(c.tls_days)]" title="Days until the certificate expires">{{ c.tls_days < 0 ? 'expired' : `${Math.floor(c.tls_days)} d` }}</span></template>
              <span v-else class="muted">–</span>
            </td>
            <td class="num ink2 hide-sm">{{ pct(c.uptime, c.uptime < 1 ? 1 : 0) }}</td>
            <td class="actions"><div class="row">
              <Btn v-if="can('operator')" size="sm" @click="runNow(c)">Check now</Btn>
              <template v-if="owner"><Btn size="sm" kind="ghost" icon="edit" title="Edit" @click="edit = c" /><Btn size="sm" kind="ghost" icon="trash" title="Remove" @click="del(c)" /></template>
            </div></td>
          </tr>
        </tbody>
      </table>
    </div>
    <CheckDialog v-if="edit" :s="s" :check="edit" @close="edit = null" @saved="saved" />
  </section>
</template>
