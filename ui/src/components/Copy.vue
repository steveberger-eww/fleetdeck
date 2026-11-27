<script setup>
// A value with a copy button. `code` renders it monospace (commands to paste into a shell).
import Btn from './Btn.vue';
import { copyText } from '@/lib/actions.js';
const props = defineProps({ text: String, label: { type: String, default: 'Copy' }, code: Boolean });
</script>
<template>
  <div :class="['copy', code && 'code']"><span>{{ text }}</span><Btn size="sm" icon="copy" @click="copyText(text)">{{ label }}</Btn></div>
</template>
