<script setup>
// A button that shows a spinner while its async @click runs and toasts the error if it fails.
//   <Btn kind="primary" icon="play" @click="run">Run</Btn>     kind: primary | ghost | danger | danger solid
//   <Btn icon="trash" title="Delete" size="sm" @click="del" />  (icon-only: title becomes the aria-label)
//   <Btn :to="`/jobs/${id}`">Open</Btn>                         (renders a router link)
import { ref, useSlots, computed } from 'vue';
import Icon from './Icon.vue';
import { toast } from '@/lib/store.js';

const props = defineProps({
  kind: { type: String, default: '' }, size: { type: String, default: '' }, icon: String, title: String,
  disabled: Boolean, type: { type: String, default: 'button' }, to: [String, Object], href: String,
  onClick: Function, // declared so we can await it
});
const slots = useSlots();
const busy = ref(false);
const iconOnly = computed(() => props.icon && !slots.default);
const cls = computed(() => ['btn', props.kind, props.size, iconOnly.value ? 'icon' : ''].filter(Boolean).join(' '));
async function click(e) {
  if (!props.onClick) return;
  const r = props.onClick(e);
  if (!r?.then) return;
  busy.value = true;
  try { await r; } catch (err) { toast(err.message, 'bad'); } finally { busy.value = false; }
}
</script>
<template>
  <RouterLink v-if="to" :to="to" :class="cls" :title="title" :aria-label="iconOnly ? title : undefined">
    <Icon v-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" /><slot />
  </RouterLink>
  <a v-else-if="href" :href="href" :class="cls" :title="title" :aria-label="iconOnly ? title : undefined">
    <Icon v-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" /><slot />
  </a>
  <button v-else :type="type" :class="cls" :disabled="disabled || busy" :title="title" :aria-label="iconOnly ? title : undefined" :aria-busy="busy || undefined" @click="click">
    <span v-if="busy" class="spin" /><Icon v-else-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" /><slot />
  </button>
</template>
