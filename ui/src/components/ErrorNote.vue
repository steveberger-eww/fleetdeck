<script setup>
import Icon from './Icon.vue';
defineProps({ error: String, retry: Function });
</script>
<template>
  <div class="callout" role="alert"><Icon name="alert" /><span class="grow">{{ error }}</span><button v-if="retry" type="button" class="btn sm" @click="retry()">Try again</button></div>
</template>
