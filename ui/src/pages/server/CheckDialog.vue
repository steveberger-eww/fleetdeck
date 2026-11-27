<script setup>
import { reactive, ref } from 'vue';
import { Modal, Btn, Field } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ s: Object, check: Object });
const emit = defineEmits(['close', 'saved']);
const c = props.check;
const v = reactive({ kind: 'http', target: '', name: '', contains: '', enabled: true, ...c, expect_status: c.expect_status ?? '' });
if (v.name === v.target) v.name = ''; // an unnamed check shows its target
const dirty = useDirty(() => v);
const err = ref('');

async function save() {
  err.value = '';
  try {
    const body = { ...v, name: v.name === v.target ? '' : v.name, expect_status: Number(v.expect_status) || 0 };
    await api(c.id ? `/api/servers/${props.s.id}/checks/${c.id}` : `/api/servers/${props.s.id}/checks`, { method: c.id ? 'PUT' : 'POST', body });
    toast('Check saved', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal :title="c.id ? 'Edit check' : 'Add check'" :dirty="dirty" @close="emit('close')">
    <div class="seg" role="group" aria-label="Check type">
      <button type="button" :aria-pressed="v.kind === 'http'" @click="v.kind = 'http'">HTTP(S)</button>
      <button type="button" :aria-pressed="v.kind === 'tcp'" @click="v.kind = 'tcp'">TCP port</button>
    </div>
    <Field :label="v.kind === 'http' ? 'URL' : 'Host and port'" :hint="v.kind === 'http' ? 'Redirects are not followed: a 301 counts as up.' : undefined">
      <input v-model="v.target" :placeholder="v.kind === 'http' ? 'https://example.com/health' : `${s.host}:5432`" autocapitalize="off" spellcheck="false" autofocus />
    </Field>
    <div v-if="v.kind === 'http'" class="cols">
      <Field label="Expected status" hint="Empty: anything below 400"><input v-model="v.expect_status" type="number" min="100" max="599" /></Field>
      <Field label="Page must contain (optional)"><input v-model="v.contains" /></Field>
    </div>
    <Field label="Name (optional)"><input v-model="v.name" :placeholder="v.target" /></Field>
    <label class="check"><input v-model="v.enabled" type="checkbox" role="switch" />Enabled</label>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.target" @click="save">Save check</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.seg { justify-self: start; }
</style>
