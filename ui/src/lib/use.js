// Composables shared by the pages.
import { ref, reactive, computed, watch, onBeforeUnmount, toValue } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { compare, blank } from './format.js';

// Load data and keep it fresh.
//   const jobs = useLoad(() => api(`/api/jobs?${qs.value}`), { key: qs, poll: 5000, paused: held })
// `key` (ref or getter): reload when it changes (the old deps array). `poll` (ms, may be a ref): refresh quietly
// while the tab is visible. `paused` (ref or getter): hold the poll, e.g. while the pointer is over a live table.
// jobs.reload() shows the loading state, jobs.reload(true) refreshes in place. Late answers of stale requests are dropped.
export function useLoad(fn, { key, poll = 0, paused } = {}) {
  const s = reactive({ loading: true, data: null, error: null, reload });
  let seq = 0;
  async function reload(quiet) {
    const n = ++seq;
    if (!quiet) s.loading = true;
    try {
      const data = await fn();
      if (n === seq) Object.assign(s, { loading: false, data, error: null });
    } catch (e) {
      if (n === seq) Object.assign(s, { loading: false, error: e.message });
    }
  }
  const stopKey = watch(() => toValue(key), () => reload(), { immediate: true });
  let timer;
  const tick = () => { if (document.visibilityState === 'visible' && !toValue(paused)) reload(true); };
  const stopPoll = watch(() => toValue(poll), (ms) => { clearInterval(timer); if (ms) timer = setInterval(tick, ms); }, { immediate: true });
  onBeforeUnmount(() => { stopKey(); stopPoll(); clearInterval(timer); seq++; });
  return s;
}

// One URL query key as a writable computed: const tag = useQuery('tag'); tag.value = 'prod'.
// `def` is what an absent key means (and is dropped from the URL). Filters replace the history entry,
// so Back leaves the page instead of undoing keystrokes.
export function useQuery(key, def = '') {
  const route = useRoute();
  const router = useRouter();
  return computed({
    get: () => (route.query[key] ?? def),
    set: (v) => setQuery(router, route, { [key]: v === def ? null : v }),
  });
}
// Patch several query keys at once (null/''/false removes one).
export function setQuery(router, route, patch) {
  const query = { ...route.query, ...patch };
  for (const k of Object.keys(query)) if (query[k] == null || query[k] === '' || query[k] === false) delete query[k];
  return router.replace({ path: route.path, query });
}

// True while the pointer is over the element or focus is inside it: <table v-bind="holdBind">.
export function useHold() {
  const held = ref(false);
  const bind = {
    onPointerenter: () => { held.value = true; },
    onPointerleave: (e) => { held.value = e.currentTarget.contains(document.activeElement); },
    onFocusin: () => { held.value = true; },
    onFocusout: (e) => { held.value = e.currentTarget.contains(e.relatedTarget) || e.currentTarget.matches(':hover'); },
  };
  return [held, bind];
}

// Remembers the first value of the getter; true once it changed. For "discard changes?" on dialogs.
export function useDirty(get) {
  const first = JSON.stringify(toValue(get));
  return computed(() => JSON.stringify(toValue(get)) !== first);
}

// Client-side sorting for tables. const sort = useSort(() => rows, 'cpu', -1); render sort.rows; <SortTh :sort="sort" k="cpu" desc>.
// `k` is a field name; pass :get="(row) => value" on SortTh for computed columns. Blanks always sort last.
export function useSort(rows, key = null, dir = 1, get = null) {
  const s = reactive({ key, dir, get, rows: [], toggle });
  function toggle(k, g, desc) {
    s.dir = s.key === k ? -s.dir : desc ? -1 : 1;
    s.key = k;
    s.get = g || null;
  }
  const sorted = computed(() => {
    const list = toValue(rows) || [];
    if (!s.key) return list;
    const g = s.get || ((r) => r[s.key]);
    return list.slice().sort((a, b) => {
      const va = g(a), vb = g(b);
      return blank(va) || blank(vb) ? compare(va, vb) : s.dir * compare(va, vb);
    });
  });
  watch(sorted, (v) => { s.rows = v; }, { immediate: true });
  return s;
}

// Ticks every second while `on` is true (live durations).
export function useNow(on) {
  const now = ref(Date.now());
  let t;
  watch(() => toValue(on), (v) => { clearInterval(t); if (v) t = setInterval(() => { now.value = Date.now(); }, 1000); }, { immediate: true });
  onBeforeUnmount(() => clearInterval(t));
  return now;
}

// Per-browser conveniences in localStorage (a remembered grouping, collapsed sections). Never for real state.
export function stored(key, def) {
  let v = def;
  try { v = JSON.parse(localStorage.getItem(key)) ?? def; } catch { /* private mode */ }
  const r = ref(v);
  watch(r, (x) => { try { localStorage.setItem(key, JSON.stringify(x)); } catch { /* private mode */ } }, { deep: true });
  return r;
}
