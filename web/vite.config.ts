import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';

// Two entries: the public portal (portal, request, status and login pages)
// and the admin console. The Go binary embeds the output directory.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: resolve(__dirname, '../internal/webui/dist/app'),
    emptyOutDir: true,
    assetsInlineLimit: 0,
    rollupOptions: {
      input: {
        portal: resolve(__dirname, 'portal.html'),
        admin: resolve(__dirname, 'admin.html'),
      },
    },
  },
  server: {
    proxy: {
      '^/(api|portal.json|status.json|icons|media|login|logout|request/|healthz)': 'http://127.0.0.1:18080',
    },
  },
});
