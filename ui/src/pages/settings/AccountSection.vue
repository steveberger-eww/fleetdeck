<script setup>
// "Profile" and "Sign-in & security": both read /api/account.
import { reactive, computed, watch } from 'vue';
import { Btn, Callout, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { me, refreshMe, switchOrg, toast } from '@/lib/store.js';
import SettingRow from './SettingRow.vue';
import SaveBar from './SaveBar.vue';
import PasskeysPanel from './PasskeysPanel.vue';
import TotpPanel from './TotpPanel.vue';
import PasswordPanel from './PasswordPanel.vue';
import SsoSummary from './SsoSummary.vue';
import SessionsPanel from './SessionsPanel.vue';

const props = defineProps({ part: { type: String, default: 'profile' } });
const HEAD = {
  profile: ['Profile', 'How you appear to others and the email you sign in with.'],
  security: ['Sign-in & security', "How you prove it's you, and where you're signed in right now."],
};
const acct = useLoad(() => api('/api/account'));
const a = computed(() => acct.data);
const reload = () => acct.reload(true);

const v = reactive({ name: '', email: '' });
watch(a, (x, old) => { if (x && !old) Object.assign(v, { name: x.name || '', email: x.email || '' }); }, { immediate: true });
const changed = computed(() => !!a.value && (v.name !== (a.value.name || '') || v.email !== a.value.email));
async function save() {
  await api('/api/account', { method: 'PUT', body: v });
  toast('Profile saved', 'ok');
  reload();
  refreshMe();
}
const weak = computed(() => a.value && !a.value.locked && !a.value.totp && !a.value.passkeys.length);
</script>
<template>
  <div class="page-h"><div><h1>{{ HEAD[props.part][0] }}</h1><p class="sub">{{ HEAD[props.part][1] }}</p></div></div>
  <ErrorNote v-if="acct.error" :error="acct.error" :retry="acct.reload" />
  <Loading v-else-if="!a" />
  <template v-else>
    <Callout v-if="a.locked" kind="info">You signed in through the single sign-on of {{ me.tenant.name }}, so this session can't change your profile, password or two-factor sign-in, or open other organizations. Sign in with your password to do that.</Callout>

    <template v-if="part === 'profile'">
      <section class="panel">
        <template v-if="!a.locked">
          <SettingRow label="Name" desc="Shown in the audit log and the people list."><input v-model="v.name" autocomplete="name" aria-label="Name" /></SettingRow>
          <SettingRow label="Email" desc="You sign in with it, and invites are matched to it."><input v-model="v.email" type="email" autocomplete="email" aria-label="Email" /></SettingRow>
          <SaveBar :changed="changed" :disabled="!v.name.trim() || !v.email.trim()" :save="save" />
        </template>
        <template v-else>
          <SettingRow label="Name">{{ a.name || '–' }}</SettingRow>
          <SettingRow label="Email">{{ a.email }}</SettingRow>
        </template>
      </section>

      <section v-if="me.tenants.length > 1" class="panel">
        <div class="panel-h"><h2>Organizations</h2><span class="sub">You're a member of {{ me.tenants.length }}.</span></div>
        <div class="table-wrap"><table class="t"><tbody>
          <tr v-for="t in me.tenants" :key="t.id">
            <td><span class="strong">{{ t.name }}</span> <span v-if="t.id === me.tenant.id" class="chip">Current</span></td>
            <td class="actions"><Btn v-if="t.id !== me.tenant.id && !me.locked" size="sm" @click="switchOrg(t.id)">Switch</Btn></td>
          </tr>
        </tbody></table></div>
      </section>
    </template>

    <template v-else>
      <Callout v-if="weak" kind="warn">Only a password protects your account. Add a passkey or turn on two-factor sign-in.</Callout>
      <template v-if="!a.locked">
        <PasskeysPanel :list="a.passkeys" @change="reload" />
        <TotpPanel :enabled="a.totp" @change="reload" />
        <PasswordPanel :has="a.has_password" @change="reload" />
        <SsoSummary />
      </template>
      <SessionsPanel :sessions="a.sessions" @change="reload" />
    </template>
  </template>
</template>
