import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

// During `vite dev` the UI runs on :5173 and proxies /api to the Go agent on
// :8080. In production the built assets are embedded into the agent binary
// (go:embed) and served same-origin, so no proxy is needed.
//
// Point the proxy at a remote agent (e.g. a test stand) with VB_API_TARGET:
//   VB_API_TARGET=http://192.0.2.10:8080 npm run dev
const apiTarget = process.env.VB_API_TARGET ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [
    vue(),
    // Element Plus is pulled in per component instead of wholesale (M2.6).
    // The panel is embedded in a binary that runs on a router with 245 MB of
    // RAM and is served from its flash: every kilobyte here is a kilobyte the
    // device stores and parses. Registering the library globally shipped all
    // of it, including the components this panel never renders.
    //
    // The resolvers also pull each component's stylesheet, which is why the
    // global `element-plus/dist/index.css` import could be dropped.
    AutoImport({ resolvers: [ElementPlusResolver()], dts: 'src/auto-imports.d.ts' }),
    Components({ resolvers: [ElementPlusResolver()], dts: 'src/components.d.ts' }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': apiTarget,
    },
  },
  build: {
    // Output into the api package so go:embed can pick it up.
    outDir: '../internal/api/dist',
    emptyOutDir: true,
  },
})
