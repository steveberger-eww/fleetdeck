<script setup>
// Instance settings (super admins): general, organizations, accounts. All read /api/instance.
import { ref, reactive, computed, watch } from 'vue';
import { Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { toast } from '@/lib/store.js';
import SettingRow from './SettingRow.vue';
import SaveBar from './SaveBar.vue';
import OrgsPanel from './OrgsPanel.vue';
import AccountsPanel from './AccountsPanel.vue';

const props = defineProps({ part: { type: String, default: 'general' } });
const HEAD = {
  general: ['General', 'Applies to this whole Fleetdeck and every organization on it.'],
  orgs: ['Organizations', 'Each has its own servers, people, scripts and alerts.'],
  accounts: ['Accounts', 'Everyone who can sign in. Super admins manage this Fleetdeck and act as admin in every organization.'],
};
const RETENTION = [
  ['retention_metrics_days', 'Metrics', 'CPU, memory, disk and network history behind the charts.', 365],
  ['retention_jobs_days', 'Job output', 'What scripts printed. The job itself stays in the list.', 3650],
  ['retention_events_days', 'Audit log', 'Changes, sign-ins, alerts and remote actions.', 3650],
];
const d = useLoad(() => api('/api/instance'));
const x = computed(() => d.data);
const reload = () => d.reload(true);

// The general forms start from the first load; later quiet reloads (orgs, accounts) leave them alone.
const url = ref('');
const urlSaved = ref('');
const ret = reactive({});
const retSaved = ref({});
watch(x, (v, old) => {
  if (!v || old) return;
  url.value = urlSaved.value = v.public_url || '';
  for (const [k] of RETENTION) ret[k] = v[k];
  retSaved.value = { ...ret };
}, { immediate: true });
const retChanged = computed(() => RETENTION.some(([k]) => ret[k] !== retSaved.value[k]));
async function saveUrl() { await api('/api/instance', { method: 'PUT', body: { public_url: url.value } }); urlSaved.value = url.value; toast('Public URL saved', 'ok'); }
async function saveRetention() {
  await api('/api/instance', { method: 'PUT', body: Object.fromEntries(RETENTION.map(([k]) => [k, ret[k]])) });
  retSaved.value = { ...ret };
  toast('Retention saved', 'ok');
}
</script>
<template>
  <div class="page-h"><div><h1>{{ HEAD[props.part][0] }}</h1><p class="sub">{{ HEAD[props.part][1] }}</p></div></div>
  <ErrorNote v-if="d.error" :error="d.error" :retry="d.reload" />
  <Loading v-else-if="!x" />
  <OrgsPanel v-else-if="part === 'orgs'" :list="x.tenants" @change="reload" />
  <AccountsPanel v-else-if="part === 'accounts'" :list="x.users" :me="x.me" @change="reload" />
  <template v-else>
    <section class="panel">
      <SettingRow label="Public URL" desc="The address people open Fleetdeck at. Single sign-on redirects and invite links use it. Empty: taken from each request, which is fine on localhost.">
        <div v-if="x.public_url_env" class="stack sm"><span class="wrap-any">{{ x.public_url_env }}</span><span class="small muted">Set by <code>FLEETDECK_PUBLIC_URL</code></span></div>
        <input v-else v-model.trim="url" aria-label="Public URL" placeholder="https://fleetdeck.example.com" autocapitalize="off" spellcheck="false" />
      </SettingRow>
      <SaveBar v-if="!x.public_url_env" :changed="url !== urlSaved" :save="saveUrl" />
    </section>

    <section class="panel">
      <div class="panel-h"><h2>Data retention</h2></div>
      <SettingRow v-for="[k, label, desc, max] in RETENTION" :key="k" :label="label" :desc="desc">
        <input v-model.number="ret[k]" type="number" class="days" min="1" :max="max" :aria-label="`${label}, days`" /><span class="muted">days</span>
      </SettingRow>
      <SaveBar note="Older data is removed for every organization." :changed="retChanged" :save="saveRetention" />
    </section>

    <section class="panel">
      <SettingRow label="Encryption key">
        <template #desc>
          <template v-if="x.secret_from_env">Keys and notification tokens are encrypted with FLEETDECK_SECRET from the environment.</template>
          <template v-else>Keys and notification tokens are encrypted with a key stored next to the database. Set <code>FLEETDECK_SECRET</code> to keep it out of backups of the data folder.</template>
        </template>
        <span v-if="x.secret_from_env" class="chip ok">From environment</span><code v-else>data/secret.key</code>
      </SettingRow>
    </section>
  </template>
</template>
<style scoped>
.days { width: 96px; text-align: right; }
</style>
