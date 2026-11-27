<script setup>
// Servers to run on: by name, by tag, or all. v-model: { all, servers: [id], tags: [tag] }. Used by the Run dialog and schedules.
import { computed } from 'vue';
import { Lamp } from '@/components';
import { servers } from '@/lib/store.js';
import { resolveTargets } from '@/lib/actions.js';
import { plural } from '@/lib/format.js';

const model = defineModel({ type: Object, required: true });
const list = computed(() => servers.value || []);
const tags = computed(() => [...new Set(list.value.flatMap((s) => s.tags))].sort());
const t = computed(() => ({ all: false, servers: [], tags: [], ...model.value }));
const set = (patch) => { model.value = { ...t.value, ...patch }; };
const flip = (arr, x) => (arr.includes(x) ? arr.filter((y) => y !== x) : [...arr, x]);
const n = computed(() => resolveTargets(t.value, list.value).length);
const viaTag = (s) => s.tags.some((x) => t.value.tags.includes(x));
</script>

<template>
  <div class="field">
    <span>Run on <span class="muted">({{ plural(n, 'server') }})</span></span>
    <label class="check"><input type="checkbox" :checked="t.all" @change="set({ all: $event.target.checked })" />All servers, including ones added later</label>
    <template v-if="!t.all">
      <div v-if="tags.length" class="row" role="group" aria-label="Tags">
        <span class="small muted">Tags</span>
        <button v-for="x in tags" :key="x" type="button" class="pill" :aria-pressed="t.tags.includes(x)" @click="set({ tags: flip(t.tags, x) })">{{ x }}</button>
      </div>
      <div class="pick">
        <label v-for="s in list" :key="s.id" class="pick-row" :title="viaTag(s) ? 'Included by tag' : undefined">
          <input type="checkbox" :checked="t.servers.includes(s.id) || viaTag(s)" :disabled="viaTag(s)" @change="set({ servers: flip(t.servers, s.id) })" />
          <Lamp :status="s.status" label />
          <span class="trunc">{{ s.name }}</span>
          <span class="small muted trunc">{{ s.host }}</span>
        </label>
        <p v-if="!list.length" class="muted small pick-empty">No servers yet.</p>
      </div>
    </template>
  </div>
</template>

<style scoped>
.pick { max-height: 208px; overflow-y: auto; border: 1px solid var(--line-2); border-radius: var(--r); background: var(--panel); padding: 4px; }
.pick-row { display: flex; align-items: center; gap: 9px; padding: 6px 8px; border-radius: var(--r-sm); cursor: pointer; min-width: 0; }
.pick-row:hover { background: var(--hover); }
.pick-row:has(input:disabled) { cursor: default; }
.pick-empty { padding: 6px 8px; }
</style>
