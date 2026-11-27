<script setup>
// Open ports and what the firewall lets through, the firewall itself, interfaces, then endpoint checks.
import { ref, computed } from 'vue';
import { Btn, Empty, Loading, ErrorNote, Callout, Output } from '@/components';
import ChecksPanel from './ChecksPanel.vue';
import UfwDockerDialog from './UfwDockerDialog.vue';
import { api } from '@/lib/api.js';
import { useLoad, useQuery } from '@/lib/use.js';
import { can, toast, confirmBox } from '@/lib/store.js';
import { bytes, rate } from '@/lib/format.js';

const props = defineProps({ s: Object });
const id = props.s.id;
const EXPOSURE = {
  open: ['', 'Open to everyone'], restricted: ['', 'Open to some sources'], blocked: ['ok', 'Blocked by firewall'],
  bypass: ['bad', 'Open: Docker bypasses ufw'], local: ['', 'This server only'], unknown: ['', 'Depends on firewalld'],
};
const FIREWALL = { ufw: ['ok', 'ufw active'], 'ufw-inactive': ['warn', 'ufw off'], firewalld: ['ok', 'firewalld active'] };

const net = useLoad(() => api(`/api/servers/${id}/network`));
const all = useQuery('ports');
const reach = ref(null);
const ufwDocker = ref(false);
const op = computed(() => can('operator'));
const n = computed(() => net.data);
const exposed = computed(() => n.value.ports.filter((p) => p.exposure !== 'local'));
const list = computed(() => (all.value ? n.value.ports : exposed.value));
const bypass = computed(() => n.value.ports.filter((p) => p.exposure === 'bypass'));
const fw = computed(() => n.value.firewall);
const fwChip = computed(() => FIREWALL[fw.value.kind] || ['warn', 'None']);
const canUfwDocker = computed(() => op.value && n.value.docker && fw.value.kind === 'ufw' && n.value.ufwDocker !== 'active');
const addrName = (a) => (a === '0.0.0.0' || a === '*' ? 'all IPv4' : a === '::' ? 'all IPv6' : a);

const check = async () => { reach.value = await api(`/api/servers/${id}/network/reach`, { method: 'POST', body: { ports: exposed.value.filter((p) => p.proto === 'tcp').map((p) => p.port) } }); };
const refresh = () => { reach.value = null; net.reload(); };
async function allow(p) {
  const route = p.docker && n.value.ufwDocker === 'active';
  const port = route ? p.containerPort : p.port;
  if (!await confirmBox({ title: `Open ${p.port}/${p.proto} to everyone?`, confirm: 'Allow', danger: true,
    body: route ? `Runs "ufw route allow proto ${p.proto} from any to any port ${port}" (${port} is the port inside the container). Every container listening on ${port} becomes reachable.` : `Runs "ufw allow ${port}/${p.proto}".` })) return;
  await api(`/api/servers/${id}/network/allow`, { method: 'POST', body: { port, proto: p.proto, route } });
  toast(`${p.port}/${p.proto} allowed`, 'ok');
  net.reload(true);
}

const ifaces = computed(() => {
  const m = new Map((n.value.addrs || []).map((a) => [a.name, { ...a }]));
  for (const i of props.s.snapshot?.ifaces || []) m.set(i.name, { ...(m.get(i.name) || { name: i.name, addrs: [] }), ...i });
  return [...m.values()].filter((i) => i.name !== 'lo' && !/^veth/.test(i.name)).map((i) => ({
    ...i, local: i.addrs.filter((a) => /^fe80:/i.test(a)), shown: i.addrs.filter((a) => !/^fe80:/i.test(a)), // link-local: noise in the table, kept in the tooltip
  }));
});
</script>

