<script setup>
// Icon rail: every page with a short label and badges for what needs you.
import { computed } from 'vue';
import { useRoute } from 'vue-router';
import { Icon, Logo } from '@/components';
import { servers, running } from '@/lib/store.js';
import { navFor } from '@/lib/nav.js';
import { containerProblem } from '@/lib/status.js';

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
</style>
