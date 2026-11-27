<script setup>
// <Field label="Host" hint="IP or name" :error="err"><input v-model="v.host" /></Field>
defineProps({ label: String, hint: String, error: String });
</script>
<template>
  <label class="field"><span>{{ label }}</span><slot /><span v-if="error" class="hint err">{{ error }}</span><span v-else-if="hint || $slots.hint" class="hint"><slot name="hint">{{ hint }}</slot></span></label>
</template>
