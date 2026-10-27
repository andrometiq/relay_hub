<script setup>
import PrimeInputText from 'primevue/inputtext'
defineOptions({ inheritAttrs: false })
defineProps({ modelValue: { default: '' }, type: { type: String, default: 'text' } })
defineEmits(['update:modelValue'])
</script>
<template><label class="relay-input-shell" :class="$attrs.class"><span v-if="$slots.prefix" class="relay-input-prefix"><slot name="prefix" /></span><PrimeInputText :model-value="modelValue" :type="type" v-bind="{...$attrs,class:undefined}" class="relay-input" @update:model-value="$emit('update:modelValue', $event)" /></label></template>
