import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', ws: true },
      '/auth': { target: 'http://127.0.0.1:18080' },
    },
  },
  build: { target: 'es2022' },
})
