import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Icons from 'unplugin-icons/vite'
import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
export default defineConfig({
  base: './', plugins: [vue(), Icons({ compiler: 'vue3', customCollections: { lucide: Object.fromEntries(Object.entries(JSON.parse(readFileSync(new URL('./vendor/lucide.json', import.meta.url), 'utf8')).icons).map(([name, icon]) => [name, '<svg viewBox="0 0 24 24">' + icon.body + '</svg>'])) } })], cacheDir: '.vite',
  resolve: {
    alias: { '@relay/ui': fileURLToPath(new URL('./src/ui/index.js', import.meta.url)) },
    dedupe: ['vue', 'vue-router'],
  },
  build: { assetsInlineLimit: 200000, cssCodeSplit: false, rollupOptions: { output: { inlineDynamicImports: false } } },
  server: { host: '127.0.0.1' },
})
