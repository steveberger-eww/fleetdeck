<script setup>
import { Logo } from '@/components';
defineProps({ title: String, sub: String });
</script>
<template>
  <main class="auth">
    <div class="auth-brand"><Logo :size="24" />Fleetdeck</div>
    <form class="auth-card" @submit.prevent>
      <div class="auth-h"><h1>{{ title }}</h1><p v-if="sub" class="muted">{{ sub }}</p></div>
      <slot />
    </form>
  </main>
</template>
<style>
.auth { min-height: 100dvh; display: grid; place-content: center; justify-items: center; gap: 20px; padding: 32px 16px; }
.auth-brand { display: flex; align-items: center; gap: 10px; font-size: var(--fs-xl); font-weight: 650; }
.auth-card { width: min(400px, calc(100vw - 32px)); display: grid; gap: 16px; padding: 28px; background: var(--panel); border: 1px solid var(--line); border-radius: 12px; box-shadow: var(--shadow-sm); }
.auth-h { display: grid; gap: 6px; margin-bottom: 4px; }
.auth-h h1 { font-size: var(--fs-xl); }
.auth-h p { font-size: var(--fs-md); }
.auth-card .btn { width: 100%; height: 38px; }
.auth .or { display: flex; align-items: center; gap: 10px; color: var(--muted); font-size: var(--fs-sm); }
.auth .or::before, .auth .or::after { content: ""; flex: 1; height: 1px; background: var(--line); }
</style>
