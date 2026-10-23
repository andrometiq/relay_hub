<script setup>
import { ref } from 'vue'
import PrimePopover from 'primevue/popover'
import RelayIcon from './RelayIcon.vue'
defineProps({ options: { type: Array, default: () => [] }, placement: { type: String, default: 'bottom' } })
const popover = ref()
const toggle = event => popover.value?.toggle(event)
const choose = option => { if (option.disabled) return; option.onClick?.(); popover.value?.hide() }
const pt = { root: { class: 'relay-menu' }, content: { class: 'relay-menu__content' } }
</script>
<template><span class="relay-dropdown-trigger" @click="toggle"><slot /></span><PrimePopover ref="popover" :pt="pt"><div role="menu" class="relay-menu__items"><button v-for="(option,index) in options" :key="option.value ?? option.label ?? index" type="button" role="menuitem" class="relay-menu__item" :disabled="option.disabled" @click="choose(option)"><RelayIcon v-if="option.icon" :name="option.icon" /><span>{{ option.label }}</span></button></div></PrimePopover></template>
