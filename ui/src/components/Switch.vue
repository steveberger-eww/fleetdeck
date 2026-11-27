<script setup>
// On/off: a native checkbox drawn as a switch. <Switch :checked="s.enabled" :label="`Enable ${s.name}`" @change="toggle(s, $event)" />
defineProps({ checked: Boolean, label: String });
</script>
<template><input type="checkbox" role="switch" :checked="checked" :aria-label="label" /></template>
