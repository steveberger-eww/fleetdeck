<script setup>
import { Empty } from '@/components';
</script>
<template>
  <div class="page"><div class="panel"><Empty title="Page not found" icon="search">The link may be outdated.<template #action><RouterLink class="btn" to="/">Back to the fleet</RouterLink></template></Empty></div></div>
</template>
