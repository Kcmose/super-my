import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src')
    }
  },
  server: {
    port: 3001,
    proxy: {
      '/api/v1/auth': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      },
      '/api/v1/admin': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      },
      '/api/v1/panel': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      },
      '/api/v1/setup': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    sourcemap: false
  }
})
