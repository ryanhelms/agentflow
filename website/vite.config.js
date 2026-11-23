import { defineConfig } from 'vite'

export default defineConfig({
  root: 'src',
  server: {
    port: 5173,
    strictPort: true,
    watch: process.env.VITE_USE_POLLING === '1' ? { usePolling: true, interval: 250 } : undefined,
  },
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
})
