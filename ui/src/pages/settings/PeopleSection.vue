<script setup>
import { reactive, ref, computed } from 'vue';
import { Btn, Copy, SortTh, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad, useSort } from '@/lib/use.js';
import { me, refreshMe, toast, confirmBox, ROLES, roleName } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';

const ROLE_HELP = { admin: 'Manages everything, sees every server.', owner: 'Adds servers and manages the ones they have.', operator: 'Runs jobs, services and the terminal on their servers.', viewer: 'Only looks.' };
const m = useLoad(() => api('/api/members'));
const inv = reactive({ email: '', role: 'operator' });
const link = ref(null); // { email, url } right after creating an invite
const sort = useSort(() => m.data?.members, 'name', 1, (u) => u.name || u.email);
const org = computed(() => me.value.tenant.name);

async function invite() {
  const r = await api('/api/members/invites', { method: 'POST', body: inv });
  link.value = { email: inv.email, url: r.url };
  inv.email = '';
  m.reload(true);
}
async function setRole(u, e) {
  const el = e.target;
  const role = el.value;
  const self = u.id === m.data.me;
  if (self && !await confirmBox({ title: 'Change your own role?', body: `You become ${roleName(role).toLowerCase()} and lose admin access to this organization.`, confirm: 'Change role', danger: true })) { el.value = u.role; return; }
  try {
    await api(`/api/members/${u.id}`, { method: 'PUT', body: { role } });
    toast(`${u.name || u.email} is now ${roleName(role).toLowerCase()}`, 'ok');
  } catch (x) { el.value = u.role; toast(x.message, 'bad'); }
  m.reload(true);
  if (self) refreshMe();
}
async function remove(u) {
  if (!await confirmBox({ title: `Remove ${u.name || u.email}?`, body: 'They lose access to this organization and its servers. Their account stays.', confirm: 'Remove', danger: true })) return;
  await api(`/api/members/${u.id}`, { method: 'DELETE' });
  toast(`${u.email} removed`);
  m.reload(true);
}
async function withdraw(x) { await api(`/api/members/invites/${x.id}`, { method: 'DELETE' }); m.reload(true); }
</script>
<template>
  <div class="page-h"><div><h1>People</h1><p class="sub">Who can open {{ org }} and what they may do. Admins see every server; everyone else sees the servers assigned to them on the server's Settings tab.</p></div></div>
  <ErrorNote v-if="m.error" :error="m.error" :retry="m.reload" />
  <Loading v-else-if="!m.data" />
  <template v-else>
    <section class="panel">
      <div class="panel-h"><h2>Invite someone</h2></div>
      <div class="panel-b form">
        <div class="row invite">
          <input v-model="inv.email" type="email" class="grow" placeholder="Email address" aria-label="Email address to invite" @keydown.enter="inv.email.trim() && invite()" />
          <select v-model="inv.role" class="auto" aria-label="Role"><option v-for="[k, l] in ROLES" :key="k" :value="k">{{ l }}</option></select>
          <Btn kind="primary" icon="plus" :disabled="!inv.email.trim()" @click="invite">Invite</Btn>
        </div>
        <p class="small ink2"><b>{{ roleName(inv.role) }}:</b> {{ ROLE_HELP[inv.role] }}</p>
        <div v-if="link" class="field"><span>Send this link to {{ link.email }}. It works once.</span><Copy :text="link.url" /></div>
      </div>
    </section>

    <section v-if="m.data.invites.length" class="panel">
      <div class="panel-h"><h2>Pending invites</h2><span class="count muted">{{ m.data.invites.length }}</span></div>
      <div class="table-wrap"><table class="t"><tbody>
        <tr v-for="x in m.data.invites" :key="x.id">
          <td>{{ x.email }}<span class="sub">by {{ x.created_by }}</span></td>
          <td class="ink2">{{ roleName(x.role) }}</td>
          <td class="ink2 nowrap hide-sm">expires {{ when(x.expires_at) }}</td>
          <td class="actions"><Btn size="sm" @click="withdraw(x)">Withdraw</Btn></td>
        </tr>
      </tbody></table></div>
    </section>

    <section class="panel">
      <div class="panel-h"><h2>Members</h2><span class="count muted">{{ m.data.members.length }}</span></div>
      <div class="table-wrap"><table class="t">
        <thead><tr>
          <SortTh :sort="sort" k="name" :get="(u) => u.name || u.email">Person</SortTh><SortTh :sort="sort" k="role">Role</SortTh>
          <SortTh :sort="sort" k="servers" desc class="num">Servers</SortTh><th class="hide-sm">Sign-in</th>
          <SortTh :sort="sort" k="created_at" desc class="hide-sm">Joined</SortTh><th><span class="sr">Actions</span></th>
        </tr></thead>
        <tbody>
          <tr v-for="u in sort.rows" :key="u.id">
            <td><span class="strong">{{ u.name || u.email }}</span> <span v-if="u.id === m.data.me" class="chip">You</span><span class="sub">{{ u.email }}</span></td>
            <td><select :value="u.role" class="auto sm" :aria-label="`Role of ${u.email}`" @change="setRole(u, $event)"><option v-for="[k, l] in ROLES" :key="k" :value="k">{{ l }}</option></select></td>
            <td class="num ink2">{{ u.role === 'admin' ? 'All' : u.servers }}</td>
            <td class="hide-sm"><div class="row nw"><span v-if="u.sso" class="tag">SSO</span><span v-if="u.totp" class="tag">2FA</span></div></td>
            <td class="ink2 nowrap hide-sm" :title="when(u.created_at)">{{ ago(u.created_at) }}</td>
            <td class="actions"><Btn v-if="u.id !== m.data.me" size="sm" kind="ghost" icon="trash" :title="`Remove ${u.email}`" @click="remove(u)" /></td>
          </tr>
        </tbody>
      </table></div>
    </section>
  </template>
</template>
<style scoped>
.invite { flex-wrap: nowrap; }
@media (max-width: 560px) { .invite { flex-wrap: wrap; } .invite select { flex: 1; } }
</style>
