import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import scopeChatCSS from "./scope-chat-css.js";
const api = process.env.LOOPFORGE_API || "http://127.0.0.1:8080";
export default defineConfig({
  base: "/chatbot/",
  plugins: [react()],
  css: { postcss: { plugins: [scopeChatCSS()] } },
  build: { outDir: "../web/native-chat", emptyOutDir: true, rollupOptions: { output: { entryFileNames: "assets/workbench.js", assetFileNames: (asset) => asset.names?.some((name) => name.endsWith(".css")) ? "assets/workbench.css" : "assets/[name]-[hash][extname]" } } },
  server: {
    port: 18769,
    proxy: {
      "/api": api,
      "/mcp": api,
      // Workspace IDE tab embeds the platform's vscode-web workbench.
      "/vscode.html": api,
      "/vscode.js": api,
      "/vscode.css": api,
      "/vscode-web": api,
      "/extensions": api,
    },
  },
});
