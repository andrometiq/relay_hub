<script setup>
import {ref} from 'vue'
import {Button,Badge,FeatherIcon} from '@relay/ui'
import {securityRequest} from '../../composables/security'
const props=defineProps({data:Object});const emit=defineEmits(['refresh']);const busy=ref(false),error=ref('')
async function revoke(values){busy.value=true;error.value='';try{await securityRequest('sessions',{csrf:props.data.CSRF,...values});emit('refresh')}catch(e){error.value=e.message}finally{busy.value=false}}
</script>
<template><div class="flex items-center justify-between gap-3 mb-6"><h1 class="text-xl font-semibold">Sessions</h1><Button label="Sign out others" :loading="busy" @click="revoke({action:'others'})"/></div><p v-if="error" role="alert" class="text-red-600">{{error}}</p><p v-if="!data.Sessions?.length" class="entity-prompt">No active sessions.</p><div class="rounded-lg border border-outline-gray-2 bg-surface-white divide-y divide-outline-gray-2"><article v-for="s in data.Sessions" :key="s.ID" class="p-5 flex gap-4 items-start"><FeatherIcon name="monitor" class="w-5 h-5 text-ink-gray-5 shrink-0 mt-1"/><div class="min-w-0 flex-1"><Badge v-if="s.Current" label="This session" theme="green"/><p class="text-sm text-ink-gray-7 break-words mt-2">{{s.Agent}}</p><p class="text-xs text-ink-gray-5 mt-2">{{s.Seen}}</p></div><Button label="Revoke" :disabled="busy" @click="revoke({id:s.ID})"/></article></div></template>
