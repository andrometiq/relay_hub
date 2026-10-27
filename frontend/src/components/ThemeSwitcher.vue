<script setup>
import { Dropdown, Button } from '@relay/ui'
import { theme, setTheme } from '../services/theme'
const choices=[{value:'light',label:'Light',icon:'sun'},{value:'dark',label:'Dark',icon:'moon'},{value:'system',label:'System',icon:'monitor'}]
</script>
<template><Dropdown :options="choices.map(choice=>({label:choice.label+(theme===choice.value?' ✓':''),icon:choice.icon,onClick:()=>setTheme(choice.value)}))"><Button :icon="choices.find(choice=>choice.value===theme)?.icon" label="Appearance" aria-label="Appearance" title="Appearance" variant="ghost"/></Dropdown></template>
