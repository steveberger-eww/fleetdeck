<script setup>
// Password + repeat with the length hint, shared by setup and invites. v-model is { password, confirm }.
import { computed } from 'vue';
const f = defineModel({ type: Object, required: true });
const emit = defineEmits(['enter']);
const short = computed(() => f.value.password.length > 0 && f.value.password.length < MIN_PW);
const left = computed(() => MIN_PW - f.value.password.length);
</script>
<script>
export const MIN_PW = 10;
export const pwOk = (f) => f.password.length >= MIN_PW && f.password === f.confirm;
</script>
<template>
  <label class="field"><span>Password</span><input v-model="f.password" type="password" autocomplete="new-password" />
    <span :class="['hint', short && 'err']">{{ short ? `${left} more character${left === 1 ? '' : 's'}. A passphrase works well.` : `At least ${MIN_PW} characters. A passphrase works well.` }}</span></label>
  <label class="field"><span>Repeat password</span><input v-model="f.confirm" type="password" autocomplete="new-password" @keydown.enter="emit('enter')" />
    <span v-if="f.confirm.length > 0 && f.confirm !== f.password" class="hint err">Doesn't match yet.</span></label>
</template>
