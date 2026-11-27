<script setup>
// One server: header with status and actions, what needs attention, then one tab per area.
import { computed, watch, nextTick, defineAsyncComponent } from 'vue';
import { Btn, Icon, Menu, Empty, Loading, Callout, StatusChip } from '@/components';
import OverviewTab from './OverviewTab.vue';
import UpdatesTab from './UpdatesTab.vue';
import ServicesTab from './ServicesTab.vue';
import LogsTab from './LogsTab.vue';
import ProcessesTab from './ProcessesTab.vue';
import ContainersTab from './ContainersTab.vue';
import NetworkTab from './NetworkTab.vue';
import JobsTab from './JobsTab.vue';
import SettingsTab from './SettingsTab.vue';
import { api } from '@/lib/api.js';
import { servers, refreshServers, can, toast, confirmBox } from '@/lib/store.js';
import { SERVER_TABS } from '@/lib/nav.js';
import { confirmRun, muteFor } from '@/lib/actions.js';
import { ago, dur, uptime, eol } from '@/lib/format.js';
import { runOn, attempt, urlHost } from './util.js';

const TerminalTab = defineAsyncComponent(() => import('./TerminalTab.vue')); // carries xterm
const props = defineProps({ id: String, tab: String });

const BODY = { overview: OverviewTab, updates: UpdatesTab, services: ServicesTab, logs: LogsTab, processes: ProcessesTab, containers: ContainersTab, network: NetworkTab, terminal: TerminalTab, jobs: JobsTab, settings: SettingsTab };
const ROLE = { terminal: 'operator', settings: 'owner' };

const s = computed(() => servers.value?.find((x) => x.id === props.id));
const n = computed(() => s.value?.snapshot);
const op = computed(() => can('operator'));
const allowed = computed(() => !ROLE[props.tab] || can(ROLE[props.tab]));
// Containers only with Docker or Proxmox (the route still works and explains).
const tabs = computed(() => SERVER_TABS.filter(([k]) => (k !== 'containers' || n.value?.docker || n.value?.pve || props.tab === 'containers') && (!ROLE[k] || can(ROLE[k]))));
const tabTo = (k) => `/servers/${props.id}${k === 'overview' ? '' : `/${k}`}`;

// Counts on the tab that fixes them, instead of a row of chips above the tabs.
function badge(k) {
  const x = n.value;
  if (!x) return null;
  if (k === 'updates' && x.updates) return { cls: x.security ? 'soft' : 'warn', v: x.updates, title: x.security ? `${x.security} security` : 'Pending updates' };
  if (k === 'services' && x.failed) return { cls: 'warn', v: x.failed, title: 'Failed units' };
  if (k === 'containers' && x.unhealthy) return { cls: 'warn', v: x.unhealthy, title: 'Unhealthy containers' };
  return null;
}

const address = computed(() => `${s.value.user}@${s.value.host}${s.value.port !== 22 ? `:${s.value.port}` : ''}`);
const osEol = computed(() => eol(n.value));
const downTitle = computed(() => `Unreachable${s.value.error ? ` for ${dur(Date.now() - s.value.error.since)}` : ''}.`);
const firing = computed(() => (s.value.firing || []).filter((a) => !(s.value.status === 'down' && a.metric === 'down'))); // the unreachable callout already says it
const cockpitUrl = computed(() => `https://${urlHost(s.value.host)}:9090`);

const checkNow = async () => { await api(`/api/servers/${s.value.id}/probe`, { method: 'POST' }); await refreshServers(); };
const reboot = attempt(async () => {
  if (!await confirmRun({ scriptId: 'reboot', name: 'Reboot', count: 1, names: [s.value.name] })) return;
  await runOn(s.value, 'reboot');
});
const installCockpit = attempt(() => runOn(s.value, 'cockpit'));
const mute = (h) => attempt(async () => {
  await muteFor(s.value.id, h, `Maintenance on ${s.value.name}`);
  toast(`Notifications for ${s.value.name} muted for ${h} h`, 'ok', { label: 'Quiet hours', to: '/alerts' });
})();
async function acceptKey() {
  if (!await confirmBox({ title: 'Trust the new host key?', body: `Only do this if you know why the key changed (reinstall, new server at the same address). Offered key: ${s.value.ssh.hostKeyMismatch}`, confirm: 'Trust new key', danger: true })) return;
  await api(`/api/servers/${s.value.id}/hostkey/accept`, { method: 'POST' });
  toast('New host key trusted', 'ok');
  refreshServers();
}

