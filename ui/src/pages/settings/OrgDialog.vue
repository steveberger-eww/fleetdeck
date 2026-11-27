<script setup>
// New organization (optionally with an invite for its first admin) or rename one.
import { reactive, ref } from 'vue';
import { Btn, Modal, Field, Copy } from '@/components';
import { api } from '@/lib/api.js';
import { useDirty } from '@/lib/use.js';
import { toast } from '@/lib/store.js';

const props = defineProps({ org: { type: Object, required: true } });
const emit = defineEmits(['close', 'saved']);
const v = reactive({ name: props.org.name || '', slug: props.org.slug || '', admin_email: '' });
const dirty = useDirty(() => v);
const err = ref('');
const link = ref(null);
async function save() {
  err.value = '';
  try {
    if (props.org.id) {
      await api(`/api/instance/tenants/${props.org.id}`, { method: 'PUT', body: { name: v.name, slug: v.slug } });
      emit('saved');
      emit('close');
      return;
    }
    const r = await api('/api/instance/tenants', { method: 'POST', body: v });
    emit('saved');
    if (r.invite_url) link.value = r.invite_url;
    else { toast(`${r.tenant.name} created`, 'ok'); emit('close'); }
  } catch (e) { err.value = e.message; }
}
</script>
<template>
  <Modal v-if="link" title="Organization created" @close="emit('close')">
    <p class="ink2">Send this link to {{ v.admin_email }}. Opening it makes them the organization's admin. It works once.</p>
    <Copy :text="link" />
    <template #footer><button type="button" class="btn primary" @click="emit('close')">Done</button></template>
  </Modal>
  <Modal v-else :title="org.id ? `Rename ${org.name}` : 'New organization'" :dirty="dirty" @close="emit('close')">
    <Field label="Name"><input v-model="v.name" autofocus /></Field>
    <Field label="Short name" :hint="org.id ? 'Part of its single sign-on link.' : 'Optional. Made from the name when empty.'"><input v-model="v.slug" autocapitalize="off" spellcheck="false" /></Field>
    <Field v-if="!org.id" label="Admin email (optional)" hint="Creates an invite link that makes this person its admin. You can also open the organization and invite people yourself.">
      <input v-model="v.admin_email" type="email" />
    </Field>
    <p v-if="err" class="err" role="alert">{{ err }}</p>
    <template #footer="{ close }">
      <button type="button" class="btn" @click="close">Cancel</button>
      <Btn kind="primary" :disabled="!v.name.trim()" @click="save">{{ org.id ? 'Save' : 'Create organization' }}</Btn>
    </template>
  </Modal>
</template>
