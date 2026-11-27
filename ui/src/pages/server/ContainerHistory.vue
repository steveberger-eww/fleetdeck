<script setup>
// CPU and memory of one container or guest, recorded every minute by the probe and kept as long as server metrics.
import { ref } from 'vue';
import { ErrorNote, LineChart } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { bytes } from '@/lib/format.js';
import { RANGES } from './util.js';

const props = defineProps({ s: Object, name: String });
const range = ref('6h');
const m = useLoad(() => api(`/api/servers/${props.s.id}/containers/metrics?name=${encodeURIComponent(props.name)}&range=${range.value}`), { key: range });
const sync = ref(null);
</script>

<template>
  <div class="stack md">
    <div class="seg" role="group" aria-label="Time range"><button v-for="r in RANGES" :key="r" type="button" :aria-pressed="r === range" @click="range = r">{{ r }}</button></div>
    <ErrorNote v-if="m.error" :error="m.error" :retry="m.reload" />
    <LineChart :sync="sync" title="CPU" :data="m.data" :fmt="(v) => `${v.toFixed(v < 10 ? 1 : 0)}%`" :series="[{ key: 'cpu', label: 'CPU (100% = one core)', color: 'var(--s1)' }]" />
    <LineChart :sync="sync" title="Memory" :data="m.data" :fmt="(v) => bytes(v)" :series="[{ key: 'mem', label: 'Memory', color: 'var(--s1)' }]" />
  </div>
</template>

<style scoped>
.seg { justify-self: start; }
</style>
