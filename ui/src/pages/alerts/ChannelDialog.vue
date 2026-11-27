<script setup>
import { reactive, ref, computed } from 'vue';
import { Modal, Btn, Field } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ channel: { type: Object, required: true }, types: Object });
const emit = defineEmits(['close', 'saved']);
// Saved secrets come back masked; an empty field keeps them.
const v = reactive({ name: '', type: 'ntfy', enabled: true, ...props.channel, config: Object.fromEntries(Object.entries(props.channel.config || {}).map(([k, x]) => [k, x === '••••••' ? '' : x])) });
const dirty = useDirty(() => v);
const err = ref('');
const t = computed(() => props.types[v.type]);
const hint = (f) => (f.secret && props.channel.id && props.channel.config?.[f.key] ? 'Saved. Leave empty to keep it.' : f.secret ? 'Stored encrypted.' : undefined);
const pickType = (type) => { v.type = type; v.config = {}; };

async function save() {
  err.value = '';
  try {
    await api(props.channel.id ? `/api/notifiers/${props.channel.id}` : '/api/notifiers', { method: props.channel.id ? 'PUT' : 'POST', body: v });
    toast('Channel saved', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal :title="channel.id ? `Edit ${channel.name}` : 'Add notification channel'" :dirty="dirty" @close="emit('close')">
    <div class="cols">
      <Field label="Type">
        <select :value="v.type" :disabled="!!channel.id" @change="pickType($event.target.value)"><option v-for="(x, k) in types" :key="k" :value="k">{{ x.label }}</option></select>
      </Field>
      <Field label="Name"><input v-model="v.name" placeholder="My phone" /></Field>
    </div>
    <Field v-for="f in t.fields" :key="f.key" :label="f.label" :hint="hint(f)">
      <input v-model="v.config[f.key]" :type="f.secret ? 'password' : 'text'" autocomplete="off" autocapitalize="off" spellcheck="false" />
    </Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name" @click="save">Save channel</Btn>
    </template>
  </Modal>
</template>
