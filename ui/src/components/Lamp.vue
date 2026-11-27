<script setup>
// Status dot. With `label` it is announced (lists without status text); without it the text next to it carries the status.
import { STATUS_TEXT } from '@/lib/status.js';
defineProps({ status: String, label: Boolean });
</script>
<template>
  <span v-if="label" :class="['lamp', status]" role="img" :aria-label="STATUS_TEXT[status] || status || 'Unknown'" />
  <span v-else :class="['lamp', status]" aria-hidden="true" />
</template>
