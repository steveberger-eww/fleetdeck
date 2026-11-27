<script setup>
// #/invite/<token>: works signed in (join with this account) and signed out (create an account, or sign in first).
import { reactive, ref, computed } from 'vue';
import AuthFrame from './AuthFrame.vue';
import Login from './Login.vue';
import NewPassword, { pwOk } from './NewPassword.vue';
import { Field, Btn } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';

const props = defineProps({ token: String, auth: Object });
const inv = useLoad(() => api(`/api/invites/${encodeURIComponent(props.token)}`));
const f = reactive({ name: '', password: '', confirm: '' });
const err = ref('');
const ROLE_TEXT = { admin: 'an admin', owner: 'an owner', operator: 'an operator', viewer: 'a viewer' };
const intoApp = () => { location.hash = '#/'; location.reload(); };
async function accept(body) {
  err.value = '';
  try { await api(`/api/invites/${encodeURIComponent(props.token)}/accept`, { method: 'POST', body }); intoApp(); } catch (e) { err.value = e.message; }
}
const d = computed(() => inv.data);
const sub = computed(() => d.value && `You're invited to ${d.value.org} as ${ROLE_TEXT[d.value.role] || d.value.role}.`);
const ssoHref = computed(() => `/api/auth/oidc/start?invite=${encodeURIComponent(props.token)}`);
const reload = () => location.reload();
const submit = () => pwOk(f) && f.name && accept({ name: f.name, password: f.password });
</script>
<template>
  <template v-if="inv.loading" />
  <AuthFrame v-else-if="inv.error" title="Invite"><p class="err" role="alert">{{ inv.error }}</p><a class="btn" href="/">Go to Fleetdeck</a></AuthFrame>
  <AuthFrame v-else-if="d.user" :title="`Join ${d.org}`" :sub="sub">
    <p class="ink2">You're signed in as <b>{{ d.user }}</b><template v-if="d.user !== d.email">. The invite was sent to <b>{{ d.email }}</b></template>.</p>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <Btn kind="primary" type="submit" @click="accept({})">Join {{ d.org }}</Btn>
    <a class="btn ghost" href="#/">Not now</a>
  </AuthFrame>
  <Login v-else-if="d.exists" :auth="auth" :title="`Sign in to join ${d.org}`" :sub="`${sub} There's already an account for ${d.email}.`" :sso-href="ssoHref" @done="reload" />
  <AuthFrame v-else :title="`Join ${d.org}`" :sub="sub">
    <template v-if="d.sso"><a class="btn primary" :href="ssoHref">Accept with {{ d.sso }}</a><div class="or">or create a password</div></template>
    <Field label="Email" hint="The invite is for this address."><input :value="d.email" disabled /></Field>
    <Field label="Your name"><input v-model="f.name" autocomplete="name" autofocus /></Field>
    <NewPassword v-model="f" @enter="submit" />
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <button :class="['btn', d.sso ? '' : 'primary']" type="submit" :disabled="!pwOk(f) || !f.name" @click="submit">Create account and join</button>
  </AuthFrame>
</template>
