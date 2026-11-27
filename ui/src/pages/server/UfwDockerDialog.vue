<script setup>
// Installs the ufw-docker rules (github.com/chaifeng/ufw-docker). Afterwards published ports are closed to the internet
// unless allowed, so let the user pick which ones stay open; 80/443 are pre-ticked.
import { ref } from 'vue';
import { Modal, Btn } from '@/components';
import { runOn } from './util.js';

const props = defineProps({ s: Object, ports: Array });
const emit = defineEmits(['close']);
const opts = [...new Set(props.ports.map((p) => `${p.containerPort}/${p.proto}`))];
const keep = ref(new Set(opts.filter((o) => /^(80|443)\//.test(o))));
const flip = (o) => { const x = new Set(keep.value); x.has(o) ? x.delete(o) : x.add(o); keep.value = x; };
const usedBy = (o) => props.ports.filter((p) => `${p.containerPort}/${p.proto}` === o).map((p) => `host ${p.port}, ${p.published.map((x) => x.container).join(', ')}`).join('; ');
const run = async () => { emit('close'); await runOn(props.s, 'ufw-docker', { KEEP_OPEN: [...keep.value].join(' ') }); };
</script>

<template>
  <Modal title="Let ufw control Docker ports" @close="emit('close')">
    <p class="ink2">Adds the <a href="https://github.com/chaifeng/ufw-docker" target="_blank" rel="noopener noreferrer">ufw-docker</a> rules to <code>/etc/ufw/after.rules</code> (a backup is kept) and reloads ufw.</p>
    <p class="ink2">Afterwards, published container ports are <b>closed to the internet</b> unless you allow them. Private networks (10.x, 172.16–31.x, 192.168.x) can still reach them.</p>
    <div v-if="opts.length" class="field">
      <span>Keep these open to everyone</span>
      <label v-for="o in opts" :key="o" class="check"><input type="checkbox" :checked="keep.has(o)" @change="flip(o)" /><span>{{ o }}<span class="hint">{{ usedBy(o) }}</span></span></label>
      <span class="hint">These are ports inside the containers, which is what ufw-docker matches on.</span>
    </div>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" @click="run">Apply</Btn>
    </template>
  </Modal>
</template>
