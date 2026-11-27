<script setup>
// Every key in every authorized_keys file, grouped by fingerprint: "where can this key still log in?"
import { ref, computed } from 'vue';
import { Btn, Icon, Empty, Callout } from '@/components';
import { api } from '@/lib/api.js';
import { toast, confirmBox } from '@/lib/store.js';

const scan = ref(null);
const busy = ref(false);
const q = ref('');
async function load() {
  busy.value = true;
  try { scan.value = await api('/api/accounts'); } catch (e) { toast(e.message, 'bad'); } finally { busy.value = false; }
}
const groups = computed(() => {
  const s = scan.value;
  if (!s) return [];
  const m = new Map();
  for (const srv of s.servers || []) for (const u of srv.users || []) for (const k of u.keys) {
    if (!m.has(k.fp)) m.set(k.fp, { ...k, comments: new Set(), where: [] });
    const g = m.get(k.fp);
    if (k.comment) g.comments.add(k.comment);
    g.where.push({
      server_id: srv.server_id, server_name: srv.server_name, user: u.name,
      own: s.own[srv.server_id] === k.fp && u.name === srv.login_user,
      maybeOwn: !s.own[srv.server_id] && u.name === srv.login_user, // logs in via agent/~/.ssh: can't tell which key
    });
  }
  const n = q.value.toLowerCase();
  return [...m.values()].map((g) => ({ ...g, comments: [...g.comments], known: s.keys.find((k) => k.fp === g.fp), others: g.where.filter((w) => !w.own) }))
    .filter((g) => !n || `${g.fp} ${g.comments.join(' ')} ${g.known?.name || ''} ${g.where.map((w) => `${w.user}@${w.server_name}`).join(' ')}`.toLowerCase().includes(n))
    .sort((a, b) => b.where.length - a.where.length);
});
const errors = computed(() => (scan.value?.servers || []).filter((s) => s.error));
const typ = (t) => t.replace(/^ssh-/, '');

async function revoke(g, list) {
  const names = list.map((w) => `${w.user}@${w.server_name}`).join(', ');
  const risky = list.some((w) => w.maybeOwn);
  if (!await confirmBox({
    title: `Remove this key from ${list.length === 1 ? names : `${list.length} accounts`}?`, danger: true, confirm: 'Remove key',
    body: `${g.type} ${g.fp}${g.comments.length ? ` (${g.comments.join(', ')})` : ''} is deleted from ${names}.${risky ? ' Careful: Fleetdeck logs in to some of these servers with the ssh-agent or a ~/.ssh key, so it can\'t tell whether this is that key.' : ''}`,
  })) return;
  const failed = [];
  for (const w of list) {
    try { await api(`/api/servers/${w.server_id}/authorized-keys/revoke`, { method: 'POST', body: { user: w.user, blob: g.blob } }); } catch (e) { failed.push(`${w.user}@${w.server_name}: ${e.message}`); }
  }
  if (failed.length) toast(`Not removed from ${failed.join('; ')}`, 'bad'); else toast('Key removed', 'ok');
  await load();
}
</script>
<template>
  <section class="panel">
    <div class="panel-h">
      <div><h2>Authorized on servers</h2><p class="sub">Every key in the users' authorized_keys files, across the fleet.</p></div>
      <div v-if="scan" class="row right">
        <label class="search w-md"><Icon name="search" /><input v-model="q" type="search" placeholder="Fingerprint, comment, user, server" aria-label="Filter keys" /></label>
        <Btn icon="refresh" :disabled="busy" @click="load">{{ busy ? 'Scanning…' : 'Scan again' }}</Btn>
      </div>
    </div>
    <Empty v-if="!scan" title="Find every key that can log in" icon="shield">
      Fleetdeck reads authorized_keys on all active servers (needs sudo) and shows where each key still has access, so old laptops and ex-colleagues' keys stand out.
      <template #action><Btn icon="search" :disabled="busy" @click="load">{{ busy ? 'Scanning…' : 'Scan servers' }}</Btn></template>
    </Empty>
    <template v-else>
      <div v-if="errors.length" class="panel-b"><Callout kind="warn" title="Couldn't read:">{{ errors.map((s) => `${s.server_name} (${s.error})`).join('; ') }}</Callout></div>
      <Empty v-if="!groups.length" :title="q ? 'No keys match' : 'No authorized keys found'" :icon="q ? 'filter' : 'key'" />
      <div v-else class="table-wrap"><table class="t">
        <thead><tr><th>Key</th><th>Can log in as</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr v-for="g in groups" :key="g.fp">
            <td class="key">
              <div class="row nw"><span class="strong trunc">{{ g.known ? g.known.name : g.comments[0] || 'no comment' }}</span><span v-if="g.known" class="tag">Fleetdeck key</span></div>
              <span class="sub wrap-any">{{ typ(g.type) }} {{ g.fp }}</span>
              <span v-if="g.options" class="sub trunc" :title="g.options">{{ g.options }}</span>
            </td>
            <td><div class="where">
              <span v-for="w in g.where" :key="`${w.server_id}/${w.user}`" :class="['loc', w.own && 'own']">
                <RouterLink :to="`/servers/${w.server_id}`">{{ w.user }}@{{ w.server_name }}</RouterLink>
                <span v-if="w.own" class="xs muted" title="Fleetdeck logs in with this key">(Fleetdeck)</span>
                <Btn v-else size="sm" kind="ghost" icon="x" :title="`Remove from ${w.user}@${w.server_name}`" @click="revoke(g, [w])" />
              </span>
            </div></td>
            <td class="actions"><Btn v-if="g.others.length > 1" size="sm" @click="revoke(g, g.others)">Remove everywhere</Btn></td>
          </tr>
        </tbody>
      </table></div>
    </template>
  </section>
</template>
<style scoped>
.key { max-width: 340px; }
.where { display: flex; flex-wrap: wrap; gap: 6px; }
.loc { display: inline-flex; align-items: center; gap: 4px; height: 26px; padding: 0 1px 0 8px; border: 1px solid var(--line); border-radius: 6px; background: var(--panel-2); font-size: var(--fs-sm); white-space: nowrap; }
.loc.own { padding-right: 8px; }
.loc a { color: var(--ink); }
.loc .btn { width: 22px; height: 22px; }
</style>
