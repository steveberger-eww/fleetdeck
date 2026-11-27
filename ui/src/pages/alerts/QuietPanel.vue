<script setup>
// Quiet hours: daily windows and one-time maintenance windows without notifications.
import { ref } from 'vue';
import { Btn, Empty, Loading, ErrorNote } from '@/components';
import QuietDialog from './QuietDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { when } from '@/lib/format.js';

const m = useLoad(() => api('/api/mutes'), { poll: 60_000 });
const hhmm = (x) => `${String(Math.floor(x / 60)).padStart(2, '0')}:${String(x % 60).padStart(2, '0')}`;
const scopeText = (w) => w.server_name || (w.tag ? `tag ${w.tag}` : 'All servers');
const del = async (w) => { await api(`/api/mutes/${w.id}`, { method: 'DELETE' }); m.reload(true); };

const adding = ref(false);
const saved = () => { adding.value = false; m.reload(true); };
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <div class="grow"><h2>Quiet hours</h2><p class="sub">No notifications during these windows. Alerts still show here, and anything still firing afterwards is sent then.</p></div>
      <Btn size="sm" icon="plus" @click="adding = true">Add quiet hours</Btn>
    </div>
    <Loading v-if="m.loading" />
    <div v-else-if="m.error" class="panel-b"><ErrorNote :error="m.error" :retry="m.reload" /></div>
    <Empty v-else-if="!m.data?.windows.length" title="Always notify" icon="mute">Add nightly quiet hours, or a one-time maintenance window before planned work.</Empty>
    <div v-else class="table-wrap">
      <table class="t">
        <tbody>
          <tr v-for="w in m.data.windows" :key="w.id">
            <td class="kind"><span v-if="w.active" class="chip warn">Active</span><span v-else class="chip">{{ w.kind === 'daily' ? 'Daily' : 'Planned' }}</span></td>
            <td>
              <span class="strong">{{ w.kind === 'daily' ? `${hhmm(w.start_at)}–${hhmm(w.end_at)} every day` : `${when(w.start_at)} – ${when(w.end_at)}` }}</span>
              <span v-if="w.name" class="sub">{{ w.name }}</span>
            </td>
            <td class="ink2">{{ scopeText(w) }}</td>
            <td class="actions"><Btn size="sm" kind="ghost" icon="trash" title="Remove" @click="del(w)" /></td>
          </tr>
        </tbody>
      </table>
    </div>

    <QuietDialog v-if="adding" :tz="m.data?.tz" @close="adding = false" @saved="saved" />
  </section>
</template>

<style scoped>
.panel-h .sub { margin-top: 1px; }
.t td.kind { width: 1%; }
</style>
