<script setup>
import { reactive, ref, computed } from 'vue';
import { Modal, Btn, Field } from '@/components';
import ScopeSelect from './ScopeSelect.vue';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { servers } from '@/lib/store.js';
import { NEEDS_THRESHOLD, NAMED, HAS_DURATION, FOR_AT_LEAST, HAS_MOUNT, inScope, scopeBody } from './rules.js';

const props = defineProps({ rule: { type: Object, required: true }, metrics: Object, custom: Array });
const emit = defineEmits(['close', 'saved']);
const r0 = props.rule;
const v = reactive({
  metric: 'cpu', threshold: 90, minutes: 5, enabled: true, ...r0,
  mount: r0.mount || '', process: r0.process || '', custom_id: r0.custom_id || props.custom[0]?.id || '', scope: r0.server_id || (r0.tag ? `tag:${r0.tag}` : ''),
});
const dirty = useDirty(() => v);
const err = ref('');
const m = computed(() => (v.metric === 'custom' ? { ...props.metrics.custom, unit: props.custom.find((c) => c.id === v.custom_id)?.unit || '' } : props.metrics[v.metric]));
const scoped = computed(() => { const s = scopeBody(v.scope); return inScope(s, servers.value || []); });
const containers = computed(() => [...new Set(scoped.value.flatMap((s) => (s.snapshot?.containers || []).map((c) => c.name)))].sort());
const mounts = computed(() => [...new Set(scoped.value.flatMap((s) => (s.snapshot?.disks || []).map((d) => d.mount)))].sort());
const pickMetric = (k) => { v.metric = k; v.threshold = props.metrics[k].def ?? v.threshold; };

async function save() {
  err.value = '';
  try {
    const body = {
      metric: v.metric, enabled: v.enabled, threshold: Number(v.threshold), minutes: Number(v.minutes), ...scopeBody(v.scope),
      mount: HAS_MOUNT(v.metric) ? v.mount : null, custom_id: v.metric === 'custom' ? v.custom_id : null, process: NAMED(v.metric) ? v.process.trim() : null,
    };
    await api(props.rule.id ? `/api/alerts/rules/${props.rule.id}` : '/api/alerts/rules', { method: props.rule.id ? 'PUT' : 'POST', body });
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal :title="rule.id ? 'Edit rule' : 'Add rule'" :dirty="dirty" @close="emit('close')">
    <Field label="Alert when">
      <select :value="v.metric" @change="pickMetric($event.target.value)"><option v-for="(x, k) in metrics" :key="k" :value="k">{{ x.label }}</option></select>
    </Field>
    <Field v-if="v.metric === 'custom'" label="Custom metric" :hint="!custom.length ? 'Add one in a server\'s Metrics panel first.' : undefined">
      <select v-model="v.custom_id"><option v-for="c in custom" :key="c.id" :value="c.id">{{ c.name }}{{ c.unit ? ` (${c.unit})` : '' }}</option></select>
    </Field>
    <Field v-if="v.metric === 'process'" label="Process name" hint="As ps -e shows it (the program name, not the full command line).">
      <input v-model="v.process" placeholder="nginx" autocapitalize="off" spellcheck="false" />
    </Field>
    <Field v-if="v.metric === 'container'" label="Container name" hint="As docker ps shows it. Compose names it project-service-1.">
      <input v-model="v.process" list="rule-containers" placeholder="web-app-1" autocapitalize="off" spellcheck="false" />
      <datalist id="rule-containers"><option v-for="x in containers" :key="x" :value="x" /></datalist>
    </Field>
    <div v-if="NEEDS_THRESHOLD(v.metric) || HAS_DURATION(v.metric)" class="cols">
      <Field v-if="NEEDS_THRESHOLD(v.metric)" :label="`Is ${m.below ? 'below' : 'above'}${m.unit ? ` (${m.unit.trim()})` : ''}`">
        <input v-model="v.threshold" type="number" min="0" step="any" />
      </Field>
      <Field v-if="HAS_DURATION(v.metric)" :label="FOR_AT_LEAST(v.metric) ? 'For at least (minutes)' : 'Averaged over (minutes)'" :hint="!FOR_AT_LEAST(v.metric) ? '0 uses the latest value' : undefined">
        <input v-model="v.minutes" type="number" min="0" max="1440" />
      </Field>
    </div>
    <Field label="Applies to" :hint="v.scope.startsWith('tag:') ? 'Includes servers that get this tag later.' : undefined"><ScopeSelect v-model="v.scope" /></Field>
    <Field v-if="HAS_MOUNT(v.metric)" label="Mount (optional)" hint="Empty watches the fullest mount.">
      <input v-model="v.mount" list="rule-mounts" placeholder="/data" autocapitalize="off" spellcheck="false" />
      <datalist id="rule-mounts"><option v-for="x in mounts" :key="x" :value="x" /></datalist>
    </Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" @click="save">Save rule</Btn>
    </template>
  </Modal>
</template>
