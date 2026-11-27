<script setup>
// Icon rail: every page with a short label, badges for what needs you, and you (organization, theme, sign out).
import { ref, computed } from 'vue';
import { useRoute } from 'vue-router';
import { Icon, Logo, Menu } from '@/components';
import { me, servers, running, switchOrg, signOut, roleName, toast } from '@/lib/store.js';
import { navFor } from '@/lib/nav.js';
import { containerProblem } from '@/lib/status.js';
import { getTheme, setTheme } from '@/lib/theme.js';

const route = useRoute();
const section = computed(() => route.path.split('/')[1] || '');
// Overview is the fleet: it stays lit on a server's page.
const isActive = (p) => (p === '/' ? !section.value || section.value === 'servers' : `/${section.value}` === p);
const SHORT = { Overview: 'Fleet', Repositories: 'Repos', 'SSH keys': 'Keys', 'Audit log': 'Audit' };

const firing = computed(() => (servers.value || []).reduce((n, s) => n + s.alerts, 0));
const broken = computed(() => (servers.value || []).reduce((n, s) => n + (s.snapshot?.containers || []).filter(containerProblem).length, 0));
const badge = (label) => {
  if (label === 'Alerts' && firing.value) return { n: firing.value, cls: '', aria: `${firing.value} firing` };
  if (label === 'Docker' && broken.value) return { n: broken.value, cls: 'warn', aria: `${broken.value} with problems` };
  if (label === 'Jobs' && running.value) return { n: running.value, cls: 'info', aria: `${running.value} running` };
  return null;
};

const theme = ref(getTheme());
const THEMES = [['dark', 'moon', 'Dark'], ['light', 'sun', 'Light'], ['system', 'monitor', 'System']];
const pickTheme = (t) => { setTheme(t); theme.value = t; };
const pickOrg = (id) => switchOrg(id).catch((x) => toast(x.message, 'bad'));
const initial = computed(() => (me.value.user.name || me.value.user.email).slice(0, 1).toUpperCase());
</script>
<template>
  <nav class="rail" aria-label="Main">
    <RouterLink class="rail-logo" to="/" title="Fleetdeck" aria-label="Fleetdeck"><span class="logo-tile"><Logo :size="20" /></span></RouterLink>
    <div class="rail-nav">
      <template v-for="([group, items], gi) in navFor()" :key="group || 'main'">
        <hr v-if="gi" />
        <RouterLink v-for="[label, p, icon] in items" :key="p" :to="p" :aria-current="isActive(p) ? 'page' : undefined" :title="label">
          <span class="ic"><Icon :name="icon" :size="18" /><span v-if="badge(label)" :class="['count', badge(label).cls]" :aria-label="badge(label).aria">{{ badge(label).n }}</span></span>
          <span class="lb">{{ SHORT[label] || label }}</span>
        </RouterLink>
      </template>
    </div>
    <div class="rail-foot">
      <Menu label="Account and organization" btn-class="rail-me">
        <template #trigger><span class="avatar sm">{{ initial }}</span></template>
        <div class="me-card">
          <div class="avatar" aria-hidden="true">{{ initial }}</div>
          <div class="grow"><div class="strong trunc">{{ me.user.name || me.user.email }}</div><div class="xs muted trunc">{{ me.user.email }}</div></div>
        </div>
        <div class="mh">Organization · {{ roleName(me.role) }}{{ me.user.super_admin ? ', super admin' : '' }}</div>
        <template v-if="!me.locked && me.tenants.length > 1">
          <button v-for="t in me.tenants" :key="t.id" type="button" class="mi" :aria-current="t.id === me.tenant.id || undefined" @click="t.id !== me.tenant.id && pickOrg(t.id)">
            <Icon :name="t.id === me.tenant.id ? 'check' : t.personal ? 'user' : 'building'" :size="15" />{{ t.name }}</button>
        </template>
        <div v-else class="mi static"><Icon name="building" :size="15" />{{ me.tenant.name }}</div>
        <hr />
        <div class="mh">Theme</div>
        <button v-for="[t, icon, label] in THEMES" :key="t" type="button" class="mi" @click="pickTheme(t)"><Icon :name="theme === t ? 'check' : icon" :size="15" />{{ label }}</button>
        <hr />
        <RouterLink class="mi" to="/settings"><Icon name="user" :size="15" />Profile and security</RouterLink>
        <button type="button" class="mi" @click="signOut"><Icon name="out" :size="15" />Sign out</button>
      </Menu>
    </div>
  </nav>
</template>
<style>
.rail { width: var(--rail-w); background: var(--rail); border-right: 1px solid var(--line); display: flex; flex-direction: column; align-items: stretch; position: sticky; top: 0; height: 100dvh; z-index: 6; }
.logo-tile { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 8px; background: var(--panel-2); border: 1px solid var(--line-2); }
.rail-logo { display: grid; place-items: center; height: var(--top-h); flex: none; border-bottom: 1px solid var(--line); }
.rail-nav { flex: 1; overflow-y: auto; scrollbar-width: none; padding: 8px 6px; display: grid; gap: 2px; align-content: start; }
.rail-nav hr { margin: 6px 10px; border-color: var(--line); }
.rail-nav a { display: grid; justify-items: center; gap: 3px; padding: 7px 2px 6px; border-radius: var(--r); color: var(--muted); text-decoration: none; }
.rail-nav a .ic { position: relative; display: grid; place-items: center; width: 34px; height: 26px; border-radius: 7px; transition: background .1s, color .1s; }
.rail-nav a .lb { font-size: 10.5px; font-weight: 500; line-height: 1.1; text-align: center; letter-spacing: .01em; }
.rail-nav a:hover { color: var(--ink); text-decoration: none; }
.rail-nav a:hover .ic { background: var(--hover); }
.rail-nav a[aria-current="page"] { color: var(--ink); }
.rail-nav a[aria-current="page"] .ic { background: var(--accent-soft); color: var(--accent); }
.rail-nav .count { position: absolute; top: -5px; right: -7px; min-width: 16px; line-height: 15px; padding: 0 4px; font-size: 10px; border: 2px solid var(--rail); box-sizing: content-box; }
.rail-foot { position: relative; display: grid; place-items: center; padding: 10px 0 12px; border-top: 1px solid var(--line); }
.rail-me { all: unset; cursor: pointer; border-radius: 50%; }
.rail-me:hover .avatar { box-shadow: 0 0 0 2px var(--line-2); }
.rail-me:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.avatar { width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; flex: none; background: var(--accent-soft); color: var(--accent); font-weight: 600; font-size: var(--fs-sm); }
.avatar.sm { width: 32px; height: 32px; }
.me-card { display: flex; align-items: center; gap: 10px; padding: 8px 10px 10px; min-width: 240px; }
.menu .mi.static { cursor: default; }
.menu .mi.static:hover { background: transparent; }
.menu .mi[aria-current="true"] { font-weight: 600; }
.menu .mi[aria-current="true"] svg { color: var(--accent); }
</style>
