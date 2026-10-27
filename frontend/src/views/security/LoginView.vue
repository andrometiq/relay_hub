<script setup>
import {ref} from 'vue'
import RelayForm from '../../components/RelayForm.vue'
import {securityRequest} from '../../composables/security'
const props=defineProps({data:Object});const model=ref({username:'',password:''}),busy=ref(false),error=ref('')
const fields=[{name:'username',label:'Username',type:'Data',required:true,wide:true,autocomplete:'username'},{name:'password',label:'Password',type:'Password',required:true,wide:true,autocomplete:'current-password'}]
async function submit(){busy.value=true;error.value='';try{await securityRequest('login',{...model.value,csrf:props.data.CSRF})}catch(e){error.value=e.message}finally{busy.value=false}}
</script>
<template><h2 class="text-lg font-medium mb-6">Sign in</h2><RelayForm v-model="model" :fields="fields" label="Sign in" :busy="busy" :error="error" @submit="submit"/></template>
