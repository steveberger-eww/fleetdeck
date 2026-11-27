<script setup>
import Icon from './Icon.vue';
import { toasts, dropToast } from '@/lib/store.js';
const ICON = { ok: 'check', bad: 'alert', warn: 'warn' };
</script>
<template>
  <div class="toasts">
    <TransitionGroup name="toast">
      <div v-for="t in toasts" :key="t.id" :class="['toast', t.kind]" :role="t.kind === 'bad' ? 'alert' : 'status'">
        <Icon :name="ICON[t.kind] || 'info'" class="ti" />
        <span class="grow">{{ t.msg }}</span>
        <RouterLink v-if="t.action" class="btn sm ghost" :to="t.action.to" @click="dropToast(t.id)">{{ t.action.label }}</RouterLink>
        <button type="button" class="btn ghost icon sm" aria-label="Dismiss" @click="dropToast(t.id)"><Icon name="x" :size="14" /></button>
      </div>
    </TransitionGroup>
  </div>
</template>
<style>
.toasts { position: fixed; right: 16px; bottom: 16px; z-index: 100; display: grid; gap: 8px; width: min(400px, calc(100vw - 32px)); }
.toast { display: flex; align-items: center; gap: 10px; padding: 8px 8px 8px 14px; border-radius: var(--r); background: var(--panel); border: 1px solid var(--line); box-shadow: var(--shadow-lg); font-size: var(--fs-md); }
.toast .ti { flex: none; color: var(--accent); }
.toast.ok .ti { color: var(--ok); }
.toast.bad { border-color: color-mix(in srgb, var(--bad) 40%, var(--line)); }
.toast.bad .ti { color: var(--bad); }
.toast.warn .ti { color: var(--warn); }
.toast-enter-active, .toast-leave-active { transition: opacity .18s, transform .18s; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(8px); }
</style>
