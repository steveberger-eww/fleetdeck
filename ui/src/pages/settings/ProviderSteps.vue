<script setup>
// Steps 1-2, the same for every provider: create it, then enter its details. v-model is { issuer, client_id, client_secret }.
import { computed } from 'vue';
import { Btn, Copy, Field } from '@/components';
import Step from './Step.vue';

const props = defineProps({ d: Object, err: String, save: Function, configured: Boolean, off: Boolean });
const f = defineModel({ type: Object, required: true });
const insecure = computed(() => props.d.redirect_url.startsWith('http://') && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])[:/]/.test(props.d.redirect_url));
</script>
<template>
  <Step n="1" title="Create a provider in authentik" :done="configured" :off="off">
    <p class="small">In the <b>Admin interface</b>, open <b>Applications → Applications</b> and click <b>Create with provider</b>. Choose <b>OAuth2/OpenID Provider</b>.</p>
    <div class="field"><span>Redirect URI (paste into authentik, matching mode <b>Strict</b>)</span><Copy :text="d.redirect_url" /></div>
    <p v-if="insecure" class="err">This starts with http://. If Fleetdeck runs behind a reverse proxy with HTTPS, start it with <code>FLEETDECK_TRUST_PROXY=1</code> or set the public URL under Settings → Instance → General, and reload this page.</p>
    <ul class="small ink2 checklist">
      <li>Client type: <b>Confidential</b></li>
      <li>Signing Key: pick one, e.g. <b>authentik Self-signed Certificate</b>. Without it, sign-in fails.</li>
    </ul>
  </Step>
  <Step n="2" title="Enter the provider details" :done="configured" :off="off">
    <Field label="Issuer URL" hint="On the provider's page: “OpenID Configuration Issuer”, like https://auth.example.com/application/o/fleetdeck/">
      <input v-model="f.issuer" placeholder="https://" :disabled="off" autocapitalize="off" spellcheck="false" />
    </Field>
    <div class="cols">
      <Field label="Client ID" hint="On the provider's page."><input v-model="f.client_id" :disabled="off" autocapitalize="off" spellcheck="false" /></Field>
      <Field label="Client secret" :hint="d.has_secret ? 'Saved. Leave empty to keep it.' : 'Click Edit on the provider to see it.'">
        <input v-model="f.client_secret" type="password" autocomplete="off" :disabled="off" />
      </Field>
    </div>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <div><Btn :kind="configured ? '' : 'primary'" :disabled="off || !f.issuer || !f.client_id || (!d.has_secret && !f.client_secret)" @click="save">Save and test</Btn></div>
  </Step>
</template>
<style scoped>
.checklist { display: grid; gap: 4px; }
</style>
