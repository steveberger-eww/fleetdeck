<script setup>
// Pending package updates, services to restart after an update, automatic updates and the update history.
import { ref, computed } from 'vue';
import { Btn, Empty, Loading, ErrorNote, SortTh } from '@/components';
import ScheduleRun from './ScheduleRun.vue';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { can, confirmBox, toast } from '@/lib/store.js';
import { when } from '@/lib/format.js';
import { runOn } from './util.js';

const props = defineProps({ s: Object });
const id = props.s.id;
const op = computed(() => can('operator'));
const view = ref('available');
const later = ref(false);

const refresh = ref(0);
const p = useLoad(() => api(`/api/servers/${id}/packages${refresh.value ? '?refresh=1' : ''}`), { key: refresh });
const list = computed(() => p.data?.packages || []);
const secFirst = (x) => (x.security ? 0 : 1);
const sort = useSort(list, 'security', 1, secFirst);
const sel = ref(new Set());
const toggle = (name) => { const x = new Set(sel.value); x.has(name) ? x.delete(name) : x.add(name); sel.value = x; };
const allSel = computed(() => list.value.length > 0 && sel.value.size === list.value.length);
const toggleAll = () => { sel.value = allSel.value ? new Set() : new Set(list.value.map((x) => x.name)); };

// Automatic updates: the systemd timers that install them for unattended-upgrades / dnf-automatic.
const AUTO_TIMERS = ['apt-daily-upgrade.timer', 'dnf-automatic-install.timer', 'dnf-automatic.timer', 'dnf5-automatic.timer'];
const auto = computed(() => props.s.snapshot?.autoUpdates);
const timers = useLoad(() => (auto.value ? api(`/api/servers/${id}/timers`) : null), { key: auto });
const autoNext = computed(() => timers.data?.find((x) => AUTO_TIMERS.includes(x.unit) && x.active === 'active' && x.next)?.next);
async function toggleAuto(e) {
  const on = auto.value;
  e.target.checked = on; // reflect reality until the job has run and the next probe reports it
  try {
    if (on && !await confirmBox({ title: `Turn off automatic updates on ${props.s.name}?`, body: 'Security fixes then only arrive when you install updates yourself or on a schedule.', confirm: 'Turn off', danger: true })) return;
    await runOn(props.s, on ? 'unattended-off' : 'unattended');
  } catch (x) { toast(x.message, 'bad'); }
}

// Services whose binaries or libraries were replaced by an update but are still running the old code.
const r = useLoad(() => api(`/api/servers/${id}/restarts`));
const safe = computed(() => (r.data || []).filter((x) => !x.careful).map((x) => x.unit));
async function restartOne(x) {
  if (x.careful && !await confirmBox({ title: `Restart ${x.unit}?`, body: 'Restarting it can end logins, desktop sessions or networking on this server.', confirm: 'Restart', danger: true })) return;
  await runOn(props.s, 'restart-services', { SERVICES: x.unit });
}

const hist = useLoad(() => (view.value === 'history' ? api(`/api/servers/${id}/packages/history`) : null), { key: view });
const ACTION_WORDS = { install: 'installed', upgrade: 'upgraded', remove: 'removed', downgrade: 'downgraded', reinstall: 'reinstalled' };
const changeSummary = (pkgs) => Object.entries(Object.groupBy(pkgs, (x) => x.action)).map(([a, l]) => `${l.length} ${ACTION_WORDS[a] || a}`).join(', ') || 'No package changes';
</script>

