<script setup>
import { computed } from 'vue'
import { FormControl, Autocomplete, ErrorMessage, Button } from 'frappe-ui'
const props = defineProps({ fields: {type:Array,required:true}, modelValue: {type:Object,required:true}, errors: {type:Object,default:()=>({})} })
const emit = defineEmits(['update:modelValue'])
const set = (key,value) => emit('update:modelValue',{...props.modelValue,[key]:value})
const valueOf = value => value && typeof value === 'object' ? value.value : value
const optionsFor = field => (field.options||[]).map(o=>typeof o==='string'?{label:o,value:o}:o)
const supported = new Set(['Data','Select','Check','Link','MultiSelect','Small Text'])
const visible = computed(()=>props.fields.filter(f=>!f.hidden))
</script>
<template>
 <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-5">
  <div v-for="field in visible" :key="field.name" :class="{'sm:col-span-2':field.wide}">
   <template v-if="field.type==='Link' || field.type==='MultiSelect'">
    <label :id="'label-'+field.name" class="block text-sm text-ink-gray-7 mb-2">{{field.label}}<span v-if="field.required"> *</span></label>
    <Autocomplete :options="optionsFor(field)" :multiple="field.type==='MultiSelect'" :model-value="modelValue[field.name]" :placeholder="field.placeholder||'Select…'" :disabled="field.readOnly" :aria-labelledby="'label-'+field.name" @update:modelValue="value=>set(field.name,Array.isArray(value)?value.map(valueOf):valueOf(value))"/>
   </template>
   <FormControl v-else-if="supported.has(field.type)" :label="field.label" :required="field.required" :type="field.type==='Check'?'checkbox':field.type==='Select'?'select':field.type==='Small Text'?'textarea':'text'" :options="field.options" :placeholder="field.placeholder" :disabled="field.readOnly" :model-value="modelValue[field.name]" @update:modelValue="value=>set(field.name,value)"/>
   <ErrorMessage v-else :message="'Unsupported field: '+field.type"/>
   <ErrorMessage v-if="errors[field.name]" class="mt-2" :message="errors[field.name]"/>
  </div>
 </div>
</template>
