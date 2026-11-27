<script setup>
// Add a server in two steps: details (optionally generating a key), then authorize the key and test the connection.
import { reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { Modal, Btn, Loading, ServerForm, Copy, Callout } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useDirty } from '@/lib/use.js';
import { refreshServers } from '@/lib/store.js';
import { serverBody, PRIV_TEXT } from '@/lib/servers.js';

const emit = defineEmits(['close']);
const router = useRouter();
const keys = useLoad(() => api('/api/keys'));
const v = reactive({ name: '', host: '', port: 22, user: 'root', tags: '', notes: '', key_id: '__new', sudo_password: '' });
const dirty = useDirty(() => v);
const created = ref(null); // { server, key }
const test = ref(null);
const err = ref('');

async function save() {
  err.value = '';
  try {
    let key = keys.data?.find((k) => k.id === v.key_id) || null;
    if (v.key_id === '__new') key = await api('/api/keys', { method: 'POST', body: { name: v.name || v.host, generate: true } });
    try {
      const server = await api('/api/servers', { method: 'POST', body: serverBody({ ...v, key_id: key?.id || null }) });
      refreshServers();
      created.value = { server, key };
    } catch (e) {
      if (v.key_id === '__new') await api(`/api/keys/${key.id}`, { method: 'DELETE' }).catch(() => {}); // no orphan key per failed attempt
      throw e;
    }
  } catch (e) { err.value = e.message; }
}
const runTest = async () => { test.value = await api(`/api/servers/${created.value.server.id}/test`, { method: 'POST' }); };
const cmd = (key) => `mkdir -p ~/.ssh && echo '${key.public_key}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`;
const openServer = () => { emit('close'); router.push(`/servers/${created.value.server.id}`); };
</script>
<template>
  <Modal v-if="!created" title="Add server" wide :dirty="dirty" @close="emit('close')">
    <Loading v-if="keys.loading" />
    <ServerForm v-else v-model="v" :keys="keys.data || []" />
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }"><button type="button" class="btn" @click="close">Cancel</button><Btn kind="primary" :disabled="!v.host" @click="save">Add server</Btn></template>
  </Modal>
  <Modal v-else :title="`Connect ${created.server.name}`" wide @close="emit('close')">
    <ol class="steps">
      <li v-if="created.key">
        <b>Authorize the key “{{ created.key.name }}”</b>
        <p class="ink2">Run this as {{ created.server.user }} on {{ created.server.host }}, or add the public key through your cloud provider's console.</p>
        <Copy :text="cmd(created.key)" code />
      </li>
      <li>
        <b>Test the connection</b>
        <div class="row"><Btn icon="term" @click="runTest">Test connection</Btn></div>
        <Callout v-if="test?.ok" kind="ok" :title="`Connected as ${test.user}.`">{{ PRIV_TEXT[test.privilege] }}<template v-if="test.host_key"><br><span class="small muted">Host key pinned: {{ test.host_key }}</span></template></Callout>
        <Callout v-else-if="test" kind="bad" title="Couldn't connect.">{{ test.error }}</Callout>
      </li>
    </ol>
    <template #footer><button type="button" class="btn" @click="emit('close')">Close</button><button type="button" class="btn primary" @click="openServer">Open server</button></template>
  </Modal>
</template>
<style scoped>
.steps { margin: 0; padding: 0; list-style: none; counter-reset: step; display: grid; gap: 20px; }
.steps li { counter-increment: step; display: grid; gap: 8px; padding-left: 36px; position: relative; }
.steps li::before { content: counter(step); position: absolute; left: 0; top: -1px; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; background: var(--accent-soft); color: var(--accent); font-size: var(--fs-sm); font-weight: 600; }
</style>
