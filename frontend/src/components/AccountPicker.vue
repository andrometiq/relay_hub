<script setup>
import {ref,watch,onMounted,onBeforeUnmount} from 'vue'
import {FormControl,ErrorMessage} from '@relay/ui'
import {hubApi} from '../services/hubApi'
import {siteChannelOptions} from '../composables/containers'
const props=defineProps({modelValue:String,disabled:Boolean,channel:{type:String,default:'WhatsApp'}}),emit=defineEmits(['update:modelValue'])
const busy=ref(false);
const query=ref(''),sites=ref([]),items=ref([]),error=ref('');let sequence=0,timer
async function search(){const current=++sequence;busy.value=true;try{const data=await hubApi('sites?'+new URLSearchParams({query:query.value.slice(0,80)}));if(current===sequence){sites.value=data.sites||[];error.value=''}}catch(e){if(current===sequence)error.value=e.message}finally{if(current===sequence)busy.value=false}}
function schedule(){clearTimeout(timer);++sequence;timer=setTimeout(search,250)}
watch([sites,()=>props.channel],()=>{items.value=siteChannelOptions(sites.value,props.channel)},{immediate:true})
onMounted(search);onBeforeUnmount(()=>{clearTimeout(timer);++sequence})
</script>
<template><div class="space-y-2"><FormControl label="Search sites" v-model="query" :disabled="disabled" @update:model-value="schedule"/><label class="block text-xs text-ink-gray-6">Site account<select aria-label="Account" class="hub-select mt-1" :value="modelValue||''" :disabled="disabled" @change="emit('update:modelValue',$event.target.value)"><option value="">Select a site account</option><option v-if="modelValue&&!items.some(row=>row.key===modelValue)" :value="modelValue">{{modelValue}}</option><option v-for="item in items" :key="item.key" :value="item.key">{{item.label}}</option></select></label><p v-if="busy" role="status" class="text-sm text-ink-gray-6">Loading sites…</p><p v-else-if="!items.length && !error" class="text-sm text-ink-gray-6">No {{channel}} accounts are linked to a matching site.</p><ErrorMessage :message="error"/></div></template>
