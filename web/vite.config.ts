import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const backend = 'http://127.0.0.1:8096'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': backend,
      '/emby': backend,
      '/health': backend,
      '/Items': backend,
      '/Videos': backend,
      '/Users': backend,
      '/System': backend,
      '/Sessions': backend,
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
})
