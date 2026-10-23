<script setup>
// Frappe DataTable 1.20.7's renderer uses HyperList 1.0.0 with fixed itemHeight,
// total, generate and refresh. This bridge keeps Vue slots in those DOM rows.
import HyperList from 'hyperlist'
import {ref,shallowRef,watch,onMounted,onBeforeUnmount} from 'vue'
const props=defineProps({items:{type:Array,required:true},rowHeight:{default:76},label:{default:'Records'}})
const emit=defineEmits(['scroll'])
const viewport=ref(),host=ref(),mounted=shallowRef([])
let engine,observer,resizeFrame,lastSize='',nodes=new Map(),destroyed=false
function stop(){engine?.destroy();engine=null}
function refresh(){
 const el=viewport.value;if(!el||destroyed)return
 if(document.hidden){stop();return}
 const height=host.value.clientHeight
 if(!props.items.length||height<1){stop();mounted.value=[];nodes.clear();el.replaceChildren();return}
 const generated=new Map()
 const config={width:'100%',height,itemHeight:props.rowHeight,total:props.items.length,scrollerTagName:'div',
  generate(index){const item=props.items[index],key=String(item.id??item.ID);let node=nodes.get(key);if(!node){node=document.createElement('div');node.dataset.rowId=key;node.style.width='100%';node.style.overflow='hidden'}node.style.height=props.rowHeight+'px';generated.set(key,{key,item,node});return node},
  applyPatch(element,fragment){const entries=[...fragment.children].filter(node=>node.dataset.rowId).map(node=>generated.get(node.dataset.rowId));nodes=new Map(entries.map(entry=>[entry.key,entry.node]));mounted.value=entries;generated.clear();element.replaceChildren(fragment)},
 }
 if(engine)engine.refresh(el,config);else engine=new HyperList(el,config)
}
watch(()=>[props.items,props.rowHeight],refresh,{flush:'post'})
function visibility(){if(document.hidden)stop();else refresh()}
onMounted(()=>{observer=new ResizeObserver(()=>{const size=host.value.clientWidth+':'+host.value.clientHeight;if(size===lastSize)return;lastSize=size;cancelAnimationFrame(resizeFrame);resizeFrame=requestAnimationFrame(refresh)});observer.observe(host.value);document.addEventListener('visibilitychange',visibility);refresh()})
onBeforeUnmount(()=>{destroyed=true;stop();observer?.disconnect();cancelAnimationFrame(resizeFrame);document.removeEventListener('visibilitychange',visibility);nodes.clear()})
defineExpose({get element(){return viewport.value},refresh})
</script>
<template><div ref="host" class="min-h-0 h-full flex-1"><div ref="viewport" class="h-full overflow-auto" style="overflow-anchor:none" :aria-label="label" @scroll.passive="emit('scroll',$event.target)"/><Teleport v-for="entry in mounted" :key="entry.key" :to="entry.node"><slot :item="entry.item"/></Teleport></div></template>
