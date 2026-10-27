/// <reference types="vitest/config" />
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'
import { defineConfig } from 'vite'

// Dev: the SPA runs on :5178 (the origin IAMKit and the API CORS allow) and
// proxies the API, so the browser only ever talks to one origin.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: [
      { find: '@', replacement: path.resolve(import.meta.dirname, './src') },
      // The shadcn base-nova registry emits `import { cn } from "cn"`.
      { find: /^cn$/, replacement: path.resolve(import.meta.dirname, './src/lib/utils.ts') },
    ],
  },
  test: { environment: 'jsdom', setupFiles: ['./src/test-setup.ts'], include: ['src/**/*.test.{ts,tsx}'], css: false },
  build: { assetsInlineLimit: file => (file.endsWith('.woff2') ? false : undefined) },
  server: {
    host: 'localhost',
    port: 5178,
    strictPort: true,
    proxy: Object.fromEntries(
      ['/api', '/health'].map(p => [p, { target: process.env.ODYSSEY_API_URL || 'http://localhost:8095' }]),
    ),
  },
  preview: { port: 5178, strictPort: true },
})
