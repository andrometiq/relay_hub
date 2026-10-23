<script setup>
import PrimeTag from 'primevue/tag'
defineProps({ label: { type: [String, Number], default: '' }, theme: { type: String, default: 'gray' } })
</script>
<template><PrimeTag :value="String(label)" :class="['relay-badge', `relay-badge--${theme}`]" /></template>
