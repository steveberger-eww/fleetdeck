<script setup>
// Panel footer: a note on the left, Save on the right (primary only once something changed).
import { Btn } from '@/components';
defineProps({ note: String, changed: Boolean, disabled: Boolean, save: Function, label: { type: String, default: 'Save' } });
</script>
<template>
  <div class="panel-f"><span class="small muted grow">{{ note }}</span><Btn :kind="changed ? 'primary' : ''" :disabled="!changed || disabled" @click="save">{{ label }}</Btn></div>
</template>
