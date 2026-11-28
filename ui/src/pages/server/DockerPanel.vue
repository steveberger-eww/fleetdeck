<script setup>
// Docker containers with live stats, grouped by Compose project. Polls every 15 s, held while you point at the table.
// Image updates are checked once on open (the server caches registry answers for hours); stacks' files can be edited.
import { ref, computed } from 'vue';
import { Btn, Icon, Menu, Modal, Empty, Loading, ErrorNote, CellBar, SortTh } from '@/components';
import ContainerLogs from './ContainerLogs.vue';
import ContainerHistory from './ContainerHistory.vue';
import ContainerDetails from './ContainerDetails.vue';
import ComposeDialog from './ComposeDialog.vue';
import { STEPS, behind, label } from './updates.js';
import { api } from '@/lib/api.js';
import { useLoad, useQuery, useSort, useHold } from '@/lib/use.js';
import { can, toast, confirmBox } from '@/lib/store.js';
import { bytes } from '@/lib/format.js';
import { runOn, attempt } from './util.js';

const props = defineProps({ s: Object });
const id = props.s.id;
const op = computed(() => can('operator'));
const [held, bind] = useHold();
const c = useLoad(() => api(`/api/servers/${id}/containers`), { poll: 15_000, paused: held });
const all = useQuery('ctr');
const logs = ref(null);
const hist = ref(null);
const info = ref(null);
const compose = ref(null); // { g, bump }
const list = computed(() => c.data?.containers || []);
const sort = useSort(list, 'name');
const cores = computed(() => props.s.snapshot?.cores || 1);

// Stopped cleanly (exit code 0) or never started: hidden in "Active". Crashed ones stay visible.
const quiet = (x) => x.state === 'created' || (x.state === 'exited' && /^Exited \(0\)/.test(x.status));
const hidden = computed(() => list.value.filter(quiet).length);
const shown = computed(() => (all.value ? sort.rows : sort.rows.filter((x) => !quiet(x))));
// Compose stacks in name order, then the standalone containers. No header rows when nothing runs in a stack.
const rows = computed(() => {
  if (!list.value.some((x) => x.project)) return shown.value.map((x) => ({ x }));
  const groups = [];
  for (const x of list.value) {
    let g = groups.find((y) => y.name === x.project);
    if (!g) groups.push(g = { name: x.project, dir: x.dir, files: x.files, all: [] });
    g.all.push(x);
  }
  groups.sort((a, b) => (!a.name) - (!b.name) || a.name.localeCompare(b.name));
  return groups.flatMap((g) => {
    const r = shown.value.filter((x) => x.project === g.name);
    return r.length ? [{ g, up: g.all.filter((x) => x.state === 'running').length }, ...r.map((x) => ({ x }))] : [];
  });
});

const post = (x, action) => api(`/api/servers/${id}/containers`, { method: 'POST', body: { id: x.id, action } });
const act = (x, action) => attempt(async () => {
  if (['stop', 'rm'].includes(action) && !await confirmBox({ title: `${action === 'rm' ? 'Remove' : 'Stop'} ${x.name}?`, body: action === 'rm' ? 'The container is deleted. Its volumes are kept.' : null, confirm: action === 'rm' ? 'Remove' : 'Stop', danger: true })) return;
  await post(x, action);
  toast(`${x.name}: ${action === 'rm' ? 'removed' : `${action} done`}`, 'ok');
  c.reload(true);
})();
async function restartStack(g) {
  const up = g.all.filter((x) => x.state === 'running');
  if (!await confirmBox({ title: `Restart ${g.name}?`, body: `Restarts its ${up.length} running containers.`, confirm: 'Restart' })) return;
  await Promise.all(up.map((x) => post(x, 'restart')));
  toast(`${g.name}: restarted`, 'ok');
  c.reload(true);
}
// Image updates: one request, not polled. "Check again" skips the server's cache of registry answers.
const upd = useLoad(() => api(`/api/servers/${id}/containers/updates`));
const checkNow = async () => { upd.data = await api(`/api/servers/${id}/containers/updates?refresh=1`); upd.error = null; };
const updOf = computed(() => new Map((upd.data || []).map((u) => [u.id, u])));
const pending = computed(() => list.value.filter((x) => behind(updOf.value.get(x.id))).length);
const stackOf = (x) => ({ name: x.project, dir: x.dir, files: x.files });
const editTo = (x, to) => { info.value = null; compose.value = { g: stackOf(x), bump: { from: updOf.value.get(x.id).ref, to } }; };
const recreateOf = (x) => { info.value = null; attempt(() => recreate(stackOf(x)))(); };

