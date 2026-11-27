<script setup>
// Which servers a rule or quiet window covers. v-model: '' = all servers, 'tag:<name>', or a server id.
import { computed } from 'vue';
import { servers } from '@/lib/store.js';
const model = defineModel({ type: String, default: '' });
const list = computed(() => servers.value || []);
const tags = computed(() => [...new Set(list.value.flatMap((s) => s.tags))].sort());
</script>
<template>
  <select v-model="model">
    <option value="">All servers</option>
    <optgroup v-if="tags.length" label="Servers tagged"><option v-for="t in tags" :key="t" :value="`tag:${t}`">{{ t }}</option></optgroup>
    <optgroup label="One server"><option v-for="s in list" :key="s.id" :value="s.id">{{ s.name }}</option></optgroup>
  </select>
</template>
