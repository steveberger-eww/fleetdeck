<script setup>
import { reactive, ref, computed } from 'vue';
import AuthFrame from './AuthFrame.vue';
import NewPassword, { pwOk } from './NewPassword.vue';
import { Field } from '@/components';
import { api } from '@/lib/api.js';

const emit = defineEmits(['done']);
const f = reactive({ token: '', name: '', email: '', organization: '', password: '', confirm: '' });
const err = ref('');
const ok = computed(() => pwOk(f) && f.token && f.name && f.email && f.organization);
async function submit() {
  if (!ok.value) return;
  try { await api('/api/auth/setup', { method: 'POST', body: { token: f.token.trim(), name: f.name, email: f.email, organization: f.organization, password: f.password } }); emit('done'); } catch (e) { err.value = e.message; }
}
</script>
<template>
  <AuthFrame title="Set up Fleetdeck" sub="Create the first account. It can manage every organization on this Fleetdeck.">
    <Field label="Setup token" hint="Printed in the server console (or docker logs) when Fleetdeck starts."><input v-model="f.token" autofocus autocomplete="off" spellcheck="false" /></Field>
    <Field label="Your name"><input v-model="f.name" autocomplete="name" /></Field>
    <Field label="Email"><input v-model="f.email" type="email" autocomplete="username" /></Field>
    <Field label="Organization" hint="Your company or team. You can add more organizations later."><input v-model="f.organization" autocomplete="organization" /></Field>
    <NewPassword v-model="f" @enter="submit" />
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <button class="btn primary" type="submit" :disabled="!ok" @click="submit">Create account and sign in</button>
  </AuthFrame>
</template>