async function recreate(g) {
  if (!await confirmBox({ title: `Pull and recreate ${g.name}?`, body: `Runs "docker compose pull" and "docker compose up -d" in ${g.dir}. Containers whose image changed restart.`, confirm: 'Pull and recreate' })) return;
  await runOn(props.s, 'compose-update', { PROJECT: g.name, DIR: g.dir, FILES: g.files });
}

// What "Clean up unused data" could free. docker system df is slow on big hosts: loaded once, not polled.
const df = useLoad(() => api(`/api/servers/${id}/containers/df`));
const reclaim = computed(() => {
  if (!df.data) return null;
  const total = df.data.filter((d) => d.type !== 'Local Volumes').reduce((a, d) => a + d.bytes, 0); // the cleanup keeps volumes
  const title = [...df.data.map((d) => `${d.type}: ${d.reclaimable} of ${d.size} unused${d.type === 'Local Volumes' ? ' (the cleanup keeps volumes)' : ''}`),
    'Images count every unused image; the cleanup removes untagged ones only.'].join('\n');
  return { text: total ? `Up to ${bytes(total)} reclaimable` : 'Nothing to reclaim', title };
});
const health = (x) => (/\(unhealthy\)/.test(x.status) ? 'unhealthy' : /\(healthy\)/.test(x.status) ? 'healthy' : '');
</script>

