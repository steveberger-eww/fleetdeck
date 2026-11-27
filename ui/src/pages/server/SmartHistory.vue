<script setup>
// The last 30 days of one disk: temperature, and wear (NVMe) or sector counts (SATA).
import { ErrorNote, Loading, LineChart } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';

const props = defineProps({ s: Object, d: Object, disk: String });
const h = useLoad(() => api(`/api/servers/${props.s.id}/smart?disk=${encodeURIComponent(props.disk)}&range=30d`));
const int = (v) => String(Math.round(v));
</script>

<template>
  <ErrorNote v-if="h.error" :error="h.error" :retry="h.reload" />
  <Loading v-else-if="!h.data" />
  <p v-else-if="!h.data.points.length" class="small muted">No history yet. It builds up with every check, every 30 minutes.</p>
  <div v-else class="charts">
    <LineChart title="Temperature, 30 days" :data="h.data" :fmt="(v) => `${int(v)} °C`" :series="[{ key: 'temp', label: 'Temperature', color: 'var(--s1)' }]" />
    <LineChart v-if="d.wear != null" title="Wear, 30 days" :data="h.data" :y-max="100" :fmt="(v) => `${int(v)}%`" :series="[{ key: 'wear', label: 'Wear', color: 'var(--s1)' }]" />
    <LineChart v-else title="Sectors, 30 days" :data="h.data" :fmt="int" :series="[{ key: 'realloc', label: 'Reallocated', color: 'var(--s1)' }, { key: 'bad', label: 'Pending + uncorrectable', color: 'var(--s2)' }]" />
  </div>
</template>

<style scoped>
.charts { display: grid; gap: 16px; grid-template-columns: repeat(auto-fill, minmax(min(100%, 340px), 1fr)); }
</style>
