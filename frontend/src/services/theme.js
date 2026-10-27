import { ref } from 'vue'
const media = window.matchMedia('(prefers-color-scheme: dark)')
const allowed = ['light', 'dark', 'system']
let stored
try { stored = localStorage.getItem('relay-theme') || localStorage.getItem('theme') } catch {}
const site = document.documentElement.dataset.themeMode
export const theme = ref(allowed.includes(stored) ? stored : allowed.includes(site) ? site : 'system')
function apply() {
  const resolved = theme.value === 'system' ? (media.matches ? 'dark' : 'light') : theme.value
  document.documentElement.dataset.theme = resolved
  document.documentElement.style.colorScheme = resolved
}
export function setTheme(value) {
  if (!allowed.includes(value)) return
  theme.value = value
  try { localStorage.setItem('relay-theme', value) } catch {}
  apply()
}
media.addEventListener('change', () => { if (theme.value === 'system') apply() })
window.addEventListener('storage', event => { if (event.key === 'relay-theme') { theme.value=allowed.includes(event.newValue)?event.newValue:'system';apply() } })
apply()
