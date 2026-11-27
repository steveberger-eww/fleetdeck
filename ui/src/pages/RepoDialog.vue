<script setup>
import { reactive, ref, computed } from 'vue';
import { Btn, Modal, Field, Copy } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ repo: { type: Object, required: true } });
const emit = defineEmits(['close', 'saved']);
const id = props.repo.id;
const keys = useLoad(() => api('/api/keys'));
const v = reactive({ name: '', url: '', branch: 'main', auth: 'none', key_id: '', username: 'git', token: '', ...props.repo });
const dirty = useDirty(() => v);
const err = ref('');
const key = computed(() => keys.data?.find((k) => k.id === v.key_id));
async function save() {
  err.value = '';
  try {
    await api(id ? `/api/repos/${id}` : '/api/repos', { method: id ? 'PUT' : 'POST', body: v });
    toast(id ? 'Repository saved' : 'Repository added, cloning now', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <Modal :title="id ? `Edit ${repo.name}` : 'Add repository'" :dirty="dirty" @close="emit('close')">
    <div class="cols">
      <Field label="Name"><input v-model="v.name" placeholder="infra" autofocus /></Field>
      <Field label="Branch"><input v-model="v.branch" autocapitalize="off" spellcheck="false" /></Field>
    </div>
    <Field label="URL" hint="https://github.com/org/infra.git or git@github.com:org/infra.git"><input v-model="v.url" autocapitalize="off" spellcheck="false" /></Field>
    <Field label="Access">
      <select v-model="v.auth">
        <option value="none">Public repository (or the Fleetdeck host's ssh-agent)</option>
        <option value="ssh">SSH deploy key</option>
        <option value="token">HTTPS access token</option>
      </select>
    </Field>
    <template v-if="v.auth === 'ssh'">
      <Field label="SSH key" hint="Add its public key as a read-only deploy key on your Git host.">
        <select v-model="v.key_id"><option value="">Choose a key</option><option v-for="k in keys.data || []" :key="k.id" :value="k.id">{{ k.name }}</option></select>
      </Field>
      <Copy v-if="key" :text="key.public_key" />
    </template>
    <div v-if="v.auth === 'token'" class="cols">
      <Field label="Username" hint="GitHub: x-access-token, GitLab: oauth2"><input v-model="v.username" autocapitalize="off" spellcheck="false" /></Field>
      <Field label="Access token" :hint="repo.has_token ? 'Saved. Leave empty to keep it.' : 'Read-only scope is enough. Stored encrypted.'"><input v-model="v.token" type="password" autocomplete="off" /></Field>
    </div>
    <p class="small ink2">Playbooks run on the Fleetdeck host and can execute commands there too (local tasks, lookups). Only add repositories you control.</p>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.url" @click="save">{{ id ? 'Save repository' : 'Add repository' }}</Btn>
    </template>
  </Modal>
</template>
