<script setup>
import { reactive, ref } from 'vue';
import { Btn, Field } from '@/components';
import { api } from '@/lib/api.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ has: Boolean });
const emit = defineEmits(['change']);
const v = reactive({ current: '', next: '', confirm: '' });
const err = ref('');
async function save() {
  err.value = '';
  if (v.next !== v.confirm) { err.value = 'New passwords do not match'; return; }
  try {
    await api('/api/account/password', { method: 'POST', body: { current: props.has ? v.current : undefined, next: v.next } });
    Object.assign(v, { current: '', next: '', confirm: '' });
    toast(props.has ? 'Password changed. Other sessions were signed out.' : 'Password set. Other sessions were signed out.', 'ok');
    emit('change');
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>Password</h2><span v-if="!has" class="chip">None yet</span></div>
    <div class="panel-b form">
      <Field v-if="has" label="Current password"><input v-model="v.current" type="password" autocomplete="current-password" /></Field>
      <p v-else class="small ink2">Your account was created through single sign-on. Add a password to also sign in with your email.</p>
      <div class="cols">
        <Field label="New password" hint="At least 10 characters."><input v-model="v.next" type="password" autocomplete="new-password" /></Field>
        <Field label="Repeat new password"><input v-model="v.confirm" type="password" autocomplete="new-password" /></Field>
      </div>
      <p v-if="err" class="err" role="alert">{{ err }}</p>
      <div><Btn kind="primary" :disabled="(has && !v.current) || v.next.length < 10" @click="save">{{ has ? 'Change password' : 'Set password' }}</Btn></div>
    </div>
  </section>
</template>
