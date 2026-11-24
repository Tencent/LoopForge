import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    // Build the web frontend for Tauri
    outDir: 'dist',
    rollupOptions: {
      input: {
        // Use the existing web/index.html as the entry point
        main: '../web/index.html'
      }
    }
  },
  server: {
    port: 3000,
    strictPort: true
  }
});
