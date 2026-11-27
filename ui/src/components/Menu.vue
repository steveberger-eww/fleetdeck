<script setup>
// Overflow menu on the native popover API.
//   <Menu label="More actions"><button class="mi" @click="x">Restart</button><hr><button class="mi danger">Remove</button></Menu>
// Items are .mi buttons/links; clicking one closes the menu.
import { ref } from 'vue';
import Icon from './Icon.vue';
// A #trigger slot replaces the button's content (btn-class then styles the button).
defineProps({ label: { type: String, default: 'More actions' }, size: { type: String, default: '' }, icon: { type: String, default: 'more' }, text: String, btnClass: String });
let n = 0;
const id = `fd-menu-${Math.random().toString(36).slice(2)}${++n}`;
const btn = ref();
function place(e) {
  if (e.newState !== 'open') return;
  const m = e.currentTarget;
  const b = btn.value.getBoundingClientRect();
  m.style.top = `${Math.max(8, Math.min(b.bottom + 4, innerHeight - m.offsetHeight - 8))}px`;
  m.style.left = `${Math.max(8, Math.min(b.right - m.offsetWidth, innerWidth - m.offsetWidth - 8))}px`;
}
const pick = (e) => { if (e.target.closest('.mi')) e.currentTarget.hidePopover(); };
</script>
<template>
  <button ref="btn" type="button" :class="btnClass || ['btn', text ? '' : 'icon', size]" :popovertarget="id" :aria-label="label" :title="text ? undefined : label"><slot name="trigger"><Icon :name="icon" :size="size === 'sm' ? 14 : 16" />{{ text }}</slot></button>
  <div :id="id" class="menu" popover="auto" role="menu" @toggle="place" @click="pick"><slot /></div>
</template>
<style>
.menu { margin: 0; position: fixed; inset: auto; min-width: 190px; padding: 4px; border: 1px solid var(--line); border-radius: var(--r); background: var(--panel); color: var(--ink); box-shadow: var(--shadow-lg); }
.menu .mi { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 9px; width: 100%; padding: 7px 10px; border-radius: 6px; font-size: var(--fs-md); color: var(--ink); cursor: pointer; white-space: nowrap; }
.menu .mi svg { color: var(--muted); }
.menu .mi:hover, .menu .mi:focus-visible { background: var(--hover); text-decoration: none; }
.menu .mi.danger, .menu .mi.danger svg { color: var(--bad); }
.menu .mi:disabled, .menu .mi[aria-disabled="true"] { opacity: .5; cursor: not-allowed; }
.menu hr { margin: 4px 0; }
.menu .mh { padding: 6px 10px 2px; font-size: var(--fs-xs); color: var(--muted); }
</style>
