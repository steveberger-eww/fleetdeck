<script setup>
// One setting: what it is and what it does on the left, the control on the right.
//   <SettingRow label="Name" desc="Shown in the sidebar."><input v-model="v.name" /></SettingRow>   (or a #desc slot)
defineProps({ label: String, desc: String });
</script>
<template>
  <div class="set">
    <div><b>{{ label }}</b><p v-if="desc || $slots.desc"><slot name="desc">{{ desc }}</slot></p></div>
    <div class="set-c"><slot /></div>
  </div>
</template>
<style scoped>
.set { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr); gap: 8px 28px; align-items: center; padding: 16px; }
.set + .set { border-top: 1px solid var(--line); }
.set b { font-weight: 600; }
.set p { margin-top: 2px; font-size: var(--fs-sm); color: var(--muted); }
.set-c { display: flex; justify-content: flex-end; align-items: center; gap: 8px; min-width: 0; overflow-wrap: anywhere; }
@media (max-width: 700px) {
  .set { grid-template-columns: minmax(0, 1fr); }
  .set-c { justify-content: flex-start; }
}
</style>
