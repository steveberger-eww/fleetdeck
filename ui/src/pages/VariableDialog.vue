<script setup>
import { reactive, ref } from 'vue';
import { Btn, Modal, Field } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ variable: { type: Object, required: true } });
const emit = defineEmits(['close', 'saved']);
const id = props.variable.id;
const v = reactive({ name: '', description: '', secret: true, ...props.variable, value: props.variable.value || '' });
const dirty = useDirty(() => v);
const err = ref('');
async function save() {
  err.value = '';
  try {
    await api(id ? `/api/variables/${id}` : '/api/variables', { method: id ? 'PUT' : 'POST', body: v });
    toast('Variable saved', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <Modal :title="id ? `Edit ${variable.name}` : 'New variable'" :dirty="dirty" @close="emit('close')">
    <Field label="Name" :hint="id ? 'Scripts refer to it by name, so it can\'t be renamed.' : 'Becomes $NAME in shell scripts and {{ NAME }} in playbooks.'">
      <input v-model="v.name" :disabled="!!id" :autofocus="!id" placeholder="CLOUDFLARE_TOKEN" autocapitalize="off" spellcheck="false" />
    </Field>
    <Field label="Value" :hint="id && variable.secret ? 'Saved. Leave empty to keep it.' : undefined">
      <textarea v-model="v.value" spellcheck="false" :autofocus="!!id" />
    </Field>
    <Field label="Description"><input v-model="v.description" /></Field>
    <label class="check"><input v-model="v.secret" type="checkbox" role="switch" />Secret: never show the value again, and mask it in job output</label>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name" @click="save">Save variable</Btn>
    </template>
  </Modal>
</template>
