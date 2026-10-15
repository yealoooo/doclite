// M1-12：在局域网地址（非安全上下文）上跑一遍浏览器上传（含生成封面）。
//
//   DOCLITE_SERVER=http://127.0.0.1:<server 端口> npx vite preview --host --port <端口> --strictPort   # 只在跑这一步时开
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m1-upload-lan.mjs \
//     --base http://<本机局域网 IP>:<端口>
//
// 用 http://<内网 IP> 打开时页面不是安全上下文：crypto.subtle、crypto.randomUUID 都没有（design §7）。
// 断言：
// - window.isSecureContext 为 false，crypto.subtle、crypto.randomUUID 都是 undefined；地址不是 localhost、127.0.0.1；
// - 上传一个带两层子文件夹的样本目录（和 m1-organize.mjs 的 files-organize 同一套文件、同一段代码）：
//   都入库、都带上了封面，batch 里的封面是 JPEG、长边 ≤ 400、方向一致。
// 在 DEMO-A 根下建临时文件夹，做完删掉（after，失败也删）。

import { assert, context, dropTemp, makeTemp, openFolderId, uploadSampleFolder } from './m1-organize.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

let temp = null

export default {
  out: '../docs/screenshots/m1',
  waitUntil: 'networkidle2',
  accounts: { [admin]: adminPassword },
  shots: [
    {
      name: 'files-upload-lan',
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
        const ctx = await context(page)
        temp = await makeTemp(ctx, '局域网')
        await openFolderId(page, temp.id)
        await uploadSampleFolder(page, ctx, '  断言 局域网地址上')
      },
      async after() {
        await dropTemp(temp)
        temp = null
      },
      delay: 500,
    },
  ],
}
