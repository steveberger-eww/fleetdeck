<script setup>
// You, top right: the current organization (switch it), theme, profile, sign out.
import { ref, computed } from 'vue';
import { Icon, Menu } from '@/components';
import { me, switchOrg, signOut, roleName, toast } from '@/lib/store.js';
import { getTheme, setTheme } from '@/lib/theme.js';

const theme = ref(getTheme());
const THEMES = [['dark', 'moon', 'Dark'], ['light', 'sun', 'Light'], ['system', 'monitor', 'System']];
const pickTheme = (t) => { setTheme(t); theme.value = t; };
const pickOrg = (id) => switchOrg(id).catch((x) => toast(x.message, 'bad'));
const initial = computed(() => (me.value.user.name || me.value.user.email).slice(0, 1).toUpperCase());
const orgIcon = (t) => (t.personal ? 'user' : 'building');
</script>
<template>
  <Menu label="Account and organization" btn-class="btn acct-btn">
    <template #trigger><span class="avatar xs" aria-hidden="true">{{ initial }}</span><span class="trunc hide-sm">{{ me.tenant.name }}</span><Icon name="chev" :size="14" class="hide-sm" /></template>
    <div class="me-card">
      <div class="avatar" aria-hidden="true">{{ initial }}</div>
      <div class="grow"><div class="strong trunc">{{ me.user.name || me.user.email }}</div><div class="xs muted trunc">{{ me.user.email }}</div></div>
    </div>
    <div class="mh">Organization · {{ roleName(me.role) }}{{ me.user.super_admin ? ', super admin' : '' }}</div>
    <template v-if="!me.locked && me.tenants.length > 1">
      <button v-for="t in me.tenants" :key="t.id" type="button" class="mi" :aria-current="t.id === me.tenant.id || undefined" @click="t.id !== me.tenant.id && pickOrg(t.id)">
        <Icon :name="t.id === me.tenant.id ? 'check' : orgIcon(t)" :size="15" />{{ t.name }}</button>
    </template>
    <div v-else class="mi static"><Icon :name="orgIcon(me.tenant)" :size="15" />{{ me.tenant.name }}</div>
    <hr />
    <div class="mh">Theme</div>
    <button v-for="[t, icon, label] in THEMES" :key="t" type="button" class="mi" @click="pickTheme(t)"><Icon :name="theme === t ? 'check' : icon" :size="15" />{{ label }}</button>
    <hr />
    <RouterLink class="mi" to="/settings"><Icon name="user" :size="15" />Profile and security</RouterLink>
    <button type="button" class="mi" @click="signOut"><Icon name="out" :size="15" />Sign out</button>
  </Menu>
</template>
<style>
.acct-btn { max-width: 240px; padding: 0 8px 0 4px; }
.acct-btn svg { color: var(--muted); flex: none; }
.avatar { width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; flex: none; background: var(--accent-soft); color: var(--accent); font-weight: 600; font-size: var(--fs-sm); }
.avatar.xs { width: 22px; height: 22px; font-size: var(--fs-xs); }
.me-card { display: flex; align-items: center; gap: 10px; padding: 8px 10px 10px; min-width: 240px; }
.menu .mi.static { cursor: default; }
.menu .mi.static:hover { background: transparent; }
.menu .mi[aria-current="true"] { font-weight: 600; }
.menu .mi[aria-current="true"] svg { color: var(--accent); }
@media (max-width: 900px) { .acct-btn { padding: 0 3px; } }
</style>
