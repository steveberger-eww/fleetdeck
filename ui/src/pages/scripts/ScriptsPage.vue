<script setup>
import { ref } from 'vue';
import { Btn, SortTh, Empty, Loading, ErrorNote } from '@/components';
import ScriptDialog from './ScriptDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { can, isSuper, openRun, toast, confirmBox } from '@/lib/store.js';
import { NEXT_WHEN } from './util.js';

const scripts = useLoad(() => api('/api/scripts'));
const sort = useSort(() => scripts.data);
const edit = ref(null);
const admin = can('admin');
// Shared-library scripts (built-ins included) are read-only for organizations; super admins edit them.
const editable = (s) => admin && (!s.shared || isSuper());
const nextName = (s) => scripts.data.find((x) => x.id === s.next_script_id)?.name || 'another script';
const vars = (s) => [...s.params.map((p) => p.name), ...s.vars].map((n) => (s.kind === 'ansible' ? n : `$${n}`)).join(' ');

async function del(s) {
  if (!await confirmBox({ title: `Delete "${s.name}"?`, body: 'Schedules that use it are deleted too, and scripts that run it next stop before it. Past jobs keep their output.', confirm: 'Delete script', danger: true })) return;
  await api(`/api/scripts/${s.id}`, { method: 'DELETE' });
  toast('Script deleted');
  scripts.reload(true);
}
async function copy(s) {
  const c = await api(`/api/scripts/${s.id}/copy`, { method: 'POST' });
  toast(`Copied as "${c.name}"`, 'ok');
  scripts.reload(true);
  edit.value = c;
}
const saved = () => { edit.value = null; scripts.reload(true); };
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Scripts</h1><p class="sub">Shell scripts and Ansible playbooks you can run on any server.</p></div>
      <div v-if="admin" class="actions"><Btn kind="primary" icon="plus" @click="edit = {}">New script</Btn></div>
    </div>

    <section class="panel">
      <Loading v-if="scripts.loading" />
      <div v-else-if="scripts.error" class="panel-b"><ErrorNote :error="scripts.error" :retry="scripts.reload" /></div>
      <Empty v-else-if="!scripts.data.length" title="No scripts yet" icon="scripts">Shell scripts and Ansible playbooks you can run on any server.</Empty>
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <SortTh :sort="sort" k="name">Script</SortTh>
            <SortTh :sort="sort" k="params" :get="(s) => s.params.length" class="hide-sm" desc>Parameters</SortTh>
            <th><span class="sr">Actions</span></th>
          </tr></thead>
          <tbody>
            <tr v-for="s in sort.rows" :key="s.id">
              <td class="script">
                <div class="row nw">
                  <span class="strong">{{ s.name }}</span>
                  <span v-if="!s.builtin && s.shared" class="tag" title="From the shared library of this Fleetdeck">Shared</span>
                  <span v-if="!s.as_root" class="tag" title="Runs as the SSH user, without sudo">{{ s.kind === 'ansible' ? 'No become' : 'SSH user' }}</span>
                  <span v-if="s.kind === 'ansible'" class="tag">Ansible</span>
                </div>
                <span v-if="s.description || s.kind === 'ansible'" class="sub"><template v-if="s.kind === 'ansible'">{{ s.playbook }}<template v-if="s.description"> · </template></template>{{ s.description }}</span>
                <span v-if="s.next_script_id" class="sub ink2">Then runs {{ nextName(s) }} {{ NEXT_WHEN[s.next_when] }}</span>
              </td>
              <td class="small ink2 hide-sm">{{ vars(s) || '–' }}</td>
              <td class="actions">
                <div class="row">
                  <Btn v-if="can('operator')" size="sm" icon="play" @click="openRun({ scriptId: s.id })">Run</Btn>
                  <Btn v-if="admin && s.shared" size="sm" kind="ghost" icon="copy" title="Make an editable copy for this organization" @click="copy(s)">Copy</Btn>
                  <Btn v-if="editable(s)" size="sm" kind="ghost" icon="edit" title="Edit" @click="edit = s" />
                  <Btn v-if="editable(s) && !s.builtin" size="sm" kind="ghost" icon="trash" title="Delete" @click="del(s)" />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <ScriptDialog v-if="edit" :key="edit.id || 'new'" :script="edit" @close="edit = null" @saved="saved" />
  </div>
</template>

<style scoped>
.script { min-width: 240px; }
.script .sub { margin-top: 2px; }
</style>
