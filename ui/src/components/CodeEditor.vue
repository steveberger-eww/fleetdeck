<script setup>
// Shell code field: CodeMirror (loaded on first use) with highlighting from our tokens.
// A plain textarea shows until the bundle loads, and stays if it fails. The value is read once on mount (use :key to reset).
//   <CodeEditor v-model="body" label="Script" tall />   lang="yaml" for Compose files (default: shell)   <CodeEditor :model-value="s.body" read-only label="Script" />
import { ref, onMounted, onBeforeUnmount } from 'vue';
const props = defineProps({ label: String, placeholder: String, autofocus: Boolean, readOnly: Boolean, tall: Boolean, lang: String });
const model = defineModel({ type: String, default: '' });
const el = ref();
const ready = ref(false);
let view;
onMounted(async () => {
  let C;
  try { C = await import('@/lib/codemirror.js'); } catch { return; }
  if (!el.value) return;
  view = new C.EditorView({ parent: el.value, state: C.EditorState.create({ doc: model.value || '', extensions: C.extensions(props, (v) => { model.value = v; }) }) });
  ready.value = true;
  if (props.autofocus) view.focus();
});
onBeforeUnmount(() => view?.destroy());
</script>
<template>
  <div ref="el" :class="['code', tall && 'tall', readOnly && 'ro']">
    <textarea v-if="!ready" v-model="model" spellcheck="false" :aria-label="label" :readonly="readOnly" :autofocus="autofocus" :placeholder="placeholder" />
  </div>
</template>
