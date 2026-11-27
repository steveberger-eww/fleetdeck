<script setup>
// <Empty title="No jobs yet" icon="jobs">Explanation.<template #action><Btn>…</Btn></template></Empty>
import Icon from './Icon.vue';
defineProps({ title: String, icon: String });
</script>
<template>
  <div class="empty"><div v-if="icon" class="ic"><Icon :name="icon" :size="20" /></div><h3>{{ title }}</h3><p v-if="$slots.default"><slot /></p><slot name="action" /></div>
</template>
