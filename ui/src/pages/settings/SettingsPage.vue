<script setup>
// Settings: a sub-navigation grouped by who a setting affects, one section per URL (/settings/<sub>).
import { computed } from 'vue';
import { useRouter } from 'vue-router';
import { SETTINGS_SECTIONS, SETTINGS_GROUPS, settingsVisible } from '@/lib/nav.js';
import AccountSection from './AccountSection.vue';
import Preferences from './Preferences.vue';
import OrgGeneral from './OrgGeneral.vue';
import PeopleSection from './PeopleSection.vue';
import OrgSsoSection from './OrgSsoSection.vue';
import InstanceSection from './InstanceSection.vue';
import SsoSection from './SsoSection.vue';

const props = defineProps({ sub: { type: String, default: '' } });
const router = useRouter();
const PAGES = {
  '': [AccountSection, { part: 'profile' }], security: [AccountSection, { part: 'security' }], preferences: [Preferences],
  org: [OrgGeneral], 'org/people': [PeopleSection], 'org/sso': [OrgSsoSection],
  instance: [InstanceSection, { part: 'general' }], 'instance/orgs': [InstanceSection, { part: 'orgs' }], 'instance/accounts': [InstanceSection, { part: 'accounts' }],
  sso: [SsoSection],
};
const mine = computed(() => SETTINGS_SECTIONS.filter(([, , g]) => settingsVisible(g)));
// /settings/sso is also where anyone connects their own account; without instance rights it belongs to "Sign-in & security".
const cur = computed(() => (mine.value.some(([p]) => p === props.sub) ? props.sub : props.sub === 'sso' ? 'security' : ''));
const page = computed(() => (props.sub === 'sso' ? 'sso' : cur.value));
const groups = computed(() => SETTINGS_GROUPS.map(([g, l]) => [l, mine.value.filter(([, , x]) => x === g)]).filter(([, items]) => items.length));
const to = (p) => `/settings${p ? `/${p}` : ''}`;
</script>
<template>
  <div class="page settings">
    <nav class="subnav hide-sm" aria-label="Settings">
      <template v-for="[label, items] in groups" :key="label">
        <div class="sn-h">{{ label }}</div>
        <RouterLink v-for="[p, l] in items" :key="p" :to="to(p)" :aria-current="p === cur ? 'page' : undefined">{{ l }}</RouterLink>
      </template>
    </nav>
    <select class="show-sm" :value="cur" aria-label="Settings section" @change="router.push(to($event.target.value))">
      <optgroup v-for="[label, items] in groups" :key="label" :label="label">
        <option v-for="[p, l] in items" :key="p" :value="p">{{ l }}</option>
      </optgroup>
    </select>
    <div :key="page" class="stack"><component :is="PAGES[page][0]" v-bind="PAGES[page][1]" /></div>
  </div>
</template>
<style scoped>
.page.settings { grid-template-columns: 200px minmax(0, 760px); justify-content: center; column-gap: 40px; align-items: start; }
.subnav { position: sticky; top: 84px; display: grid; gap: 1px; align-content: start; }
.sn-h { padding: 16px 10px 6px; font-size: var(--fs-xs); font-weight: 600; color: var(--muted); }
.sn-h:first-child { padding-top: 4px; }
.subnav a { display: flex; align-items: center; min-height: 32px; padding: 4px 10px; border-radius: 6px; color: var(--ink-2); font-weight: 500; }
.subnav a:hover { background: var(--hover); color: var(--ink); text-decoration: none; }
.subnav a[aria-current="page"] { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
@media (max-width: 700px) { .page.settings { grid-template-columns: minmax(0, 1fr); } }
</style>
