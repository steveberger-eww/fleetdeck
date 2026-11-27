<script setup>
// Generate an ed25519 key, or import an existing private key.
import { reactive, ref } from 'vue';
import { Btn, Modal, Field } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ mode: { type: String, default: 'generate' } });
const emit = defineEmits(['close', 'saved']);
const gen = props.mode === 'generate';
const v = reactive({ name: '', private_key: '', passphrase: '' });
const dirty = useDirty(() => v);
const err = ref('');
async function save() {
  err.value = '';
  try {
    await api('/api/keys', { method: 'POST', body: gen ? { name: v.name, generate: true } : v });
    toast(gen ? 'Key generated' : 'Key imported', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <Modal :title="gen ? 'Generate SSH key' : 'Import SSH key'" :dirty="dirty" @close="emit('close')">
    <Field label="Name"><input v-model="v.name" placeholder="homelab" autofocus /></Field>
    <p v-if="gen" class="small ink2">Creates an ed25519 key pair. Copy the public key to each server's <code>~/.ssh/authorized_keys</code>, or use the "Authorize SSH public key" script.</p>
    <template v-else>
      <Field label="Private key" hint="OpenSSH or PEM format."><textarea v-model="v.private_key" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></Field>
      <Field label="Passphrase" hint="Only if the key has one. Stored encrypted."><input v-model="v.passphrase" type="password" autocomplete="off" /></Field>
    </template>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name" @click="save">{{ gen ? 'Generate key' : 'Import key' }}</Btn>
    </template>
  </Modal>
</template>
