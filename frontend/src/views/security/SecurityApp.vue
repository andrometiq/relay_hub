<script setup>
import HubStatus from "../../components/HubStatus.vue";
import ThemeSwitcher from "../../components/ThemeSwitcher.vue";
import {computed,defineAsyncComponent,onMounted,onBeforeUnmount,ref} from 'vue'
import {Button,FeatherIcon} from '@relay/ui'
import {prefix,securityRequest} from '../../composables/security'
const view=ref(location.pathname.split('/').at(-1)),data=ref(null),error=ref(''),mobile=ref(false)
const icons={users:'users',tokens:'key',password:'lock',sessions:'monitor'}
const views={login:defineAsyncComponent(()=>import('./LoginView.vue')),users:defineAsyncComponent(()=>import('./UsersView.vue')),tokens:defineAsyncComponent(()=>import('./TokensView.vue')),sessions:defineAsyncComponent(()=>import('./SessionsView.vue')),password:defineAsyncComponent(()=>import('./PasswordView.vue'))}
const title={users:'Users',tokens:'Auth keys',password:'Password',sessions:'Sessions'}
const nav=computed(()=>Object.entries(title).filter(([key])=>key!=='users'||data.value?.User?.role==='admin'))
const status=ref('Checking'),updated=ref(''),revision=ref(0);
let generation=0
async function refresh(){const ticket=++generation;error.value='';try{const result=await securityRequest(view.value);if(ticket===generation){data.value=result;revision.value++;status.value="Ready";updated.value=new Date().toLocaleTimeString()}}catch(e){if(ticket===generation){error.value=e.message;status.value="Unavailable"}}}
function navigate(key){mobile.value=false;history.pushState({},'',prefix+'/auth/'+key);view.value=key;data.value=null;refresh()}
function back(){mobile.value=false;view.value=location.pathname.split('/').at(-1);data.value=null;refresh()}
onMounted(()=>window.addEventListener('popstate',back));onBeforeUnmount(()=>window.removeEventListener('popstate',back))
async function logout(){try{await securityRequest('logout',{csrf:data.value.CSRF})}catch(e){error.value=e.message}}
onMounted(refresh)
</script>
<template><div v-if="view==='login'" class="min-h-screen bg-surface-gray-1 flex items-center justify-center p-5"><div class="fixed top-3 right-3"><ThemeSwitcher /></div><div class="w-full max-w-sm"><div class="flex items-center justify-center gap-2 mb-8"><FeatherIcon name="radio" class="w-6 h-6 text-blue-600"/><h1 class="text-xl font-semibold">Relay Hub</h1></div><div class="bg-surface-white border border-outline-gray-2 rounded-xl p-7"><component v-if="data" :is="views.login" :data="data"/><p v-else>{{error||'Loading…'}}</p></div></div></div><div v-else class="hub-shell" @keydown.esc="mobile=false">
  <button v-if="mobile" class="hub-nav-backdrop" aria-label="Close navigation" @click="mobile=false" />
  <aside class="hub-nav" :class="{'mobile-nav-open':mobile}" aria-label="Account navigation">
    <a :href="prefix+'/'" class="hub-brand" aria-label="Relay Hub home"><span class="hub-brand-mark"><FeatherIcon name="radio" class="h-4 w-4" /></span><strong class="nav-label">Relay Hub</strong></a>
    <a v-for="[path,label] in [['overview','Overview'],['containers','Containers'],['sites','Sites'],['connections','Connections'],['deliveries','Deliveries'],['events','Events']]" :key="path" :href="prefix+'/#/'+path" class="hub-nav-item"><span class="nav-label">{{ label }}</span></a>
    <div class="hub-nav-caption nav-label">Users &amp; access</div>
    <a v-for="[key,label] in nav" :key="key" :href="prefix+'/auth/'+key" @click.prevent="navigate(key)" class="hub-nav-item" :class="{selected:view===key}" :aria-label="label" :title="label" :aria-current="view===key?'page':undefined">
      <FeatherIcon :name="icons[key]" class="h-4 w-4" /><span class="nav-label">{{label}}</span>
    </a>
    <div class="hub-nav-footer"><a :href="prefix+'/'" class="hub-nav-item" aria-label="Hub workspace" title="Hub workspace"><FeatherIcon name="grid" class="h-4 w-4" /><span class="nav-label">Hub workspace</span></a></div>
  </aside>
  <main class="min-w-0 flex flex-col min-h-0">
    <header class="hub-header">
      <button class="hub-mobile-menu" aria-label="Toggle navigation" :aria-expanded="mobile" @click="mobile=!mobile"><FeatherIcon name="menu" class="h-4 w-4" /></button>
      <span class="hub-breadcrumb">Relay Hub</span><span class="hub-breadcrumb text-ink-gray-3">/</span><span class="text-base font-medium">{{title[view]}}</span>
      <HubStatus :status="status" :error="error" :updated="updated" /><div class="ml-auto flex items-center gap-2"><Button label="Refresh" @click="refresh" /><ThemeSwitcher /><Button label="Sign out" :disabled="!data?.CSRF" @click="logout" /></div>
    </header>
    <div class="hub-page overflow-auto">
      <p v-if="error" role="alert" class="text-ink-red-2 mb-4">{{error}}</p>
      <component :key="revision" v-if="data&&views[view]" :is="views[view]" :data="data" @refresh="refresh" />
      <p v-else-if="!error">Loading…</p>
    </div>
  </main>
</div></template>
