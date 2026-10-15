// M2-4：在局域网地址（非安全上下文）上看 TIFF 原图（浏览器里 Worker 解码，交给 OpenSeadragon）。
//
//   DOCLITE_SERVER=http://127.0.0.1:<server 端口> npx vite preview --host --port <端口> --strictPort   # 只在跑这一步时开
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m2-viewer-lan.mjs \
//     --base http://<本机局域网 IP>:<端口>
//
// 用 http://<内网 IP> 打开时页面不是安全上下文：crypto.subtle、crypto.randomUUID 都没有（design §7）。
// 断言：
// - window.isSecureContext 为 false，crypto.subtle、crypto.randomUUID 都是 undefined；地址不是 localhost、127.0.0.1；
// - /files 右键"查看原图"打开 LZW 的 TIFF，Worker 下载、解码，原图画完（data-state=ready、data-mode=worker）；
//   再切到 G4 的 TIFF，同样画完；JPEG（整图模式）也能显示。
// 样本同 m2-viewer.mjs（DEMO-A 的"M2-3 分块冒烟"、M1 验收的 JPEG），不改数据。

import { assert, openFolder, openViewer, settle, viewerContext, waitViewer } from '../lib/viewer-shots.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

export default {
  out: '../docs/screenshots/m2',
  waitUntil: 'networkidle2',
  accounts: { [admin]: adminPassword },
  shots: [
    {
      name: 'viewer-lan-tiff',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const env = await page.evaluate(() => ({
          secure: window.isSecureContext,
          host: location.hostname,
          subtle: typeof crypto.subtle,
          randomUUID: typeof crypto.randomUUID,
          worker: typeof Worker,
          offscreen: typeof OffscreenCanvas,
        }))
        assert(!['localhost', '127.0.0.1', '[::1]'].includes(env.host), `要用局域网地址打开（现在是 ${env.host}）`)
        assert(env.secure === false, `应该不是安全上下文：isSecureContext=${env.secure}`)
        assert(env.subtle === 'undefined' && env.randomUUID === 'undefined', `非安全上下文里不该有 crypto.subtle、randomUUID：${env.subtle}、${env.randomUUID}`)
        console.log(
          `  断言 非安全上下文：${env.host}，isSecureContext=false，crypto.subtle、crypto.randomUUID 都是 undefined（Worker ${env.worker}、OffscreenCanvas ${env.offscreen}）`,
        )
        const origin = new URL(page.url()).origin
        const ctx = await viewerContext(origin, { user: admin, password: adminPassword })

        // JPEG（整图模式）。
        await openFolder(page, origin, ctx.files['IMG_1.jpg'].folder_id)
        await openViewer(page, 'IMG_1.jpg')
        let d = await waitViewer(page, ['ready', 'failed', 'too_large', 'unsupported'])
        assert(d.state === 'ready' && d.mode === 'image', `局域网地址上 JPEG 应该能显示：${d.state} ${d.mode}`)
        console.log('  断言 局域网地址上 JPEG：整图模式画完')

        // TIFF：LZW → 切到 G4（同一个文件夹，"下一个"）。
        await openFolder(page, origin, ctx.files['tiff-lzw-rgb.tif'].folder_id)
        await openViewer(page, 'tiff-g4-1bit.tif')
        d = await waitViewer(page, ['ready', 'failed', 'too_large', 'unsupported'])
        assert(d.state === 'ready' && d.mode === 'worker', `局域网地址上 G4 TIFF 应该能显示：${d.state} ${d.mode}`)
        console.log('  断言 局域网地址上 G4 TIFF：Worker 解码、画完')
        await page.click('[data-testid=viewer-next]')
        await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.state === 'loading', { timeout: 5000, polling: 20 }).catch(() => {})
        d = await waitViewer(page, ['ready', 'failed', 'too_large', 'unsupported'])
        const title = await page.$eval('[data-testid=viewer-title]', (el) => el.textContent)
        assert(title === 'tiff-lzw-rgb.tif' && d.state === 'ready' && d.mode === 'worker', `局域网地址上 LZW TIFF 应该能显示：${title} ${d.state} ${d.mode}`)
        console.log('  断言 局域网地址上 LZW TIFF：Worker 解码、画完')
        await settle(page)
      },
      delay: 400,
    },
  ],
}
