<script setup>
// Auto-scrolling output pane; stays pinned to the bottom unless the user scrolls up. Copy and download on hover.
import { ref, computed, watch, nextTick } from 'vue';
import Btn from './Btn.vue';
import { stripAnsi } from '@/lib/format.js';
import { copyText, download } from '@/lib/actions.js';
const props = defineProps({ text: String, name: { type: String, default: 'output' }, short: Boolean });
const el = ref();
let pinned = true;
const clean = computed(() => stripAnsi(props.text));
watch(clean, async () => { await nextTick(); if (pinned && el.value) el.value.scrollTop = el.value.scrollHeight; }, { immediate: true, flush: 'post' });
const onScroll = (e) => { const t = e.target; pinned = t.scrollHeight - t.scrollTop - t.clientHeight < 30; };
</script>
<template>
  <div class="out-wrap">
    <div v-if="clean" class="out-tools">
      <Btn size="sm" icon="copy" title="Copy output" @click="copyText(clean, 'Output copied')" />
      <Btn size="sm" icon="download" title="Download output" @click="download(clean, `${name}.txt`)" />
    </div>
    <pre ref="el" :class="['out', short && 'short']" tabindex="0" @scroll="onScroll">{{ clean || ' ' }}</pre>
  </div>
</template>
