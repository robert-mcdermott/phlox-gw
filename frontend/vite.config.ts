import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Dev: Vite serves the React app and proxies API/gateway routes to the Go
// server (default 127.0.0.1:8080, override with VITE_PROXY_TARGET).
// Build: emits to ./dist, which the Go binary embeds via //go:embed frontend/dist/*.
const proxyTarget = process.env.VITE_PROXY_TARGET || 'http://127.0.0.1:8080'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: proxyTarget, changeOrigin: true },
      '/v1': { target: proxyTarget, changeOrigin: true },
      '/anthropic': { target: proxyTarget, changeOrigin: true },
      '/health': { target: proxyTarget, changeOrigin: true },
      '/ready': { target: proxyTarget, changeOrigin: true },
    },
  },
})
