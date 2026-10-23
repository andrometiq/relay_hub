<script setup>
import {ref,onMounted,onBeforeUnmount,nextTick,watch} from 'vue'
import {Button} from '@relay/ui'
import {useMobile} from '../composables/useMobile'
import FrappeRows from './FrappeRows.vue'
const mobile=useMobile(),mobileBox=ref();
const props=defineProps({load:Function,query:String,rowHeight:{default:76}})
const items=ref([]),busy=ref(false),error=ref(''),more=ref(true),earlier=ref(false),box=ref();let generation=0
const id=row=>row.id??row.ID

function mobileAnchor(nextItems) {
 const el=mobileBox.value;
 if(!el)return;
 const retained=new Set(nextItems.map(item=>String(id(item))));
 const viewport=el.getBoundingClientRect();
 for(const row of el.children){
  const rect=row.getBoundingClientRect();
  if(rect.bottom>viewport.top&&rect.top<viewport.bottom&&retained.has(row.dataset.rowId)){
   return {id:row.dataset.rowId,top:rect.top-viewport.top};
  }
 }
}

async function fetchPage(direction='after',reset=false){
 if(busy.value&&!reset)return;
 const ticket=reset?++generation:generation;
 busy.value=true;error.value='';
 try{
  const cursor=reset?'':direction==='before'?id(items.value[0]||{}):id(items.value.at(-1)||{});
  const result=await props.load({cursor:cursor||'',direction,q:props.query||''});
  if(ticket!==generation)return;
  let nextItems=reset?result.items:direction==='before'?[...result.items,...items.value]:[...items.value,...result.items];
  if(direction==='after'){more.value=result.more;if(reset)earlier.value=false}else earlier.value=result.more;
  let offset=direction==='before'?result.items.length*props.rowHeight:0;
  if(nextItems.length>200){
   const drop=nextItems.length-200;
   if(direction==='after'){nextItems=nextItems.slice(drop);offset-=drop*props.rowHeight;earlier.value=true}
   else{nextItems=nextItems.slice(0,200);more.value=true}
  }
  // Mobile cards have variable heights. Measure a surviving visible record
  // immediately before updating, including any scrolling during the request.
  const anchor=!reset&&mobile.value?mobileAnchor(nextItems):null;
  items.value=nextItems;
  await nextTick();
  if(ticket!==generation)return;
  const el=mobile.value?mobileBox.value:box.value?.element;
  if(!el)return;
  if(reset)el.scrollTop=0;
  else if(mobile.value){
   const row=anchor&&[...el.children].find(row=>row.dataset.rowId===anchor.id);
   if(row)el.scrollTop+=row.getBoundingClientRect().top-el.getBoundingClientRect().top-anchor.top;
  }else el.scrollTop=Math.max(0,el.scrollTop+offset);
 }catch(e){if(ticket===generation)error.value=e.message}
 finally{if(ticket===generation)busy.value=false}
}
function onScroll(el){
 if(el.scrollTop<250&&earlier.value&&!busy.value)fetchPage('before')
 else if(el.scrollHeight-el.scrollTop-el.clientHeight<250&&more.value&&!busy.value)fetchPage()
}
let timer;watch(()=>props.query,()=>{clearTimeout(timer);timer=setTimeout(()=>fetchPage('after',true),250)})
onMounted(()=>fetchPage('after',true));onBeforeUnmount(()=>{generation++;clearTimeout(timer)})
defineExpose({reload:()=>fetchPage('after',true)})
</script>
<template><div><div class="infinite-list rounded-lg border border-outline-gray-2 bg-surface-white flex flex-col" style="height:min(68vh,720px)"><div v-if="mobile" ref="mobileBox" class="overflow-auto flex-1" style="overflow-anchor:none" @scroll.passive="onScroll($event.target)"><div v-for="item in items" :key="id(item)" :data-row-id="id(item)" class="border-b border-outline-gray-1"><slot :row="item" /></div></div><FrappeRows v-else ref="box" :items="items" :row-height="rowHeight" label="Records" @scroll="onScroll"><template #default="{item}"><div class="border-b border-outline-gray-1 h-full"><slot :row="item"/></div></template></FrappeRows><div v-if="!items.length&&!busy&&!error" class="p-8 text-center text-ink-gray-5">No results</div><div v-if="busy" role="status" class="infinite-list__status">Loading…</div><div v-if="error" class="p-3 text-red-600" role="alert">{{error}} <Button label="Retry" @click="fetchPage('after',!items.length)"/></div></div></div></template>
