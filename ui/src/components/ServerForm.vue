<script setup>
// Add / edit server fields. v-model is { name, host, port, user, tags, notes, key_id, sudo_password }.
// key_id '__new' means "generate a key for this server"; '' means the agent / ~/.ssh of the Fleetdeck host.
import Field from './Field.vue';
defineProps({ keys: { type: Array, default: () => [] }, hasSudo: Boolean, allowNewKey: { type: Boolean, default: true } });
const v = defineModel({ type: Object, required: true });
</script>
<template>
  <div class="form">
    <div class="cols">
      <Field label="Name"><input v-model="v.name" placeholder="web-1" autofocus /></Field>
      <Field label="Tags" hint="Comma separated, e.g. prod, web"><input v-model="v.tags" /></Field>
    </div>
    <div class="cols host-cols">
      <Field label="Host"><input v-model="v.host" placeholder="203.0.113.10 or web1.example.com" autocapitalize="off" spellcheck="false" /></Field>
      <Field label="Port"><input v-model="v.port" type="number" min="1" max="65535" /></Field>
      <Field label="User"><input v-model="v.user" autocapitalize="off" spellcheck="false" /></Field>
    </div>
    <Field label="SSH key" :hint="v.user !== 'root' ? 'Non-root users need passwordless sudo for updates and admin actions.' : undefined">
      <select v-model="v.key_id">
        <option v-if="allowNewKey" value="__new">Generate a new key for this server</option>
        <option v-for="k in keys" :key="k.id" :value="k.id">{{ k.name }}</option>
        <option value="">Use the ssh-agent or ~/.ssh key of the Fleetdeck host</option>
      </select>
    </Field>
    <Field v-if="v.user !== 'root'" label="Sudo password (optional)"
      :hint="hasSudo ? 'Saved. Leave empty to keep it.' : 'Only if this user needs a password for sudo. Stored encrypted and never shown again; prefer passwordless sudo if you can.'">
      <input v-model="v.sudo_password" type="password" autocomplete="new-password" :placeholder="hasSudo ? '••••••••' : ''" />
    </Field>
    <Field label="Notes"><textarea v-model="v.notes" /></Field>
  </div>
</template>
<style>
.host-cols { grid-template-columns: minmax(0, 3fr) minmax(80px, 1fr) minmax(0, 2fr); }
@media (max-width: 560px) { .host-cols { grid-template-columns: minmax(0, 1fr); } }
</style>
