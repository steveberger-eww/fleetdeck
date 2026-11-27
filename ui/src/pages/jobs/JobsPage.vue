<script setup>
import { JobsTable } from '@/components';
import { useQuery } from '@/lib/use.js';

const status = useQuery('status');
const FILTERS = [['', 'All'], ['running', 'Running'], ['failed', 'Failed']];
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div><h1>Jobs</h1><p class="sub">Every script run, with per-server output. Kept for 90 days by default.</p></div>
    </div>
    <section class="panel">
      <div class="panel-h">
        <div class="seg" role="group" aria-label="Status">
          <button v-for="[k, l] in FILTERS" :key="k" type="button" :aria-pressed="status === k" @click="status = k">{{ l }}</button>
        </div>
      </div>
      <JobsTable :limit="40" :status="status" />
    </section>
  </div>
</template>
