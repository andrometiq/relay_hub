import './services/theme'
import { createApp } from 'vue'
import PrimeVue from 'primevue/config'
import App from './Root.vue'
import './style.css'
createApp(App).use(PrimeVue, { unstyled: true }).mount('#app')
