<script setup>
import {ref,computed,onMounted,onBeforeUnmount,nextTick,watch} from 'vue'
import {Button} from '@relay/ui'
import FrappeRows from './FrappeRows.vue'
const props=defineProps({load:Function,query:String,rowHeight:{default:76}})
const items=ref([]),busy=ref(false),error=ref(''),more=ref(true),earlier=ref(false),box=ref();let generation=0
const id=row=>row.id??row.ID

async function fetchPage(direction='after',reset=false){if(busy.value&&!reset)return;const ticket=reset?++generation:generation;busy.value=true;error.value='';try{const cursor=reset?'':direction==='before'?id(items.value[0]||{}):id(items.value.at(-1)||{});const result=await props.load({cursor:cursor||'',direction,q:props.query||''});if(ticket!==generation)return;const old=items.value;items.value=reset?result.items:direction==='before'?[...result.items,...old]:[...old,...result.items];if(direction==='after'){more.value=result.more;if(reset)earlier.value=false}else earlier.value=result.more;let offset=direction==='before'?result.items.length*props.rowHeight:0;if(items.value.length>200){const drop=items.value.length-200;if(direction==='after'){items.value=items.value.slice(drop);offset-=drop*props.rowHeight;earlier.value=true}else{items.value=items.value.slice(0,200);more.value=true}}await nextTick();if(box.value?.element){box.value.element.scrollTop=reset?0:Math.max(0,box.value.element.scrollTop+offset)}}catch(e){if(ticket===generation)error.value=e.message}finally{if(ticket===generation)busy.value=false}}
function onScroll(el){
 if(el.scrollTop<250&&earlier.value&&!busy.value)fetchPage('before')
 else if(el.scrollHeight-el.scrollTop-el.clientHeight<250&&more.value&&!busy.value)fetchPage()
}
let timer;watch(()=>props.query,()=>{clearTimeout(timer);timer=setTimeout(()=>fetchPage('after',true),250)})
onMounted(()=>fetchPage('after',true));onBeforeUnmount(()=>{generation++;clearTimeout(timer)})
defineExpose({reload:()=>fetchPage('after',true)})
</script>
<template><div><div class="infinite-list rounded-lg border border-outline-gray-2 bg-surface-white flex flex-col" style="height:min(68vh,720px)"><FrappeRows ref="box" :items="items" :row-height="rowHeight" label="Records" @scroll="onScroll"><template #default="{item}"><div class="border-b border-outline-gray-1 h-full"><slot :row="item"/></div></template></FrappeRows><div v-if="!items.length&&!busy" class="p-8 text-center text-ink-gray-5">No results</div><div v-if="busy" role="status" class="infinite-list__status">Loading…</div><div v-if="error" class="p-3 text-red-600" role="alert">{{error}} <Button label="Retry" @click="fetchPage()"/></div></div></div></template>
