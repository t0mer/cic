import path from 'node:path'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const target = process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8787'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: false,
    assetsDir: 'assets',
    rolldownOptions: {
      output: {
        // maplibre-gl is only reachable through the lazy Map route: give it its own async chunk.
        codeSplitting: { groups: [{ name: 'maplibre', test: /node_modules[\\/]maplibre-gl/ }] },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': target,
      '/healthz': target,
    },
  },
  test: { environment: 'jsdom', globals: false, include: ['src/**/*.test.{ts,tsx}'] },
})
