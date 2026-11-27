<script setup>
import { reactive, computed } from 'vue';
import { api } from '@/lib/api.js';
import { me, refreshMe, toast } from '@/lib/store.js';
import SettingRow from './SettingRow.vue';
import SaveBar from './SaveBar.vue';
import PrometheusPanel from './PrometheusPanel.vue';

const t = computed(() => me.value.tenant);
const v = reactive({ name: t.value.name, slug: t.value.slug });
const changed = computed(() => v.name !== t.value.name || v.slug !== t.value.slug);
async function save() {
  const r = await api('/api/tenant', { method: 'PUT', body: v });
  Object.assign(v, { name: r.name, slug: r.slug });
  await refreshMe();
  toast('Organization saved', 'ok');
}
</script>
<template>
  <div class="page-h"><div><h1>General</h1><p class="sub">Applies to everyone in {{ t.name }}.</p></div></div>
  <section class="panel">
    <SettingRow label="Name" desc="Shown in the sidebar and the organization switcher."><input v-model="v.name" aria-label="Organization name" /></SettingRow>
    <SettingRow label="Short name" :desc="`Lowercase letters, digits and dashes. Part of your single sign-on link (…/?org=${v.slug || 'short-name'}).`">
      <input v-model="v.slug" aria-label="Short name" autocapitalize="off" spellcheck="false" />
    </SettingRow>
    <SaveBar :changed="changed" :disabled="!v.name.trim()" :save="save" />
  </section>
  <PrometheusPanel />
</template>