<template>
  <div class="stack">
    <ErrorNote v-if="r.error" :error="`Couldn't check for services to restart: ${r.error}`" :retry="r.reload" />
    <section v-else-if="r.data?.length" class="panel">
      <div class="panel-h">
        <h2>Services to restart</h2><span class="sub">Still running code that an update replaced. Restart them to finish the update without a reboot.</span>
        <Btn v-if="op && safe.length" size="sm" kind="primary" class="right" @click="runOn(s, 'restart-services', { SERVICES: safe.join(' ') })">Restart {{ safe.length === r.data.length ? 'all' : `${safe.length} safe` }}</Btn>
      </div>
      <div class="panel-b row">
        <span v-for="x in r.data" :key="x.unit" class="row nw restart">
          <span :class="['chip', x.careful && 'warn']" :title="x.careful ? 'Can interrupt sessions or networking: restart on its own' : undefined">{{ x.unit.replace(/\.service$/, '') }}</span>
          <Btn v-if="op" size="sm" kind="ghost" icon="refresh" :title="`Restart ${x.unit}`" @click="restartOne(x)" />
        </span>
      </div>
    </section>

    <section class="panel">
      <div class="panel-h">
        <h2 class="sr">Updates</h2>
        <div class="seg" role="group" aria-label="Updates view">
          <button type="button" :aria-pressed="view === 'available'" @click="view = 'available'">Available updates</button>
          <button type="button" :aria-pressed="view === 'history'" @click="view = 'history'">History</button>
        </div>
        <span v-if="p.data" class="sub">{{ p.data.manager || 'unknown package manager' }}</span>
        <div v-if="view === 'available'" class="row right">
          <Btn size="sm" icon="refresh" :disabled="p.loading" @click="refresh++">Refresh package lists</Btn>
          <Btn v-if="op && sel.size" size="sm" @click="runOn(s, 'upgrade-packages', { PACKAGES: [...sel].join(' ') })">Upgrade {{ sel.size }} selected</Btn>
          <Btn v-if="can('admin') && list.length" size="sm" icon="clock" @click="later = true">Later…</Btn>
          <Btn v-if="op && list.length" size="sm" kind="primary" @click="runOn(s, 'update')">Upgrade all {{ list.length }}</Btn>
        </div>
      </div>
      <div v-if="auto != null" class="panel-b row auto">
        <label class="check"><input type="checkbox" role="switch" :checked="auto" :disabled="!op" @change="toggleAuto" />Automatic security updates</label>
        <span class="small muted">{{ auto ? `${s.snapshot.pkg === 'apt' ? 'unattended-upgrades' : 'dnf-automatic'} installs updates by itself${autoNext ? `, next run ${when(autoNext)}` : ''}.` : 'Updates are only installed when you or a schedule run them.' }}</span>
      </div>

      <template v-if="view === 'history'">
        <Loading v-if="hist.loading" />
        <div v-else-if="hist.error" class="panel-b"><ErrorNote :error="hist.error" :retry="hist.reload" /></div>
        <Empty v-else-if="!hist.data?.length" title="No update history" icon="clock">Fleetdeck reads the apt history log or dnf history. Neither has entries on this server.</Empty>
        <div v-else class="table-wrap">
          <table class="t">
            <thead><tr><th>When</th><th class="hide-sm">Source</th><th>Changes</th></tr></thead>
            <tbody>
              <tr v-for="(x, i) in hist.data" :key="i" class="top">
                <td class="nowrap ink2 shrink" title="Server's local time">{{ x.when }}</td>
                <td class="ink2 hide-sm"><span v-if="x.source === 'unattended-upgrades'" class="chip">automatic</span><span v-else class="small">{{ x.source || '–' }}</span></td>
                <td>
                  <details v-if="x.packages.length"><summary>{{ changeSummary(x.packages) }}</summary>
                    <ul class="pkgs"><li v-for="(c, j) in x.packages" :key="j"><span class="ink2">{{ c.action }}</span> {{ c.name }} <span class="muted">{{ c.version }}</span></li></ul>
                  </details>
                  <template v-else>{{ changeSummary(x.packages) }}</template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <Loading v-else-if="p.loading" />
      <div v-else-if="p.error" class="panel-b"><ErrorNote :error="p.error" :retry="p.reload" /></div>
      <Empty v-else-if="!list.length" title="Everything is up to date" icon="check">Fleetdeck checks the package cache every minute. Refresh the package lists to look for new releases now.</Empty>
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <th class="check-cell"><input type="checkbox" aria-label="Select all" :disabled="!op" :checked="allSel" @change="toggleAll" /></th>
            <SortTh :sort="sort" k="security" :get="secFirst" class="shrink"><span class="sr">Security</span></SortTh>
            <SortTh :sort="sort" k="name">Package</SortTh><SortTh :sort="sort" k="from" class="hide-sm">Installed</SortTh>
            <SortTh :sort="sort" k="version">Available</SortTh><SortTh :sort="sort" k="source" class="hide-sm">Source</SortTh>
          </tr></thead>
          <tbody>
            <tr v-for="x in sort.rows" :key="x.name" :class="sel.has(x.name) && 'selected'">
              <td class="check-cell"><input type="checkbox" :disabled="!op" :checked="sel.has(x.name)" :aria-label="`Select ${x.name}`" @change="toggle(x.name)" /></td>
              <td class="shrink"><span v-if="x.security" class="chip warn">security</span></td>
              <td class="strong">{{ x.name }}</td><td class="ink2 hide-sm">{{ x.from || '–' }}</td><td>{{ x.version }}</td><td class="ink2 hide-sm">{{ x.source }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
    <ScheduleRun v-if="later" :s="s" script-id="update" @close="later = false" />
  </div>
</template>

<style scoped>
.restart { gap: 2px; }
.auto { gap: 8px 16px; border-bottom: 1px solid var(--line); }
.shrink { width: 1%; }
.t tr.top td { vertical-align: top; }
.pkgs { list-style: none; margin: 6px 0 0; padding: 0; font-size: var(--fs-sm); display: grid; gap: 2px; }
</style>
