<script setup>
// Sortable header cell for useSort(): <SortTh :sort="sort" k="cpu" desc class="num">CPU</SortTh>
defineProps({ sort: Object, k: String, get: Function, desc: Boolean });
</script>
<template>
  <th class="sortable" :aria-sort="sort.key === k ? (sort.dir > 0 ? 'ascending' : 'descending') : 'none'">
    <button type="button" @click="sort.toggle(k, get, desc)"><slot /><span class="arrow" aria-hidden="true">{{ sort.key === k ? (sort.dir > 0 ? '▲' : '▼') : '' }}</span></button>
  </th>
</template>
