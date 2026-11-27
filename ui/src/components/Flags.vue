<script setup>
// A server's attention chips, most urgent first; `max` folds the rest into "+N".
import { computed } from 'vue';
import { flagList } from '@/lib/status.js';
const props = defineProps({ s: Object, max: { type: Number, default: Infinity } });
const f = computed(() => flagList(props.s));
</script>
<template>
  <span v-for="[kind, text, title] in f.slice(0, max)" :key="text" :class="['chip', kind]" :title="title">{{ text }}</span>
  <span v-if="f.length > max" class="chip" :title="f.slice(max).map(([, t]) => t).join(', ')">+{{ f.length - max }}</span>
</template>
