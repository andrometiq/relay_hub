import test from 'node:test'
import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import {computed,ref,watch} from 'vue'
const source=(await readFile(new URL('../src/components/fields/ChoiceField.vue',import.meta.url),'utf8'))
 .split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
const make=new Function('computed','ref','watch','onBeforeUnmount','useId','defineProps','defineEmits',source+'\nreturn {focus,schedule,keydown,compositionStart,compositionEnd,query,close};')
for(const multiple of [false,true])test(`IME completion and Enter selection (${multiple?'multiple':'single'})`,async()=>{
 const queries=[],events=[]
 const props={multiple,modelValue:multiple?['existing']:'',field:{search:async q=>{queries.push(q);return [{value:'new',label:'தமிழ்'}]}}}
 const field=make(computed,ref,watch,()=>{},()=> 'test',()=>props,()=> (...args)=>events.push(args))
 const key=(extra={})=>({key:'Enter',preventDefault(){},...extra})
 field.focus();await new Promise(r=>setTimeout(r,0))
 field.keydown(key({isComposing:true}));field.keydown(key({keyCode:229}))
 assert.equal(events.length,0)
 field.compositionStart();field.query.value='த';field.schedule({isComposing:true})
 await new Promise(r=>setTimeout(r,350))
 assert.deepEqual(queries,[''],'No partial composition search')
 field.query.value='தமிழ்';field.compositionEnd()
 await new Promise(r=>setTimeout(r,350))
 assert.deepEqual(queries,['','தமிழ்'])
 field.keydown(key({repeat:true}));assert.equal(events.length,0)
 field.keydown(key())
 assert.deepEqual(events,[['update:modelValue',multiple?['existing','new']:'new']])
 field.close()
})
