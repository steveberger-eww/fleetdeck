<script setup>
// Signed in, but in no organization (yet).
import AuthFrame from './AuthFrame.vue';
import { Btn, Icon } from '@/components';
import { me, switchOrg, signOut } from '@/lib/store.js';
</script>
<template>
  <AuthFrame title="You're not in an organization yet" :sub="`Signed in as ${me.user.email}.`">
    <template v-if="me.tenants.length"><p class="ink2">Open one of these:</p><Btn v-for="t in me.tenants" :key="t.id" @click="switchOrg(t.id)">{{ t.name }}</Btn></template>
    <p v-else class="ink2">Ask an admin of your organization for an invite link. Opening it adds this account.</p>
    <button type="button" class="btn ghost" @click="signOut"><Icon name="out" />Sign out</button>
  </AuthFrame>
</template>
