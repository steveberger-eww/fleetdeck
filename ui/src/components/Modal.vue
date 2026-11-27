<script setup>
// Native <dialog> modal. Mount it to open it (v-if), emit close to unmount.
//   <Modal title="Add server" :dirty="dirty" @close="open = false">
//     ...body...
//     <template #footer="{ close }"><Btn @click="close">Cancel</Btn><Btn kind="primary" @click="save">Save</Btn></template>
//   </Modal>
// `dirty`: closing with Esc, ✕, a click outside or the footer's close() asks before throwing the changes away.
import { ref, onMounted } from 'vue';
import Icon from './Icon.vue';
import { confirmBox } from '@/lib/store.js';

const props = defineProps({ title: String, wide: Boolean, dirty: Boolean, size: String });
const emit = defineEmits(['close']);
const el = ref();
async function close() {
  if (props.dirty && !await confirmBox({ title: 'Discard your changes?', confirm: 'Discard', danger: true })) return;
  emit('close');
}
onMounted(() => { if (!el.value.open) el.value.showModal(); });
const backdrop = (e) => { if (e.target === el.value && !props.dirty) close(); };
</script>
<template>
  <dialog ref="el" :class="['modal', wide && 'wide', size]" :aria-label="title" @cancel.prevent="close" @mousedown="backdrop">
    <div class="modal-h"><h2>{{ title }}</h2><button type="button" class="btn ghost icon sm" aria-label="Close" @click="close"><Icon name="x" /></button></div>
    <div class="modal-b"><slot :close="close" /></div>
    <div v-if="$slots.footer" class="modal-f"><slot name="footer" :close="close" /></div>
  </dialog>
</template>
<style>
.modal { width: min(560px, calc(100vw - 24px)); max-height: calc(100dvh - 48px); padding: 0; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); color: var(--ink); box-shadow: var(--shadow-lg); display: flex; flex-direction: column; }
.modal:not([open]) { display: none; }
.modal.wide { width: min(780px, calc(100vw - 24px)); }
.modal.xl { width: min(1040px, calc(100vw - 24px)); }
.modal::backdrop { background: var(--scrim); }
.modal[open] { animation: modal-in .16s ease-out; }
@keyframes modal-in { from { opacity: 0; transform: translateY(6px) scale(.99); } }
.modal-h { display: flex; align-items: center; gap: 12px; padding: 16px 16px 12px 20px; }
.modal-h h2 { flex: 1; font-size: var(--fs-lg); }
.modal-b { padding: 4px 20px 20px; overflow-y: auto; display: grid; gap: 16px; align-content: start; }
.modal-f { display: flex; align-items: center; justify-content: flex-end; gap: 8px; padding: 12px 20px; border-top: 1px solid var(--line); background: var(--panel-2); border-radius: 0 0 12px 12px; }
@media (max-width: 700px) { .modal { max-height: calc(100dvh - 16px); } .modal-b { padding: 4px 16px 16px; } .modal-f { padding: 12px 16px; flex-wrap: wrap; } }
</style>
