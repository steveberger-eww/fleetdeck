<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount, defineAsyncComponent } from 'vue';
import { useRoute } from 'vue-router';
import { Icon, Empty } from '@/components';
import Rail from './Rail.vue';
import FleetList from './FleetList.vue';
import Palette from './Palette.vue';
import { me, servers, can, openRun, closeRun, runRequest, dismissConfirm, refreshServers, refreshRunning } from '@/lib/store.js';
import { SERVER_TAB_LABEL, settingsPages } from '@/lib/nav.js';
import { isMac } from '@/lib/format.js';

const RunDialog = defineAsyncComponent(() => import('@/pages/jobs/RunDialog.vue'));
const route = useRoute();
const navOpen = ref(false);
const palette = ref(false);

const refresh = () => { refreshServers(); refreshRunning(); };
let timer;
const onKey = (e) => { if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); palette.value = !palette.value; } };
onMounted(() => {
  refresh();
  timer = setInterval(() => document.visibilityState === 'visible' && refresh(), 15_000);
  addEventListener('keydown', onKey);
});
onBeforeUnmount(() => { clearInterval(timer); removeEventListener('keydown', onKey); });
// A new page closes the drawer, the Run dialog and any open confirm (path only: typing a filter must not).
watch(() => route.path, () => { navOpen.value = false; closeRun(); dismissConfirm(); });

const parts = computed(() => route.path.split('/').filter(Boolean));
// The fleet (home and every server page) keeps the server list column open.
const inFleet = computed(() => !parts.value.length || parts.value[0] === 'servers');
const allowed = computed(() => !route.meta.role || can(route.meta.role));
// Breadcrumbs: [label, to?] — the last one is the current page.
const crumbs = computed(() => {
  const p = parts.value;
  if (p[0] === 'servers' && p[1]) {
    const s = servers.value?.find((x) => x.id === p[1]);
    const name = s?.name || 'Server';
    return [['Fleet', '/'], ...(p[2] ? [[name, `/servers/${p[1]}`], [SERVER_TAB_LABEL[p[2]] || p[2]]] : [[name]])];
  }
  if (p[0] === 'settings') {
    const [label, , group] = settingsPages().find(([, path]) => path === `/${p.join('/')}`) || ['Profile', '', 'Your account'];
    return [['Settings', '/settings'], [group], [label]];
  }
  if (p[0] === 'jobs' && p[1]) return [['Jobs', '/jobs'], [`#${p[1]}`]];
  return [];
});
const runHere = () => openRun(parts.value[0] === 'servers' && parts.value[1] ? { targets: { servers: [parts.value[1]] } } : undefined);
</script>
<template>
  <div :class="['app', navOpen && 'open', inFleet && 'fleet']">
    <div class="scrim" @click="navOpen = false" />
    <Rail />
    <FleetList v-if="inFleet" class="fl-side" />
    <div class="main">
      <header class="topbar">
        <button type="button" class="btn ghost icon menu-btn" aria-label="Open menu" @click="navOpen = true"><Icon name="menu" /></button>
        <nav v-if="crumbs.length" class="crumbs" aria-label="Breadcrumb">
          <template v-for="([label, to], i) in crumbs" :key="i">
            <Icon v-if="i" name="chevr" :size="14" class="sep" />
            <RouterLink v-if="to && i < crumbs.length - 1" :to="to">{{ label }}</RouterLink>
            <span v-else :class="i === crumbs.length - 1 && 'cur'" :aria-current="i === crumbs.length - 1 ? 'page' : undefined">{{ label }}</span>
          </template>
        </nav>
        <div class="top-r">
          <button type="button" class="search-btn" @click="palette = true"><Icon name="search" /><span class="trunc">Search servers, pages, actions</span><span class="kbd">{{ isMac ? '⌘K' : 'Ctrl K' }}</span></button>
          <button v-if="can('operator')" type="button" class="btn primary" aria-label="Run script" @click="runHere"><Icon name="play" /><span class="hide-sm">Run script</span></button>
        </div>
      </header>
      <main>
        <RouterView v-if="allowed" v-slot="{ Component }"><component :is="Component" :key="route.path.startsWith('/servers/') ? route.params.id : route.path.startsWith('/settings') ? 'settings' : route.path" /></RouterView>
        <div v-else class="page"><div class="panel"><Empty title="Not available with your role" icon="lock">An admin of {{ me.tenant.name }} can change your role.<template #action><RouterLink class="btn" to="/">Back to the fleet</RouterLink></template></Empty></div></div>
      </main>
    </div>
    <Palette v-if="palette" @close="palette = false" />
    <RunDialog v-if="runRequest" v-bind="runRequest" @close="closeRun" />
  </div>
</template>
<style>
.app { display: grid; grid-template-columns: var(--rail-w) minmax(0, 1fr); min-height: 100dvh; }
.app.fleet { grid-template-columns: var(--rail-w) var(--list-w) minmax(0, 1fr); }
.main { min-width: 0; display: flex; flex-direction: column; }
.topbar { display: flex; align-items: center; gap: 12px; height: var(--top-h); padding: 0 24px; border-bottom: 1px solid var(--line); background: color-mix(in srgb, var(--bg) 88%, transparent); backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px); position: sticky; top: 0; z-index: 5; }
.topbar .menu-btn { display: none; margin-left: -8px; }
.crumbs { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: var(--fs-md); color: var(--muted); }
.crumbs > * { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.crumbs a { color: var(--muted); }
.crumbs a:hover { color: var(--ink); }
.crumbs .sep { flex: none; color: var(--faint); }
.crumbs .cur { color: var(--ink); font-weight: 500; }
.top-r { margin-left: auto; display: flex; align-items: center; gap: 8px; min-width: 0; }
.search-btn { display: flex; align-items: center; gap: 8px; width: 280px; height: var(--ctl); padding: 0 6px 0 9px; border: 1px solid var(--line-2); border-radius: var(--r); background: var(--panel); color: var(--faint); font: inherit; font-size: var(--fs-md); cursor: pointer; min-width: 0; }
.search-btn > span.trunc { flex: 1; text-align: left; color: var(--muted); }
.search-btn:hover { border-color: var(--faint); }
.scrim { display: none; }
@media (max-width: 1280px) { .search-btn { width: 200px; } }
@media (max-width: 900px) {
  .app, .app.fleet { grid-template-columns: minmax(0, 1fr); }
  .app .fl-side { display: none; }
  .rail { position: fixed; inset: 0 auto 0 0; z-index: 30; transform: translateX(-100%); transition: transform .2s ease; box-shadow: var(--shadow-lg); }
  .app.open .rail { transform: none; }
  .app.open .scrim { display: block; position: fixed; inset: 0; background: var(--scrim); z-index: 25; }
  .topbar { padding: 0 12px; }
  .topbar .menu-btn { display: inline-flex; }
  .crumbs { display: none; }
  .top-r { flex: 1; }
  .search-btn { flex: 1; width: auto; }
  .search-btn .kbd { display: none; }
}
</style>
