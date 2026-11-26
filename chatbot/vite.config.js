import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
const api = process.env.LOOPFORGE_API || "http://127.0.0.1:8080";
export default defineConfig({
  base: "/chatbot/",
  plugins: [react()],
  build: {
    outDir: "../web/native-chat", emptyOutDir: true,
    rollupOptions: {
      input: {
        main: new URL("./index.html", import.meta.url).pathname,
        "prompt-editor": new URL("./src/prompt-editor-entry.jsx", import.meta.url).pathname,
      },
      output: { entryFileNames: (chunk) => chunk.name === "prompt-editor" ? "prompt-editor.js" : "assets/[name]-[hash].js" },
    },
  },
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
