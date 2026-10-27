<script setup>
import { FormControl } from '@relay/ui'
defineProps({field:Object,modelValue:[Boolean,Number]});defineEmits(['update:modelValue'])
</script>
<template><FormControl type="checkbox" :label="field.label" :model-value="!!modelValue" :disabled="field.readOnly" @update:model-value="$emit('update:modelValue',$event)"/></template>
