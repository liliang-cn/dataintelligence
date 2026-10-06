import path from 'node:path';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// `npm run dev` proxies the API to DI_URL (default: a local `di serve` on :41900).
// Against a deployed instance, set DI_URL and DI_TOKEN; the token is sent as a
// Bearer header by the proxy, so the browser never needs it.
export default defineConfig(({ mode }) => {
  const env = { ...process.env, ...loadEnv(mode, process.cwd(), 'DI_') };
  const target = env.DI_URL ?? 'http://127.0.0.1:41900';
  const headers: Record<string, string> = env.DI_TOKEN ? { Authorization: `Bearer ${env.DI_TOKEN}` } : {};
  const proxy = { target, changeOrigin: true, secure: true, headers };
  return {
    plugins: [react(), tailwindcss()],
    resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
    server: { port: 4731, host: true, proxy: { '/v1': proxy, '/ui': proxy } },
    build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 1200 },
  };
});
