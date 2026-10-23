<script setup>
import PrimeMessage from 'primevue/message'
defineProps({ message: { type: [String, Error], default: '' } })
</script>
<template><PrimeMessage v-if="message" severity="error" variant="simple" class="relay-error" role="alert">{{ message instanceof Error ? message.message : message }}</PrimeMessage></template>
