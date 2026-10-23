<script setup>
defineProps({field:Object,modelValue:[String,Number]});defineEmits(['update:modelValue'])
</script>
<template><label class="block text-sm text-ink-gray-7">{{field.label}}<select class="mt-2 block w-full rounded bg-surface-gray-2 border-0 px-3 py-2 text-sm" :value="modelValue" :required="field.required" :disabled="field.readOnly" @change="$emit('update:modelValue',$event.target.value)"><option value="" disabled>Select…</option><option v-for="o in field.options||[]" :key="o.value??o" :value="o.value??o">{{o.label??o}}</option></select></label></template>
