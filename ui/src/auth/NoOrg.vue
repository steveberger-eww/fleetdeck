<script setup>
// Signed in, but in no organization (yet).
import AuthFrame from './AuthFrame.vue';
import { Btn, Icon } from '@/components';
import { me, switchOrg, createPersonal, signOut, toast } from '@/lib/store.js';
</script>
<template>
  <AuthFrame title="You're not in an organization yet" :sub="`Signed in as ${me.user.email}.`">
    <template v-if="me.tenants.length"><p class="ink2">Open one of these:</p><Btn v-for="t in me.tenants" :key="t.id" @click="switchOrg(t.id)">{{ t.name }}</Btn></template>
    <template v-else><p class="ink2">Ask an admin of your organization for an invite link. Opening it adds this account.</p>
      <p v-if="!me.locked" class="ink2">Or manage your own servers in a personal organization that only you see.</p>
      <Btn v-if="!me.locked" @click="createPersonal().catch((x) => toast(x.message, 'bad'))">Create personal organization</Btn></template>
    <button type="button" class="btn ghost" @click="signOut"><Icon name="out" />Sign out</button>
  </AuthFrame>
</template>
