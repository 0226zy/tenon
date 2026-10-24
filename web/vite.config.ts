import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    host: '127.0.0.1',
    port: 7100,
    strictPort: false,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        // 仅可信本地开发代理固定为后端配置的来源；生产仍执行完整 CSRF 校验。
        changeOrigin: true,
        headers: { Origin: 'http://127.0.0.1:8080' },
        ws: true,
      },
      '/preview-local': {
        target: 'http://127.0.0.1:8081',
        changeOrigin: false,
        rewrite: (path) => path.replace(/^\/preview-local/, '') || '/',
        ws: true,
      },
    },
  },
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 1800,
    rollupOptions: {
      output: {
        manualChunks: {
          monaco: ['monaco-editor'],
          vendor: ['vue', 'vue-router', 'pinia'],
        },
      },
    },
  },
})
