import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
const api = process.env.LOOPFORGE_API || "http://127.0.0.1:8080";
export default defineConfig({
  base: "/chatbot/",
  plugins: [react()],
  build: { outDir: "../web/native-chat", emptyOutDir: true },
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
