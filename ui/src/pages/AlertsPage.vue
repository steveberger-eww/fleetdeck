<script setup>
// Alert rules, what is firing, where notifications go, quiet hours and the history.
import { ref, computed } from 'vue';
import { useRouter } from 'vue-router';
import { Btn, SortTh, Empty, Loading, ErrorNote, Callout } from '@/components';
import Switch from '@/components/Switch.vue';
import RuleDialog from './alerts/RuleDialog.vue';
import ChannelDialog from './alerts/ChannelDialog.vue';
import QuietPanel from './alerts/QuietPanel.vue';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { servers, toast, confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';
import { ruleText, mountMissing } from './alerts/rules.js';

const router = useRouter();
const a = useLoad(() => api('/api/alerts'), { poll: 30_000 });
const n = useLoad(() => api('/api/notifiers'));
const rule = ref(null);
const channel = ref(null);
const sort = useSort(() => a.data?.rules);
const all = computed(() => servers.value || []);
const channels = computed(() => n.data?.notifiers.filter((c) => c.enabled).length || 0);
const text = (r) => ruleText(r, a.data.metrics);
const firingBy = (r) => a.data.firing.filter((f) => f.rule_id === r.id).length;
const eventText = (e) => e.message.replace(/^(Firing|Resolved): /, '');
const JOB_MODES = [['off', 'Never'], ['scheduled', 'Scheduled and webhook jobs'], ['all', 'All jobs']];

// A switch whose request fails flips back.
async function flip(url, item, e, list) {
  try { await api(url, { method: 'PUT', body: { enabled: !item.enabled } }); } catch (x) { e.target.checked = item.enabled; toast(x.message, 'bad'); }
  list.reload(true);
}
async function delRule(r) {
  if (!await confirmBox({ title: 'Delete this rule?', body: text(r), confirm: 'Delete rule', danger: true })) return;
  await api(`/api/alerts/rules/${r.id}`, { method: 'DELETE' });
  a.reload(true);
}
async function testChannel(c) {
  await api(`/api/notifiers/${c.id}/test`, { method: 'POST' });
  toast(`Test sent via ${c.name}`, 'ok');
}
async function delChannel(c) {
  if (!await confirmBox({ title: `Delete "${c.name}"?`, confirm: 'Delete channel', danger: true })) return;
  await api(`/api/notifiers/${c.id}`, { method: 'DELETE' });
  n.reload(true);
}
async function setJobMode(mode) {
  try {
    await api('/api/settings', { method: 'PUT', body: { notify_jobs: mode } });
    n.reload(true);
    toast('Saved', 'ok');
  } catch (e) { toast(e.message, 'bad'); }
}
const toChannels = () => document.getElementById('channels')?.scrollIntoView({ behavior: 'smooth' });
const openServer = (e, f) => { if (!e.target.closest('a, button')) router.push(`/servers/${f.server_id}`); };
const ruleSaved = () => { rule.value = null; a.reload(true); };
const channelSaved = () => { channel.value = null; n.reload(true); };
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Alerts</h1><p class="sub">Rules are checked after every probe. You get one message when a rule fires and one when it resolves.</p></div>
    </div>

    <Loading v-if="a.loading || n.loading" :rows="6" />
    <ErrorNote v-else-if="a.error" :error="a.error" :retry="a.reload" />
    <template v-else>
      <section v-if="a.data.firing.length" class="panel">
        <div class="panel-h"><h2>Firing now</h2><span class="count">{{ a.data.firing.length }}</span></div>
        <div class="table-wrap">
          <table class="t">
            <tbody>
              <tr v-for="f in a.data.firing" :key="`${f.rule_id}-${f.server_id}`" class="click firing" @click="openServer($event, f)">
                <td><RouterLink class="rowlink" :to="`/servers/${f.server_id}`">{{ f.server_name }}</RouterLink> <span class="ink2">{{ f.message }}</span></td>
                <td class="muted nowrap num" :title="when(f.since)">since {{ ago(f.since) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="panel">
        <div class="panel-h"><h2>Rules</h2><Btn size="sm" icon="plus" class="right" @click="rule = {}">Add rule</Btn></div>
        <div v-if="a.data.rules.length && !channels && !n.error" class="panel-b divided">
          <Callout kind="warn">Alerts only show in Fleetdeck. Add a notification channel to get them pushed to your phone or chat.
            <template #actions><button type="button" class="btn sm" @click="toChannels">Notification channels</button></template>
          </Callout>
        </div>
        <Empty v-if="!a.data.rules.length" title="No rules" icon="bell">Add a rule to get notified when a server goes down or fills up.</Empty>
        <div v-else class="table-wrap">
          <table class="t">
            <thead><tr>
              <SortTh :sort="sort" k="enabled" class="on" desc>On</SortTh>
              <SortTh :sort="sort" k="metric" :get="text">Condition</SortTh>
              <SortTh :sort="sort" k="scope" :get="(r) => r.server_name || r.tag || ''">Applies to</SortTh>
              <SortTh :sort="sort" k="firing" :get="firingBy" class="hide-sm" desc>Now</SortTh>
              <th><span class="sr">Actions</span></th>
            </tr></thead>
            <tbody>
              <tr v-for="r in sort.rows" :key="r.id" :class="!r.enabled && 'off'">
                <td class="on"><Switch :checked="r.enabled" :label="`Enabled: ${text(r)}`" @change="flip(`/api/alerts/rules/${r.id}`, r, $event, a)" /></td>
                <td>{{ text(r) }}</td>
                <td class="ink2"><template v-if="r.server_name">{{ r.server_name }}</template><span v-else-if="r.tag" class="tag">{{ r.tag }}</span><template v-else>All servers</template></td>
                <td class="hide-sm">
                  <span v-if="firingBy(r)" class="chip bad">Firing on {{ firingBy(r) }}</span>
                  <span v-else-if="mountMissing(r, all).length" class="chip warn" :title="mountMissing(r, all).map((s) => s.name).join(', ')">Mount not found</span>
                  <span v-else class="muted small">{{ r.enabled ? 'Quiet' : 'Off' }}</span>
                </td>
                <td class="actions"><div class="row">
                  <Btn size="sm" kind="ghost" icon="edit" title="Edit" @click="rule = r" />
                  <Btn size="sm" kind="ghost" icon="trash" title="Delete" @click="delRule(r)" />
                </div></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section id="channels" class="panel">
        <div class="panel-h"><h2>Notification channels</h2><Btn size="sm" icon="plus" class="right" :disabled="!n.data" @click="channel = {}">Add channel</Btn></div>
        <div v-if="n.error" class="panel-b"><ErrorNote :error="n.error" :retry="n.reload" /></div>
        <template v-else>
          <Empty v-if="!n.data.notifiers.length" title="Nowhere to send alerts yet" icon="bell">Alerts still show up here and in the sidebar. Add ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook to get pushed.</Empty>
          <div v-else class="table-wrap">
            <table class="t">
              <tbody>
                <tr v-for="c in n.data.notifiers" :key="c.id" :class="!c.enabled && 'off'">
                  <td class="on"><Switch :checked="c.enabled" :label="`Enabled: ${c.name}`" @change="flip(`/api/notifiers/${c.id}`, c, $event, n)" /></td>
                  <td class="strong">{{ c.name }}</td>
                  <td class="ink2">{{ n.data.types[c.type]?.label || c.type }}</td>
                  <td class="actions"><div class="row">
                    <Btn size="sm" kind="ghost" @click="testChannel(c)">Send test</Btn>
                    <Btn size="sm" kind="ghost" icon="edit" title="Edit" @click="channel = c" />
                    <Btn size="sm" kind="ghost" icon="trash" title="Delete" @click="delChannel(c)" />
                  </div></td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="panel-f row">
            <span class="ink2">Notify about failed jobs</span>
            <div class="seg" role="group" aria-label="Failed job notifications">
              <button v-for="[k, l] in JOB_MODES" :key="k" type="button" :aria-pressed="n.data.notify_jobs === k" @click="setJobMode(k)">{{ l }}</button>
            </div>
          </div>
        </template>
      </section>

      <QuietPanel />

      <section class="panel">
        <div class="panel-h"><h2>History</h2><span class="sub">Last 100 events</span></div>
        <Empty v-if="!a.data.history.length" title="Nothing has fired yet" icon="check" />
        <div v-else class="table-wrap history">
          <table class="t">
            <tbody>
              <tr v-for="(e, i) in a.data.history" :key="i">
                <td class="nowrap ink2 shrink" :title="when(e.t)">{{ ago(e.t) }}</td>
                <td class="shrink"><span :class="['chip', e.level === 'error' ? 'bad' : 'quiet']">{{ e.level === 'error' ? 'Fired' : 'Resolved' }}</span></td>
                <td>{{ eventText(e) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <RuleDialog v-if="rule" :rule="rule" :metrics="a.data.metrics" :custom="a.data.custom || []" @close="rule = null" @saved="ruleSaved" />
      <ChannelDialog v-if="channel" :channel="channel" :types="n.data.types" @close="channel = null" @saved="channelSaved" />
    </template>
  </div>
</template>

<style scoped>
.t .on, .t .shrink { width: 1%; }
.t td.on { padding-right: 0; }
tr.off td:not(.on, .actions) { opacity: .6; }
.firing td:first-child { box-shadow: inset 3px 0 0 var(--bad); }
.divided { border-bottom: 1px solid var(--line); }
.panel-f { border-radius: 0 0 var(--r-lg) var(--r-lg); }
.history { max-height: 400px; overflow-y: auto; border-radius: 0 0 var(--r-lg) var(--r-lg); }
</style>
