import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// 开发期将 /api 与 /healthz 代理到本地后端（默认 127.0.0.1:8080），
// 生产环境由 Go 服务直接提供静态资源，无需代理。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.VITE_API_PROXY ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      "/healthz": {
        target: process.env.VITE_API_PROXY ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
  build: {
    // 输出到 web/frontend/dist，由 Go 的 //go:embed all:dist 打包进二进制。
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
})
