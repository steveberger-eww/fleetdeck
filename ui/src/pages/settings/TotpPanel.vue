<script setup>
import { ref, computed } from 'vue';
import { Btn, Field } from '@/components';
import { api } from '@/lib/api.js';
import { toast } from '@/lib/store.js';

defineProps({ enabled: Boolean });
const emit = defineEmits(['change']);
const setup = ref(null); // { qr, secret } while setting up
const code = ref('');
const pw = ref('');
const err = ref('');
const vFocus = { mounted: (el) => el.focus() };
const secret = computed(() => setup.value?.secret.match(/.{1,4}/g).join(' '));

async function begin() { setup.value = await api('/api/account/totp/begin', { method: 'POST' }); err.value = ''; }
async function enable() {
  try {
    await api('/api/account/totp/enable', { method: 'POST', body: { code: code.value } });
    setup.value = null; code.value = '';
    toast('Two-factor sign-in is on', 'ok');
    emit('change');
  } catch (e) { err.value = e.message; }
}
async function disable() {
  try {
    await api('/api/account/totp/disable', { method: 'POST', body: { password: pw.value, code: code.value } });
    pw.value = ''; code.value = '';
    toast('Two-factor sign-in is off');
    emit('change');
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>Two-factor sign-in</h2><span :class="['chip', enabled ? 'ok' : 'warn']">{{ enabled ? 'On' : 'Off' }}</span></div>
    <div class="panel-b form">
      <template v-if="enabled">
        <p class="small ink2">Turning it off needs your password and a current code.</p>
        <div class="cols">
          <Field label="Password"><input v-model="pw" type="password" autocomplete="current-password" /></Field>
          <Field label="Code"><input v-model="code" inputmode="numeric" maxlength="6" autocomplete="one-time-code" @keydown.enter="disable" /></Field>
        </div>
        <div><Btn @click="disable">Turn off two-factor sign-in</Btn></div>
      </template>
      <template v-else-if="!setup">
        <p class="small ink2">Ask for a code from an authenticator app (1Password, Aegis, Google Authenticator…) at every sign-in.</p>
        <div><Btn kind="primary" @click="begin">Set up two-factor sign-in</Btn></div>
      </template>
      <div v-else class="row top lg">
        <img class="qr" :src="setup.qr" width="168" height="168" alt="QR code for your authenticator app" />
        <div class="stack sm grow">
          <p class="small">Scan the code, or enter this secret manually:</p>
          <span class="secret wrap-any">{{ secret }}</span>
          <Field label="Code from the app"><input v-model="code" v-focus inputmode="numeric" maxlength="6" autocomplete="one-time-code" @keydown.enter="enable" /></Field>
          <div class="row"><Btn kind="primary" @click="enable">Turn on</Btn><button type="button" class="btn ghost" @click="setup = null">Cancel</button></div>
        </div>
      </div>
      <p v-if="err" class="err" role="alert">{{ err }}</p>
    </div>
  </section>
</template>
<style scoped>
.qr { flex: none; border-radius: var(--r-sm); border: 1px solid var(--line); background: #fff; }
.secret { font-weight: 600; letter-spacing: .04em; }
</style>
