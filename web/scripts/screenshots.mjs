#!/usr/bin/env node
// 用无头 Chrome 给页面截图（puppeteer-core + 本机已安装的 Chrome，不下载浏览器）。
//
//   npm run screenshots -- --spec scripts/shots/m0.mjs [选项]
//
// 选项：
//   --spec <文件>       截图清单（必填），格式见 scripts/shots/m0.mjs
//   --base <URL>        前端地址，默认 http://127.0.0.1:5173（托管了 web/dist 的 server、vite preview，或 devshell——
//                       有壳模式截图的脚本要用 devshell，见 scripts/lib/wails-mock.mjs）
//   --out <目录>        输出目录，默认取清单里的 out（相对 web/）
//   --account 名:密码   覆盖或补充清单里的账号密码，可重复
//   --only a,b          只截这几张（按 name）
//   --chrome <路径>     Chrome 可执行文件；默认按 CHROME_PATH 环境变量、再按常见安装位置查找
//   --width/--height    视口大小，默认 1440×900
//   --scale <倍数>      设备像素比，默认 1
//
// 每个账号用一个独立的浏览器上下文，通过登录页登录一次，之后依次打开它的各个页面。
// 清单里写了 init(page) 的截图单独用一个新的上下文（未登录），先执行 init 再打开页面，
// 例如注入 Wails 桥的模拟（scripts/lib/wails-mock.mjs，scripts/shots/m1-shell.mjs）。写了 shell: true 的截图用壳模式的地址
// （`http://wails.localhost:<端口>`，页面认为自己在壳里）打开 path。
// 打开页面时默认等到网络空闲（networkidle0）；页面常开一条 SSE 连接时（例如 /files）要在清单或这一张上写
// waitUntil: 'networkidle2'（M1-10）。after(page) 在截图之后执行（清理测试数据等）；前面的步骤失败时也执行
// （放在 finally 里），它自己出错也算这一张失败。

import { existsSync, mkdirSync } from 'node:fs'
import path from 'node:path'
import { parseArgs } from 'node:util'
import { pathToFileURL } from 'node:url'

import puppeteer from 'puppeteer-core'

import { shellOrigin } from './lib/wails-mock.mjs'

const { values: args } = parseArgs({
  options: {
    spec: { type: 'string' },
    base: { type: 'string', default: process.env.DOCLITE_WEB ?? 'http://127.0.0.1:5173' },
    out: { type: 'string' },
    account: { type: 'string', multiple: true, default: [] },
    only: { type: 'string' },
    chrome: { type: 'string', default: process.env.CHROME_PATH },
    width: { type: 'string', default: '1440' },
    height: { type: 'string', default: '900' },
    scale: { type: 'string', default: '1' },
  },
})

if (!args.spec) {
  console.error('缺少 --spec，例如：npm run screenshots -- --spec scripts/shots/m0.mjs')
  process.exit(2)
}

const spec = (await import(pathToFileURL(path.resolve(args.spec)).href)).default
const base = args.base.replace(/\/$/, '')
const outDir = path.resolve(args.out ?? spec.out)
const accounts = { ...spec.accounts }
for (const a of args.account) {
  const i = a.indexOf(':')
  accounts[a.slice(0, i)] = a.slice(i + 1)
}
const only = args.only ? new Set(args.only.split(',')) : null
const shots = spec.shots.filter((s) => !only || only.has(s.name))

function findChrome() {
  const candidates = [
    args.chrome,
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
  ]
  const found = candidates.find((p) => p && existsSync(p))
  if (!found) throw new Error('找不到 Chrome，请用 --chrome 或 CHROME_PATH 指定')
  return found
}

const browser = await puppeteer.launch({
  executablePath: findChrome(),
  headless: true,
  // 本机地址不走代理；界面语言用中文。
  // 后台标签页不节流定时器（M1-12：两个页面看实时刷新时，一个在前台操作，另一个在后台等更新）。
  args: [
    '--no-proxy-server',
    '--lang=zh-CN',
    '--hide-scrollbars',
    '--disable-background-timer-throttling',
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
  ],
  defaultViewport: {
    width: Number(args.width),
    height: Number(args.height),
    deviceScaleFactor: Number(args.scale),
  },
})

/** 每个账号一个浏览器上下文（互不共享 Cookie）；null 表示未登录。 */
const contexts = new Map()

async function pageFor(user) {
  const key = user ?? ''
  if (contexts.has(key)) return contexts.get(key)
  const context = await browser.createBrowserContext()
  const page = await context.newPage()
  if (user) {
    if (!(user in accounts)) throw new Error(`清单里没有账号 ${user} 的密码`)
    await page.goto(`${base}/login`, { waitUntil: 'networkidle0' })
    await page.type('#username', user)
    await page.type('#password', accounts[user])
    await Promise.all([
      page.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }),
      page.click('[data-testid=login-submit]'),
    ])
  }
  contexts.set(key, page)
  return page
}

mkdirSync(outDir, { recursive: true })
let failed = 0

for (const shot of shots) {
  const file = path.join(outDir, `${shot.name}.png`)
  let isolated = null
  let page = null
  try {
    if (shot.init) {
      isolated = await browser.createBrowserContext()
      page = await isolated.newPage()
      await shot.init(page)
    } else {
      page = await pageFor(shot.user ?? null)
    }
    const origin = shot.shell ? shellOrigin(base) : base
    await page.goto(origin + shot.path, { waitUntil: shot.waitUntil ?? spec.waitUntil ?? 'networkidle0' })
    if (shot.waitFor) await page.waitForSelector(shot.waitFor, { timeout: 15000 })
    if (shot.prepare) await shot.prepare(page)
    await page.evaluate(() => document.fonts.ready)
    // 等过渡动画结束。
    await new Promise((r) => setTimeout(r, shot.delay ?? 300))
    await page.screenshot({ path: file })
    console.log(`✓ ${shot.name}  ${shot.user ?? '(未登录)'}  ${shot.path}`)
  } catch (err) {
    failed++
    console.error(`✗ ${shot.name}: ${err.message}`)
  } finally {
    // after 总会执行（前面失败了也执行，用来清理测试数据）；它自己出错也算这一张失败。
    if (shot.after && page) {
      try {
        await shot.after(page)
      } catch (err) {
        failed++
        console.error(`✗ ${shot.name}（after）: ${err.message}`)
      }
    }
    await isolated?.close()
  }
}

await browser.close()
console.log(`截图目录：${outDir}`)
process.exit(failed ? 1 : 0)
