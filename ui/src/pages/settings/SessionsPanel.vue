<script setup>
import { Btn } from '@/components';
import { api } from '@/lib/api.js';
import { confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';

defineProps({ sessions: { type: Array, default: () => [] } });
const emit = defineEmits(['change']);
function browserName(ua = '') {
  const b = /Firefox\//.test(ua) ? 'Firefox' : /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl/.test(ua) ? 'curl' : 'Browser';
  const os = /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${b} on ${os}` : b;
}
async function revoke(s) { await api(`/api/account/sessions/${s.id}`, { method: 'DELETE' }); emit('change'); }
async function others() {
  if (!await confirmBox({ title: 'Sign out everywhere else?', confirm: 'Sign out other sessions', danger: true })) return;
  await api('/api/account/sessions/revoke-others', { method: 'POST' });
  emit('change');
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>Signed-in sessions</h2><Btn v-if="sessions.length > 1" size="sm" class="right" @click="others">Sign out others</Btn></div>
    <div class="table-wrap"><table class="t"><tbody>
      <tr v-for="s in sessions" :key="s.id">
        <td><span class="strong">{{ browserName(s.ua) }}</span> <span v-if="s.current" class="chip">This session</span><span class="sub">{{ s.ip }}, signed in {{ when(s.created_at) }}</span></td>
        <td class="ink2 nowrap">active {{ ago(s.seen_at) }}</td>
        <td class="actions"><Btn v-if="!s.current" size="sm" @click="revoke(s)">Sign out</Btn></td>
      </tr>
    </tbody></table></div>
  </section>
</template>
