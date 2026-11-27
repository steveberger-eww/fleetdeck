<script setup>
// Value + small usage bar for table cells; value is 0..1. Color only at warn (75%) / bad (90%).
import { level, pct } from '@/lib/format.js';
defineProps({ value: Number, warn: { type: Number, default: 0.75 }, bad: { type: Number, default: 0.9 } });
</script>
<template>
  <span v-if="value == null" class="faint">–</span>
  <div v-else class="cellbar"><span :class="level(value, warn, bad)"><slot>{{ pct(value) }}</slot></span><div class="bar" aria-hidden="true"><i :class="level(value, warn, bad)" :style="{ width: `${Math.round(Math.min(1, value || 0) * 100)}%` }" /></div></div>
</template>
<style>
.cellbar { display: grid; gap: 4px; min-width: 72px; }
.cellbar > span { font-size: var(--fs-sm); font-weight: 500; }
.cellbar > span.warn { color: var(--warn); }
.cellbar > span.bad { color: var(--bad); }
</style>
