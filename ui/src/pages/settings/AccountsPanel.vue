<script setup>
import { useRouter } from 'vue-router';
import { Btn, SortTh } from '@/components';
import { api } from '@/lib/api.js';
import { useSort } from '@/lib/use.js';
import { refreshMe, toast, confirmBox } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';

const props = defineProps({ list: { type: Array, default: () => [] }, me: [String, Number] }); // me: your account id
const emit = defineEmits(['change']);
const router = useRouter();
const sort = useSort(() => props.list, 'email');
async function setSuper(u, e) {
  const on = !u.super_admin;
  e.target.checked = !!u.super_admin; // the list reload shows the real state
  if (!on && !await confirmBox({ title: `Remove super admin rights from ${u.email}?`, body: u.id === props.me ? 'You lose access to this page and to organizations you are not a member of.' : null, confirm: 'Remove rights', danger: true })) return;
  try { await api(`/api/instance/users/${u.id}`, { method: 'PUT', body: { super_admin: on } }); } catch (x) { toast(x.message, 'bad'); }
  if (u.id === props.me) { await refreshMe(); router.push('/settings'); } else emit('change');
}
async function del(u) {
  if (!await confirmBox({ title: `Delete the account ${u.email}?`, body: 'They are removed from every organization and signed out.', confirm: 'Delete account', danger: true })) return;
  await api(`/api/instance/users/${u.id}`, { method: 'DELETE' });
  toast(`${u.email} deleted`);
  emit('change');
}
</script>
<template>
  <section class="panel">
    <div class="panel-h"><h2>All accounts</h2><span class="count muted">{{ list.length }}</span></div>
    <div class="table-wrap"><table class="t">
      <thead><tr>
        <SortTh :sort="sort" k="email">Account</SortTh><SortTh :sort="sort" k="orgs" class="hide-sm">Organizations</SortTh>
        <SortTh :sort="sort" k="super_admin" desc>Super admin</SortTh><SortTh :sort="sort" k="created_at" desc class="hide-sm">Created</SortTh>
        <th><span class="sr">Actions</span></th>
      </tr></thead>
      <tbody>
        <tr v-for="u in sort.rows" :key="u.id">
          <td><span class="strong">{{ u.name || u.email }}</span> <span v-if="u.id === me" class="chip">You</span><span class="sub">{{ u.email }}</span></td>
          <td class="small ink2 hide-sm">{{ u.orgs || '–' }}</td>
          <td><input type="checkbox" role="switch" :checked="!!u.super_admin" :aria-label="`Super admin: ${u.email}`" @change="setSuper(u, $event)" /></td>
          <td class="ink2 nowrap hide-sm" :title="when(u.created_at)">{{ ago(u.created_at) }}</td>
          <td class="actions"><Btn v-if="u.id !== me" size="sm" kind="ghost" icon="trash" :title="`Delete ${u.email}`" @click="del(u)" /></td>
        </tr>
      </tbody>
    </table></div>
  </section>
</template>
