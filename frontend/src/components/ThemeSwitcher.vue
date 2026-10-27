<script setup>
import { Dropdown, Button } from '@relay/ui'
import { theme, setTheme } from '../services/theme'
const choices=[{value:'light',label:'Light',icon:'sun'},{value:'dark',label:'Dark',icon:'moon'},{value:'system',label:'System',icon:'monitor'}]
</script>
<template><Dropdown :options="choices.map(choice=>({value:choice.value,label:choice.label,icon:choice.icon,checked:theme===choice.value,onClick:()=>setTheme(choice.value)}))"><Button :icon="choices.find(choice=>choice.value===theme)?.icon" aria-label="Appearance" aria-haspopup="menu" title="Appearance" variant="ghost"/></Dropdown></template>
