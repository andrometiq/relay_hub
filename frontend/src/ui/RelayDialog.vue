<script setup>
import { computed } from 'vue'
import PrimeDialog from 'primevue/dialog'
const props = defineProps({ modelValue: Boolean, options: { type: Object, default: () => ({}) }, disableOutsideClickToClose: Boolean })
const emit = defineEmits(['update:modelValue', 'close'])
const size = computed(() => `relay-dialog--${props.options.size || 'md'}`)
const close = () => { emit('update:modelValue', false); emit('close') }
const pt = { mask: { class: 'relay-dialog-mask' }, root: { class: ['relay-dialog', size.value] }, header: { class: 'relay-dialog__header' }, title: { class: 'relay-dialog__title' }, closeButton: { class: 'relay-dialog__close' }, content: { class: 'relay-dialog__content' }, footer: { class: 'relay-dialog__footer' } }
</script>
<template><PrimeDialog :visible="modelValue" modal :header="options.title" :dismissable-mask="!disableOutsideClickToClose" :close-on-escape="!disableOutsideClickToClose" :pt="pt" @update:visible="value => value ? $emit('update:modelValue', true) : close()"><slot name="body-content" /><slot /><template v-if="$slots.actions" #footer><slot name="actions" :close="close" /></template></PrimeDialog></template>
