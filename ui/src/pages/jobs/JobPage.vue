<script setup>
// One job: live output per server over an EventSource, then re-run / cancel.
import { ref, computed, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Btn, Icon, JobChip, Loading, ErrorNote, Output, CodeEditor } from '@/components';
import { api } from '@/lib/api.js';
import { useQuery, useNow } from '@/lib/use.js';
import { servers, can, refreshServers, refreshRunning, toast, confirmBox } from '@/lib/store.js';
import { confirmRun } from '@/lib/actions.js';
import { ago, dur, when } from '@/lib/format.js';

const props = defineProps({ id: String });
const router = useRouter();
const job = ref(null);
const error = ref('');
const sel = useQuery('host');
const ANSIBLE = '__ansible__';

const fetchJob = () => api(`/api/jobs/${props.id}`).then((j) => { job.value = j; });
watch(() => props.id, (id, _, onCleanup) => {
  job.value = null;
  error.value = '';
  const es = new EventSource(`/api/jobs/${id}/stream`);
  const run = (server) => job.value?.runs.find((r) => r.server_id === server);
  es.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.type === 'snapshot') job.value = m.job;
    else if (m.type === 'data') { const r = run(m.server); if (r) r.output += m.data; }
    else if (m.type === 'run') { const r = run(m.server); if (r) Object.assign(r, { status: m.status, exit_code: m.code ?? r.exit_code }); }
    else if (m.type === 'job') {
      if (job.value) Object.assign(job.value, { status: m.status, finished_at: Date.now() });
      es.close();
      refreshServers(); // the servers were re-probed before the job finished
      refreshRunning();
      setTimeout(() => fetchJob().catch(() => {}), 1000); // a chained script starts right after
    }
  };
  es.onerror = () => { es.close(); fetchJob().catch((x) => { error.value = x.message; }); };
  onCleanup(() => es.close());
}, { immediate: true });

const now = useNow(() => job.value?.status === 'running');
// A waiting retry: reload when it should have started, to link the next attempt.
watch(() => job.value?.retry_at, (at, _, onCleanup) => {
  if (!at) return;
  const t = setTimeout(() => fetchJob().catch(() => {}), Math.max(0, at - Date.now()) + 3000);
  onCleanup(() => clearTimeout(t));
});

const runs = computed(() => (job.value?.runs || []).slice().sort((a, b) => (b.server_id === ANSIBLE) - (a.server_id === ANSIBLE)));
const hosts = computed(() => runs.value.filter((r) => r.server_id !== ANSIBLE));
const current = computed(() => {
  const r = runs.value;
  return r.find((x) => x.server_id === sel.value) || (job.value?.kind === 'ansible' ? r[0] : r.find((x) => x.status === 'failed') || r.find((x) => x.status === 'running') || r[0]);
});
const failed = computed(() => hosts.value.filter((r) => !['success', 'skipped', 'running', 'queued'].includes(r.status)));
const SUMMARY = [['success', 'ok', 'succeeded'], ['failed', 'bad', 'failed'], ['interrupted', 'warn', 'interrupted'], ['cancelled', 'warn', 'cancelled'], ['running', 'info', 'running'], ['queued', '', 'queued'], ['skipped', '', 'skipped']];
const summary = computed(() => {
  const counts = {};
  for (const r of hosts.value) counts[r.status] = (counts[r.status] || 0) + 1;
  return SUMMARY.filter(([k]) => counts[k]).map(([k, cls, l]) => [k, cls, `${counts[k]} ${l}`]);
});
const took = computed(() => (job.value.finished_at || now.value) - job.value.created_at);
const knownServer = computed(() => current.value && current.value.server_id !== ANSIBLE && (servers.value || []).some((s) => s.id === current.value.server_id));

async function rerun(failedOnly) {
  const list = failedOnly ? failed.value : hosts.value;
  if (job.value.script_id && !await confirmRun({ scriptId: job.value.script_id, name: job.value.name, count: list.length, names: list.map((r) => r.server_name) })) return;
  const r = await api(`/api/jobs/${props.id}/rerun`, { method: 'POST', body: { failed_only: failedOnly } });
  router.push(`/jobs/${r.id}`);
}
async function cancelRetry() {
  await api(`/api/jobs/${props.id}/cancel`, { method: 'POST' });
  await fetchJob();
  toast('Retry cancelled');
}
async function cancel() {
  if (!await confirmBox({ title: 'Cancel this job?', body: 'Output stops immediately. A command already running on a server may still finish there.', confirm: 'Cancel job', danger: true })) return;
  await api(`/api/jobs/${props.id}/cancel`, { method: 'POST' });
  toast('Job cancelled');
}
</script>

