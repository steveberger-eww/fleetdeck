<script setup>
// docker inspect, the parts an admin looks for. Environment values only for roles allowed to see them.
// u: the image update check for this container (DockerPanel loads it); emits edit(newRef) and recreate to apply one.
import { ref, computed } from 'vue';
import { Btn, Loading, ErrorNote } from '@/components';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { when } from '@/lib/format.js';
import { can } from '@/lib/store.js';
import { SCALE, STEPS, withTag, needsEdit } from './updates.js';

const props = defineProps({ s: Object, x: Object, u: Object, checking: Boolean, updateError: String });
const emit = defineEmits(['edit', 'recreate']);
const op = computed(() => can('operator'));
const step = computed(() => STEPS[props.u?.step]);
const tag = computed(() => { const m = props.u?.ref.match(/:([^:/@]+)(@|$)/); return m ? m[1] : 'latest'; });
const major = computed(() => parseInt(String(props.u?.version).replace(/^v/, ''), 10));
// Pulling the same tag again gets it: a new build, a channel that moved on, or an image already pulled.
const byPull = computed(() => props.u && (props.u.pulled || props.u.newBuild));
const d = useLoad(() => api(`/api/servers/${props.s.id}/containers/inspect?id=${encodeURIComponent(props.x.id)}`));
const reveal = ref(false);
const never = (t) => !t || t.startsWith('0001'); // docker's zero time: never started
const at = (t) => (never(t) ? '–' : when(t));
</script>

