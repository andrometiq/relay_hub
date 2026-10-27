<script setup>
import { ref } from 'vue'
import PrimePopover from 'primevue/popover'
import RelayIcon from './RelayIcon.vue'
defineProps({ options: { type: Array, default: () => [] }, placement: { type: String, default: 'bottom' } })
const popover = ref()
const trigger = ref()
const menu = ref()
const toggle = event => popover.value?.toggle(event)
const items = () => [...(menu.value?.querySelectorAll('.relay-menu__item:not(:disabled)') || [])]
const focusCurrent = () => (items().find(item => item.getAttribute('aria-checked') === 'true') || items()[0])?.focus()
const returnFocus = () => { const active = document.activeElement; if (!active || active === document.body || active.closest('.relay-menu')) trigger.value?.querySelector('button,[tabindex]')?.focus() }
const move = step => { const list = items(); list[(list.indexOf(document.activeElement) + step + list.length) % list.length]?.focus() }
const choose = option => { if (option.disabled) return; option.onClick?.(); popover.value?.hide() }
const pt = { root: { class: 'relay-menu' }, content: { class: 'relay-menu__content' } }
</script>
<template><span ref="trigger" class="relay-dropdown-trigger" @click="toggle"><slot /></span><PrimePopover ref="popover" :pt="pt" @show="focusCurrent" @hide="returnFocus"><div ref="menu" role="menu" class="relay-menu__items" @keydown.down.prevent="move(1)" @keydown.up.prevent="move(-1)" @keydown.tab.prevent="popover?.hide()"><button v-for="(option,index) in options" :key="option.value ?? option.label ?? index" type="button" :role="option.checked === undefined ? 'menuitem' : 'menuitemradio'" :aria-checked="option.checked === undefined ? undefined : String(option.checked)" class="relay-menu__item" :disabled="option.disabled" @click="choose(option)"><RelayIcon v-if="option.icon" :name="option.icon" /><span>{{ option.label }}</span><RelayIcon v-if="option.checked" name="check" class="relay-menu__check" /></button></div></PrimePopover></template>