<template>
  <section v-if="c.loading && !c.data" class="panel"><Loading /></section>
  <ErrorNote v-else-if="c.error" :error="c.error" :retry="c.reload" />
  <section v-else-if="!c.data.installed" class="panel"><Empty title="Docker isn't installed on this server" icon="box">Install Docker and its containers show up here with live stats.</Empty></section>
  <section v-else class="panel">
    <div class="panel-h">
      <h2>Containers</h2><span class="sub">{{ list.filter((x) => x.state === 'running').length }} of {{ list.length }} running{{ upd.loading ? ' · checking for updates…' : pending ? ` · ${pending} with updates` : '' }}{{ held ? ' · paused while you point at the table' : '' }}</span>
      <div v-if="hidden" class="seg" role="group" aria-label="Show">
        <button type="button" :aria-pressed="!all" title="Hides containers that stopped cleanly (exit code 0) or never started" @click="all = ''">Active ({{ list.length - hidden }})</button>
        <button type="button" :aria-pressed="!!all" @click="all = 'all'">All ({{ list.length }})</button>
      </div>
      <div class="row nw right">
        <span v-if="reclaim" class="small muted nowrap hide-sm" :title="reclaim.title">{{ reclaim.text }}</span>
        <Btn v-if="op" size="sm" kind="ghost" icon="refresh" :disabled="upd.loading" title="Ask the registries again. Answers are otherwise kept for 6 hours." @click="checkNow">Check for updates</Btn>
        <Btn v-if="op" size="sm" @click="runOn(s, 'docker-prune')">Clean up unused data</Btn>
      </div>
    </div>
    <Empty v-if="!list.length" title="No containers" icon="box" />
    <Empty v-else-if="!shown.length" title="No active containers" icon="box">{{ hidden }} stopped cleanly. Show all to see them.</Empty>
    <div v-else class="table-wrap" v-bind="bind">
      <table class="t">
        <thead><tr>
          <SortTh :sort="sort" k="state">State</SortTh><SortTh :sort="sort" k="name">Name</SortTh><SortTh :sort="sort" k="image" class="hide-sm">Image</SortTh>
          <SortTh :sort="sort" k="status" class="hide-sm">Status</SortTh><SortTh :sort="sort" k="cpu" :get="(x) => parseFloat(x.cpu)" class="num" desc>CPU</SortTh>
          <SortTh :sort="sort" k="mem" :get="(x) => parseFloat(x.memPct)" class="num" desc>Memory</SortTh><th class="num hide-md">Net I/O</th><th class="num hide-md">Disk I/O</th>
          <SortTh :sort="sort" k="ports" class="hide-md">Ports</SortTh><th><span class="sr">Actions</span></th>
        </tr></thead>
        <tbody>
          <template v-for="r in rows" :key="r.g ? `g:${r.g.name}` : r.x.id">
            <tr v-if="r.g" class="grp">
              <td colspan="10"><div class="row nw">
                <b>{{ r.g.name || 'Standalone' }}</b><span class="small muted nowrap">{{ r.up }} of {{ r.g.all.length }} running</span>
                <span v-if="r.g.dir" class="small muted trunc hide-sm" :title="r.g.files || r.g.dir">{{ r.g.dir }}</span>
                <div v-if="op && r.g.name" class="row nw right">
                  <Btn size="sm" :disabled="!r.g.files" :title="r.g.files ? 'Edit the Compose files' : 'The containers carry no Compose files'" @click="compose = { g: r.g }">Edit</Btn>
                  <Btn v-if="r.up" size="sm" @click="restartStack(r.g)">Restart</Btn>
                  <Btn size="sm" :disabled="!r.g.dir" :title="r.g.dir ? 'docker compose pull, then up -d' : 'The containers carry no Compose directory'" @click="recreate(r.g)">Pull and recreate</Btn>
                </div>
              </div></td>
            </tr>
            <tr v-else>
              <td><div class="row nw">
                <span :class="['chip', r.x.state === 'running' ? 'ok' : ['exited', 'dead'].includes(r.x.state) ? '' : 'warn']">{{ r.x.state }}</span>
                <span v-if="health(r.x)" :class="['chip', health(r.x) === 'unhealthy' ? 'bad' : 'ok']">{{ health(r.x) }}</span>
              </div></td>
              <td><div class="row nw">
                <button type="button" class="cellbtn" title="Show details" @click="info = r.x">{{ r.x.name }}</button>
                <button v-if="behind(updOf.get(r.x.id))" type="button" :class="['chip', 'chipbtn', STEPS[updOf.get(r.x.id).step].kind]" title="See the update" @click="info = r.x">{{ label(updOf.get(r.x.id)) }}</button>
              </div></td>
              <td class="ink2 hide-sm"><span class="trunc cell" :title="r.x.image">{{ r.x.image }}</span></td>
              <td class="ink2 nowrap hide-sm">{{ r.x.status }}</td>
              <td class="num"><CellBar v-if="r.x.cpu" :value="parseFloat(r.x.cpu) / 100 / cores">{{ r.x.cpu }}</CellBar><template v-else>–</template></td>
              <td class="num nowrap"><CellBar v-if="r.x.memPct" :value="parseFloat(r.x.memPct) / 100"><span :title="r.x.mem">{{ r.x.memPct }}</span></CellBar><span v-else class="ink2">{{ r.x.mem }}</span></td>
              <td class="num small ink2 nowrap hide-md" title="Received / sent since the container started">{{ r.x.net || '–' }}</td>
              <td class="num small ink2 nowrap hide-md" title="Read / written since the container started">{{ r.x.block || '–' }}</td>
              <td class="small ink2 hide-md"><span class="trunc cell" :title="r.x.ports">{{ r.x.ports }}</span></td>
              <td class="actions"><div class="row">
                <Btn size="sm" @click="logs = r.x">Logs</Btn>
                <Menu size="sm" :label="`More actions for ${r.x.name}`">
                  <button type="button" class="mi" @click="info = r.x"><Icon name="info" />Details</button>
                  <button type="button" class="mi" @click="hist = r.x.name"><Icon name="clock" />History</button>
                  <template v-if="op">
                    <hr>
                    <template v-if="r.x.state === 'running'">
                      <button type="button" class="mi" @click="act(r.x, 'restart')"><Icon name="refresh" />Restart</button>
                      <button type="button" class="mi" @click="act(r.x, 'stop')"><Icon name="stop" />Stop</button>
                    </template>
                    <template v-else>
                      <button type="button" class="mi" @click="act(r.x, 'start')"><Icon name="play" />Start</button>
                      <button type="button" class="mi danger" @click="act(r.x, 'rm')"><Icon name="trash" />Remove…</button>
                    </template>
                  </template>
                </Menu>
              </div></td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
    <Modal v-if="logs" wide :title="`Logs: ${logs.name}`" @close="logs = null"><ContainerLogs :s="s" :x="logs" /></Modal>
    <Modal v-if="hist" wide :title="`History: ${hist}`" @close="hist = null"><ContainerHistory :s="s" :name="hist" /></Modal>
    <Modal v-if="info" wide :title="info.name" @close="info = null">
      <ContainerDetails :s="s" :x="info" :u="updOf.get(info.id)" :checking="upd.loading" :update-error="upd.error" @edit="(to) => editTo(info, to)" @recreate="recreateOf(info)" />
    </Modal>
    <ComposeDialog v-if="compose" :s="s" :g="compose.g" :bump="compose.bump" @close="compose = null" />
  </section>
</template>

<style scoped>
.t tr.grp td, .t tr.grp:hover td { background: var(--panel-2); padding-top: 6px; padding-bottom: 6px; }
.cell { display: block; max-width: 220px; }
.cellbtn { all: unset; cursor: pointer; font-weight: 500; color: var(--ink); border-radius: 4px; }
.cellbtn:hover { color: var(--accent); }
.cellbtn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.chipbtn { cursor: pointer; font-family: inherit; }
</style>
