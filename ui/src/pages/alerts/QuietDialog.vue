<script setup>
import { reactive, ref } from 'vue';
import { Modal, Btn, Field } from '@/components';
import ScopeSelect from './ScopeSelect.vue';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { scopeBody } from './rules.js';

defineProps({ tz: String });
const emit = defineEmits(['close', 'saved']);
const localInput = (t) => new Date(t - new Date(t).getTimezoneOffset() * 60e3).toISOString().slice(0, 16);
const v = reactive({ kind: 'daily', name: '', start: '22:00', end: '07:00', from: localInput(Date.now()), to: localInput(Date.now() + 2 * 3600e3), scope: '' });
const dirty = useDirty(() => v);
const err = ref('');

async function save() {
  err.value = '';
  try {
    const times = v.kind === 'daily' ? { start: v.start, end: v.end } : { start: new Date(v.from).getTime(), end: new Date(v.to).getTime() };
    await api('/api/mutes', { method: 'POST', body: { kind: v.kind, name: v.name, ...times, ...scopeBody(v.scope) } });
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal title="Add quiet hours" :dirty="dirty" @close="emit('close')">
    <div class="seg" role="group" aria-label="Kind">
      <button type="button" :aria-pressed="v.kind === 'daily'" @click="v.kind = 'daily'">Every day</button>
      <button type="button" :aria-pressed="v.kind === 'once'" @click="v.kind = 'once'">One time</button>
    </div>
    <template v-if="v.kind === 'daily'">
      <div class="cols">
        <Field label="From"><input v-model="v.start" type="time" /></Field>
        <Field label="Until"><input v-model="v.end" type="time" /></Field>
      </div>
      <p class="small ink2">In the Fleetdeck server's time zone{{ tz ? ` (${tz})` : '' }}. A window past midnight, like 22:00–07:00, works.</p>
    </template>
    <div v-else class="cols">
      <Field label="From"><input v-model="v.from" type="datetime-local" /></Field>
      <Field label="Until"><input v-model="v.to" type="datetime-local" /></Field>
    </div>
    <Field label="Applies to"><ScopeSelect v-model="v.scope" /></Field>
    <Field label="Note (optional)"><input v-model="v.name" placeholder="Datacenter maintenance" /></Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" @click="save">Save</Btn>
    </template>
  </Modal>
</template>
