<script setup>
import { ref } from 'vue';
import { Btn, Empty, Loading, ErrorNote } from '@/components';
import VariableDialog from './VariableDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { confirmBox, toast } from '@/lib/store.js';

const vars = useLoad(() => api('/api/variables'));
const edit = ref(null); // {} for a new one
async function del(v) {
  if (!await confirmBox({ title: `Delete ${v.name}?`, confirm: 'Delete variable', danger: true })) return;
  await api(`/api/variables/${v.id}`, { method: 'DELETE' });
  toast('Variable deleted');
  vars.reload(true);
}
const saved = () => { edit.value = null; vars.reload(true); };
</script>
<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Variables</h1><p class="sub">Shared values for scripts and playbooks, stored encrypted.</p></div>
      <div class="actions"><Btn kind="primary" icon="plus" @click="edit = {}">New variable</Btn></div>
    </div>
    <section class="panel">
      <Loading v-if="vars.loading" />
      <div v-else-if="vars.error" class="panel-b"><ErrorNote :error="vars.error" :retry="vars.reload" /></div>
      <Empty v-else-if="!vars.data.length" title="No variables yet" icon="vars">Add a token, password or domain once and use it from any script instead of pasting it into each one. A script only gets the variables you tick in it, as environment variables (Ansible: extra vars).</Empty>
      <div v-else class="table-wrap"><table class="t">
        <thead><tr><th>Name</th><th>Value</th><th class="hide-sm">Used by</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr v-for="v in vars.data" :key="v.id">
            <td><span class="strong">{{ v.name }}</span> <span v-if="v.secret" class="tag">secret</span><span v-if="v.description" class="sub">{{ v.description }}</span></td>
            <td class="small ink2"><span class="val trunc" :title="v.secret ? undefined : v.value">{{ v.secret ? '••••••' : v.value }}</span></td>
            <td class="small ink2 hide-sm">{{ v.used_by.map((s) => s.name).join(', ') || '–' }}</td>
            <td class="actions"><div class="row">
              <Btn size="sm" kind="ghost" icon="edit" :title="`Edit ${v.name}`" @click="edit = v" />
              <Btn size="sm" kind="ghost" icon="trash" :title="v.used_by.length ? 'Used by scripts' : `Delete ${v.name}`" :disabled="v.used_by.length > 0" @click="del(v)" />
            </div></td>
          </tr>
        </tbody>
      </table></div>
    </section>
    <VariableDialog v-if="edit" :variable="edit" @close="edit = null" @saved="saved" />
  </div>
</template>
<style scoped>
.val { display: block; max-width: 220px; }
</style>
