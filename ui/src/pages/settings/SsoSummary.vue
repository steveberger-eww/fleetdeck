<script setup>
// Single sign-on on the security page: its state, and a way to /settings/sso.
// Hidden when there is nothing to connect to and it isn't yours to set up.
import { computed } from 'vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';

const sso = useLoad(() => api('/api/sso'));
const d = computed(() => sso.data);
</script>
<template>
  <section v-if="d && (d.configured || d.can_configure)" class="panel">
    <div class="panel-h"><h2>Single sign-on</h2><span :class="['chip', d.linked && 'ok']">{{ d.linked ? 'On' : 'Off' }}</span></div>
    <div class="panel-b form">
      <p v-if="d.linked" class="small ink2">Connected as <b>{{ d.account }}</b>. Password sign-in is {{ d.password_login ? 'still on as a fallback' : 'off' }}.</p>
      <p v-else class="small ink2">{{ d.configured ? 'Connect your account to sign in with single sign-on instead of a password.' : 'Let everyone on this Fleetdeck sign in with authentik, Keycloak or another OpenID Connect provider.' }}</p>
      <div><RouterLink :class="['btn', !d.linked && 'primary']" to="/settings/sso">{{ d.linked ? 'Manage' : d.configured ? 'Connect account' : 'Set up single sign-on' }}</RouterLink></div>
    </div>
  </section>
</template>