<template>
  <ErrorNote v-if="d.error" :error="d.error" :retry="d.reload" />
  <Loading v-else-if="!d.data" :rows="6" />
  <dl v-else class="kv">
    <template v-if="u || checking || updateError">
      <dt class="sub">Version</dt>
      <template v-if="!u"><dt>Updates</dt><dd class="muted">{{ checking ? 'Checking the registry…' : updateError || 'Not checked' }}</dd></template>
      <template v-else>
        <dt>Running</dt><dd>{{ u.version || tag }} <span v-if="u.channel" class="muted small">via the {{ tag }} tag</span></dd>
        <template v-if="step">
          <dt>Update</dt>
          <dd>
            <div class="scale" role="img" :aria-label="`Update size: ${step.label}`">
              <span v-for="[k, l] in SCALE" :key="k" :class="['rung', k === step.rung && `on ${step.kind}`]">{{ k === step.rung ? step.label : l }}</span>
            </div>
            <p class="small ink2 why">{{ step.text }}<template v-if="u.behind > 1"> It's {{ u.behind }} major versions behind: old major versions usually stop getting security fixes, so plan this one.</template></p>
          </dd>
          <template v-if="u.newest"><dt>Newest</dt><dd>{{ u.newest }}</dd></template>
          <template v-if="u.safe && !Number.isNaN(major)"><dt>Newest {{ major }}.x</dt><dd>{{ u.safe }} <span class="muted small">the smaller step, same major version</span></dd></template>
          <template v-if="u.step !== 'current'">
            <dt>How</dt>
            <dd class="how">
              <p v-if="u.pulled" class="small">A newer image for this tag is already on the server. Recreating the container applies it.</p>
              <template v-if="!x.project">
                <p class="small">Not part of a Compose stack: pull <b>{{ needsEdit(u) ? withTag(u.ref, u.safe || u.newest) : u.ref }}</b> and recreate the container with the same options.</p>
              </template>
              <p v-else-if="!op" class="small muted">An operator can apply it from here.</p>
              <template v-else>
                <div v-if="needsEdit(u) && x.files" class="row">
                  <Btn v-if="u.safe" size="sm" kind="primary" @click="emit('edit', withTag(u.ref, u.safe))">Use {{ u.safe }}</Btn>
                  <Btn size="sm" :kind="u.safe ? '' : 'primary'" @click="emit('edit', withTag(u.ref, u.newest))">Use {{ u.newest }}</Btn>
                  <span class="small muted">Opens the Compose file with the new tag to review.</span>
                </div>
                <div v-if="byPull && x.dir" class="row">
                  <Btn size="sm" :kind="needsEdit(u) ? '' : 'primary'" @click="emit('recreate')">Pull and recreate {{ x.project }}</Btn>
                  <span class="small muted">{{ u.pulled ? 'Recreates the stack\'s changed containers.' : 'Pulls the same tag again and recreates what changed.' }}</span>
                </div>
              </template>
            </dd>
          </template>
        </template>
        <template v-if="u.error"><dt>Can't check</dt><dd class="muted">{{ u.error }}</dd></template>
      </template>
    </template>

    <dt class="sub">Container</dt>
    <dt>Image</dt><dd>{{ x.image }} <span class="muted small" :title="d.data.image">{{ d.data.image.replace('sha256:', '').slice(0, 12) }}</span></dd>
    <template v-if="d.data.cmd"><dt>Command</dt><dd class="small">{{ d.data.cmd }}</dd></template>
    <template v-if="d.data.user"><dt>User</dt><dd>{{ d.data.user }}</dd></template>
    <dt>Created</dt><dd>{{ at(d.data.created) }}</dd>
    <dt>Started</dt><dd>{{ at(d.data.startedAt) }}</dd>
    <template v-if="x.state !== 'running' && !never(d.data.startedAt)"><dt>Exit code</dt><dd>{{ d.data.exitCode }} <span v-if="d.data.oomKilled" class="chip bad">out of memory</span></dd></template>
    <dt>Restart policy</dt><dd>{{ d.data.restart }}</dd>
    <dt>Restarts</dt><dd><span v-if="d.data.restartCount" class="chip warn">{{ d.data.restartCount }}</span><template v-else>0</template></dd>

    <template v-if="x.project">
      <dt class="sub">Compose</dt>
      <dt>Project</dt><dd>{{ x.project }}</dd>
      <template v-if="x.dir"><dt>Directory</dt><dd>{{ x.dir }}</dd></template>
      <template v-if="x.files"><dt>Files</dt><dd><div v-for="f in x.files.split(',')" :key="f">{{ f }}</div></dd></template>
    </template>

    <dt class="sub">Storage</dt>
    <template v-if="!d.data.mounts.length"><dt>Mounts</dt><dd class="muted">None</dd></template>
    <template v-for="m in d.data.mounts" :key="m.target"><dt>{{ m.target }}</dt><dd>{{ m.source }} <span class="tag">{{ m.type }}</span> <span v-if="!m.rw" class="tag">read-only</span></dd></template>

    <dt class="sub">Networks</dt>
    <template v-if="!d.data.networks.length"><dt>Networks</dt><dd class="muted">None</dd></template>
    <template v-for="n in d.data.networks" :key="n.name"><dt>{{ n.name }}</dt><dd>{{ n.ip || '–' }}</dd></template>

    <dt class="sub">Environment <button v-if="!d.data.envHidden && d.data.env.length" type="button" class="btn ghost sm" @click="reveal = !reveal">{{ reveal ? 'Hide values' : 'Show values' }}</button></dt>
    <template v-if="!d.data.env.length"><dt>Variables</dt><dd class="muted">None</dd></template>
    <template v-for="e in d.data.env" :key="e.name">
      <dt>{{ e.name }}</dt>
      <dd class="small"><span v-if="d.data.envHidden || !reveal" class="muted">••••••</span><template v-else-if="e.value">{{ e.value }}</template><span v-else class="muted">empty</span></dd>
    </template>
    <dd v-if="d.data.envHidden" class="sub note small muted">Values can hold secrets: your role sees the names only.</dd>
  </dl>
</template>

<style scoped>
.kv .sub { grid-column: 1 / -1; display: flex; align-items: center; gap: 8px; font-size: var(--fs-xs); font-weight: 600; color: var(--muted); margin-top: 10px; }
.kv .sub:first-child { margin-top: 0; }
.kv .sub .btn { font-weight: 500; }
.kv .sub.note { font-weight: 400; font-size: var(--fs-sm); margin-top: 0; }
.kv dt { overflow-wrap: anywhere; }
.scale { display: inline-flex; gap: 3px; flex-wrap: wrap; }
.rung { height: 22px; padding: 0 9px; display: inline-flex; align-items: center; border-radius: 999px; font-size: var(--fs-xs); color: var(--faint); border: 1px solid var(--line); }
.rung.on { font-weight: 600; border-color: transparent; }
.rung.ok { background: var(--ok-soft); color: var(--ok); }
.rung.info { background: var(--accent-soft); color: var(--accent); }
.rung.warn { background: var(--warn-soft); color: var(--warn); }
.rung.bad { background: var(--bad-soft); color: var(--bad); }
.why { margin: 6px 0 0; }
.how { display: grid; gap: 8px; }
.how p { margin: 0; }
</style>
