<script setup>
import { ref } from 'vue';
import { Btn } from '@/components';
import { api, passkeysSupported, createPasskey } from '@/lib/api.js';
import { toast, confirmBox } from '@/lib/store.js';
import { ago } from '@/lib/format.js';

defineProps({ list: { type: Array, default: () => [] } });
const emit = defineEmits(['change']);
const supported = passkeysSupported();
const name = ref('');
const err = ref('');
async function add() {
  err.value = '';
  try {
    const options = await api('/api/account/passkeys/begin', { method: 'POST', body: { name: name.value } });
    await api('/api/account/passkeys/finish', { method: 'POST', body: { credential: await createPasskey(options) } });
    name.value = '';
    toast('Passkey added', 'ok');
    emit('change');
  } catch (e) { if (e.name !== 'NotAllowedError') err.value = e.message; } // NotAllowedError: the browser prompt was cancelled
}
async function remove(p) {
  if (!await confirmBox({ title: `Remove the passkey "${p.name}"?`, body: 'You can no longer sign in with it. Also remove it from the device or password manager.', confirm: 'Remove passkey', danger: true })) return;
  await api(`/api/account/passkeys/${encodeURIComponent(p.id)}`, { method: 'DELETE' });
  toast('Passkey removed');
  emit('change');
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>Passkeys</h2><span v-if="list.length" class="chip ok">{{ list.length }}</span></div>
    <div class="panel-b form">
      <p class="small ink2">Sign in with your fingerprint, face or device PIN instead of a password and code. A passkey counts as both factors.</p>
      <div v-if="supported" class="row nw">
        <input v-model="name" class="grow" placeholder="Name, e.g. MacBook or YubiKey" aria-label="Passkey name" @keydown.enter="name.trim() && add()" />
        <Btn kind="primary" :disabled="!name.trim()" @click="add">Add passkey</Btn>
      </div>
      <p v-else class="small ink2">Passkeys need HTTPS (or localhost) and a browser that supports them.</p>
      <p v-if="err" class="err" role="alert">{{ err }}</p>
    </div>
    <div v-if="list.length" class="table-wrap list"><table class="t"><tbody>
      <tr v-for="p in list" :key="p.id">
        <td><span class="strong">{{ p.name }}</span><span class="sub">Added {{ ago(p.created_at) }}{{ p.last_used ? `, last used ${ago(p.last_used)}` : ', never used' }}</span></td>
        <td class="actions"><Btn size="sm" kind="ghost" icon="trash" :title="`Remove ${p.name}`" @click="remove(p)" /></td>
      </tr>
    </tbody></table></div>
  </section>
</template>
<style scoped>
.list { border-top: 1px solid var(--line); }
</style>
