<script setup>
defineProps({ size: { type: Number, default: 22 } });
</script>
<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <rect x="2" y="3" width="20" height="5" rx="1.5" fill="var(--ink)" /><rect x="2" y="9.5" width="20" height="5" rx="1.5" fill="var(--ink)" /><rect x="2" y="16" width="20" height="5" rx="1.5" fill="var(--ink)" />
    <circle cx="5.5" cy="5.5" r="1.2" fill="var(--ok)" /><circle cx="5.5" cy="12" r="1.2" fill="var(--ok)" /><circle cx="5.5" cy="18.5" r="1.2" fill="var(--warn)" />
  </svg>
</template>
