<script setup>
// The server list column: always there while you're in the fleet, problems first.
import { ref, computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Icon, Lamp, Btn } from '@/components';
import { servers, can, openRun } from '@/lib/store.js';
import { fleetFilter as f, filterServers } from '@/lib/fleet.js';
import { flagList, lampOf } from '@/lib/status.js';
import { pct, level, plural } from '@/lib/format.js';

const route = useRoute();
const router = useRouter();
const current = computed(() => (route.path.startsWith('/servers/') ? route.params.id : null));
const all = computed(() => servers.value || []);
const tags = computed(() => [...new Set(all.value.flatMap((s) => s.tags))].sort());
const list = computed(() => filterServers(all.value));
const filtered = computed(() => f.q || f.status || f.tag);
const clear = () => Object.assign(f, { q: '', status: '', tag: '' });
const SEG = [['', 'All'], ['problems', 'Critical'], ['warnings', 'Warnings'], ['updates', 'Updates']];

// The row's headline: the most urgent flag, or nothing when healthy.
const flag = (s) => { const [kind, text, title] = flagList(s)[0] || []; return kind === 'bad' || kind === 'warn' ? { kind, text, title } : null; };

const sel = ref(new Set());
const toggle = (id) => { const n = new Set(sel.value); n.has(id) ? n.delete(id) : n.add(id); sel.value = n; };
const allSel = computed(() => list.value.length > 0 && list.value.every((s) => sel.value.has(s.id)));
const toggleAll = () => { sel.value = allSel.value ? new Set() : new Set(list.value.map((s) => s.id)); };
</script>
<template>
  <aside class="fleet-list" aria-label="Servers">
    <div class="fl-h">
      <div class="row nw">
        <h2>Servers</h2><span class="muted small">{{ filtered ? `${list.length} of ${all.length}` : all.length }}</span>
        <Btn v-if="can('owner')" class="right" size="sm" kind="ghost" icon="plus" title="Add server" @click="router.push({ path: '/', query: { add: '1' } })" />
      </div>
      <label class="search"><Icon name="search" :size="14" /><input v-model="f.q" type="search" class="sm" placeholder="Filter by name, host, OS, tag" aria-label="Filter servers" /></label>
      <div class="row nw">
        <div class="seg fl-seg" role="group" aria-label="Show">
          <button v-for="[k, l] in SEG" :key="k" type="button" :aria-pressed="f.status === k" @click="f.status = k">{{ l }}</button>
        </div>
      </div>
      <div v-if="tags.length || filtered" class="row nw">
        <select v-if="tags.length" v-model="f.tag" class="sm" aria-label="Tag"><option value="">All tags</option><option v-for="t in tags" :key="t" :value="t">{{ t }}</option></select>
        <button v-if="filtered" type="button" class="btn ghost sm" @click="clear">Clear</button>
      </div>
    </div>

    <div class="fl-body">
      <div v-if="sel.size" class="fl-all"><label class="check xs muted"><input type="checkbox" :checked="allSel" @change="toggleAll" />Select all</label></div>
      <ul role="list">
        <li v-for="s in list" :key="s.id" :class="['fl-row', `st-${s.status}`, current === s.id && 'cur', sel.has(s.id) && 'sel']">
          <input v-if="can('operator')" type="checkbox" class="fl-check" :checked="sel.has(s.id)" :aria-label="`Select ${s.name}`" @change="toggle(s.id)" />
          <RouterLink :to="`/servers/${s.id}`" :aria-current="current === s.id ? 'page' : undefined">
            <span class="l1"><Lamp :status="lampOf(s)" label /><span class="nm trunc">{{ s.name }}</span>
              <span v-if="s.alerts" class="count soft" :aria-label="`${s.alerts} alerts`">{{ s.alerts }}</span></span>
            <span class="l2">
              <span v-if="flag(s)" :class="['fl-flag', flag(s).kind]" :title="flag(s).title">{{ flag(s).text }}</span>
              <span v-else-if="s.status === 'paused'" class="faint">Paused</span>
              <span v-else class="trunc">{{ s.host }}</span>
              <span v-if="s.snapshot && s.status === 'up'" class="fl-m"><span>CPU <b :class="level(s.snapshot.cpu)">{{ pct(s.snapshot.cpu) }}</b></span><span>Mem <b :class="level(s.snapshot.mem)">{{ pct(s.snapshot.mem) }}</b></span></span>
            </span>
          </RouterLink>
        </li>
      </ul>
      <div v-if="servers && !list.length" class="fl-empty">
        <template v-if="all.length">No servers match.<button type="button" class="btn sm" @click="clear">Clear filters</button></template>
        <template v-else>No servers yet.</template>
      </div>
    </div>

    <div v-if="sel.size" class="fl-bulk" role="region" aria-label="Selection">
      <div class="row nw"><b class="small">{{ plural(sel.size, 'server') }} selected</b><button type="button" class="btn ghost sm right" @click="sel = new Set()">Clear</button></div>
      <div class="row nw">
        <Btn size="sm" kind="primary" icon="play" class="grow" @click="openRun({ targets: { servers: [...sel] } })">Run script</Btn>
        <Btn size="sm" icon="refresh" class="grow" @click="openRun({ targets: { servers: [...sel] }, scriptId: 'update' })">Update</Btn>
      </div>
    </div>
  </aside>
