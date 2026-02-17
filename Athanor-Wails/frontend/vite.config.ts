import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    watch: {
      ignored: [
        '**/wailsjs/**',
        '**/node_modules/**',
        '**/.git/**',
      ],
      // Windows NTFS 兼容：轮询间隔 1 秒
      usePolling: true,
      interval: 1000,
    },
    hmr: {
      overlay: false,
    },
  },
})