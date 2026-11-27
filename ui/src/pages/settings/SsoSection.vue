<script setup>
// /settings/sso: super admins set up the instance provider; everyone connects their own account here.
import { computed } from 'vue';
import { Btn, Empty, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { toast } from '@/lib/store.js';
import { useProvider } from './sso.js';
import ProviderSteps from './ProviderSteps.vue';
import Step from './Step.vue';
import SettingRow from './SettingRow.vue';

const { sso, f, err, save, remove } = useProvider('/api/sso');
const d = computed(() => sso.data);
const admin = computed(() => !!d.value?.can_configure);
const n = computed(() => (admin.value ? 2 : 0)); // people who can't configure it only see the steps for their own account
const REMOVE = 'Everyone signs in with their password again. Connected accounts are forgotten.';
async function setPw(enabled) {
  try { await api('/api/sso/password-login', { method: 'PUT', body: { enabled } }); sso.reload(true); } catch (e) { toast(e.message, 'bad'); }
}
</script>
<template>
  <div class="page-h"><div>
    <h1>Single sign-on</h1>
    <p v-if="d" class="sub">{{ admin
      ? 'One provider for every organization on this Fleetdeck: authentik or any OpenID Connect provider (Keycloak, Authelia, Zitadel…). Accounts are matched by email; new people still need an invite. The steps use authentik\'s names.'
      : 'Sign in with your company account instead of a password.' }}</p>
  </div></div>
  <ErrorNote v-if="sso.error" :error="sso.error" :retry="sso.reload" />
  <Loading v-else-if="!d || !f" />
  <section v-else-if="!admin && !d.configured" class="panel">
    <Empty title="Single sign-on isn't set up" icon="link">A super admin can set it up for this Fleetdeck.
      <template #action><RouterLink class="btn" to="/settings/security">Back to sign-in & security</RouterLink></template></Empty>
  </section>
  <template v-else>
    <ProviderSteps v-if="admin" v-model="f" :d="d" :err="err" :save="save" :configured="d.configured" />

    <Step :n="n + 1" title="Connect your account" :done="d.linked" :off="!d.configured">
      <p v-if="d.linked" class="small">Connected as <b>{{ d.account }}</b>.</p>
      <p v-else class="small">Sign in at your provider once to connect it to your Fleetdeck account. Accounts with the same email address connect by themselves on their first sign-in.</p>
      <p class="small ink2">Sign-ins this way skip Fleetdeck's own 2FA, so turn on MFA in your provider.</p>
      <div>
        <a v-if="d.configured" :class="['btn', !d.linked && 'primary']" href="/api/auth/oidc/start?link=1">{{ d.linked ? 'Connect a different account' : 'Connect account' }}</a>
        <button v-else type="button" class="btn" disabled>Connect account</button>
      </div>
    </Step>

    <Step :n="n + 2" title="Password sign-in (optional)" :done="d.linked && !d.password_login" :off="!d.linked">
      <p class="small">Keep your password as a fallback, or turn it off so only single sign-on works for your account.</p>
      <p v-if="d.password_forced" class="small ink2"><code>FLEETDECK_PASSWORD_LOGIN=1</code> is set, so passwords always work right now.</p>
      <p v-else class="small ink2">Locked out? Start Fleetdeck with <code>FLEETDECK_PASSWORD_LOGIN=1</code> to use passwords again.</p>
      <div><div class="seg" role="group" aria-label="Password sign-in">
        <button type="button" :aria-pressed="!!d.password_login" :disabled="!d.linked" @click="setPw(true)">On</button>
        <button type="button" :aria-pressed="!d.password_login" :disabled="!d.linked || d.password_forced" @click="setPw(false)">Off</button>
      </div></div>
    </Step>

    <section v-if="admin && (d.issuer || d.client_id)" class="panel">
      <SettingRow label="Remove single sign-on" :desc="REMOVE"><Btn @click="remove(REMOVE)">Remove single sign-on</Btn></SettingRow>
    </section>
  </template>
</template>