</template>
<style>
.fleet-list { width: var(--list-w); background: var(--panel); border-right: 1px solid var(--line); display: flex; flex-direction: column; position: sticky; top: 0; height: 100dvh; min-width: 0; }
.fl-h { display: grid; gap: 8px; padding: 0 12px 10px; border-bottom: 1px solid var(--line); }
.fl-h > .row:first-child { height: var(--top-h); margin-bottom: -2px; }
.fl-h h2 { font-size: var(--fs-md); font-weight: 600; }
.fl-h select { flex: 1; }
.fl-seg { width: 100%; }
.fl-seg button { flex: 1; padding: 0 4px; font-size: var(--fs-xs); }
.fl-body { flex: 1; overflow-y: auto; scrollbar-width: thin; padding: 6px 6px 12px; }
.fl-body ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 1px; }
.fl-all { padding: 2px 8px 6px; }
.fl-row { position: relative; border-radius: var(--r); }
.fl-row > a { display: grid; gap: 2px; padding: 6px 8px 6px 10px; border-radius: var(--r); color: var(--ink-2); text-decoration: none; }
.fl-row > a:hover { background: var(--hover); color: var(--ink); text-decoration: none; }
.fl-row.cur > a { background: var(--accent-soft); color: var(--ink); }
.fl-row.cur > a::before { content: ""; position: absolute; left: 0; top: 8px; bottom: 8px; width: 2px; border-radius: 2px; background: var(--accent); }
.fl-row.sel > a { background: var(--hover); }
.fl-row .l1 { display: flex; align-items: center; gap: 8px; min-width: 0; }
.fl-row .nm { font-weight: 500; color: var(--ink); }
.fl-row .l1 .count { margin-left: auto; }
.fl-row .l2 { display: flex; align-items: center; gap: 8px; padding-left: 16px; font-size: var(--fs-xs); color: var(--muted); min-width: 0; }
.fl-flag { font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-flag.bad { color: var(--bad); }
.fl-flag.warn { color: var(--warn); }
.fl-m { margin-left: auto; display: flex; gap: 8px; flex: none; }
.fl-m { color: var(--faint); }
.fl-m b { display: inline-block; min-width: 26px; text-align: right; font-weight: 500; color: var(--ink-2); }
.fl-m b.warn { color: var(--warn); } .fl-m b.bad { color: var(--bad); }
.fl-row.st-paused .nm { color: var(--muted); }
/* the checkbox appears on hover, or for good once anything is selected */
.fl-check { position: absolute; left: -2px; top: 9px; z-index: 1; opacity: 0; width: 14px; height: 14px; }
.fl-row:hover .fl-check, .fl-check:focus-visible, .fl-check:checked, .fleet-list:has(.fl-check:checked) .fl-check { opacity: 1; }
.fleet-list:has(.fl-check:checked) .fl-row > a { padding-left: 20px; }
.fleet-list:has(.fl-check:checked) .fl-check { left: 4px; }
.fl-empty { display: grid; gap: 8px; justify-items: start; padding: 12px 10px; color: var(--muted); font-size: var(--fs-sm); }
.fl-bulk { display: grid; gap: 8px; padding: 10px 12px; border-top: 1px solid var(--line); background: var(--panel-2); }
</style>
