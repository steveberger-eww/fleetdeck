<script setup>
// Run a saved script or a one-off command on some servers. Hosted by the shell: openRun({ targets, scriptId }).
import { ref, computed, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Modal, Btn, Field, Loading, ParamInput, CodeEditor } from '@/components';
import TargetPicker from './TargetPicker.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { servers } from '@/lib/store.js';
import { confirmRun, resolveTargets } from '@/lib/actions.js';
import { plural } from '@/lib/format.js';

const props = defineProps({ targets: Object, scriptId: String });
const emit = defineEmits(['close']);
const router = useRouter();

const lastScript = () => { try { return sessionStorage.getItem('fd-last-script') || 'update'; } catch { return 'update'; } };
const scripts = useLoad(() => api('/api/scripts'));
const mode = ref('script');
const scriptId = ref(props.scriptId || lastScript());
const params = ref({});
const command = ref('');
const asRoot = ref(true);
const check = ref(false);
const targets = ref(props.targets || { servers: [], tags: [] });
const err = ref('');

const script = computed(() => scripts.data?.find((s) => s.id === scriptId.value));
watch(() => script.value?.id, () => { if (script.value) params.value = Object.fromEntries(script.value.params.map((p) => [p.name, p.default])); }, { immediate: true });
const picked = computed(() => resolveTargets(targets.value, servers.value || []));
const n = computed(() => picked.value.length);
const where = computed(() => (n.value === 0 ? 'Pick at least one server.' : `${picked.value.slice(0, 3).map((s) => s.name).join(', ')}${n.value > 3 ? ` and ${n.value - 3} more` : ''}`));
const varRef = (p) => (script.value.kind === 'ansible' ? `{{ ${p.name} }}` : `$${p.name}`);

async function run() {
  err.value = '';
  const isScript = mode.value === 'script';
  if (isScript && !await confirmRun({ scriptId: scriptId.value, name: script.value?.name || scriptId.value, count: n.value, names: picked.value.map((s) => s.name) })) return;
  try {
    const body = isScript
      ? { script_id: scriptId.value, params: params.value, targets: targets.value, options: { check: check.value } }
      : { body: command.value, as_root: asRoot.value, targets: targets.value };
    const { id } = await api('/api/jobs', { method: 'POST', body });
    if (isScript) try { sessionStorage.setItem('fd-last-script', scriptId.value); } catch { /* private mode */ }
    emit('close');
    router.push(`/jobs/${id}`);
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal title="Run a script" wide @close="emit('close')">
    <div class="seg" role="group" aria-label="What to run">
      <button type="button" :aria-pressed="mode === 'script'" @click="mode = 'script'">Saved script</button>
      <button type="button" :aria-pressed="mode === 'command'" @click="mode = 'command'">One-off command</button>
    </div>

    <template v-if="mode === 'script'">
      <Loading v-if="scripts.loading" />
      <template v-else>
        <Field label="Script" :hint="script?.description">
          <select v-model="scriptId"><option v-for="s in scripts.data || []" :key="s.id" :value="s.id">{{ s.name }}</option></select>
        </Field>
        <ParamInput v-for="p in script?.params || []" :key="p.name" v-model="params[p.name]" :p="p">
          <template #hint>{{ script.kind === 'ansible' ? 'Extra variable' : 'Available as' }} <code>{{ varRef(p) }}</code></template>
        </ParamInput>
        <template v-if="script?.kind === 'ansible'">
          <p class="small ink2">Runs <code>ansible-playbook {{ script.playbook }}</code> from the latest commit of its repository.</p>
          <label class="check"><input v-model="check" type="checkbox" />Dry run: show what would change (--check --diff)</label>
        </template>
        <details v-else-if="script"><summary>Show script</summary><CodeEditor :key="script.id" :model-value="script.body" read-only label="Script" /></details>
      </template>
    </template>

    <template v-else>
      <Field label="Command" hint="Runs with sh. Multiple lines are fine."><CodeEditor v-model="command" autofocus label="Command" placeholder="df -h /" /></Field>
      <label class="check"><input v-model="asRoot" type="checkbox" />Run as root (via sudo when the SSH user isn't root)</label>
    </template>

    <TargetPicker v-model="targets" />
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <span class="small muted grow trunc" :title="picked.map((s) => s.name).join(', ')">{{ where }}</span>
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" icon="play" :disabled="!n" @click="run">Run on {{ plural(n, 'server') }}</Btn>
    </template>
  </Modal>
</template>
