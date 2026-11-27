<script setup>
import { ref } from 'vue';
import { getTheme, setTheme } from '@/lib/theme.js';
import { getClock, setClock } from '@/lib/format.js';
import SettingRow from './SettingRow.vue';

const THEMES = [['system', 'Match system'], ['light', 'Light'], ['dark', 'Dark']];
const CLOCKS = [['', 'Browser default'], ['24', '24-hour'], ['12', '12-hour']];
const theme = ref(getTheme());
const clock = getClock();
const pick = (t) => { setTheme(t); theme.value = t; };
</script>
<template>
  <div class="page-h"><div><h1>Preferences</h1><p class="sub">How Fleetdeck looks in this browser. Saved here only, not on your account.</p></div></div>
  <section class="panel">
    <SettingRow label="Theme" desc="Match system follows the light or dark mode of your operating system.">
      <div class="seg" role="group" aria-label="Theme"><button v-for="[k, l] in THEMES" :key="k" type="button" :aria-pressed="theme === k" @click="pick(k)">{{ l }}</button></div>
    </SettingRow>
    <SettingRow label="Time format" desc="Used for every time and date. Changing it reloads the page.">
      <div class="seg" role="group" aria-label="Time format"><button v-for="[k, l] in CLOCKS" :key="k" type="button" :aria-pressed="clock === k" @click="clock !== k && setClock(k)">{{ l }}</button></div>
    </SettingRow>
  </section>
</template>
