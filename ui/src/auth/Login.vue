<script setup>
// ?org=<slug> (the organization's own sign-in link) adds its single sign-on button. `ssoHref` points the instance button elsewhere (invites).
import { reactive, ref } from 'vue';
import AuthFrame from './AuthFrame.vue';
import { Field, Icon } from '@/components';
import { api, passkeysSupported, getPasskey } from '@/lib/api.js';

const props = defineProps({ auth: Object, title: String, sub: String, ssoHref: { type: String, default: '/api/auth/oidc/start' } });
const emit = defineEmits(['done']);
const f = reactive({ email: '', password: '', code: '' });
const needCode = ref(false);
const err = ref('');
const busy = ref(false);
const org = props.auth.org;
async function submit() {
  if (!needCode.value && (!f.email || !f.password)) { err.value = 'Enter your email and password.'; return; }
  busy.value = true; err.value = '';
  try {
    const r = await api('/api/auth/login', { method: 'POST', body: { email: f.email, password: f.password, code: needCode.value ? f.code : undefined } });
    if (r.totp_required) needCode.value = true; else emit('done');
  } catch (e) { err.value = e.message; } finally { busy.value = false; }
}
async function passkey() {
  busy.value = true; err.value = '';
  try {
    const { token, options } = await api('/api/auth/passkey/begin', { method: 'POST' });
    await api('/api/auth/passkey/finish', { method: 'POST', body: { token, credential: await getPasskey(options) } });
    emit('done');
  } catch (e) { if (e.name !== 'NotAllowedError') err.value = e.message; } finally { busy.value = false; } // NotAllowedError: the prompt was closed
}
</script>
<template>
  <AuthFrame :title="title || (org ? `Sign in to ${org.name}` : 'Sign in')" :sub="needCode ? 'Enter the 6-digit code from your authenticator app.' : sub">
    <template v-if="!needCode && (org?.sso || auth.sso)">
      <a v-if="org?.sso" class="btn primary" :href="`/api/auth/oidc/start?org=${encodeURIComponent(org.slug)}`">Sign in with {{ org.name }}</a>
      <a v-if="auth.sso" :class="['btn', org?.sso ? '' : 'primary']" :href="ssoHref">Sign in with {{ auth.sso }}</a>
      <div class="or">or use your password</div>
    </template>
    <template v-if="!needCode">
      <Field label="Email"><input v-model="f.email" type="text" autocomplete="username" autofocus /></Field>
      <Field label="Password"><input v-model="f.password" type="password" autocomplete="current-password" @keydown.enter="submit" /></Field>
    </template>
    <Field v-else label="Authentication code"><input v-model="f.code" inputmode="numeric" autocomplete="one-time-code" autofocus maxlength="6" @keydown.enter="submit" /></Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <button :class="['btn', org?.sso || auth.sso ? '' : 'primary']" type="submit" :disabled="busy" @click="submit"><span v-if="busy" class="spin" />{{ busy ? 'Signing in…' : 'Sign in' }}</button>
    <button v-if="!needCode && passkeysSupported()" class="btn" type="button" :disabled="busy" @click="passkey"><Icon name="key" />Sign in with a passkey</button>
  </AuthFrame>
</template>
