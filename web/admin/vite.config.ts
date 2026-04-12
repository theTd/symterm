import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const target = env.VITE_ADMIN_TARGET || 'http://127.0.0.1:6040';

  return {
    base: '/admin/',
    build: {
      emptyOutDir: true,
      outDir: '../../internal/admin/webdist',
    },
    plugins: [react(), tailwindcss()],
    server: {
      port: 5173,
      proxy: {
        '/admin/api': {
          target,
          changeOrigin: true,
        },
        '/admin/ws': {
          target,
          ws: true,
          changeOrigin: true,
        },
      },
    },
    test: {
      environment: 'happy-dom',
      setupFiles: './src/test/setup.ts',
      css: true,
    },
  };
});
