import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Icons from 'unplugin-icons/vite'
import { readFileSync } from 'node:fs'
export default defineConfig({
  base: './', plugins: [vue(), Icons({ compiler: 'vue3', customCollections: { lucide: Object.fromEntries(Object.entries(JSON.parse(readFileSync(new URL('./vendor/lucide.json', import.meta.url), 'utf8')).icons).map(([name, icon]) => [name, '<svg viewBox="0 0 24 24">' + icon.body + '</svg>'])) } })], cacheDir: '.vite',
  resolve: { dedupe: ['vue', 'vue-router'] },
  build: { assetsInlineLimit: 200000, cssCodeSplit: false, rollupOptions: { output: { inlineDynamicImports: true } } },
  server: { host: '127.0.0.1' },
})
