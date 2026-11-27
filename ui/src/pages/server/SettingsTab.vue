<script setup>
// Connection, host key, monitoring, who has access, and removing the server. Owners and up.
import { reactive, ref, computed, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Btn, Loading, ErrorNote, ServerForm, Copy, Callout } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { can, toast, confirmBox, refreshServers } from '@/lib/store.js';
import { muteFor } from '@/lib/actions.js';
import { serverBody, PRIV_TEXT } from '@/lib/servers.js';

const props = defineProps({ s: Object });
const router = useRouter();
const id = props.s.id;
const keys = useLoad(() => api('/api/keys'));
const v = reactive({ name: props.s.name, host: props.s.host, port: props.s.port, user: props.s.user, tags: props.s.tags.join(', '), notes: props.s.notes, key_id: props.s.key_id || '', sudo_password: '' });
const test = ref(null);
const key = computed(() => keys.data?.find((k) => k.id === v.key_id));

async function save() {
  let keyId = v.key_id;
  const fresh = keyId === '__new';
  if (fresh) keyId = (await api('/api/keys', { method: 'POST', body: { name: v.name, generate: true } })).id;
  try {
    await api(`/api/servers/${id}`, { method: 'PUT', body: serverBody({ ...v, key_id: keyId }) });
  } catch (e) {
    if (fresh) await api(`/api/keys/${keyId}`, { method: 'DELETE' }).catch(() => {}); // no orphan key per failed save
    throw e;
  }
  if (fresh) { v.key_id = keyId; keys.reload(true); }
  v.sudo_password = '';
  await refreshServers();
  toast('Server saved', 'ok');
}
const runTest = async () => { test.value = await api(`/api/servers/${id}/test`, { method: 'POST' }); };
async function clearSudo() {
  if (!await confirmBox({ title: 'Remove the saved sudo password?', body: 'Updates and admin actions will fail unless this user has passwordless sudo.', confirm: 'Remove password', danger: true })) return;
  await api(`/api/servers/${id}`, { method: 'PUT', body: { clear_sudo_password: true } });
  await refreshServers();
  toast('Sudo password removed');
}
async function pause(e) {
  const was = props.s.paused;
  e.target.checked = !was; // reflect reality until saved
  try {
    await api(`/api/servers/${id}`, { method: 'PUT', body: { paused: !was } });
    await refreshServers();
    toast(was ? 'Monitoring resumed' : 'Monitoring paused', 'ok');
  } catch (x) { toast(x.message, 'bad'); }
}
const mute = async (h) => { await muteFor(id, h, `Maintenance on ${props.s.name}`); toast(`Notifications for ${props.s.name} muted for ${h} h`, 'ok'); };
async function remove() {
  const name = props.s.name;
  if (!await confirmBox({ title: `Remove ${name}?`, body: 'Fleetdeck forgets this server, its metrics and alert state. Nothing changes on the server itself.', confirm: 'Remove server', danger: true, typeToConfirm: name })) return;
  await api(`/api/servers/${id}`, { method: 'DELETE' });
  await refreshServers();
  router.push('/');
  toast(`${name} removed`);
}

// Who besides the admins sees this server (owners, operators and viewers of the organization).
const m = useLoad(() => api(`/api/servers/${id}/members`));
const sel = ref(null);
watch(() => m.data, (d) => { if (d) sel.value = new Set(d.filter((u) => u.assigned).map((u) => u.id)); }, { immediate: true });
const changed = computed(() => m.data && sel.value && m.data.some((u) => !!u.assigned !== sel.value.has(u.id)));
const flip = (uid) => { const x = new Set(sel.value); x.has(uid) ? x.delete(uid) : x.add(uid); sel.value = x; };
const saveAccess = async () => { await api(`/api/servers/${id}/members`, { method: 'PUT', body: { user_ids: [...sel.value] } }); toast('Access saved', 'ok'); m.reload(true); };
</script>

