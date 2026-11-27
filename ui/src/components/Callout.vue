<script setup>
// <Callout kind="warn" title="Reboot needed">Text. <template #actions><Btn size="sm">…</Btn></template></Callout>
// kind: bad (default) | warn | info | ok | plain
import Icon from './Icon.vue';
const props = defineProps({ kind: { type: String, default: 'bad' }, title: String, icon: String });
const ICON = { bad: 'alert', warn: 'warn', info: 'info', ok: 'check', plain: 'info' };
</script>
<template>
  <div :class="['callout', kind]" :role="kind === 'bad' ? 'alert' : 'status'">
    <Icon :name="icon || ICON[kind]" />
    <div class="grow"><b v-if="title">{{ title }}</b><template v-if="title && $slots.default">{{ ' ' }}</template><slot /></div>
    <div v-if="$slots.actions" class="row nw"><slot name="actions" /></div>
  </div>
</template>
