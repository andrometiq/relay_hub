<script setup>
import {ref} from 'vue'
import RelayForm from '../../components/RelayForm.vue'
import {securityRequest} from '../../composables/security'
const props=defineProps({data:Object});const model=ref({old_password:'',password:'',revoke_tokens:false}),busy=ref(false),error=ref('')
const fields=[{name:'old_password',label:'Current password',type:'Password',required:true,wide:true,autocomplete:'current-password'},{name:'password',label:'New password',type:'Password',required:true,minLength:14,wide:true,autocomplete:'new-password'},{name:'revoke_tokens',label:'Also revoke my API tokens and disconnect integrations',type:'Check',wide:true}]
async function submit(){busy.value=true;try{await securityRequest('password',{...model.value,csrf:props.data.CSRF})}catch(e){error.value=e.message}finally{busy.value=false}}
</script>
<template><section class="max-w-xl rounded-lg border border-outline-gray-2 bg-surface-white p-6"><h1 class="text-xl font-semibold mb-2">Change password</h1><p class="text-sm text-ink-gray-5 mb-6">Signs out your browser sessions. Existing integration tokens stay active unless you choose to revoke them below.</p><RelayForm v-model="model" :fields="fields" label="Change password" :busy="busy" :error="error" @submit="submit"/></section></template>
