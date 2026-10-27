<script setup>
import { FormControl } from '@relay/ui'
defineProps({field:Object,modelValue:[String,Number]});defineEmits(['update:modelValue'])
</script>
<template><FormControl :label="field.label" :type="field.type==='Password'?'password':field.type==='Int'?'number':field.type==='Small Text'?'textarea':'text'" :model-value="modelValue" :required="field.required" :disabled="field.readOnly" :placeholder="field.placeholder" :autocomplete="field.autocomplete" :min="field.min" :max="field.max" @update:model-value="$emit('update:modelValue',$event)"/></template>
