<script setup>
import {defineAsyncComponent} from 'vue'
const View=defineAsyncComponent(()=>location.pathname.includes('/auth/')?import('./views/security/SecurityApp.vue'):import('./App.vue'))
</script>
<template><Suspense><component :is="View"/><template #fallback><p class="p-6 text-sm text-ink-gray-5">Loading Relay…</p></template></Suspense></template>
