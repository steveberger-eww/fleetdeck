<script setup>
// A numbered single sign-on step; `done` shows a check, `off` dims it (not reachable yet).
import { Icon } from '@/components';
defineProps({ n: [Number, String], title: String, done: Boolean, off: Boolean });
</script>
<template>
  <section :class="['panel', off && 'off']">
    <div class="panel-h"><h2><span :class="['n', done && 'ok']"><Icon v-if="done" name="check" :size="13" /><template v-else>{{ n }}</template></span>{{ title }}<span v-if="done" class="sr">(done)</span></h2></div>
    <div class="panel-b form"><slot /></div>
  </section>
</template>
<style scoped>
h2 { display: flex; align-items: center; gap: 10px; }
.n { display: inline-grid; place-items: center; flex: none; width: 22px; height: 22px; border-radius: 50%; background: var(--panel-2); border: 1px solid var(--line-2); color: var(--ink-2); font-size: var(--fs-xs); font-weight: 600; }
.n.ok { background: var(--ok-soft); color: var(--ok); border-color: transparent; }
.off .panel-b { opacity: .55; }
</style>
