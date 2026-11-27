<script setup>
import { ref, computed } from 'vue';
import { Btn, Icon, Empty, Loading, ErrorNote, Output } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';

const props = defineProps({ s: Object, x: Object });
const since = ref('');
const q = ref('');
const logs = useLoad(() => api(`/api/servers/${props.s.id}/containers/logs?id=${encodeURIComponent(props.x.id)}&lines=${since.value ? 5000 : 500}${since.value ? `&since=${since.value}` : ''}`), { key: since });
const text = computed(() => {
  const t = logs.data?.text || '';
  const n = q.value.toLowerCase();
  return n ? t.split('\n').filter((l) => l.toLowerCase().includes(n)).join('\n') : t;
});
</script>

<template>
  <div class="stack md">
    <div class="row">
      <select v-model="since" class="auto" aria-label="Time range">
        <option value="">Last 500 lines</option><option value="15m">Last 15 min</option><option value="1h">Last hour</option><option value="6h">Last 6 hours</option><option value="24h">Last day</option>
      </select>
      <label class="search grow"><Icon name="search" /><input v-model="q" type="search" placeholder="Filter lines" aria-label="Filter lines" /></label>
      <Btn size="sm" kind="ghost" icon="refresh" @click="logs.reload()">Refresh</Btn>
    </div>
    <ErrorNote v-if="logs.error" :error="logs.error" :retry="logs.reload" />
    <Loading v-else-if="logs.loading && !logs.data" />
    <Empty v-else-if="q && !text" title="No matching lines" icon="filter" />
    <Output v-else :name="x.name" :text="text" />
  </div>
</template>
