<script setup>
// Metric charts with a shared crosshair, then filesystems, system facts and disk health.
import { ref, computed } from 'vue';
import { Btn, LineChart, ErrorNote, CellBar } from '@/components';
import CustomMetricDialog from './CustomMetricDialog.vue';
import DiskHealth from './DiskHealth.vue';
import { api } from '@/lib/api.js';
import { useLoad, useQuery } from '@/lib/use.js';
import { can } from '@/lib/store.js';
import { bytes, rate, pct, when } from '@/lib/format.js';
import { RANGES } from './util.js';

const props = defineProps({ s: Object });
const range = useQuery('range', '1h');
const id = props.s.id;
const m = useLoad(() => api(`/api/servers/${id}/metrics?range=${range.value}`), { key: range, poll: 60_000 });
const custom = useLoad(() => api(`/api/servers/${id}/custom?range=${range.value}`), { key: range, poll: 60_000 });
const sync = ref(null);
const edit = ref(null); // custom metric being added ({}) or edited
const n = computed(() => props.s.snapshot);
const hasTemp = computed(() => m.data?.points.some((p) => p.temp != null));
const hasGPU = computed(() => n.value?.gpus?.length > 0 || m.data?.points.some((p) => p.gpu != null));

const P = (v) => `${Math.round(v * 100)}%`;
const R = (v) => rate(v);
const customNum = (v) => (Math.abs(v) >= 100 ? Math.round(v) : +v.toFixed(2)).toLocaleString();
const unitOf = (u) => (!u ? '' : /^[%°]/.test(u) ? u : ` ${u}`);
// Latest reading from the snapshot: the value, or why there is none (missing until the next probe after adding it).
const customNow = (x, unit) => (!x ? 'Waiting for the next check' : x.v != null ? `${customNum(x.v)}${unitOf(unit)}` : `⚠ ${x.err}`);
const saved = () => { edit.value = null; custom.reload(true); };
</script>

