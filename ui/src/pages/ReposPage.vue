<script setup>
import { ref, computed } from 'vue';
import { Btn, Empty, Loading, ErrorNote, Callout } from '@/components';
import RepoDialog from './RepoDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { confirmBox, toast } from '@/lib/store.js';
import { ago, when } from '@/lib/format.js';

const ACCESS = { none: 'Public, or the ssh-agent of the Fleetdeck host', ssh: 'SSH key' };
const repos = useLoad(() => api('/api/repos'), { poll: 15_000 });
const tools = useLoad(() => api('/api/tools'));
const t = computed(() => tools.data);
const missing = computed(() => (t.value ? [!t.value.git && 'git', !t.value.ansible && 'ansible-playbook'].filter(Boolean) : []));
const edit = ref(null); // {} for a new one

async function sync(r) { await api(`/api/repos/${r.id}/sync`, { method: 'POST' }); toast(`${r.name} synced`, 'ok'); repos.reload(true); }
async function trustKey(r) {
  if (!await confirmBox({ title: 'Trust the Git server\'s new host key?', body: 'Only do this if you know why the key changed (server reinstall, new Git host). A changed key can also mean someone is intercepting the connection.', confirm: 'Trust new key', danger: true })) return;
  await api(`/api/repos/${r.id}/forget-hostkey`, { method: 'POST' });
  toast('New host key trusted and repository synced', 'ok');
  repos.reload(true);
}
async function del(r) {
  if (!await confirmBox({ title: `Delete repository "${r.name}"?`, body: 'Removes the local checkout. The remote repository is not touched.', confirm: 'Delete repository', danger: true })) return;
  await api(`/api/repos/${r.id}`, { method: 'DELETE' });
  repos.reload(true);
}
const commit = (c) => { const [hash, ...msg] = c.split(' '); return [hash, msg.join(' ')]; };
const saved = () => { edit.value = null; repos.reload(true); };
</script>
<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Repositories</h1><p class="sub">Git repositories with Ansible playbooks. Fleetdeck pulls the branch before every run.</p></div>
      <div class="actions"><Btn kind="primary" icon="plus" @click="edit = {}">Add repository</Btn></div>
    </div>

    <Callout v-if="missing.length && repos.data?.length" kind="warn">
      <template v-if="!t.git"><b>git</b> is not installed on the Fleetdeck host. </template>
      <template v-if="!t.ansible"><b>ansible-playbook</b> is not installed on the Fleetdeck host, so playbooks can't run. Install it with <code>pipx install ansible-core</code> or your package manager (the Docker image includes it).</template>
    </Callout>

    <Loading v-if="repos.loading" />
    <ErrorNote v-else-if="repos.error" :error="repos.error" :retry="repos.reload" />
    <section v-else-if="!repos.data.length" class="panel">
      <Empty title="No repositories yet" icon="git">
        Connect a Git repository with your playbooks, then create an Ansible script that points at a playbook in it.
        <template v-if="missing.length"><br><br><span class="ink2">First install {{ missing.join(' and ') }} on the Fleetdeck host (<code>pipx install ansible-core</code>; the Docker image includes both).</span></template>
        <template #action><Btn icon="plus" @click="edit = {}">Add repository</Btn></template>
      </Empty>
    </section>
    <template v-else>
      <section v-for="r in repos.data" :key="r.id" class="panel">
        <div class="panel-h">
          <h2>{{ r.name }}</h2><span class="tag">{{ r.branch }}</span>
          <span v-if="r.last_error" class="chip bad">Sync failed</span><span v-else-if="r.last_commit" class="chip ok">Synced</span><span v-else class="chip">Not synced</span>
          <div class="row nw right">
            <Btn size="sm" icon="refresh" @click="sync(r)">Sync now</Btn>
            <Btn size="sm" kind="ghost" icon="edit" :title="`Edit ${r.name}`" @click="edit = r" />
            <Btn size="sm" kind="ghost" icon="trash" :title="r.used_by.length ? 'Used by scripts' : `Delete ${r.name}`" :disabled="r.used_by.length > 0" @click="del(r)" />
          </div>
        </div>
        <div class="panel-b stack md">
          <span class="small ink2 wrap-any">{{ r.url }}</span>
          <Callout v-if="r.last_error" kind="bad">{{ r.last_error }}
            <template v-if="r.last_error.startsWith('Host key verification failed')" #actions><Btn size="sm" @click="trustKey(r)">Trust new host key</Btn></template>
          </Callout>
          <dl class="kv">
            <dt>Last commit</dt><dd><template v-if="r.last_commit"><code>{{ commit(r.last_commit)[0] }}</code> {{ commit(r.last_commit)[1] }}</template><template v-else>–</template></dd>
            <dt>Last sync</dt><dd :title="when(r.last_sync)">{{ ago(r.last_sync) }}</dd>
            <dt>Access</dt><dd>{{ r.auth === 'token' ? `Access token as ${r.username}` : ACCESS[r.auth] }}</dd>
            <dt>Used by</dt><dd>{{ r.used_by.length ? r.used_by.map((s) => s.name).join(', ') : 'No scripts yet' }}</dd>
          </dl>
        </div>
      </section>
    </template>

    <RepoDialog v-if="edit" :repo="edit" @close="edit = null" @saved="saved" />
  </div>
</template>
