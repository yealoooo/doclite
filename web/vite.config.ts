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
    // web 是独立的模块（M7-5）：server 只提供 /api、不托管界面，页面由 vite 提供（部署时是 nginx 容器，M8）。
    // npm run dev 和 npm run preview（preview.proxy 默认沿用 server.proxy）都把 /api（包括实时推送）转发给 server：
    // 默认本机 9080（server/.env 里的 LISTEN_ADDR），别的地址用环境变量 DOCLITE_SERVER 指定，例如
    //   DOCLITE_SERVER=http://127.0.0.1:29080 npm run preview -- --port 24173 --strictPort
    // 端口、监听地址用 vite 的命令行参数（--port、--host、--strictPort），dev 默认 5173、preview 默认 4173。
    // 默认只监听 127.0.0.1（preview.host 默认沿用这里）：vite 默认的 localhost 在 Mac 上只监听 ::1，
    // http://127.0.0.1:<端口> 打不开；局域网里别的电脑要打开时加 --host（监听所有地址）或 --host <本机 IP>。
    host: '127.0.0.1',
    proxy: {
      '/api': {
        target: process.env.DOCLITE_SERVER ?? 'http://127.0.0.1:9080',
        changeOrigin: true,
        // SSE（M1-10）：Node 的 http 响应在第一次写 body 之前不发响应头，而 server 的事件流连上后要等第一条事件
        // 或 15 秒的保活注释才有 body，页面会一直以为没连上。收到 text/event-stream 时，等代理把状态码和响应头
        // 设好（同一轮里同步完成）后马上发出响应头。只影响 vite 开发和预览；部署时 nginx 对实时推送关缓冲（M8）。
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
