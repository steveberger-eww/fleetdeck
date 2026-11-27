<script setup>
// Line chart with a recessive grid, one y-axis, crosshair + tooltip. Gaps in data break the line.
//   <LineChart title="CPU" :series="[{ key: 'cpu', label: 'CPU', color: 'var(--s1)' }]" :data="{ from, to, bucket, points: [{ t, cpu }] }"
//              :fmt="(v) => pct(v)" :y-max="1" current="12%" :sync="hoverRef" />
// Charts given the same `sync` ref share the crosshair: hover one, all show that minute. Slot #actions sits in the header.
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { when, timeFmt, dayFmt } from '@/lib/format.js';

const props = defineProps({
  title: String, series: Array, data: Object, fmt: { type: Function, default: (v) => v.toFixed(1) }, yMax: Number, current: String, sync: Object,
});
const plot = ref();
const w = ref(420);
const own = ref(null);
const hoverT = computed({ get: () => (props.sync ? props.sync.value : own.value), set: (v) => { if (props.sync) props.sync.value = v; else own.value = v; } });
let ro;
onMounted(() => { ro = new ResizeObserver(([e]) => { w.value = Math.max(200, e.contentRect.width); }); ro.observe(plot.value); });
onBeforeUnmount(() => ro?.disconnect());

const H = 160, pr = 8, pt = 10, pb = 22;
function niceMax(v) {
  if (!(v > 0)) return 1;
  const e = 10 ** Math.floor(Math.log10(v));
  const m = v / e;
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * e;
}
const pts = computed(() => props.data?.points || []);
const from = computed(() => props.data?.from || Date.now() - 3600e3);
const to = computed(() => props.data?.to || Date.now());
const span = computed(() => to.value - from.value);
const max = computed(() => props.yMax ?? niceMax(Math.max(0, ...pts.value.flatMap((p) => props.series.map((s) => p[s.key] ?? 0)))));
// Left padding fits the widest y-axis label.
const pl = computed(() => Math.max(36, Math.max(...[0, 0.5, 1].map((f) => props.fmt(max.value * f).length)) * 6.4 + 12));
const x = (t) => pl.value + ((t - from.value) / span.value) * (w.value - pl.value - pr);
const y = (v) => pt + (1 - Math.min(v, max.value) / max.value) * (H - pt - pb);
const paths = computed(() => {
  const gap = (props.data?.bucket || 60e3) * 2.5;
  return props.series.map((s) => {
    let d = '';
    let prev = null;
    for (const p of pts.value) {
      if (p[s.key] == null) { prev = null; continue; }
      d += `${prev && p.t - prev.t <= gap ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p[s.key]).toFixed(1)}`;
      prev = p;
    }
    return { ...s, d };
  });
});
const tfmt = (t) => (span.value <= 864e5 ? timeFmt : dayFmt).format(new Date(t));
const xticks = computed(() => [0.02, 0.35, 0.68, 0.98].map((f) => from.value + f * span.value));
const hover = computed(() => {
  if (hoverT.value == null || !pts.value.length) return null;
  let h = pts.value[0];
  for (const p of pts.value) if (Math.abs(p.t - hoverT.value) < Math.abs(h.t - hoverT.value)) h = p;
  return h;
});
const move = (e) => {
  if (!pts.value.length) return;
  const r = e.currentTarget.getBoundingClientRect();
  hoverT.value = from.value + ((e.clientX - r.left - pl.value) / (w.value - pl.value - pr)) * span.value;
};
</script>
<template>
  <div class="panel chart">
    <div class="chart-h">
      <h3>{{ title }}</h3>
      <div v-if="series.length > 1" class="legend"><span v-for="s in series" :key="s.key"><i :style="{ background: s.color }" />{{ s.label }}</span></div>
      <span v-if="current" class="now">{{ current }}</span>
      <span v-if="$slots.actions" class="chart-act"><slot name="actions" /></span>
    </div>
    <div ref="plot" class="plot">
      <svg :viewBox="`0 0 ${w} ${H}`" role="img" :aria-label="`${title} chart${current ? `, now ${current}` : ''}`" @pointermove="move" @pointerleave="hoverT = null">
        <g class="grid"><line v-for="f in [0, 0.5, 1]" :key="f" :x1="pl" :x2="w - pr" :y1="y(max * f)" :y2="y(max * f)" /></g>
        <g class="axis">
          <text v-for="f in [0, 0.5, 1]" :key="f" :x="pl - 8" :y="y(max * f) + 4" text-anchor="end">{{ fmt(max * f) }}</text>
          <text v-for="(t, i) in xticks" :key="i" :x="x(t)" :y="H - 4" :text-anchor="i === 0 ? 'start' : i === 3 ? 'end' : 'middle'">{{ tfmt(t) }}</text>
        </g>
        <path v-for="p in paths" :key="p.key" class="line" :d="p.d" :stroke="p.color" />
        <text v-if="!pts.length" :x="(w + pl) / 2" :y="H / 2" text-anchor="middle" class="nodata">No data for this range yet</text>
        <g v-if="hover">
          <line class="cross" :x1="x(hover.t)" :x2="x(hover.t)" :y1="pt" :y2="H - pb" />
          <template v-for="s in series" :key="s.key"><circle v-if="hover[s.key] != null" class="dot" :cx="x(hover.t)" :cy="y(hover[s.key])" r="3.5" :fill="s.color" /></template>
        </g>
      </svg>
      <div v-if="hover" class="tip" :style="{ left: `${Math.min(Math.max(x(hover.t) + 12, 0), w - 180)}px` }">
        <div class="k">{{ when(hover.t) }}</div>
        <div v-for="s in series" :key="s.key"><i :style="{ background: s.color }" />{{ s.label }} <b>{{ hover[s.key] == null ? '–' : fmt(hover[s.key]) }}</b></div>
      </div>
    </div>
  </div>
