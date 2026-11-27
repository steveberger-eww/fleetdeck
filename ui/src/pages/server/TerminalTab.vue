<script setup>
// An SSH shell in the browser (xterm.js over a websocket). Opening one is recorded in the audit log.
import { ref, watch, onMounted, onBeforeUnmount } from 'vue';
import { Icon } from '@/components';
import { toast } from '@/lib/store.js';
import '@xterm/xterm/css/xterm.css';

const props = defineProps({ s: Object });
const el = ref();
const state = ref('connecting');
const session = ref(0);

// Fill the viewport below wherever the terminal starts (callouts above it vary).
const size = () => { const e = el.value; if (e && !document.fullscreenElement) e.style.height = `${Math.max(320, innerHeight - (e.getBoundingClientRect().top + scrollY) - 24)}px`; };
onMounted(() => { size(); addEventListener('resize', size); });
onBeforeUnmount(() => removeEventListener('resize', size));

let stop = () => {};
function connect() {
  let ws;
  let term;
  let ro;
  let disposed = false;
  state.value = 'connecting';
  (async () => {
    const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')]);
    if (disposed) return;
    term = new Terminal({ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace', fontSize: 13, cursorBlink: true, scrollback: 5000,
      theme: { background: '#11161D', foreground: '#E4E9F0', cursor: '#4A94D6', selectionBackground: '#2E3846' } });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el.value);
    fit.fit();
    ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/servers/${props.s.id}/terminal?cols=${term.cols}&rows=${term.rows}`);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => { state.value = 'open'; term.focus(); };
    ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
    ws.onclose = () => { if (!disposed) { state.value = 'closed'; term.write('\r\n\x1b[90m[session closed]\x1b[0m\r\n'); } };
    term.onData((d) => ws.readyState === 1 && ws.send(JSON.stringify({ t: 'i', d })));
    term.onResize(({ cols, rows }) => ws.readyState === 1 && ws.send(JSON.stringify({ t: 'r', c: cols, r: rows })));
    ro = new ResizeObserver(() => { try { fit.fit(); } catch { /* hidden */ } });
    ro.observe(el.value);
  })().catch((e) => { state.value = `error: ${e.message}`; });
  return () => { disposed = true; ro?.disconnect(); ws?.close(); term?.dispose(); };
}
watch(session, () => { stop(); stop = connect(); }, { flush: 'post' });
onMounted(() => { stop = connect(); });
onBeforeUnmount(() => stop());

const full = () => el.value.requestFullscreen?.().then(() => el.value.querySelector('textarea')?.focus()).catch((e) => toast(e.message, 'bad'));
</script>

<template>
  <div class="stack sm">
    <div class="row">
      <span :class="['chip', state === 'open' ? 'ok' : state === 'closed' ? '' : state.startsWith('error') ? 'bad' : 'info']">{{ state === 'open' ? `Connected as ${s.user}` : state === 'closed' ? 'Disconnected' : state }}</span>
      <span class="small muted hide-sm">Sessions are recorded in the audit log (opened, not keystrokes).</span>
      <div class="row nw right">
        <button v-if="state === 'closed'" type="button" class="btn sm" @click="session++">Reconnect</button>
        <button type="button" class="btn sm ghost" title="Full screen (Esc leaves)" @click="full"><Icon name="full" :size="14" />Full screen</button>
      </div>
    </div>
    <div ref="el" class="term" />
  </div>
</template>

<style scoped>
.term { min-width: 0; overflow: hidden; background: #11161D; border-radius: var(--r-lg); padding: 12px; height: 480px; min-height: 320px; border: 1px solid var(--line); box-shadow: var(--shadow-sm); }
.term:fullscreen { height: 100vh !important; border-radius: 0; border: 0; }
.term :deep(.xterm) { height: 100%; }
</style>
