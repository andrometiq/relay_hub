<script setup>
import ChoiceField from './ChoiceField.vue'
defineProps({field:Object,modelValue:[String,Object,Array]})
defineEmits(['update:modelValue'])
</script>
<template><ChoiceField :field="field" :model-value="modelValue" multiple @update:model-value="$emit('update:modelValue',$event)"/></template>