<template>
  <div class="page">
    <ErrorNote v-if="error" :error="error" />
    <Loading v-else-if="!job" :rows="6" />
    <template v-else>
      <div class="page-h">
        <div>
          <div class="row"><h1>{{ job.name }}</h1><JobChip :status="job.status" /></div>
          <p class="sub">
            Job #{{ job.id }}, started by
            <RouterLink v-if="job.parent_job" :to="`/jobs/${job.parent_job}`">#{{ job.parent_job }}</RouterLink><template v-else>{{ job.actor }}</template>
            <span :title="when(job.created_at)">{{ ' ' }}{{ ago(job.created_at) }}</span>, {{ job.finished_at ? 'took' : 'running for' }} {{ dur(took) }}<template v-if="job.options?.check">. Dry run, nothing was changed</template>
            <template v-if="job.next_job"> · Then ran <RouterLink :to="`/jobs/${job.next_job}`">#{{ job.next_job }}</RouterLink></template>
            <template v-if="job.retry_of"> · Attempt {{ job.attempt }}, retrying <RouterLink :to="`/jobs/${job.retry_of}`">#{{ job.retry_of }}</RouterLink></template>
            <template v-if="job.retry_job"> · Retried as <RouterLink :to="`/jobs/${job.retry_job}`">#{{ job.retry_job }}</RouterLink></template>
            <template v-if="job.retry_at"> · Attempt {{ job.attempt + 1 }} starts {{ when(job.retry_at) }}</template>
          </p>
        </div>
        <div v-if="can('operator')" class="actions">
          <Btn v-if="job.status === 'running'" icon="stop" @click="cancel">Cancel</Btn>
          <Btn v-if="job.retry_at" icon="stop" @click="cancelRetry">Cancel retry</Btn>
          <template v-if="job.status !== 'running'">
            <Btn v-if="failed.length && failed.length < hosts.length" @click="rerun(true)">Re-run failed</Btn>
            <Btn icon="refresh" @click="rerun(false)">Re-run</Btn>
          </template>
        </div>
      </div>

      <div v-if="hosts.length > 1" class="row" aria-label="Servers by status">
        <span v-for="[k, cls, text] in summary" :key="k" :class="['chip', cls]">{{ text }}</span>
      </div>

      <div :class="['job-grid', runs.length > 1 && 'split']">
        <nav v-if="runs.length > 1" class="panel hostlist" aria-label="Servers">
          <button v-for="r in runs" :key="r.server_id" type="button" :aria-pressed="r === current" @click="sel = r.server_id">
            <span class="grow trunc">{{ r.server_name }}</span><JobChip :status="r.status" />
          </button>
        </nav>
        <section class="panel">
          <div class="panel-h">
            <h2 class="trunc">{{ current?.server_name }}</h2>
            <JobChip v-if="runs.length === 1" :status="current?.status" />
            <span v-if="current?.exit_code != null" class="small muted">exit code {{ current.exit_code }}</span>
            <RouterLink v-if="knownServer" class="right small" :to="`/servers/${current.server_id}`">Open server</RouterLink>
          </div>
          <div class="panel-b">
            <Output :name="`job-${job.id}-${current?.server_name || ''}`" :text="current?.output || (current?.status === 'queued' ? 'Waiting for a free slot…' : '')" />
          </div>
        </section>
      </div>

      <details class="panel script">
        <summary class="panel-h"><Icon name="chevr" class="disclose" /><h2>Script</h2></summary>
        <div class="panel-b stack md">
          <dl v-if="Object.keys(job.params || {}).length" class="kv">
            <template v-for="(v, k) in job.params" :key="k"><dt>${{ k }}</dt><dd>{{ v }}</dd></template>
          </dl>
          <dl v-if="job.kind === 'ansible'" class="kv">
            <dt>Playbook</dt><dd>{{ job.body }}</dd>
            <dt>Commit</dt><dd>{{ job.commit_sha || '–' }}</dd>
            <dt>Mode</dt><dd>{{ job.options?.check ? 'Dry run (--check --diff)' : 'Apply' }}{{ job.as_root ? ', become root' : '' }}</dd>
          </dl>
          <template v-else>
            <CodeEditor :key="job.id" :model-value="job.body" read-only label="Command" />
            <p class="small muted">Ran {{ job.as_root ? 'as root' : 'as the SSH user' }}.</p>
          </template>
        </div>
      </details>
    </template>
  </div>
</template>

<style scoped>
.job-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; align-items: start; }
.job-grid.split { grid-template-columns: 260px minmax(0, 1fr); }
.hostlist { display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; padding: 6px; max-height: calc(100vh - 96px); overflow-y: auto; position: sticky; top: 72px; }
.hostlist button { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; min-width: 0; padding: 7px 10px; border-radius: var(--r-sm); color: var(--ink-2); font-weight: 500; cursor: pointer; }
.hostlist button:hover { background: var(--hover); color: var(--ink); }
.hostlist button:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.hostlist button[aria-pressed="true"] { background: var(--accent-soft); color: var(--ink); box-shadow: inset 3px 0 0 var(--accent); }
.hostlist .chip { flex: none; }
.script > summary { list-style: none; cursor: pointer; margin: 0; border-bottom: 0; color: var(--ink); }
.script > summary::-webkit-details-marker { display: none; }
.script[open] > summary { border-bottom: 1px solid var(--line); margin: 0; }
.script .disclose { color: var(--muted); transition: transform .12s; }
.script[open] .disclose { transform: rotate(90deg); }
@media (max-width: 800px) {
  .job-grid.split { grid-template-columns: minmax(0, 1fr); }
  .hostlist { position: static; max-height: 240px; }
}
</style>
