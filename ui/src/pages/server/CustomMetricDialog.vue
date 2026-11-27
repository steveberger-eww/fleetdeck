<script setup>
// Add or edit a custom metric: a shell command that prints one number, run with every check.
import { reactive, ref, computed } from 'vue';
import { Modal, Btn, Field, Callout, CodeEditor } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { servers, toast, confirmBox } from '@/lib/store.js';

const props = defineProps({ s: Object, metric: Object });
const emit = defineEmits(['close', 'saved']);
const m = props.metric;
const tags = computed(() => [...new Set((servers.value || []).flatMap((x) => x.tags))].sort());
// scope: '' = all servers, 'tag:<name>', or a server id. New metrics start on this server.
const v = reactive({ name: '', unit: '', command: '', ...m, scope: m.id ? m.server_id || (m.tag ? `tag:${m.tag}` : '') : props.s.id });
const dirty = useDirty(() => v);
const err = ref('');

async function save() {
  err.value = '';
  try {
    const tag = v.scope.startsWith('tag:') ? v.scope.slice(4) : null;
    const body = { name: v.name, unit: v.unit, command: v.command, tag, server_id: tag ? null : v.scope || null };
    await api(m.id ? `/api/custom-metrics/${m.id}` : '/api/custom-metrics', { method: m.id ? 'PUT' : 'POST', body });
    if (!m.id) toast(`${v.name} added. The first value arrives with the next check, within a minute.`, 'ok');
    emit('saved');
  } catch (e) { err.value = e.message; }
}
async function del() {
  if (!await confirmBox({ title: `Delete "${m.name}"?`, body: 'Its history and any alert rules that use it are deleted too.', confirm: 'Delete metric', danger: true })) return;
  await api(`/api/custom-metrics/${m.id}`, { method: 'DELETE' });
  emit('saved');
}
</script>

<template>
  <Modal :title="m.id ? `Edit ${m.name}` : 'Add custom metric'" wide :dirty="dirty" @close="emit('close')">
    <Callout kind="warn" title="The command runs as root">
      on every server it applies to, once a minute, with the same rights as Fleetdeck's own checks (root via <code>sudo -n</code> where available). Adding or changing it is recorded in the audit log.
    </Callout>
    <div class="cols">
      <Field label="Name"><input v-model="v.name" placeholder="Backups" maxlength="60" autofocus /></Field>
      <Field label="Unit (optional)"><input v-model="v.unit" placeholder="files" maxlength="16" /></Field>
    </div>
    <div class="field">
      <span>Command</span>
      <CodeEditor v-model="v.command" label="Command" placeholder="ls /var/backups | wc -l" />
      <span class="hint">Must print one number. Anything else is recorded as missing. Stopped after 5 seconds.</span>
    </div>
    <Field label="Applies to" :hint="v.scope.startsWith('tag:') ? 'Includes servers that get this tag later.' : undefined">
      <select v-model="v.scope">
        <option :value="s.id">Only {{ s.name }}</option>
        <optgroup v-if="tags.length" label="Servers tagged"><option v-for="t in tags" :key="t" :value="`tag:${t}`">{{ t }}</option></optgroup>
        <option value="">All servers</option>
      </select>
    </Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <Btn v-if="m.id" kind="ghost" icon="trash" @click="del">Delete</Btn>
      <span class="grow" />
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name.trim() || !v.command.trim()" @click="save">Save metric</Btn>
    </template>
  </Modal>
</template>
