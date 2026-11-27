<script setup>
defineProps({ rows: { type: Number, default: 3 } });
const widths = ['60%', '100%', '80%', '90%', '70%'];
</script>
<template>
  <div class="stack sm" style="padding: 16px" aria-busy="true" aria-label="Loading"><div v-for="i in rows" :key="i" class="skel" :style="{ width: widths[(i - 1) % widths.length] }" /></div>
</template>
