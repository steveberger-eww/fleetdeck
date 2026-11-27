<script setup>
import Lamp from './Lamp.vue';
import { STATUS_TEXT } from '@/lib/status.js';
defineProps({ status: String });
</script>
<template>
  <span :class="['chip', status === 'up' ? 'ok' : status === 'down' ? 'bad' : '']"><Lamp :status="status" />{{ STATUS_TEXT[status] || status }}</span>
</template>
