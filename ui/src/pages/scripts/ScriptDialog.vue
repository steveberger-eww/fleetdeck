<script setup>
// New / edit script: shell body or Ansible playbook, parameters, shared variables, retries and what runs next.
import { reactive, ref, computed } from 'vue';
import { Modal, Btn, Field, Callout, CodeEditor } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useDirty } from '@/lib/use.js';
import { isSuper, toast } from '@/lib/store.js';
import { NEXT_WHEN } from './util.js';

const props = defineProps({ script: { type: Object, required: true } });
const emit = defineEmits(['close', 'saved']);
const v = reactive({
  name: '', description: '', body: '#!/bin/sh\nset -e\n\n', as_root: true, params: [], vars: [], kind: 'shell', repo_id: '', playbook: '',
  next_script_id: '', next_when: 'success', next_ok_only: false, retries: 0, retry_delay: 5, ...JSON.parse(JSON.stringify(props.script)),
});
v.repo_id ??= '';
v.next_script_id ??= '';
const dirty = useDirty(() => v);
const err = ref('');
const ansible = computed(() => v.kind === 'ansible');

const all = useLoad(() => api('/api/scripts'));
const repos = useLoad(() => api('/api/repos'));
const shared = useLoad(() => api('/api/variables'));
const files = useLoad(() => (ansible.value && v.repo_id ? api(`/api/repos/${v.repo_id}/files`) : Promise.resolve([])), { key: () => `${v.kind}:${v.repo_id}` });
// A shared script can only run another shared one next.
const nextChoices = computed(() => (all.data || []).filter((x) => x.id !== props.script.id && (!(props.script.shared || v.shared) || x.shared)));
const playbookHint = computed(() => files.error || (files.loading && v.repo_id ? 'Loading files…' : 'Path inside the repository'));

const addParam = () => v.params.push({ name: '', label: '', default: '', required: false });
const moveParam = (i, d) => { const ps = v.params; [ps[i], ps[i + d]] = [ps[i + d], ps[i]]; };
const setType = (p, type) => Object.assign(p, { type, default: type === 'checkbox' ? 'false' : '' });
const choices = (p) => (p.options || []).map((o) => o.trim()).filter(Boolean);
const toggleVar = (name) => { v.vars = v.vars.includes(name) ? v.vars.filter((n) => n !== name) : [...v.vars, name]; };

