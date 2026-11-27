<script setup>
// docker inspect, the parts an admin looks for. Environment values only for roles allowed to see them.
import { ref } from 'vue';
import { Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { when } from '@/lib/format.js';

const props = defineProps({ s: Object, x: Object });
const d = useLoad(() => api(`/api/servers/${props.s.id}/containers/inspect?id=${encodeURIComponent(props.x.id)}`));
const reveal = ref(false);
const never = (t) => !t || t.startsWith('0001'); // docker's zero time: never started
const at = (t) => (never(t) ? '–' : when(t));
</script>

<template>
  <ErrorNote v-if="d.error" :error="d.error" :retry="d.reload" />
  <Loading v-else-if="!d.data" :rows="6" />
  <dl v-else class="kv">
    <dt class="sub">Container</dt>
    <dt>Image</dt><dd>{{ x.image }} <span class="muted small" :title="d.data.image">{{ d.data.image.replace('sha256:', '').slice(0, 12) }}</span></dd>
    <template v-if="d.data.cmd"><dt>Command</dt><dd class="small">{{ d.data.cmd }}</dd></template>
    <template v-if="d.data.user"><dt>User</dt><dd>{{ d.data.user }}</dd></template>
    <dt>Created</dt><dd>{{ at(d.data.created) }}</dd>
    <dt>Started</dt><dd>{{ at(d.data.startedAt) }}</dd>
    <template v-if="x.state !== 'running' && !never(d.data.startedAt)"><dt>Exit code</dt><dd>{{ d.data.exitCode }} <span v-if="d.data.oomKilled" class="chip bad">out of memory</span></dd></template>
    <dt>Restart policy</dt><dd>{{ d.data.restart }}</dd>
    <dt>Restarts</dt><dd><span v-if="d.data.restartCount" class="chip warn">{{ d.data.restartCount }}</span><template v-else>0</template></dd>

    <template v-if="x.project">
      <dt class="sub">Compose</dt>
      <dt>Project</dt><dd>{{ x.project }}</dd>
      <template v-if="x.dir"><dt>Directory</dt><dd>{{ x.dir }}</dd></template>
      <template v-if="x.files"><dt>Files</dt><dd><div v-for="f in x.files.split(',')" :key="f">{{ f }}</div></dd></template>
    </template>

    <dt class="sub">Storage</dt>
    <template v-if="!d.data.mounts.length"><dt>Mounts</dt><dd class="muted">None</dd></template>
    <template v-for="m in d.data.mounts" :key="m.target"><dt>{{ m.target }}</dt><dd>{{ m.source }} <span class="tag">{{ m.type }}</span> <span v-if="!m.rw" class="tag">read-only</span></dd></template>

    <dt class="sub">Networks</dt>
    <template v-if="!d.data.networks.length"><dt>Networks</dt><dd class="muted">None</dd></template>
    <template v-for="n in d.data.networks" :key="n.name"><dt>{{ n.name }}</dt><dd>{{ n.ip || '–' }}</dd></template>

    <dt class="sub">Environment <button v-if="!d.data.envHidden && d.data.env.length" type="button" class="btn ghost sm" @click="reveal = !reveal">{{ reveal ? 'Hide values' : 'Show values' }}</button></dt>
    <template v-if="!d.data.env.length"><dt>Variables</dt><dd class="muted">None</dd></template>
    <template v-for="e in d.data.env" :key="e.name">
      <dt>{{ e.name }}</dt>
      <dd class="small"><span v-if="d.data.envHidden || !reveal" class="muted">••••••</span><template v-else-if="e.value">{{ e.value }}</template><span v-else class="muted">empty</span></dd>
    </template>
    <dd v-if="d.data.envHidden" class="sub note small muted">Values can hold secrets: your role sees the names only.</dd>
  </dl>
</template>

<style scoped>
.kv .sub { grid-column: 1 / -1; display: flex; align-items: center; gap: 8px; font-size: var(--fs-xs); font-weight: 600; color: var(--muted); margin-top: 10px; }
.kv .sub:first-child { margin-top: 0; }
.kv .sub .btn { font-weight: 500; }
.kv .sub.note { font-weight: 400; font-size: var(--fs-sm); margin-top: 0; }
.kv dt { overflow-wrap: anywhere; }
</style>
