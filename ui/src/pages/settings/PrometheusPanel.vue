<script setup>
// Scrape token for /api/metrics: only its hash is stored, so the token is shown once, right after generating it.
import { ref, computed } from 'vue';
import { Btn, Copy, Output, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { confirmBox } from '@/lib/store.js';
import SettingRow from './SettingRow.vue';

const t = useLoad(() => api('/api/tenant/metrics-token'));
const fresh = ref(null);
async function generate() {
  if (t.data.enabled && !await confirmBox({ title: 'Generate a new token?', body: 'The current token stops working. Update your Prometheus config with the new one.', confirm: 'Generate new token', danger: true })) return;
  fresh.value = (await api('/api/tenant/metrics-token', { method: 'POST' })).token;
  t.reload(true);
}
async function off() {
  if (!await confirmBox({ title: 'Turn off Prometheus scraping?', body: 'The token stops working and scrapes get 401.', confirm: 'Turn off', danger: true })) return;
  await api('/api/tenant/metrics-token', { method: 'DELETE' });
  fresh.value = null;
  t.reload(true);
}
const config = computed(() => fresh.value && `scrape_configs:
  - job_name: fleetdeck
    scheme: ${t.data.url.split(':')[0]}
    metrics_path: /api/metrics
    authorization:
      credentials: ${fresh.value}
    static_configs:
      - targets: ['${t.data.url.split('/')[2]}']`);
</script>
<template>
  <ErrorNote v-if="t.error" :error="t.error" :retry="t.reload" />
  <section v-else class="panel">
    <div class="panel-h"><h2>Prometheus</h2><span v-if="t.data" :class="['chip', t.data.enabled && 'ok']">{{ t.data.enabled ? 'On' : 'Off' }}</span></div>
    <Loading v-if="!t.data" :rows="2" />
    <template v-else>
      <SettingRow label="Scrape endpoint" desc="The latest state of every server in this organization (paused ones left out), with a bearer token."><Copy :text="t.data.url" class="grow" /></SettingRow>
      <div v-if="fresh" class="panel-b stack sm fresh">
        <p class="small">Copy the token now. It isn't shown again.</p>
        <Copy :text="fresh" />
        <Output :text="config" name="prometheus" short />
      </div>
      <div class="panel-f">
        <span class="small muted grow">{{ t.data.enabled ? 'Anyone with the token can read your servers\' metrics, names and mounts.' : 'Generate a token to let Prometheus scrape Fleetdeck.' }}</span>
        <Btn v-if="t.data.enabled" @click="off">Turn off</Btn>
        <Btn :kind="t.data.enabled ? '' : 'primary'" @click="generate">{{ t.data.enabled ? 'New token' : 'Generate token' }}</Btn>
      </div>
    </template>
  </section>
</template>
<style scoped>
.fresh { border-top: 1px solid var(--line); }
</style>
