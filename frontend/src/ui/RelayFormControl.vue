<script setup>
import { computed, useAttrs } from 'vue'
import PrimeCheckbox from 'primevue/checkbox'
import PrimeInputText from 'primevue/inputtext'
import PrimeSelect from 'primevue/select'
import PrimeTextarea from 'primevue/textarea'

defineOptions({ inheritAttrs: false })
const props = defineProps({
  modelValue: { default: '' }, label: String, type: { type: String, default: 'text' }, options: { type: Array, default: () => [] },
  disabled: Boolean, required: Boolean, rows: { type: [Number, String], default: 3 },
})
const emit = defineEmits(['update:modelValue'])
const attrs = useAttrs()
const inputAttrs = computed(() => { const { class: _class, ...rest } = attrs; return rest })
const normalizedOptions = computed(() => props.options.map(option => typeof option === 'object'
  ? { label: option.label ?? option.value, value: option.value ?? option.label, disabled: Boolean(option.disabled) }
  : { label: option, value: option }))
const inputType = computed(() => ['Data', 'Small Text', 'Int'].includes(props.type) ? ({ Data: 'text', 'Small Text': 'textarea', Int: 'number' }[props.type]) : props.type)
const id = `relay-field-${Math.random().toString(36).slice(2)}`
const update = value => emit('update:modelValue', value)
const selectPt = { overlay: { class: 'relay-select-overlay' }, list: { class: 'relay-select-list' }, option: { class: 'relay-select-option' }, label: { class: 'relay-select-label' }, dropdown: { class: 'relay-select-trigger' } }
const checkboxPt = { root: { class: 'relay-checkbox' }, input: { class: 'relay-checkbox__input' }, box: { class: 'relay-checkbox__box' }, icon: { class: 'relay-checkbox__icon' } }
</script>

<template>
  <label v-if="inputType === 'checkbox'" :class="['relay-checkbox-field', attrs.class]" :for="id">
    <PrimeCheckbox :input-id="id" :model-value="Boolean(modelValue)" binary :disabled="disabled" :pt="checkboxPt" v-bind="inputAttrs" @update:model-value="update" />
    <span v-if="label">{{ label }}<span v-if="required" aria-hidden="true"> *</span></span>
  </label>
  <label v-else :class="['relay-field', attrs.class]" :for="id">
    <span v-if="label" class="relay-field__label">{{ label }}<span v-if="required" aria-hidden="true"> *</span></span>
    <PrimeSelect v-if="inputType === 'select'" :input-id="id" :model-value="modelValue" :options="normalizedOptions" option-label="label" option-value="value" option-disabled="disabled" :disabled="disabled" :pt="selectPt" v-bind="inputAttrs" class="relay-control relay-select" @update:model-value="update" />
    <PrimeTextarea v-else-if="inputType === 'textarea'" :id="id" :model-value="modelValue" :rows="Number(rows)" :disabled="disabled" :required="required" v-bind="inputAttrs" class="relay-control relay-textarea" @update:model-value="update" />
    <PrimeInputText v-else :id="id" :model-value="modelValue" :type="inputType" :disabled="disabled" :required="required" v-bind="inputAttrs" class="relay-control" @update:model-value="update" />
  </label>
</template>
