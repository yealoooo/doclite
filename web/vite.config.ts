import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// 版本号只有 package.json 这一个来源：壳（tauri.conf.json 的 version）也读它。
const pkg = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')) as { version: string }

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    // 开发时把 /api 转发到本机的 doclite-server（server/.env 里的 LISTEN_ADDR）。
    // server 不在 9080 时用环境变量 DOCLITE_SERVER 指定，例如 DOCLITE_SERVER=http://127.0.0.1:9081 npm run dev。
    // vite preview 也沿用这个代理。
    proxy: {
      '/api': {
        target: process.env.DOCLITE_SERVER ?? 'http://127.0.0.1:9080',
        changeOrigin: true,
        // SSE（M1-10）：Node 的 http 响应在第一次写 body 之前不发响应头，而 server 的事件流连上后要等第一条事件
        // 或 15 秒的保活注释才有 body，页面会一直以为没连上。收到 text/event-stream 时，等代理把状态码和响应头
        // 设好（同一轮里同步完成）后马上发出响应头。只影响 vite 开发和预览，生产环境浏览器直连 server。
        configure(proxy) {
          proxy.on('proxyRes', (proxyRes, _req, res) => {
            if (!String(proxyRes.headers['content-type'] ?? '').startsWith('text/event-stream')) return
            process.nextTick(() => {
              if ('flushHeaders' in res && !res.headersSent) res.flushHeaders()
            })
          })
        },
      },
    },
  },
})
