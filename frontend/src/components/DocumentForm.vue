<script setup>
import { computed } from 'vue'
import { ErrorMessage } from '@relay/ui'
import TextField from './fields/TextField.vue'
import SelectField from './fields/SelectField.vue'
import CheckField from './fields/CheckField.vue'
import LinkField from './fields/LinkField.vue'
import MultiSelectField from './fields/MultiSelectField.vue'
const props=defineProps({fields:{type:Array,required:true},modelValue:{type:Object,required:true},errors:{type:Object,default:()=>({})}})
const emit=defineEmits(['update:modelValue'])
const components={Data:TextField,Password:TextField,Int:TextField,'Small Text':TextField,Select:SelectField,Check:CheckField,Link:LinkField,MultiSelect:MultiSelectField}
const visible=computed(()=>props.fields.filter(f=>!f.hidden))
</script>
<template><div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-5"><div v-for="field in visible" :key="field.name" :class="{'sm:col-span-2':field.wide}"><component v-if="components[field.type]" :is="components[field.type]" :field="field" :model-value="modelValue[field.name]" @update:model-value="emit('update:modelValue',{...modelValue,[field.name]:$event})"/><ErrorMessage v-else :message="'Unsupported field: '+field.type"/><ErrorMessage v-if="errors[field.name]" :message="errors[field.name]" class="mt-2"/></div></div></template>