watch(() => [props.tab, !!s.value], async () => {
  await nextTick();
  document.querySelector('.srv-tabs [aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}, { immediate: true });
</script>

<template>
  <div class="page">
    <Loading v-if="!servers" :rows="5" />
    <div v-else-if="!s" class="panel">
      <Empty title="Server not found" icon="server">It may have been removed.<template #action><RouterLink class="btn" to="/">Back to the fleet</RouterLink></template></Empty>
    </div>
    <template v-else>
      <div class="page-h srv-h">
        <div class="grow">
          <div class="row lg"><h1>{{ s.name }}</h1><StatusChip :status="s.status" /><span v-for="t in s.tags" :key="t" class="tag">{{ t }}</span></div>
          <div class="meta">
            <span>{{ address }}</span>
            <template v-if="n">
              <span>{{ n.os }} <span v-if="osEol" :class="['chip', osEol.kind]" title="No more security updates from the vendor after this date">{{ osEol.text }}</span></span>
              <span>Up {{ uptime(n.uptime) }}</span>
              <span>Checked {{ ago(n.t) }}</span>
            </template>
          </div>
        </div>
        <div class="actions">
          <Btn v-if="op" icon="refresh" title="Check now" @click="checkNow" />
          <Menu v-if="op || n?.cockpit" :label="`More actions for ${s.name}`">
            <a v-if="n?.cockpit" class="mi" :href="cockpitUrl" target="_blank" rel="noopener noreferrer"><Icon name="ext" />Open Cockpit</a>
            <button v-else-if="n && op" type="button" class="mi" title="Storage, networking and firewall management" @click="installCockpit"><Icon name="plus" />Install Cockpit</button>
            <template v-if="can('admin')">
              <button type="button" class="mi" @click="mute(1)"><Icon name="mute" />Mute alerts for 1 h</button>
              <button type="button" class="mi" @click="mute(4)"><Icon name="mute" />Mute alerts for 4 h</button>
            </template>
            <template v-if="op"><hr><button type="button" class="mi danger" @click="reboot"><Icon name="power" />Reboot…</button></template>
          </Menu>
        </div>
      </div>

      <Callout v-if="s.ssh?.hostKeyMismatch" kind="bad" title="The host key changed.">
        Fleetdeck refuses to connect, because this can mean someone is intercepting the connection.
        Pinned: <span class="wrap-any">{{ s.host_key }}</span>, offered: <span class="wrap-any">{{ s.ssh.hostKeyMismatch }}</span>
        <template v-if="can('owner')" #actions><Btn size="sm" @click="acceptKey">Trust new key</Btn></template>
      </Callout>
      <Callout v-else-if="s.status === 'down'" kind="bad" :title="downTitle">
        {{ s.error?.message }}
        <template v-if="can('owner')" #actions><RouterLink class="btn sm" :to="`/servers/${s.id}/settings`">Connection settings</RouterLink></template>
      </Callout>
      <Callout v-if="s.ssh?.sudoRejected" kind="warn" title="The server rejected the stored sudo password.">
        Fleetdeck stopped using it so the account doesn't get locked. Update it to resume admin actions.
        <template v-if="can('owner')" #actions><RouterLink class="btn sm" :to="`/servers/${s.id}/settings`">Update password</RouterLink></template>
      </Callout>
      <Callout v-if="firing.length" kind="bad" :title="firing.length === 1 ? `Alert firing ${ago(firing[0].since)}:` : `${firing.length} alerts firing`">
        <template v-if="firing.length === 1">{{ firing[0].message }}</template>
        <ul v-else><li v-for="(a, i) in firing" :key="i">{{ a.message }} <span class="muted">({{ ago(a.since) }})</span></li></ul>
        <template v-if="can('admin')" #actions><RouterLink class="btn sm" to="/alerts">Alert rules</RouterLink></template>
      </Callout>

      <nav class="tabs srv-tabs" aria-label="Server sections">
        <RouterLink v-for="[k, label] in tabs" :key="k" :to="tabTo(k)" :aria-current="tab === k ? 'page' : undefined">
          {{ label }}<span v-if="badge(k)" :class="['count', badge(k).cls]" :title="badge(k).title">{{ badge(k).v }}</span>
        </RouterLink>
      </nav>

      <component :is="BODY[tab] || OverviewTab" v-if="allowed" :s="s" />
      <div v-else class="panel"><Empty title="Not available with your role" icon="lock" /></div>
    </template>
  </div>
</template>

<style scoped>
.srv-h { align-items: flex-start; }
.srv-h .row { gap: 10px; }
.meta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 18px; margin-top: 6px; color: var(--muted); }
.srv-tabs { margin-bottom: -4px; }
</style>
