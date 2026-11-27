<script setup>
// systemd services, plus timers (the cron jobs of modern distros). A row opens the unit's status.
import { ref, computed } from 'vue';
import { Btn, Icon, Menu, Modal, Empty, Loading, ErrorNote, SortTh, Output } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useQuery, useSort } from '@/lib/use.js';
import { can, toast, confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';
import { attempt, logsTo } from './util.js';

const props = defineProps({ s: Object });
const id = props.s.id;
const op = computed(() => can('operator'));
const svc = useLoad(() => api(`/api/servers/${id}/services`));
const failedCount = computed(() => (svc.data || []).filter((u) => u.active === 'failed').length);
// Open on "Failed" when something failed; otherwise "Running".
const show = useQuery('show');
const filter = computed(() => show.value || (failedCount.value ? 'failed' : 'running'));
const q = useQuery('q');
const match = (x) => !q.value || `${x.unit} ${x.description}`.toLowerCase().includes(q.value.toLowerCase());
const STATE = { running: 'active', failed: 'failed', inactive: 'inactive' };
const list = computed(() => (svc.data || []).filter((u) => (!STATE[filter.value] || u.active === STATE[filter.value]) && match(u)));
const sort = useSort(list, 'unit');
const FILTERS = computed(() => [['running', 'Running'], ['failed', `Failed${failedCount.value ? ` (${failedCount.value})` : ''}`], ['inactive', 'Stopped'], ['all', 'All'], ['timers', 'Timers']]);

const cap = (w) => w[0].toUpperCase() + w.slice(1);
const name = (u) => u.replace(/\.(service|timer)$/, '');
const post = (unit, action) => api(`/api/servers/${id}/services`, { method: 'POST', body: { unit, action } });
async function act(u, action) {
  if (['stop', 'disable'].includes(action) && !await confirmBox({ title: `${cap(action)} ${u.unit}?`, confirm: cap(action), danger: true })) return false;
  await post(u.unit, action);
  toast(`${u.unit}: ${action} done`, 'ok');
  svc.reload(true);
  return true;
}
const menuAct = (u, action) => attempt(() => act(u, action))();

const status = ref(null); // { u, text }
const showStatus = async (u) => { status.value = { u, text: (await api(`/api/servers/${id}/services/status?unit=${encodeURIComponent(u.unit)}`)).text }; };
const openRow = (e, u) => { if (!e.target.closest('button, a')) attempt(() => showStatus(u))(); };
const fromModal = async (action) => {
  const u = status.value.u;
  if (await act(u, action)) await showStatus({ ...u, active: action === 'stop' ? 'inactive' : 'active', sub: 'running' });
};

// ---------- timers ----------
const t = useLoad(() => (filter.value === 'timers' ? api(`/api/servers/${id}/timers`) : null), { key: () => filter.value === 'timers' });
const timers = computed(() => (t.data || []).filter(match));
const tsort = useSort(timers, 'next');
const runNow = async (x) => { await post(x.activates, 'start'); toast(`${x.activates} started`, 'ok'); t.reload(true); };
async function toggleTimer(x) {
  const on = x.enabled !== 'enabled';
  if (!on && !await confirmBox({ title: `Disable ${x.unit}?`, body: `${x.activates} stops running on its schedule.`, confirm: 'Disable', danger: true })) return;
  for (const a of on ? ['enable', 'start'] : ['stop', 'disable']) await post(x.unit, a);
  toast(`${x.unit} ${on ? 'enabled' : 'disabled'}`, 'ok');
  t.reload(true);
}
const timerMenu = (x) => attempt(() => toggleTimer(x))();
</script>

<template>
  <section class="panel">
    <div class="panel-h">
      <div class="seg" role="group" aria-label="Filter">
        <button v-for="[k, l] in FILTERS" :key="k" type="button" :aria-pressed="filter === k" @click="show = k">{{ l }}</button>
      </div>
      <label class="search w-sm"><Icon name="search" /><input v-model="q" type="search" :placeholder="filter === 'timers' ? 'Filter timers' : 'Filter services'" aria-label="Filter services" /></label>
      <Btn size="sm" kind="ghost" icon="refresh" class="right" @click="filter === 'timers' ? t.reload() : svc.reload()">Refresh</Btn>
    </div>

    <template v-if="filter === 'timers'">
      <Loading v-if="t.loading && !t.data" />
      <div v-else-if="t.error" class="panel-b"><ErrorNote :error="t.error" :retry="t.reload" /></div>
      <Empty v-else-if="!timers.length" title="No timers match" icon="clock" />
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <SortTh :sort="tsort" k="unit">Timer</SortTh><SortTh :sort="tsort" k="next">Next run</SortTh><SortTh :sort="tsort" k="last" desc class="hide-sm">Last run</SortTh>
            <SortTh :sort="tsort" k="result">Last result</SortTh><SortTh :sort="tsort" k="enabled" class="hide-sm">Start at boot</SortTh><th><span class="sr">Actions</span></th>
          </tr></thead>
          <tbody>
            <tr v-for="x in tsort.rows" :key="x.unit">
              <td><span class="strong">{{ name(x.unit) }}</span><span class="sub trunc desc">{{ x.description }}</span></td>
              <td class="nowrap">
                <template v-if="x.active === 'active'"><span v-if="x.next" :title="when(x.next)">{{ ago(x.next) }}</span><span v-else class="muted" :title="x.nextText">{{ x.nextText && x.nextText !== 'n/a' ? x.nextText : '–' }}</span></template>
                <span v-else class="muted">inactive</span>
              </td>
              <td class="nowrap ink2 hide-sm"><span v-if="x.last" :title="when(x.last)">{{ ago(x.last) }}</span><span v-else class="muted" :title="x.lastText">{{ x.lastText && x.lastText !== 'n/a' ? x.lastText : '–' }}</span></td>
              <td><span v-if="x.running" class="chip info">running</span><span v-else-if="x.result && x.last" :class="['chip', x.result === 'success' ? 'ok' : 'bad']">{{ x.result }}</span></td>
              <td class="ink2 hide-sm">{{ x.enabled }}</td>
              <td class="actions"><div class="row">
                <Btn v-if="op" size="sm" icon="play" :disabled="!x.activates || x.running" @click="runNow(x)">Run now</Btn>
                <Menu v-if="x.activates || (op && ['enabled', 'disabled'].includes(x.enabled))" size="sm" :label="`More actions for ${x.unit}`">
                  <RouterLink v-if="x.activates" class="mi" :to="logsTo(s, x.activates)"><Icon name="audit" />Logs</RouterLink>
                  <button v-if="op && ['enabled', 'disabled'].includes(x.enabled)" type="button" class="mi" @click="timerMenu(x)"><Icon name="power" />{{ x.enabled === 'enabled' ? 'Disable' : 'Enable' }}</button>
                </Menu>
              </div></td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <Loading v-else-if="svc.loading && !svc.data" />
    <div v-else-if="svc.error" class="panel-b"><ErrorNote :error="svc.error" :retry="svc.reload" /></div>
    <Empty v-else-if="!list.length" title="No services match" icon="filter" />
    <div v-else class="table-wrap">
      <table class="t">
        <thead><tr>
          <SortTh :sort="sort" k="active">State</SortTh><SortTh :sort="sort" k="unit">Service</SortTh><SortTh :sort="sort" k="description" class="hide-sm">Description</SortTh>
          <SortTh :sort="sort" k="enabled" class="hide-sm">Start at boot</SortTh><th><span class="sr">Actions</span></th>
        </tr></thead>
        <tbody>
          <tr v-for="u in sort.rows" :key="u.unit" class="click" @click="openRow($event, u)">
            <td><span :class="['chip', u.active === 'active' ? 'ok' : u.active === 'failed' ? 'bad' : '']">{{ u.active === 'active' ? u.sub : u.active }}</span></td>
            <td><button type="button" class="cellbtn" title="Show status" @click="attempt(() => showStatus(u))()">{{ name(u.unit) }}</button></td>
            <td class="ink2 hide-sm"><span class="trunc desc">{{ u.description }}</span></td>
            <td class="ink2 hide-sm">{{ u.enabled }}</td>
            <td class="actions">
              <div v-if="op" class="row">
                <Btn v-if="u.active === 'active'" size="sm" @click="act(u, 'restart')">Restart</Btn>
                <Btn v-else size="sm" @click="act(u, 'start')">Start</Btn>
                <Menu size="sm" :label="`More actions for ${u.unit}`">
                  <button v-if="u.active === 'active'" type="button" class="mi" @click="menuAct(u, 'stop')"><Icon name="stop" />Stop</button>
                  <button v-if="u.enabled === 'enabled'" type="button" class="mi" @click="menuAct(u, 'disable')"><Icon name="power" />Disable</button>
                  <button v-else-if="u.enabled === 'disabled'" type="button" class="mi" @click="menuAct(u, 'enable')"><Icon name="power" />Enable</button>
                  <RouterLink class="mi" :to="logsTo(s, u.unit)"><Icon name="audit" />Logs</RouterLink>
                </Menu>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal v-if="status" wide :title="status.u.unit" @close="status = null">
      <Output :name="status.u.unit" :text="status.text" />
      <template #footer>
        <RouterLink class="btn ghost" :to="logsTo(s, status.u.unit)">Open logs</RouterLink>
        <span class="grow" />
        <template v-if="op">
          <template v-if="status.u.active === 'active'">
            <Btn @click="fromModal('stop')">Stop</Btn><Btn kind="primary" @click="fromModal('restart')">Restart</Btn>
          </template>
          <Btn v-else kind="primary" @click="fromModal('start')">Start</Btn>
        </template>
      </template>
    </Modal>
  </section>
</template>

<style scoped>
.desc { display: block; max-width: 360px; }
.cellbtn { all: unset; cursor: pointer; font-weight: 500; color: var(--ink); border-radius: 4px; }
.cellbtn:hover { color: var(--accent); }
.cellbtn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
</style>