async function save() {
  err.value = '';
  try {
    await api(props.script.id ? `/api/scripts/${props.script.id}` : '/api/scripts', { method: props.script.id ? 'PUT' : 'POST', body: v });
    toast('Script saved', 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
</script>

<template>
  <Modal wide :title="script.id ? `Edit ${script.name}` : 'New script'" :dirty="dirty" @close="emit('close')">
    <div class="cols">
      <Field label="Name"><input v-model="v.name" autofocus /></Field>
      <Field label="Description"><input v-model="v.description" /></Field>
    </div>
    <div v-if="!script.id" class="seg" role="group" aria-label="Script type">
      <button type="button" :aria-pressed="!ansible" @click="v.kind = 'shell'">Shell script</button>
      <button type="button" :aria-pressed="ansible" @click="v.kind = 'ansible'">Ansible playbook</button>
    </div>

    <template v-if="ansible">
      <Callout v-if="repos.data && !repos.data.length" kind="info">Add a Git repository first.
        <template #actions><RouterLink class="btn sm" to="/repos">Repositories</RouterLink></template>
      </Callout>
      <div v-else class="cols">
        <Field label="Repository">
          <select v-model="v.repo_id"><option value="">Choose a repository</option><option v-for="r in repos.data || []" :key="r.id" :value="r.id">{{ r.name }} ({{ r.branch }})</option></select>
        </Field>
        <Field label="Playbook" :hint="playbookHint">
          <input v-model="v.playbook" list="fd-playbooks" placeholder="site.yml" autocapitalize="off" spellcheck="false" />
          <datalist id="fd-playbooks"><option v-for="f in files.data || []" :key="f" :value="f" /></datalist>
        </Field>
      </div>
      <label class="check"><input v-model="v.as_root" type="checkbox" />Become root (ansible_become)</label>
      <p class="small ink2">Fleetdeck builds the inventory from the servers you pick: hosts use the server names, tags become groups.</p>
    </template>
    <template v-else>
      <Field label="Script" hint="Runs with sh on each server. Non-zero exit marks that server as failed."><CodeEditor v-model="v.body" tall label="Script" /></Field>
      <label class="check"><input v-model="v.as_root" type="checkbox" />Run as root (via sudo when the SSH user isn't root)</label>
    </template>

    <div class="field">
      <span>{{ ansible ? 'Extra variables' : 'Parameters' }}</span>
      <div v-for="(p, i) in v.params" :key="i" class="param">
        <div class="row nw">
          <input :value="p.name" :placeholder="ansible ? 'name' : 'NAME'" aria-label="Variable name" class="pname" autocapitalize="off" spellcheck="false"
            @input="p.name = ansible ? $event.target.value : $event.target.value.toUpperCase()" />
          <input v-model="p.label" placeholder="Label" aria-label="Label" class="grow" />
          <select :value="p.type || 'text'" aria-label="Type" class="auto" @change="setType(p, $event.target.value)">
            <option value="text">Text</option><option value="checkbox">Checkbox</option><option value="choice">Choice</option>
          </select>
          <Btn size="sm" kind="ghost" icon="up" title="Move up" :disabled="i === 0" @click="moveParam(i, -1)" />
          <Btn size="sm" kind="ghost" icon="chev" title="Move down" :disabled="i === v.params.length - 1" @click="moveParam(i, 1)" />
          <Btn size="sm" kind="ghost" icon="x" title="Remove parameter" @click="v.params.splice(i, 1)" />
        </div>
        <div class="row nw">
          <label v-if="p.type === 'checkbox'" class="check small nowrap"><input type="checkbox" :checked="p.default === 'true'" @change="p.default = String($event.target.checked)" />On by default</label>
          <template v-else-if="p.type === 'choice'">
            <input :value="(p.options || []).join(',')" placeholder="Choices, comma-separated" aria-label="Choices" class="grow" @input="p.options = $event.target.value.split(',')" />
            <select v-model="p.default" aria-label="Default choice" class="auto"><option v-for="o in choices(p)" :key="o" :value="o">{{ o }}</option></select>
          </template>
          <template v-else>
            <input v-model="p.default" placeholder="Default" aria-label="Default value" class="grow" />
            <label class="check small nowrap"><input v-model="p.required" type="checkbox" />Required</label>
          </template>
        </div>
      </div>
      <div><Btn size="sm" icon="plus" @click="addParam">Add parameter</Btn></div>
    </div>

    <label v-if="!script.id && isSuper()" class="check"><input v-model="v.shared" type="checkbox" />Add to the shared library (all organizations, read-only for them)</label>

    <div class="field">
      <span>Shared variables</span>
      <div v-if="shared.data?.length" class="row" role="group" aria-label="Shared variables">
        <button v-for="x in shared.data" :key="x.name" type="button" class="pill" :aria-pressed="v.vars.includes(x.name)" :title="x.description" @click="toggleVar(x.name)">{{ x.name }}</button>
      </div>
      <span v-else class="small muted">None yet. Add tokens and passwords under <RouterLink to="/variables">Variables</RouterLink>.</span>
      <span class="hint">Ticked variables are passed {{ ansible ? 'as extra vars' : 'as environment variables' }}. Secret values are masked in the output.</span>
    </div>

    <div class="field">
      <span>If it fails</span>
      <div class="row nw">
        <select v-model.number="v.retries" class="grow" aria-label="Retries">
          <option v-for="n in [0, 1, 2, 3, 4, 5]" :key="n" :value="n">{{ n ? `Retry ${n === 1 ? 'once' : `up to ${n} times`}` : "Don't retry" }}</option>
        </select>
        <select v-if="v.retries > 0" v-model.number="v.retry_delay" class="auto" aria-label="Wait between attempts">
          <option v-for="m in [1, 2, 5, 10, 15, 30, 60]" :key="m" :value="m">after {{ m }} min</option>
        </select>
      </div>
      <span v-if="v.retries > 0" class="hint">Only the servers that failed run again. Chains and "job failed" notifications wait for the last attempt. Dry runs don't retry.</span>
    </div>

    <div class="field">
      <span>After this script finishes, run</span>
      <div class="row nw">
        <select v-model="v.next_script_id" class="grow" aria-label="Next script">
          <option value="">Nothing</option><option v-for="x in nextChoices" :key="x.id" :value="x.id">{{ x.name }}</option>
        </select>
        <select v-if="v.next_script_id" v-model="v.next_when" class="auto" aria-label="When">
          <option v-for="(l, k) in NEXT_WHEN" :key="k" :value="k">{{ l }}</option>
        </select>
      </div>
      <template v-if="v.next_script_id">
        <label class="check"><input v-model="v.next_ok_only" type="checkbox" />Only on the servers where this script succeeded</label>
        <span class="hint">The next script uses its parameter defaults. Cancelled jobs and dry runs don't continue; a chain stops after 10 jobs.</span>
      </template>
    </div>

    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name" @click="save">Save script</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.param { display: grid; gap: 6px; padding: 10px; border: 1px solid var(--line); border-radius: var(--r); background: var(--panel-2); }
.pname { width: 160px; flex: none; }
@media (max-width: 700px) { .param .row { flex-wrap: wrap; } .pname { width: 100%; } }
</style>
