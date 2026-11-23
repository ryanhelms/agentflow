import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  root: 'src',
  plugins: [react()],
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
