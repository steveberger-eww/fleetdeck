<script setup>
// A script parameter's input by type. Values are strings; a checkbox is 'true' or 'false'.
import Field from './Field.vue';
defineProps({ p: Object, hint: String });
const model = defineModel({ type: String, default: '' });
</script>
<template>
  <div v-if="p.type === 'checkbox'" class="field">
    <label class="check"><input type="checkbox" :checked="model === 'true'" @change="model = String($event.target.checked)" />{{ p.label }}</label>
    <span v-if="hint || $slots.hint" class="hint"><slot name="hint">{{ hint }}</slot></span>
  </div>
  <Field v-else :label="p.label" :hint="hint">
    <select v-if="p.type === 'choice'" v-model="model"><option v-for="o in p.options" :key="o" :value="o">{{ o }}</option></select>
    <input v-else v-model="model" :required="p.required" />
    <template v-if="$slots.hint" #hint><slot name="hint" /></template>
  </Field>
</template>
