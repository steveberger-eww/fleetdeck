<script setup>
import { ICONS } from '@/lib/icons.js';
defineProps({ name: { type: String, required: true }, size: { type: [Number, String], default: 16 } });
</script>
<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="ICONS[name]" /></svg>
</template>
