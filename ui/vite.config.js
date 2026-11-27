import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath } from 'node:url';

// Dev: `npm run dev` serves the UI on :5173 and proxies the API (and the terminal websocket) to the Go server.
// No changeOrigin: the backend checks Origin against Host, so both must stay the dev server's.
const backend = process.env.FLEETDECK_DEV_BACKEND || 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: { proxy: { '/api': { target: backend, ws: true } } },
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 900 },
});
