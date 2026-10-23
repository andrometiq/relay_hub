<script setup>
import {ref,onMounted,onBeforeUnmount} from 'vue'
import {FormControl,ErrorMessage} from '@relay/ui'
import {hubApi} from '../services/hubApi'
const props=defineProps({modelValue:String,disabled:Boolean}),emit=defineEmits(['update:modelValue'])
const busy=ref(false);
const query=ref(''),items=ref([]),error=ref('');let sequence=0,timer
async function search(){const current=++sequence;busy.value=true;try{const data=await hubApi('accounts?'+new URLSearchParams({query:query.value.slice(0,80)}));if(current===sequence){items.value=data.accounts||[];error.value=''}}catch(e){if(current===sequence)error.value=e.message}finally{if(current===sequence)busy.value=false}}
function schedule(){clearTimeout(timer);++sequence;timer=setTimeout(search,250)}
onMounted(search);onBeforeUnmount(()=>{clearTimeout(timer);++sequence})
</script>
<template><div class="space-y-2"><FormControl label="Search accounts" v-model="query" :disabled="disabled" @update:model-value="schedule"/><label class="block text-xs text-ink-gray-6">Account<select aria-label="Account" class="hub-select mt-1" :value="modelValue||''" :disabled="disabled" @change="emit('update:modelValue',$event.target.value)"><option value="">Select account</option><option v-if="modelValue&&!items.some(row=>row.id===modelValue)" :value="modelValue">{{modelValue}}</option><option v-for="item in items" :key="item.id" :value="item.id">{{item.id}}</option></select></label><p v-if="busy" role="status" class="text-sm text-ink-gray-6">Loading accounts…</p><p v-else-if="!items.length && !error" class="text-sm text-ink-gray-6">No matching accounts.</p><ErrorMessage :message="error"/></div></template>
