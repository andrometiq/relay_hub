<script setup>
import { computed } from 'vue'
import PrimeButton from 'primevue/button'
import RelayIcon from './RelayIcon.vue'

const props = defineProps({
  label: { type: [String, Number], default: '' }, icon: String, iconLeft: String, iconRight: String,
  variant: { type: String, default: 'subtle' }, theme: { type: String, default: 'gray' },
  size: { type: String, default: 'sm' }, loading: Boolean, disabled: Boolean, type: { type: String, default: 'button' },
})
const classes = computed(() => [
  'relay-button', `relay-button--${props.variant}`, `relay-button--${props.theme}`, `relay-button--${props.size}`,
  { 'relay-button--icon-only': !props.label && !useDefault.value, 'is-loading': props.loading },
])
const slots = defineSlots()
const useDefault = computed(() => Boolean(slots.default))
</script>

<template>
  <PrimeButton :type="type" :disabled="disabled || loading" :loading="loading" :class="classes">
    <RelayIcon v-if="loading" name="loader-circle" class="relay-button__spinner" />
    <RelayIcon v-else-if="iconLeft || icon" :name="iconLeft || icon" />
    <span v-if="label" class="relay-button__label">{{ label }}</span><slot v-else />
    <RelayIcon v-if="iconRight" :name="iconRight" />
  </PrimeButton>
</template>
