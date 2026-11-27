<script setup>
import { ref } from 'vue';
import { Btn, Empty, Loading, ErrorNote } from '@/components';
import KeyDialog from './KeyDialog.vue';
import AuthorizedKeys from './AuthorizedKeys.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { can, confirmBox, toast } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';
import { copyText } from '@/lib/actions.js';

const keys = useLoad(() => api('/api/keys'));
const dialog = ref(null); // 'generate' | 'import'
// "ed25519 …last 24 characters of the key": enough to tell keys apart.
const short = (pub) => { const [t = '', b = ''] = pub.split(' '); return `${t.replace(/^ssh-/, '')} …${b.slice(-24)}`; };
async function del(k) {
  if (!await confirmBox({ title: `Delete key "${k.name}"?`, body: 'The private key is destroyed. Servers that still authorize its public key keep it in authorized_keys until you remove it there.', confirm: 'Delete key', danger: true })) return;
  await api(`/api/keys/${k.id}`, { method: 'DELETE' });
  toast('Key deleted');
  keys.reload(true);
}
const saved = () => { dialog.value = null; keys.reload(true); };
</script>
<template>
  <div class="page">
    <div class="page-h">
      <div><h1>SSH keys</h1><p class="sub">Private keys are encrypted at rest and only decrypted in memory to open connections. They are never shown again.</p></div>
      <div class="actions"><Btn icon="upload" @click="dialog = 'import'">Import key</Btn><Btn kind="primary" icon="plus" @click="dialog = 'generate'">Generate key</Btn></div>
    </div>

    <section class="panel">
      <Loading v-if="keys.loading" />
      <div v-else-if="keys.error" class="panel-b"><ErrorNote :error="keys.error" :retry="keys.reload" /></div>
      <Empty v-else-if="!keys.data.length" title="No keys stored" icon="key">Generate an ed25519 key here, or import one you already use. Servers without a key fall back to the ssh-agent of the Fleetdeck host.</Empty>
      <div v-else class="table-wrap"><table class="t">
        <thead><tr><th>Key</th><th>Used by</th><th class="hide-sm">Added</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr v-for="k in keys.data" :key="k.id">
            <td class="key"><span class="strong">{{ k.name }}</span> <span v-if="k.has_passphrase" class="tag">passphrase</span><span class="sub trunc" :title="k.public_key">{{ short(k.public_key) }}</span></td>
            <td class="small">
              <template v-for="(s, i) in k.used_by" :key="s.id">{{ i ? ', ' : '' }}<RouterLink :to="`/servers/${s.id}`">{{ s.name }}</RouterLink></template>
              <span v-if="!k.used_by.length" class="muted">No server</span>
            </td>
            <td class="ink2 nowrap hide-sm" :title="when(k.created_at)">{{ ago(k.created_at) }}</td>
            <td class="actions"><div class="row">
              <Btn size="sm" kind="ghost" icon="copy" @click="copyText(k.public_key, `Public key of ${k.name} copied`)">Copy public key</Btn>
              <Btn v-if="can('admin')" size="sm" kind="ghost" icon="trash" :title="k.used_by.length ? 'In use by servers' : `Delete ${k.name}`" :disabled="k.used_by.length > 0" @click="del(k)" />
            </div></td>
          </tr>
        </tbody>
      </table></div>
    </section>

    <AuthorizedKeys v-if="can('admin')" />
    <KeyDialog v-if="dialog" :mode="dialog" @close="dialog = null" @saved="saved" />
  </div>
</template>
<style scoped>
.key { max-width: 320px; }
</style>
