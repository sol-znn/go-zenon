import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// In the lab image labd serves this page and answers /rpc and /api itself.
// `npm run dev` is the same page with labd beside it: start labd on 8470
// (`go run .` in lab/server) and the dev server forwards to it. LAB_BACKEND
// points it elsewhere.
const backend = process.env.LAB_BACKEND ?? 'http://127.0.0.1:8470'

export default defineConfig({
  plugins: [vue()],
  build: {
    target: 'es2022',
    // Two pages: the console at /, and the scenarios page at /scenarios/,
    // which labd serves from the directory of that name.
    rollupOptions: {
      input: { console: 'index.html', scenarios: 'scenarios/index.html' },
    },
    // Mermaid is the bulk of the bundle and only the swap bench draws with
    // it, so it loads when that bench opens.
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5184,
    proxy: {
      '/rpc': { target: backend, changeOrigin: false },
      '/api': { target: backend, changeOrigin: false },
    },
  },
  test: {
    include: ['tests/**/*.test.ts'],
    environment: 'node',
  },
})
