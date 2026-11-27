<script setup>
// /settings/org/sso: an organization's own provider (business license).
import { computed } from 'vue';
import { Btn, Copy, Field, Callout, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { me, toast } from '@/lib/store.js';
import { useProvider } from './sso.js';
import ProviderSteps from './ProviderSteps.vue';
import Step from './Step.vue';
import SettingRow from './SettingRow.vue';

const JOIN_ROLES = [['', 'Only people who are already members'], ['viewer', 'Anyone from the provider, as viewer'], ['operator', 'Anyone from the provider, as operator'], ['owner', 'Anyone from the provider, as owner']];
const { sso, f, err, save, remove } = useProvider('/api/tenant/sso');
const d = computed(() => sso.data);
const configured = computed(() => !!(d.value?.issuer && d.value?.client_id && d.value?.has_secret));
const off = computed(() => !d.value?.licensed);
const org = computed(() => me.value.tenant.name);
const removeText = computed(() => `People of ${org.value} sign in with their password or invite again.`);
async function setRole(e) {
  try { await api('/api/tenant/sso', { method: 'PUT', body: { role: e.target.value } }); sso.reload(true); toast('Saved', 'ok'); } catch (x) { e.target.value = d.value.role; toast(x.message, 'bad'); }
}
</script>
<template>
  <div class="page-h"><div><h1>Single sign-on</h1>
    <p class="sub">People of {{ org }} sign in with your organization's own authentik or other OpenID Connect provider (Keycloak, Authelia, Zitadel…). The steps use authentik's names.</p></div></div>
  <ErrorNote v-if="sso.error" :error="sso.error" :retry="sso.reload" />
  <Loading v-else-if="!d || !f" />
  <template v-else>
    <Callout v-if="off" kind="info">An organization's own single sign-on needs a <b>business license</b>.</Callout>
    <ProviderSteps v-model="f" :d="d" :err="err" :save="save" :configured="configured" :off="off" />

    <Step n="3" title="Who can sign in" :done="configured" :off="off || !configured">
      <Field label="People without a Fleetdeck account" hint="Accounts are matched by email. Super admins can't sign in this way.">
        <select :value="d.role" :disabled="off || !configured" @change="setRole"><option v-for="[k, l] in JOIN_ROLES" :key="k" :value="k">{{ l }}</option></select>
      </Field>
      <p class="small ink2">Sign-ins this way skip Fleetdeck's own 2FA, so turn on MFA in your provider.</p>
    </Step>

    <Step n="4" title="Share the sign-in link" :done="configured" :off="off || !configured">
      <Copy :text="d.login_url" />
      <p class="small ink2">Sign-ins through this link open {{ org }} only. Switching organizations or changing a password or 2FA needs a normal sign-in.</p>
    </Step>

    <section v-if="d.issuer || d.client_id" class="panel">
      <SettingRow label="Remove single sign-on" :desc="removeText"><Btn @click="remove(removeText)">Remove single sign-on</Btn></SettingRow>
    </section>
  </template>
</template>
