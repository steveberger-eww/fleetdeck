<script setup>
// journald entries of this server, or merged across servers. Every filter lives in the URL.
import { ref, computed, watch, onBeforeUnmount } from 'vue';
import { Btn, Icon, Empty, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useQuery } from '@/lib/use.js';
import { servers } from '@/lib/store.js';
import { logTime } from '@/lib/format.js';

const props = defineProps({ s: Object });
const PRIOS = [['', 'All levels'], ['3', 'Errors'], ['4', 'Warnings and worse'], ['6', 'Info and worse']];
const PRIO_NAME = ['emerg', 'alert', 'crit', 'error', 'warn', 'notice', 'info', 'debug'];
const SINCE = [['15m', 'Last 15 min'], ['1h', 'Last hour'], ['6h', 'Last 6 hours'], ['24h', 'Last day'], ['7d', 'Last week']];

const unit = useQuery('unit');
const priority = useQuery('level');
const since = useQuery('since', '1h');
const lines = useQuery('lines', '500');
const q = useQuery('q');
const follow = useQuery('follow');
const scope = useQuery('scope'); // '' = this server, 'all' or 'tag:<name>'
const tags = computed(() => [...new Set((servers.value || []).flatMap((x) => x.tags))].sort());

// The unit field writes to the URL once typing pauses.
const draft = ref(unit.value);
watch(unit, (u) => { draft.value = u; });
let timer;
watch(draft, (d) => { clearTimeout(timer); if (d !== unit.value) timer = setTimeout(() => { unit.value = d; }, 400); });
onBeforeUnmount(() => clearTimeout(timer));

const scrolled = ref(false);
const qs = computed(() => new URLSearchParams(Object.entries({ unit: unit.value, priority: priority.value, since: since.value, lines: lines.value }).filter(([, v]) => v)).toString());
// Following pauses while you scroll back through older entries.
// Across servers: each server's newest entries (at most 500), merged by time; a server that fails is one error row.
const logs = useLoad(() => (scope.value
  ? api(`/api/logs?scope=${encodeURIComponent(scope.value)}&${qs.value}`)
  : api(`/api/servers/${props.s.id}/logs?${qs.value}`).then((entries) => ({ entries, errors: [] }))),
{ key: () => `${qs.value}|${scope.value}`, poll: computed(() => (follow.value && !scope.value ? 5000 : 0)), paused: scrolled });
const list = computed(() => {
  const n = q.value.toLowerCase();
  return (logs.data?.entries || []).filter((e) => !n || `${e.server || ''} ${e.id} ${e.msg}`.toLowerCase().includes(n)).reverse();
});
const failed = computed(() => logs.data?.errors || []);
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <select v-model="scope" class="auto" aria-label="Servers">
        <option value="">This server</option><option value="all">All servers</option>
        <optgroup v-if="tags.length" label="Servers tagged"><option v-for="t in tags" :key="t" :value="`tag:${t}`">{{ t }}</option></optgroup>
      </select>
      <input v-model="draft" class="unit" placeholder="Unit, e.g. nginx" aria-label="Unit" autocapitalize="off" spellcheck="false" />
      <select v-model="priority" class="auto" aria-label="Level"><option v-for="[v, l] in PRIOS" :key="v" :value="v">{{ l }}</option></select>
      <select v-model="since" class="auto" aria-label="Time range"><option v-for="[v, l] in SINCE" :key="v" :value="v">{{ l }}</option></select>
      <select v-model="lines" class="auto" aria-label="Lines"><option v-for="v in ['200', '500', '2000', '5000']" :key="v" :value="v">{{ v }} lines</option></select>
      <label class="search w-sm"><Icon name="search" /><input v-model="q" type="search" placeholder="Search" aria-label="Search messages" /></label>
      <div class="row nw right">
        <button v-if="!scope" type="button" class="btn sm" :aria-pressed="!!follow" title="Reload every 5 seconds" @click="follow = follow ? '' : '1'">
          <span v-if="follow" :class="['lamp', scrolled ? 'paused' : 'running']" aria-hidden="true" />{{ follow && scrolled ? 'Paused' : 'Follow' }}
        </button>
        <Btn size="sm" kind="ghost" icon="refresh" @click="logs.reload()">Refresh</Btn>
      </div>
    </div>
    <Loading v-if="logs.loading && !logs.data" :rows="6" />
    <div v-else-if="logs.error" class="panel-b"><ErrorNote :error="logs.error" :retry="logs.reload" /></div>
    <Empty v-else-if="!list.length && !failed.length" title="No log entries" icon="audit">Nothing matches these filters. Widen the time range or clear the search.</Empty>
    <div v-else class="logs" tabindex="0" aria-label="Log entries" @scroll="scrolled = $event.target.scrollTop > 40">
      <div v-for="f in failed" :key="`err-${f.server}`" class="e p3"><span class="muted">–</span><span class="lv">error</span><span class="trunc ink2">{{ f.server }}</span><span class="m">Couldn't read logs from {{ f.server }}: {{ f.error }}</span></div>
      <div v-for="(e, i) in list" :key="i" :class="['e', `p${e.p}`]">
        <span class="muted">{{ logTime(e.t) }}</span><span class="lv">{{ PRIO_NAME[e.p] }}</span>
        <span class="trunc ink2" :title="e.server ? `${e.server} · ${e.unit}` : e.unit"><b v-if="e.server">{{ e.server }} </b>{{ e.id }}</span>
        <span class="m">{{ e.msg }}</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.unit { width: 180px; }
.logs { font-size: var(--fs-sm); max-height: 65vh; overflow-y: auto; border-radius: 0 0 var(--r-lg) var(--r-lg); }
.logs:focus-visible { outline-offset: -2px; }
.e { display: grid; grid-template-columns: 130px 52px 160px minmax(0, 1fr); gap: 12px; padding: 4px 16px; border-bottom: 1px solid var(--line); align-items: baseline; }
.e:last-child { border-bottom: 0; }
.e:hover { background: var(--panel-2); }
.e .lv { color: var(--muted); }
.e .m { white-space: pre-wrap; overflow-wrap: anywhere; }
.e.p0 .lv, .e.p1 .lv, .e.p2 .lv, .e.p3 .lv, .e.p0 .m, .e.p1 .m, .e.p2 .m, .e.p3 .m { color: var(--bad); }
.e.p4 .lv { color: var(--warn); }
@media (max-width: 700px) { .e { grid-template-columns: 1fr; gap: 0; } .unit { width: 100%; } }
</style>