<template>
  <div class="stack">
    <section v-if="net.loading && !n" class="panel"><Loading /></section>
    <ErrorNote v-else-if="net.error" :error="net.error" :retry="net.reload" />
    <template v-else>
      <Callout v-if="bypass.length" kind="bad" :title="`Docker publishes ${bypass.map((p) => `${p.port}/${p.proto}`).join(', ')} past ufw.`">
        These ports are reachable even though ufw blocks them, because Docker's own rules come first.
        <template v-if="op" #actions><Btn size="sm" @click="ufwDocker = true">Let ufw control Docker</Btn></template>
      </Callout>
      <Callout v-if="n.ufwDocker === 'written'" kind="warn" title="The ufw-docker rules are written but not active.">Reboot the server to apply them.</Callout>

      <section class="panel">
        <div class="panel-h">
          <h2>Open ports</h2><span class="sub">What is listening, and whether the firewall lets it through.</span>
          <div class="seg" role="group" aria-label="Show">
            <button type="button" :aria-pressed="!all" @click="all = ''">Reachable ({{ exposed.length }})</button>
            <button type="button" :aria-pressed="!!all" @click="all = 'all'">All ({{ n.ports.length }})</button>
          </div>
          <div class="row right">
            <Btn v-if="op" size="sm" title="Try a TCP connection to each port from the Fleetdeck host" :disabled="!exposed.some((p) => p.proto === 'tcp')" @click="check">Test from Fleetdeck</Btn>
            <Btn size="sm" kind="ghost" icon="refresh" title="Refresh" @click="refresh" />
          </div>
        </div>
        <Empty v-if="!list.length" :title="all ? 'Nothing is listening' : 'Nothing is reachable from outside'" icon="net"><template v-if="!all && n.ports.length">Every listening port is bound to this server only.</template></Empty>
        <div v-else class="table-wrap">
          <table class="t">
            <thead><tr>
              <th class="num">Port</th><th class="hide-sm">Listening on</th><th>Program</th><th>Exposure</th>
              <th v-if="reach" title="TCP connect from the Fleetdeck host">From Fleetdeck</th><th><span class="sr">Actions</span></th>
            </tr></thead>
            <tbody>
              <tr v-for="p in list" :key="`${p.port}/${p.proto}`">
                <td class="num nowrap strong">{{ p.port }}<span class="muted">/{{ p.proto }}</span></td>
                <td class="small ink2 hide-sm">{{ p.addrs.map(addrName).join(', ') }}</td>
                <td>
                  <template v-if="p.published.length"><div v-for="(x, i) in p.published" :key="i"><span class="tag">docker</span> {{ x.container }} <span class="muted small">→ {{ x.containerPort }}</span></div></template>
                  <template v-else>{{ p.processes.join(', ') || '–' }}</template>
                </td>
                <td><span :class="['chip', EXPOSURE[p.exposure][0]]">{{ EXPOSURE[p.exposure][1] }}</span></td>
                <td v-if="reach">
                  <span v-if="p.proto !== 'tcp' || reach[p.port] == null" class="muted">–</span>
                  <span v-else-if="reach[p.port]" :class="['chip', p.exposure === 'blocked' && 'bad']" :title="p.exposure === 'blocked' ? 'The firewall should block this, but it answered' : undefined">Reachable</span>
                  <span v-else class="chip">No answer</span>
                </td>
                <td class="actions"><Btn v-if="op && ['blocked', 'restricted'].includes(p.exposure)" size="sm" @click="allow(p)">Allow</Btn></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="reach" class="panel-f small muted">"From Fleetdeck" shows what the Fleetdeck host can reach. If it sits in the same network or VPN, the internet may see less.</div>
      </section>

      <div class="grid-2">
        <section class="panel">
          <div class="panel-h"><h2>Firewall</h2><span :class="['chip', fwChip[0]]">{{ fwChip[1] }}</span></div>
          <div class="panel-b stack sm">
            <p v-if="fw.kind === 'ufw'" class="small ink2">Incoming: <b>{{ fw.incoming }}</b> · Outgoing: <b>{{ fw.outgoing }}</b><template v-if="fw.routed"> · Routed: <b>{{ fw.routed }}</b></template></p>
            <p v-if="fw.kind === 'none' || fw.kind === 'ufw-inactive'" class="small ink2">Every port listening on a public address is reachable. Turn on ufw from the terminal (allow SSH first: <code>ufw allow OpenSSH &amp;&amp; ufw enable</code>).</p>
            <div v-if="n.docker && fw.kind === 'ufw'" class="row">
              <span class="small ink2 grow">Docker ports: <template v-if="n.ufwDocker === 'active'"><b>controlled by ufw</b> (ufw-docker). Open one with "Allow" on its port.</template><template v-else><b>bypass ufw.</b> Docker's NAT rules come before ufw's.</template></span>
              <Btn v-if="canUfwDocker" size="sm" @click="ufwDocker = true">Let ufw control Docker</Btn>
            </div>
            <p v-if="fw.kind === 'ufw' && !fw.rules.length" class="small muted">No rules: only the defaults apply.</p>
            <Output v-if="fw.kind === 'firewalld'" :text="fw.raw" name="firewalld" short />
          </div>
          <div v-if="fw.kind === 'ufw' && fw.rules.length" class="table-wrap">
            <table class="t compact">
              <thead><tr><th>To</th><th>Action</th><th>From</th></tr></thead>
              <tbody><tr v-for="(r, i) in fw.rules" :key="i"><td class="small">{{ r[0] }}</td><td class="small">{{ r[1] }}</td><td class="small ink2">{{ r[2] }}</td></tr></tbody>
            </table>
          </div>
        </section>

        <section class="panel">
          <div class="panel-h"><h2>Interfaces</h2>
            <span class="sub">{{ n.gateway ? `Gateway ${n.gateway}${n.gatewayDev ? ` via ${n.gatewayDev}` : ''}` : 'No default route' }}{{ n.dns.length ? ` · DNS ${n.dns.join(', ')}` : '' }}</span></div>
          <div class="table-wrap">
            <table class="t">
              <thead><tr><th>Interface</th><th>Addresses</th><th class="num">Traffic</th></tr></thead>
              <tbody>
                <tr v-for="i in ifaces" :key="i.name">
                  <td class="nowrap"><span class="strong">{{ i.name }}</span> <span v-if="i.state && i.state !== 'UP' && i.state !== 'UNKNOWN'" class="chip">{{ i.state.toLowerCase() }}</span></td>
                  <td class="small ink2 wrap-any" :title="i.local.length ? `Link-local: ${i.local.join(', ')}` : undefined">
                    <template v-if="i.shown.length"><div v-for="a in i.shown" :key="a">{{ a }}</div></template>
                    <span v-else-if="i.local.length" class="muted">link-local only</span><template v-else>–</template>
                  </td>
                  <td class="num small nowrap" :title="i.rxTotal != null ? `${bytes(i.rxTotal)} received, ${bytes(i.txTotal)} sent since boot` : undefined">
                    <template v-if="i.rx == null">–</template><template v-else><div>↓ {{ rate(i.rx) }}</div><div class="ink2">↑ {{ rate(i.tx) }}</div></template>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
      <UfwDockerDialog v-if="ufwDocker" :s="s" :ports="n.ports.filter((p) => p.docker)" @close="ufwDocker = false" />
    </template>
    <ChecksPanel :s="s" />
  </div>
</template>
