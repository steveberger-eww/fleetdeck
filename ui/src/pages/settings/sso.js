// Loads a provider view (/api/sso for the instance, /api/tenant/sso for an organization) and keeps the form for steps 1-2.
import { ref, watch } from 'vue';
import { api } from '@/lib/api.js';
import { useLoad } from '@/lib/use.js';
import { toast, confirmBox } from '@/lib/store.js';

export function useProvider(path) {
  const sso = useLoad(() => api(path));
  const f = ref(null);
  const err = ref('');
  const reset = () => { if (sso.data) f.value = { issuer: sso.data.issuer, client_id: sso.data.client_id, client_secret: '' }; };
  watch(() => sso.data, () => { if (!f.value) reset(); }, { immediate: true });
  async function save() {
    err.value = '';
    try {
      await api(path, { method: 'PUT', body: f.value });
      await sso.reload(true);
      reset();
      toast('Saved. Fleetdeck can reach your provider.', 'ok');
    } catch (e) { err.value = e.message; }
  }
  async function remove(body) {
    if (!await confirmBox({ title: 'Remove single sign-on?', body, confirm: 'Remove', danger: true })) return;
    await api(path, { method: 'DELETE' });
    await sso.reload(true);
    reset();
    toast('Single sign-on removed');
  }
  return { sso, f, err, save, remove };
}
