<script setup>
// Edit a Compose stack's files: which ones comes from its containers' labels. The server checks the result with
// `docker compose config` before writing and keeps the old file as .bak; "Save and apply" then pulls and runs up -d.
// bump ({ from, to }): opens with that image reference already changed, e.g. from "Use 1.27.2" in a container's details.
import { reactive, ref, computed, watch } from 'vue';
import { Modal, Btn, Callout, CodeEditor, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { toast } from '@/lib/store.js';
import { runOn } from './util.js';

const props = defineProps({ s: Object, g: Object, bump: Object });
const emit = defineEmits(['close']);
const d = useLoad(() => api(`/api/servers/${props.s.id}/compose?project=${encodeURIComponent(props.g.name)}`));
const text = reactive({}); // path -> what's in the editor
const cur = ref('');
const bumped = ref(null); // files the bump changed; [] = the image wasn't found
const err = ref('');

const base = (p) => p.split('/').pop();
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
watch(() => d.data, (c) => {
  if (!c) return;
  for (const f of c.files) text[f.path] = f.content;
  cur.value = c.files.find((f) => !f.missing)?.path || c.files[0]?.path || '';
  if (!props.bump) return;
  // `image: nginx:1.25.3`, quoted or not, with or without a comment after it.
  const src = `^(\\s*image:\\s*["']?)${esc(props.bump.from)}(["']?\\s*(?:#.*)?)$`;
  const re = new RegExp(src, 'gm');
  bumped.value = c.files.filter((f) => !f.env && !f.missing && new RegExp(src, 'm').test(f.content)).map((f) => f.path);
  for (const p of bumped.value) text[p] = text[p].replace(re, `$1${props.bump.to}$2`);
  if (bumped.value.length) cur.value = bumped.value[0];
}, { immediate: true });

const file = computed(() => d.data?.files.find((f) => f.path === cur.value));
const changed = computed(() => (d.data?.files || []).filter((f) => !f.missing && text[f.path] !== f.content));
const dirty = computed(() => changed.value.length > 0);

async function save(apply) {
  err.value = '';
  try {
    await api(`/api/servers/${props.s.id}/compose`, { method: 'PUT', body: { project: props.g.name, files: changed.value.map((f) => ({ path: f.path, content: text[f.path], hash: f.hash })) } });
  } catch (e) { err.value = e.message; return; }
  toast(`${props.g.name}: saved`, 'ok');
  if (apply) await runOn(props.s, 'compose-update', { PROJECT: props.g.name, DIR: props.g.dir, FILES: props.g.files });
  emit('close');
}
</script>

<template>
  <Modal size="xl" :title="`Compose: ${g.name}`" :dirty="dirty" @close="emit('close')">
    <ErrorNote v-if="d.error" :error="d.error" :retry="d.reload" />
    <Loading v-else-if="!d.data" :rows="8" />
    <template v-else>
      <div class="row">
        <div v-if="d.data.files.length > 1" class="seg" role="group" aria-label="File">
          <button v-for="f in d.data.files" :key="f.path" type="button" :aria-pressed="f.path === cur" :title="f.path" @click="cur = f.path">
            {{ base(f.path) }}<span v-if="changed.includes(f)" class="dot" aria-label="changed" />
          </button>
        </div>
        <span class="small muted trunc" :title="cur">{{ cur }}</span>
      </div>
      <Callout v-if="bumped && bumped.length" kind="info">Changed the image to <b>{{ bump.to }}</b> in {{ bumped.map(base).join(', ') }}. Review it, then save and apply.</Callout>
      <Callout v-else-if="bumped" kind="warn">Couldn't find <b>image: {{ bump.from }}</b> in the files, maybe it's set through a variable. Change it to <b>{{ bump.to }}</b> by hand.</Callout>
      <Callout v-if="file?.env" kind="warn">Environment file: its values are often passwords. Compose reads it on the next apply.</Callout>
      <Callout v-if="file?.missing" kind="bad" title="Not on the server.">The stack was started with this file, but it's gone. Fleetdeck can only edit files that exist.</Callout>
      <CodeEditor v-else-if="file" :key="cur" v-model="text[cur]" :lang="file.env ? '' : 'yaml'" tall :label="base(cur)" />
      <Callout v-if="err" kind="bad"><span class="pre-wrap">{{ err }}</span></Callout>
      <p class="small muted">Saving checks the files with <code>docker compose config</code> first and keeps the old version as a <code>.bak</code> next to each file. Containers only change when you apply.</p>
    </template>
    <template #footer="{ close }">
      <Btn @click="close">Cancel</Btn>
      <Btn :disabled="!dirty" @click="save(false)">Save</Btn>
      <Btn kind="primary" :disabled="!dirty" title="Save, then docker compose pull and up -d" @click="save(true)">Save and apply</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.dot { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); }
</style>
