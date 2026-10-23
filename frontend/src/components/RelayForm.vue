<script setup>
import { ref } from 'vue'
import { Button,ErrorMessage } from '@relay/ui'
import DocumentForm from './DocumentForm.vue'
import { validateFields } from './formValidation'
const props=defineProps({fields:Array,modelValue:Object,label:{default:'Save'},busy:Boolean,error:String})
const emit=defineEmits(['update:modelValue','submit']);const errors=ref({})
function submit(){if(props.busy)return;errors.value=validateFields(props.fields,props.modelValue);if(!Object.keys(errors.value).length)emit('submit')}
</script>
<template><form @submit.prevent="submit" class="space-y-6"><DocumentForm :fields="fields" :model-value="modelValue" :errors="errors" @update:model-value="emit('update:modelValue',$event)"/><ErrorMessage v-if="error" :message="error"/><div class="flex gap-2"><Button type="submit" variant="solid" :label="label" :loading="busy"/><slot name="actions"/></div></form></template>
