<script setup>
import { ref } from 'vue';
import { Btn, SortTh } from '@/components';
import { api } from '@/lib/api.js';
import { useSort } from '@/lib/use.js';
import { me, refreshMe, switchOrg, toast, confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';
import OrgDialog from './OrgDialog.vue';

const props = defineProps({ list: { type: Array, default: () => [] } });
const emit = defineEmits(['change']);
const sort = useSort(() => props.list, 'name');
const edit = ref(null); // {} for a new one
async function del(t) {
  if (!await confirmBox({ title: `Delete ${t.name}?`, body: `Its ${t.personal_of ? 'servers, ' : ''}scripts, keys, schedules, alert rules and audit log are deleted too.`, confirm: 'Delete organization', danger: true, typeToConfirm: t.slug })) return;
  await api(`/api/instance/tenants/${t.id}`, { method: 'DELETE' });
  toast(`${t.name} deleted`);
  emit('change');
}
function saved() {
  emit('change');
  if (edit.value?.id === me.value.tenant.id) refreshMe();
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>All organizations</h2><span class="count muted">{{ list.length }}</span>
      <Btn size="sm" icon="plus" class="right" @click="edit = {}">New organization</Btn></div>
    <div class="table-wrap"><table class="t">
      <thead><tr>
        <SortTh :sort="sort" k="name">Organization</SortTh><SortTh :sort="sort" k="servers" desc class="num">Servers</SortTh>
        <SortTh :sort="sort" k="members" desc class="num">People</SortTh><SortTh :sort="sort" k="created_at" desc class="hide-sm">Created</SortTh>
        <th><span class="sr">Actions</span></th>
      </tr></thead>
      <tbody>
        <tr v-for="t in sort.rows" :key="t.id">
          <td><span class="strong">{{ t.name }}</span> <span v-if="t.id === me.tenant.id" class="chip">Current</span><span class="sub">{{ t.personal_of ? `Personal, ${t.personal_of}` : t.slug }}</span></td>
          <td class="num">{{ t.servers }}</td>
          <td class="num">{{ t.members }}</td>
          <td class="ink2 nowrap hide-sm" :title="when(t.created_at)">{{ ago(t.created_at) }}</td>
          <td class="actions"><div class="row">
            <Btn v-if="t.id !== me.tenant.id && !me.locked && !t.personal_of" size="sm" kind="ghost" @click="switchOrg(t.id)">Open</Btn>
            <Btn size="sm" kind="ghost" icon="edit" :title="`Rename ${t.name}`" @click="edit = t" />
            <Btn size="sm" kind="ghost" icon="trash" :title="t.servers && !t.personal_of ? 'Remove its servers first' : `Delete ${t.name}`" :disabled="(t.servers > 0 && !t.personal_of) || t.id === me.tenant.id" @click="del(t)" />
          </div></td>
        </tr>
      </tbody>
    </table></div>
    <OrgDialog v-if="edit" :org="edit" @close="edit = null" @saved="saved" />
  </section>
</template>
