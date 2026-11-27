<script setup>
import Lamp from './Lamp.vue';
import Icon from './Icon.vue';
import { JOB_STATUS } from '@/lib/status.js';
defineProps({ status: String });
</script>
<template>
  <span :class="['chip', JOB_STATUS[status]?.[0] || '']"><Lamp v-if="status === 'running'" status="running" /><Icon v-else-if="status === 'success'" name="check" :size="13" class="ok-ink" />{{ JOB_STATUS[status]?.[1] || status }}</span>
</template>
