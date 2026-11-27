<script setup>
// Command palette (⌘K / Ctrl K): jump to a server, page or action.
import { ref, computed, onMounted, watch, nextTick } from 'vue';
import { useRouter } from 'vue-router';
import { Icon, Lamp } from '@/components';
import { servers, can, openRun } from '@/lib/store.js';
import { byAttention } from '@/lib/status.js';
import { navFor, settingsPages } from '@/lib/nav.js';

const emit = defineEmits(['close']);
const router = useRouter();
const dlg = ref();
const q = ref('');
const i = ref(0);
onMounted(() => dlg.value.showModal());
const go = (p) => router.push(p);
const items = computed(() => {
  const n = q.value.toLowerCase().trim();
  const sorted = (servers.value || []).slice().sort(byAttention);
  const all = [
    ...sorted.map((s) => ({ group: 'Servers', label: s.name, hint: s.host, status: s.status, run: () => go(`/servers/${s.id}`) })),
    can('operator') && { group: 'Actions', label: 'Run a script…', icon: 'play', run: () => openRun() },
    can('owner') && { group: 'Actions', label: 'Add server…', icon: 'plus', run: () => go('/?add=1') },
    // per-server actions only once you type, so the empty list stays short
    ...(n ? sorted.flatMap((s) => [
      can('operator') && { group: 'Actions', label: `Terminal: ${s.name}`, hint: s.host, icon: 'term', run: () => go(`/servers/${s.id}/terminal`) },
      { group: 'Actions', label: `Logs: ${s.name}`, hint: s.host, icon: 'audit', run: () => go(`/servers/${s.id}/logs`) },
      can('operator') && { group: 'Actions', label: `Update: ${s.name}`, hint: s.host, icon: 'refresh', run: () => openRun({ targets: { servers: [s.id] }, scriptId: 'update' }) },
    ]) : []),
    ...navFor().flatMap(([, list]) => list).map(([label, path, icon]) => ({ group: 'Pages', label, icon, run: () => go(path) })),
    ...(n ? settingsPages().map(([label, path, group]) => ({ group: 'Settings', label: `${group} › ${label}`, icon: 'gear', run: () => go(path) })) : []),
  ].filter(Boolean);
  return (n ? all.filter((x) => `${x.label} ${x.hint || ''}`.toLowerCase().includes(n)) : all).slice(0, 40);
});
watch(q, () => { i.value = 0; });
watch(i, async (v) => { await nextTick(); document.getElementById(`pal-${v}`)?.scrollIntoView({ block: 'nearest' }); });
const pick = (x) => { emit('close'); x?.run(); };
function key(e) {
  if (e.key === 'ArrowDown') { e.preventDefault(); i.value = Math.min(i.value + 1, items.value.length - 1); }
  if (e.key === 'ArrowUp') { e.preventDefault(); i.value = Math.max(i.value - 1, 0); }
  if (e.key === 'Enter') pick(items.value[i.value]);
}
</script>
<template>
  <dialog ref="dlg" class="palette" aria-label="Search" @cancel.prevent="emit('close')" @mousedown="$event.target === dlg && emit('close')">
    <div class="pal-in"><Icon name="search" /><input v-model="q" autofocus placeholder="Jump to a server, page or action…" role="combobox" aria-expanded="true" aria-controls="pal-list"
      :aria-activedescendant="items.length ? `pal-${i}` : undefined" aria-label="Search" @keydown="key" /><span class="kbd">Esc</span></div>
    <ul id="pal-list" role="listbox">
      <template v-for="(x, n) in items" :key="`${x.group}-${x.label}`">
        <li v-if="n === 0 || items[n - 1].group !== x.group" class="grp" role="presentation">{{ x.group }}</li>
        <li :id="`pal-${n}`" role="option" :aria-selected="n === i" @mouseenter="i = n" @click="pick(x)">
          <Lamp v-if="x.status" :status="x.status" label /><Icon v-else :name="x.icon" />
          <span class="grow trunc">{{ x.label }}</span><span v-if="x.hint" class="muted small">{{ x.hint }}</span>
        </li>
      </template>
      <li v-if="!items.length" class="none">Nothing matches “{{ q }}”</li>
    </ul>
  </dialog>
</template>
<style>
.palette { width: min(620px, calc(100vw - 24px)); margin: 12vh auto auto; padding: 0; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); color: var(--ink); box-shadow: var(--shadow-lg); overflow: hidden; }
.palette::backdrop { background: var(--scrim); }
.pal-in { display: flex; align-items: center; gap: 10px; padding: 0 14px; border-bottom: 1px solid var(--line); color: var(--muted); }
.pal-in input { border: 0 !important; box-shadow: none !important; height: 52px; padding: 0; font-size: var(--fs-lg); background: transparent; }
.palette ul { list-style: none; margin: 0; padding: 6px; max-height: min(420px, 60vh); overflow-y: auto; }
.palette li[role="option"] { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 6px; cursor: pointer; }
.palette li[role="option"] svg { color: var(--muted); flex: none; }
.palette li[role="option"] .lamp { margin: 0 4px; }
.palette li[aria-selected="true"] { background: var(--accent-soft); }
.palette li[aria-selected="true"] svg { color: var(--accent); }
.palette .grp { padding: 10px 10px 4px; font-size: var(--fs-xs); font-weight: 600; color: var(--muted); }
.palette .none { padding: 18px 10px; color: var(--muted); text-align: center; }
</style>
