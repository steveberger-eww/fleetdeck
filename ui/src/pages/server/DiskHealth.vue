<script setup>
// S.M.A.R.T. from the probe (every 30 min). Virtual machines have no SMART, so stay quiet there.
import { ref, computed } from 'vue';
import { Btn, Callout } from '@/components';
import SmartHistory from './SmartHistory.vue';
import { can } from '@/lib/store.js';
import { dur } from '@/lib/format.js';
import { runOn } from './util.js';

const props = defineProps({ s: Object });
const open = ref(null); // disk whose history is shown
const sm = computed(() => props.s.snapshot.smart);
// smartctl needs root: with 'noroot' the System panel already says sudo is missing.
const show = computed(() => sm.value && !(props.s.snapshot.virt && !sm.value.disks.length) && sm.value.status !== 'noroot');
const key = (d) => d.serial || d.dev;
const num = (v) => (v == null ? '–' : v);
const health = (d) => (!d.passed ? ['bad', 'Failing'] : d.problem ? ['warn', 'Warning'] : ['ok', 'OK']);
const sectors = (d) => (d.pending == null && d.uncorrectable == null ? (d.mediaErrors == null ? '–' : `${d.mediaErrors} media err.`) : (d.pending || 0) + (d.uncorrectable || 0));
</script>

<template>
  <template v-if="show">
    <Callout v-if="sm.status === 'none' && can('operator')" kind="info">
      Install smartmontools to watch the health of this server's disks (S.M.A.R.T.).
      <template #actions><Btn size="sm" @click="runOn(s, 'smartmontools')">Install smartmontools</Btn></template>
    </Callout>
    <section v-else-if="sm.status !== 'none' && sm.disks.length" class="panel">
      <div class="panel-h"><h2>Disk health</h2><span class="sub">S.M.A.R.T., checked every 30 minutes. Sleeping disks are skipped.</span></div>
      <div class="table-wrap">
        <table class="t">
          <thead><tr>
            <th>Health</th><th>Disk</th><th class="num">Temp</th><th class="num hide-sm">Powered on</th>
            <th class="num hide-sm" title="Reallocated sectors">Realloc.</th><th class="num" title="Pending + uncorrectable sectors">Bad sectors</th>
            <th class="num" title="NVMe: percentage of rated endurance used">Wear</th><th><span class="sr">Actions</span></th>
          </tr></thead>
          <tbody>
            <template v-for="d in sm.disks" :key="key(d)">
              <tr>
                <td><span :class="['chip', health(d)[0]]">{{ health(d)[1] }}</span></td>
                <td>{{ d.dev }}<span class="sub">{{ d.model }}{{ d.serial ? ` · ${d.serial}` : '' }}</span></td>
                <td class="num">{{ d.temp == null ? '–' : `${d.temp} °C` }}</td>
                <td class="num ink2 hide-sm">{{ d.hours == null ? '–' : dur(d.hours * 3600e3) }}</td>
                <td class="num hide-sm">{{ num(d.realloc) }}</td>
                <td :class="['num', (d.pending > 0 || d.uncorrectable > 0) && 'bad-ink']">{{ sectors(d) }}</td>
                <td class="num">{{ d.wear == null ? '–' : `${d.wear}%` }}</td>
                <td class="actions"><Btn size="sm" kind="ghost" :aria-expanded="open === key(d)" @click="open = open === key(d) ? null : key(d)">{{ open === key(d) ? 'Hide' : 'History' }}</Btn></td>
              </tr>
              <tr v-if="open === key(d)" class="hist"><td colspan="8"><SmartHistory :s="s" :d="d" :disk="key(d)" /></td></tr>
            </template>
          </tbody>
        </table>
      </div>
    </section>
  </template>
</template>

<style scoped>
.t tr.hist td, .t tr.hist:hover td { background: var(--panel-2); padding-top: 12px; padding-bottom: 12px; }
</style>
