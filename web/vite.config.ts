import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwind from '@tailwindcss/vite';

export default defineConfig({
  plugins: [react(), tailwind()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8443', changeOrigin: true, secure: false },
      '/events': { target: 'http://127.0.0.1:8443', changeOrigin: true, secure: false, ws: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    target: 'es2022',
  },
});
