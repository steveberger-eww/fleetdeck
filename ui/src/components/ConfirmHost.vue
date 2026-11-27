<script setup>
import { ref, watch } from 'vue';
import Modal from './Modal.vue';
import { confirmState, answerConfirm } from '@/lib/store.js';
const typed = ref('');
watch(confirmState, () => { typed.value = ''; });
const ok = () => !confirmState.value?.typeToConfirm || typed.value === confirmState.value.typeToConfirm;
</script>
<template>
  <Modal v-if="confirmState" :key="confirmState.title" :title="confirmState.title" @close="answerConfirm(false)">
    <p v-if="confirmState.body" class="ink2">{{ confirmState.body }}</p>
    <label v-if="confirmState.typeToConfirm" class="field"><span>Type <b>{{ confirmState.typeToConfirm }}</b> to confirm</span>
      <input v-model="typed" autofocus autocomplete="off" @keydown.enter="ok() && answerConfirm(true)" /></label>
    <template #footer>
      <button type="button" class="btn" @click="answerConfirm(false)">Cancel</button>
      <button type="button" :class="['btn', confirmState.danger ? 'danger solid' : 'primary']" :disabled="!ok()" @click="answerConfirm(true)">{{ confirmState.confirm || 'Confirm' }}</button>
    </template>
  </Modal>
</template>
