import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig(({ mode }) => ({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: '../bridge/static',
    emptyOutDir: true,
    // The built SPA is embedded into the Go binary (web/bridge/assets.go),
    // so a source map is ~790 KB of binary for something no deployment
    // reads. Vite's dev server always has source maps regardless.
    sourcemap: false,
  },
  test: {
    // DOMPurify needs a real DOM to bind to, and component tests need one
    // too. Without this vitest defaults to node and sanitize() is absent.
    environment: 'jsdom',
  },
  /*
    Svelte ships a server build and a client build. Without the browser
    condition, vitest resolves the server one and mounting a component
    fails with lifecycle_function_unavailable. Scoped to test mode so the
    production build keeps vite's own resolution.
  */
  resolve: mode === 'test' ? { conditions: ['browser'] } : {},
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:7700',
        changeOrigin: true,
      },
    },
  },
}))
