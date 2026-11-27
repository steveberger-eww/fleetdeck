<script setup>
// Proxmox guests (read-only, from the per-minute probe; managing them stays in Proxmox) and Docker containers.
import { ref, computed } from 'vue';
import { Btn, Modal, Empty, CellBar, SortTh } from '@/components';
import DockerPanel from './DockerPanel.vue';
import ContainerHistory from './ContainerHistory.vue';
import { useSort } from '@/lib/use.js';
import { bytes, pct, uptime } from '@/lib/format.js';
import { urlHost } from './util.js';

const props = defineProps({ s: Object });
const pve = computed(() => props.s.snapshot?.pve);
const hist = ref(null);
const sort = useSort(() => pve.value?.guests || [], 'vmid');
const running = computed(() => (pve.value?.guests || []).filter((g) => g.status === 'running').length);
</script>

<template>
  <div class="stack">
    <section v-if="pve" class="panel">
      <div class="panel-h">
        <h2>Virtual machines &amp; LXC</h2><span class="sub">{{ running }} of {{ pve.guests.length }} running on node {{ pve.node }}</span>
        <Btn size="sm" kind="ghost" icon="ext" class="right" :href="`https://${urlHost(s.host)}:8006`" target="_blank" rel="noopener noreferrer">Open Proxmox</Btn>
      </div>
      <Empty v-if="pve.error" title="Can't read the guests" icon="alert">{{ pve.error }}</Empty>
      <Empty v-else-if="!pve.guests.length" title="No guests on this node" icon="box" />
      <div v-else class="table-wrap">
        <table class="t">
          <thead><tr>
            <SortTh :sort="sort" k="status">State</SortTh><SortTh :sort="sort" k="vmid" class="num">ID</SortTh><SortTh :sort="sort" k="name">Name</SortTh>
            <SortTh :sort="sort" k="type" class="hide-sm">Type</SortTh><SortTh :sort="sort" k="cpu" class="num" desc>CPU</SortTh><SortTh :sort="sort" k="mem" class="num" desc>Memory</SortTh>
            <SortTh :sort="sort" k="disk" class="num hide-sm" desc>Disk</SortTh><SortTh :sort="sort" k="uptime" class="hide-sm" desc>Up</SortTh><th><span class="sr">Actions</span></th>
          </tr></thead>
          <tbody>
            <tr v-for="g in sort.rows" :key="g.vmid">
              <td><span :class="['chip', g.status === 'running' ? 'ok' : g.status === 'stopped' ? '' : 'warn']">{{ g.status }}</span></td>
              <td class="num">{{ g.vmid }}</td><td class="strong">{{ g.name }}</td><td class="ink2 hide-sm">{{ g.type === 'lxc' ? 'LXC' : 'VM' }}</td>
              <td class="num"><CellBar v-if="g.status === 'running'" :value="g.cpu"><span :title="`of ${g.maxcpu} cores`">{{ pct(g.cpu) }}</span></CellBar><template v-else>–</template></td>
              <td class="num nowrap"><CellBar v-if="g.status === 'running' && g.maxmem" :value="g.mem / g.maxmem"><span :title="`${bytes(g.mem)} of ${bytes(g.maxmem)}`">{{ bytes(g.mem) }}</span></CellBar><span v-else class="ink2">{{ bytes(g.maxmem) }}</span></td>
              <td class="num ink2 nowrap hide-sm">{{ g.disk ? `${bytes(g.disk)} of ${bytes(g.maxdisk)}` : bytes(g.maxdisk) }}</td>
              <td class="ink2 nowrap hide-sm">{{ g.status === 'running' ? uptime(g.uptime) : '–' }}</td>
              <td class="actions"><Btn size="sm" kind="ghost" @click="hist = g">History</Btn></td>
            </tr>
          </tbody>
        </table>
      </div>
      <Modal v-if="hist" wide :title="`History: ${hist.name}`" @close="hist = null"><ContainerHistory :s="s" :name="`pve:${hist.vmid}`" /></Modal>
    </section>
    <DockerPanel v-if="!pve || s.snapshot.docker" :s="s" />
  </div>
</template>
