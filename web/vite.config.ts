import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// During `vite dev` the UI runs on :5173 and proxies /api to the Go agent on
// :8080. In production the built assets are embedded into the agent binary
// (go:embed) and served same-origin, so no proxy is needed.
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
  build: {
    // Output into the api package so go:embed can pick it up.
    outDir: '../internal/api/dist',
    emptyOutDir: true,
  },
})