<template>
  <div class="stack">
    <div class="row">
      <h2>Metrics</h2>
      <div class="row right">
        <Btn v-if="can('admin')" size="sm" icon="plus" @click="edit = {}">Add custom metric</Btn>
        <a class="btn sm" :href="`/api/servers/${s.id}/metrics.csv?range=${range}`" download :title="`Every reading of the last ${range} as CSV`">CSV</a>
        <div class="seg" role="group" aria-label="Time range"><button v-for="r in RANGES" :key="r" type="button" :aria-pressed="r === range" @click="range = r">{{ r }}</button></div>
      </div>
    </div>
    <ErrorNote v-if="m.error" :error="m.error" :retry="m.reload" />
    <ErrorNote v-if="custom.error" :error="custom.error" :retry="custom.reload" />
    <div class="charts">
      <LineChart :sync="sync" title="CPU" :data="m.data" :y-max="1" :fmt="P" :series="[{ key: 'cpu', label: 'CPU', color: 'var(--s1)' }]" :current="n && `${pct(n.cpu)} of ${n.cores} cores`" />
      <LineChart :sync="sync" title="Memory" :data="m.data" :y-max="1" :fmt="P" :series="[{ key: 'mem', label: 'Memory', color: 'var(--s1)' }, { key: 'swap', label: 'Swap', color: 'var(--s2)' }]" :current="n && `${bytes(n.memUsed)} of ${bytes(n.memTotal)}`" />
      <LineChart :sync="sync" title="Network" :data="m.data" :fmt="R" :series="[{ key: 'rx', label: 'In', color: 'var(--s1)' }, { key: 'tx', label: 'Out', color: 'var(--s2)' }]" :current="n && `↓ ${rate(n.rx)} ↑ ${rate(n.tx)}`" />
      <LineChart :sync="sync" title="Disk I/O" :data="m.data" :fmt="R" :series="[{ key: 'dr', label: 'Read', color: 'var(--s1)' }, { key: 'dw', label: 'Write', color: 'var(--s2)' }]" />
      <LineChart :sync="sync" title="Load (1 min)" :data="m.data" :fmt="(v) => v.toFixed(v < 10 ? 1 : 0)" :series="[{ key: 'load1', label: 'Load', color: 'var(--s1)' }]" :current="n && n.load.map((l) => l.toFixed(2)).join(', ')" />
      <LineChart v-if="hasTemp" :sync="sync" title="Temperature" :data="m.data" :fmt="(v) => `${Math.round(v)}°`" :series="[{ key: 'temp', label: 'Temperature', color: 'var(--s1)' }]" :current="n?.temp != null ? `${Math.round(n.temp)} °C` : undefined" />
      <LineChart v-if="hasGPU" :sync="sync" title="GPU" :data="m.data" :y-max="1" :fmt="P" :series="[{ key: 'gpu', label: 'Usage', color: 'var(--s1)' }, { key: 'gpu_mem', label: 'Memory', color: 'var(--s2)' }]"
        :current="n?.gpu != null ? `${pct(n.gpu)}${n.gpus.length > 1 ? ` (busiest of ${n.gpus.length})` : ''}` : undefined" />
      <LineChart v-for="c in custom.data || []" :key="c.id" :sync="sync" :title="c.unit ? `${c.name} (${c.unit})` : c.name" :data="c.series" :fmt="customNum"
        :series="[{ key: 'v', label: c.name, color: 'var(--s1)' }]" :current="customNow(n?.custom?.[c.id], c.unit)">
        <template v-if="can('admin')" #actions><Btn size="sm" kind="ghost" icon="edit" :title="`Edit ${c.name}`" @click="edit = c" /></template>
      </LineChart>
    </div>
    <CustomMetricDialog v-if="edit" :s="s" :metric="edit" @close="edit = null" @saved="saved" />

    <div v-if="n" class="grid-2">
      <section class="panel">
        <div class="panel-h"><h2>Filesystems</h2></div>
        <div class="table-wrap">
          <table class="t">
            <thead><tr><th>Mount</th><th class="hide-sm">Type</th><th>Used</th><th class="num hide-sm">Inodes</th><th class="num">Size</th></tr></thead>
            <tbody>
              <tr v-for="d in n.disks" :key="d.mount">
                <td>{{ d.mount }}</td><td class="ink2 hide-sm">{{ d.type }}</td>
                <td><CellBar :value="d.pct" :warn="0.8" /></td>
                <td :class="['num small nowrap hide-sm', d.inodePct >= 0.9 ? 'bad-ink' : 'ink2']">{{ d.inodePct != null ? pct(d.inodePct) : '–' }}</td>
                <td class="num ink2 nowrap">{{ bytes(d.used) }} / {{ bytes(d.size) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel">
        <div class="panel-h"><h2>System</h2></div>
        <div class="panel-b">
          <dl class="kv">
            <dt class="sub">Hardware</dt>
            <dt>CPU</dt><dd>{{ n.cpuModel || '–' }}, {{ n.cores }} cores</dd>
            <dt>Memory</dt><dd>{{ bytes(n.memTotal) }}{{ n.swapTotal ? `, ${bytes(n.swapTotal)} swap` : ', no swap' }}</dd>
            <template v-if="n.gpus?.length"><dt>GPU</dt><dd><div v-for="(g, i) in n.gpus" :key="i">{{ g.name }}{{ g.memTotal ? `, ${bytes(g.memTotal)}` : '' }}{{ g.temp != null ? `, ${Math.round(g.temp)} °C` : '' }}{{ g.power != null ? `, ${Math.round(g.power)} W` : '' }}</div></dd></template>
            <template v-if="n.virt"><dt>Virtualization</dt><dd>{{ n.virt }}</dd></template>
            <dt class="sub">Software</dt>
            <dt>OS</dt><dd>{{ n.os }}</dd>
            <dt>Kernel</dt><dd>{{ n.kernel }} ({{ n.arch }})</dd>
            <dt>Packages</dt><dd>{{ n.pkg || 'unknown' }}{{ n.updates != null ? `, ${n.updates} pending` : '' }}{{ n.autoUpdates != null ? `, automatic updates ${n.autoUpdates ? 'on' : 'off'}` : '' }}</dd>
            <template v-if="n.docker"><dt>Docker</dt><dd>{{ n.docker.running }} of {{ n.docker.total }} containers running</dd></template>
            <dt>Booted</dt><dd>{{ when(n.t - n.uptime * 1000) }}</dd>
            <dt>Processes</dt><dd>{{ n.procs }}</dd>
            <dt class="sub">Access</dt>
            <dt>Hostname</dt><dd>{{ n.hostname }}</dd>
            <dt>Addresses</dt><dd>{{ n.ips.join(', ') || '–' }}</dd>
            <dt>Checks run as</dt>
            <dd>{{ n.probeUser }}<template v-if="n.probeUser !== 'root'"> <span class="chip warn">no passwordless sudo</span><template v-if="can('owner')"> <RouterLink class="small" :to="`/servers/${s.id}/settings`">Settings</RouterLink></template></template></dd>
            <template v-if="s.notes"><dt>Notes</dt><dd class="pre-wrap">{{ s.notes }}</dd></template>
          </dl>
        </div>
      </section>
    </div>
    <DiskHealth v-if="n" :s="s" />
  </div>
</template>

<style scoped>
.charts { display: grid; gap: 16px; grid-template-columns: repeat(auto-fill, minmax(min(100%, 340px), 1fr)); }
.kv .sub { grid-column: 1 / -1; font-size: var(--fs-xs); font-weight: 600; color: var(--muted); margin-top: 10px; }
.kv .sub:first-child { margin-top: 0; }
</style>