</template>
<style>
.chart { padding: 14px 16px 10px; min-width: 0; }
.chart-h { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; min-height: 28px; margin-bottom: 6px; }
.chart-h h3 { font-size: var(--fs-md); font-weight: 600; }
.chart-h .now { margin-left: auto; font-size: var(--fs-md); font-weight: 600; }
.chart-h .chart-act { display: flex; gap: 6px; }
.chart-h .now + .chart-act { margin-left: 0; }
.chart-h .legend + .chart-act, .chart-h h3 + .chart-act { margin-left: auto; }
.legend { display: flex; gap: 12px; font-size: var(--fs-xs); color: var(--muted); }
.legend span { display: inline-flex; align-items: center; gap: 5px; }
.legend i, .chart .tip i { width: 8px; height: 8px; border-radius: 2px; display: inline-block; }
.chart .plot { position: relative; }
.chart svg { width: 100%; height: 160px; display: block; touch-action: pan-y; }
.chart .grid line { stroke: var(--line); stroke-width: 1; }
.chart .axis text { fill: var(--muted); font-size: 11px; font-family: var(--font); }
.chart .nodata { fill: var(--muted); font-size: 13px; font-family: var(--font); }
.chart .line { fill: none; stroke-width: 1.75; stroke-linejoin: round; stroke-linecap: round; }
.chart .cross { stroke: var(--line-2); stroke-dasharray: 3 3; }
.chart .dot { stroke: var(--panel); stroke-width: 2; }
.chart .tip { position: absolute; top: 4px; pointer-events: none; padding: 8px 10px; border-radius: var(--r); background: var(--panel); border: 1px solid var(--line); box-shadow: var(--shadow-lg); font-size: var(--fs-xs); display: grid; gap: 3px; min-width: 160px; z-index: 2; }
.chart .tip .k { color: var(--muted); margin-bottom: 2px; }
.chart .tip div { display: flex; align-items: center; gap: 6px; }
.chart .tip b { margin-left: auto; font-weight: 600; }
</style>
