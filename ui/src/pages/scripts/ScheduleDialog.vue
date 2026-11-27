<script setup>
// New / edit schedule, or (with `webhook`) a webhook: same fields without the cron.
import { reactive, ref, computed, watch } from 'vue';
import { Modal, Btn, Field, Loading, ParamInput } from '@/components';
import TargetPicker from '../jobs/TargetPicker.vue';
import Switch from '@/components/Switch.vue';
import { api } from '@/lib/api.js';
import { useLoad, useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';
import { when } from '@/lib/format.js';
import { PRESETS } from './util.js';

const props = defineProps({ schedule: { type: Object, required: true }, webhook: Boolean });
const emit = defineEmits(['close', 'saved']);
const scripts = useLoad(() => api('/api/scripts'));
const v = reactive({ name: '', script_id: 'update', cron: '0 4 * * *', targets: { all: true }, params: {}, enabled: true, ...JSON.parse(JSON.stringify(props.schedule)) });
const dirty = useDirty(() => v);
const preview = ref({ next: [], error: '' });
const err = ref('');
const script = computed(() => scripts.data?.find((s) => s.id === v.script_id));
const title = props.schedule.id ? `Edit ${props.schedule.name}` : props.webhook ? 'New webhook' : 'New schedule';

watch(() => v.cron, (cron, _, onCleanup) => {
  if (props.webhook) return;
  const t = setTimeout(() => api('/api/cron/preview', { method: 'POST', body: { cron } })
    .then((r) => { preview.value = { next: r.next, error: '' }; }, (e) => { preview.value = { next: [], error: e.message }; }), 250);
  onCleanup(() => clearTimeout(t));
}, { immediate: true });

const pickScript = (id) => { v.script_id = id; v.params = {}; };
async function save() {
  err.value = '';
  try {
    const base = props.webhook ? '/api/triggers' : '/api/schedules';
    const r = await api(props.schedule.id ? `${base}/${props.schedule.id}` : base, { method: props.schedule.id ? 'PUT' : 'POST', body: v });
    toast(props.webhook ? 'Webhook saved' : 'Schedule saved', 'ok');
    emit('saved', r);
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal wide :title="title" :dirty="dirty" @close="emit('close')">
    <div class="cols">
      <Field label="Name"><input v-model="v.name" autofocus /></Field>
      <Field label="Script">
        <Loading v-if="scripts.loading" :rows="1" />
        <select v-else :value="v.script_id" @change="pickScript($event.target.value)"><option v-for="s in scripts.data || []" :key="s.id" :value="s.id">{{ s.name }}</option></select>
      </Field>
    </div>
    <ParamInput v-for="p in script?.params || []" :key="`${v.script_id}:${p.name}`" :p="p" :model-value="v.params[p.name] ?? p.default" @update:model-value="(x) => { v.params[p.name] = x; }" />

    <template v-if="!webhook">
      <Field label="When" hint="Cron format: minute hour day-of-month month day-of-week">
        <input v-model="v.cron" autocapitalize="off" spellcheck="false" />
      </Field>
      <div class="row" role="group" aria-label="Presets">
        <button v-for="[l, c] in PRESETS" :key="c" type="button" class="pill" :aria-pressed="v.cron === c" @click="v.cron = c">{{ l }}</button>
      </div>
      <p v-if="preview.error" class="err">{{ preview.error }}</p>
      <p v-else class="small ink2">Next runs: {{ preview.next.map(when).join('; ') || '–' }}</p>
    </template>

    <TargetPicker v-model="v.targets" />
    <label class="check switch-row"><Switch :checked="v.enabled" @change="v.enabled = $event.target.checked" />Enabled</label>
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name" @click="save">{{ webhook ? 'Save webhook' : 'Save schedule' }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.switch-row { align-items: center; }
</style>
