<script setup>
import { computed } from 'vue'
import PrimeAvatar from 'primevue/avatar'
const props = defineProps({ label: { type: String, default: '' }, image: String, size: { type: String, default: 'md' }, shape: { type: String, default: 'circle' } })
const initials = computed(() => props.label.trim().split(/\s+/).slice(0, 2).map(part => part[0]).join('').toUpperCase())
</script>
<template><PrimeAvatar :label="initials" :image="image" :shape="shape" :class="['relay-avatar', `relay-avatar--${size}`]" /></template>
