<script setup>
// Auth gate: server unreachable → setup → invite → sign in → no organization → the app.
import { ref, onMounted, onBeforeUnmount, computed } from 'vue';
import { useRoute } from 'vue-router';
import { api } from '@/lib/api.js';
import { me, refreshMe } from '@/lib/store.js';
import ToastHost from '@/components/ToastHost.vue';
import ConfirmHost from '@/components/ConfirmHost.vue';
import AuthFrame from './auth/AuthFrame.vue';
import Login from './auth/Login.vue';
import Setup from './auth/Setup.vue';
import Invite from './auth/Invite.vue';
import NoOrg from './auth/NoOrg.vue';
import AppShell from './layout/AppShell.vue';

const route = useRoute();
const auth = ref(null);
const org = new URLSearchParams(location.search).get('org');
async function check() {
  try {
    const a = await api(`/api/auth/state${org ? `?org=${encodeURIComponent(org)}` : ''}`);
    if (a.authed) await refreshMe();
    auth.value = a;
  } catch { auth.value = { error: true }; }
}
const retry = () => { auth.value = null; check(); };
const signedOut = () => { if (auth.value) auth.value = { ...auth.value, authed: false }; };
onMounted(() => { check(); addEventListener('fd-signed-out', signedOut); });
onBeforeUnmount(() => removeEventListener('fd-signed-out', signedOut));
const invite = computed(() => (route.path.startsWith('/invite/') ? route.params.token : null));
</script>
<template>
  <template v-if="!auth" />
  <AuthFrame v-else-if="auth.error" title="Fleetdeck's server isn't reachable" sub="Check that it is running, then try again.">
    <button type="button" class="btn primary" @click="retry">Try again</button>
  </AuthFrame>
  <Setup v-else-if="auth.setup" @done="check" />
  <Invite v-else-if="invite" :key="invite" :token="invite" :auth="auth" />
  <Login v-else-if="!auth.authed" :auth="auth" @done="check" />
  <template v-else-if="!me" />
  <NoOrg v-else-if="!me.tenant" />
  <AppShell v-else />
  <ConfirmHost /><ToastHost />
</template>
