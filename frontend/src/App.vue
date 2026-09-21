<script setup>
import { ref, onMounted } from 'vue'
import { Button, Badge } from 'frappe-ui'
const status=ref('Checking'), busy=ref(false)
async function refresh(){busy.value=true;try{const response=await fetch('/readyz');status.value=response.ok?'Ready':'Unavailable'}catch{status.value='Unavailable'}finally{busy.value=false}}
onMounted(refresh)
</script>
<template><div class="min-h-screen bg-surface-gray-1 text-ink-gray-9"><header class="h-16 border-b bg-surface-white flex items-center px-8 gap-3"><strong>Relay Hub</strong><Badge label="Development"/></header><main class="max-w-3xl mx-auto p-8"><div class="flex items-center justify-between mb-6"><h1 class="text-2xl font-semibold">Overview</h1><Button label="Refresh" :loading="busy" @click="refresh"/></div><div class="rounded-lg border bg-surface-white p-6 space-y-5"><div class="flex justify-between"><span>Service</span><Badge :label="status" :theme="status==='Ready'?'green':'gray'"/></div><div class="flex justify-between"><span>Database</span><span>PostgreSQL 18</span></div><div class="flex justify-between"><span>Channels</span><span class="text-ink-gray-5">Not configured</span></div></div></main></div></template>