<template>
  <div class="grid-2">
    <section class="panel">
      <div class="panel-h"><h2>Connection</h2></div>
      <div class="panel-b stack md">
        <Loading v-if="keys.loading" />
        <ServerForm v-else v-model="v" :keys="keys.data || []" :has-sudo="s.has_sudo_password" />
        <div v-if="s.has_sudo_password"><button type="button" class="btn sm ghost" @click="clearSudo">Remove saved sudo password</button></div>
        <div v-if="key" class="field"><span>Public key to authorize on the server</span><Copy :text="key.public_key" /></div>
        <div class="row"><Btn kind="primary" @click="save">Save changes</Btn><Btn icon="term" @click="runTest">Test connection</Btn></div>
        <Callout v-if="test?.ok" kind="ok" :title="`Connected as ${test.user}.`">{{ PRIV_TEXT[test.privilege] }}</Callout>
        <Callout v-else-if="test" kind="bad" title="Couldn't connect.">{{ test.error }}</Callout>
      </div>
    </section>

    <div class="stack">
      <section class="panel">
        <div class="panel-h"><h2>Host key</h2></div>
        <div class="panel-b stack sm">
          <p class="ink2 small">Pinned on the first connection. If it changes, Fleetdeck refuses to connect and raises an alert.</p>
          <p :class="['small wrap-any', !s.host_key && 'muted']">{{ s.host_key || 'Not pinned yet' }}</p>
        </div>
      </section>

      <section class="panel">
        <div class="panel-h"><h2>Monitoring</h2></div>
        <div class="panel-b stack sm">
          <label class="check"><input type="checkbox" role="switch" :checked="!s.paused" @change="pause" />Monitor this server</label>
          <p class="ink2 small">{{ s.paused ? 'Paused: no checks, no alerts. Jobs and the terminal still work.' : 'Checked every minute. Pause during maintenance to silence alerts.' }}</p>
          <template v-if="can('admin')">
            <p class="ink2 small">Planned work? Keep monitoring but hold back notifications for a while. Anything still firing afterwards is sent then.</p>
            <div class="row"><Btn v-for="h in [1, 4]" :key="h" size="sm" icon="mute" @click="mute(h)">Mute for {{ h }} h</Btn><RouterLink class="btn sm ghost" to="/alerts">Quiet hours</RouterLink></div>
          </template>
        </div>
      </section>

      <section class="panel">
        <div class="panel-h"><h2>Access</h2></div>
        <div class="panel-b stack sm">
          <p class="ink2 small">Admins always see every server. Pick who else sees {{ s.name }}; what they can do depends on their role.</p>
          <ErrorNote v-if="m.error" :error="m.error" :retry="m.reload" />
          <Loading v-else-if="!sel" :rows="2" />
          <p v-else-if="!m.data.length" class="small muted">Nobody else is in this organization yet.<template v-if="can('admin')"> Invite people under <RouterLink to="/settings/org/people">Settings → People</RouterLink>.</template></p>
          <template v-else>
            <div class="people">
              <label v-for="u in m.data" :key="u.id" class="check"><input type="checkbox" :checked="sel.has(u.id)" @change="flip(u.id)" />
                <span>{{ u.name || u.email }}<span class="hint">{{ u.role }}{{ u.name ? `, ${u.email}` : '' }}</span></span></label>
            </div>
            <div><Btn :kind="changed ? 'primary' : ''" :disabled="!changed" @click="saveAccess">Save access</Btn></div>
          </template>
        </div>
      </section>

      <section class="panel">
        <div class="panel-h"><h2>Remove server</h2></div>
        <div class="panel-b stack sm">
          <p class="ink2 small">Deletes its history from Fleetdeck. The server itself is not touched.</p>
          <div><Btn icon="trash" @click="remove">Remove {{ s.name }}</Btn></div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.people { display: grid; gap: 10px; padding: 4px 0; }
</style>
