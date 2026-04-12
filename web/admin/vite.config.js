import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
export default defineConfig(function (_a) {
    var mode = _a.mode;
    var env = loadEnv(mode, process.cwd(), '');
    var target = env.VITE_ADMIN_TARGET || 'http://127.0.0.1:6040';
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
                    target: target,
                    changeOrigin: true,
                },
                '/admin/ws': {
                    target: target,
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
