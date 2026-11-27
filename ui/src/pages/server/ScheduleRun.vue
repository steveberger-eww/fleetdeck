<script setup>
// One-off run of a script on this server, "at 22:00" or "in 3 hours".
import { reactive, ref, computed } from 'vue';
import { Modal, Btn, Field, Loading, ParamInput } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { toast } from '@/lib/store.js';
import { when } from '@/lib/format.js';

const props = defineProps({ s: Object, scriptId: { type: String, default: 'update' } });
const emit = defineEmits(['close']); // close(saved)
const scripts = useLoad(() => api('/api/scripts'));
const v = reactive({ script_id: props.scriptId, params: {}, mode: 'at', hours: 2, time: '22:00' });
const err = ref('');
const script = computed(() => scripts.data?.find((x) => x.id === v.script_id));

const HHMM = /^([01]?\d|2[0-3]):([0-5]\d)$/;
const runAt = computed(() => {
  if (v.mode === 'in') return Number(v.hours) > 0 ? Date.now() + Number(v.hours) * 3600e3 : null;
  const m = HHMM.exec(v.time.trim());
  if (!m) return null;
  const d = new Date();
  d.setHours(+m[1], +m[2], 0, 0);
  if (d <= Date.now()) d.setDate(d.getDate() + 1); // already past today: tomorrow
  return d.getTime();
});

async function save() {
  err.value = '';
  try {
    const at = runAt.value;
    await api('/api/schedules', { method: 'POST', body: { name: `${script.value?.name || v.script_id} on ${props.s.name}`.slice(0, 100), script_id: v.script_id, params: v.params, targets: { servers: [props.s.id] }, run_at: at } });
    toast(`Scheduled for ${when(at)}`, 'ok', { label: 'Show', to: `/servers/${props.s.id}/jobs` });
    emit('close', true);
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal :title="`Run later on ${s.name}`" @close="emit('close')">
    <Field label="Script">
      <Loading v-if="scripts.loading" :rows="1" />
      <select v-else v-model="v.script_id" @change="v.params = {}"><option v-for="x in scripts.data" :key="x.id" :value="x.id">{{ x.name }}</option></select>
    </Field>
    <ParamInput v-for="p in script?.params || []" :key="p.name" :p="p" :model-value="v.params[p.name] ?? p.default" @update:model-value="(x) => { v.params[p.name] = x; }" />
    <div class="seg" role="group" aria-label="When">
      <button type="button" :aria-pressed="v.mode === 'at'" @click="v.mode = 'at'">At a time</button>
      <button type="button" :aria-pressed="v.mode === 'in'" @click="v.mode = 'in'">In some hours</button>
    </div>
    <Field v-if="v.mode === 'at'" label="Time (24-hour)" hint="Today, or tomorrow if that time has passed"><input v-model="v.time" inputmode="numeric" placeholder="22:00" autofocus /></Field>
    <Field v-else label="Hours from now"><input v-model="v.hours" type="number" min="0.25" step="0.25" autofocus /></Field>
    <p class="small ink2">{{ runAt ? `Runs ${when(runAt)}, once.` : 'Enter a time like 22:00.' }}</p>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!runAt || !script" @click="save">Schedule</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.seg { justify-self: start; }
</style>
