<script setup>
// Tiny line of [t, v0..1] points for table cells.
import { computed } from 'vue';
const props = defineProps({ points: Array, idx: { type: Number, default: 1 } });
const d = computed(() => {
  const pts = (props.points || []).filter((p) => p[props.idx] != null);
  if (pts.length < 2) return '';
  const t0 = pts[0][0];
  const span = pts[pts.length - 1][0] - t0 || 1;
  return pts.map((p, i) => `${i ? 'L' : 'M'}${(((p[0] - t0) / span) * 100).toFixed(2)},${(19 - p[props.idx] * 18).toFixed(2)}`).join('');
});
</script>
<template>
  <svg class="spark" viewBox="0 0 100 20" preserveAspectRatio="none" aria-hidden="true"><template v-if="d"><path class="area" :d="`${d}L100,20L0,20Z`" /><path class="ln" :d="d" /></template></svg>
</template>
<style>
.spark { width: 100%; max-width: 120px; height: 22px; display: block; }
.spark .ln { fill: none; stroke: var(--s1); stroke-width: 1.5; vector-effect: non-scaling-stroke; stroke-linejoin: round; }
.spark .area { fill: var(--s1); opacity: .1; }
</style>
