import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

// 开发代理目标：默认指向本机 amkr（18900），可用 AMKR_DEV_TARGET 覆盖。
const devTarget = process.env.AMKR_DEV_TARGET || "http://127.0.0.1:18900";

export default defineConfig({
  base: "./",
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "src"),
    },
  },
  build: {
    // 构建产物由 Go 侧 //go:embed webui 原样嵌入，必须落在仓库根的 webui/。
    outDir: "../webui",
    emptyOutDir: true,
    assetsDir: "assets",
    rollupOptions: {
      input: {
        index: resolve(import.meta.dirname, "index.html"),
        login: resolve(import.meta.dirname, "login.html"),
        panel: resolve(import.meta.dirname, "panel.html"),
        guest: resolve(import.meta.dirname, "guest.html"),
      },
      output: {
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: devTarget },
      "/metrics": { target: devTarget },
      "/health": { target: devTarget },
      "/v1": { target: devTarget },
      "/ws": { target: devTarget, ws: true },
      "/ui": { target: devTarget },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
