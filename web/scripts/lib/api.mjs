// seed 和截图脚本共用的接口小工具（M9-3 从原来的 m3-demo.mjs 里留下的通用部分）：登录会话、上传一页合成的扫描页、
// 时间戳、拦截页面请求计数。演示数据由 scripts/seed.mjs 建，截图脚本要的临时数据各自建。

import { makeScanPage } from './scan-pages.mjs'

/**
 * 一个登录会话（各自的 Cookie）。返回 `call(method, path, body?, {allow?})`：body 是对象（JSON）或 FormData；
 * 状态码在 allow 里的错误不抛，返回 `{status: 数字, code, message, details}`。
 */
export async function apiLogin(origin, username, password) {
  let cookie = ''
  const call = async (method, path, body, { allow = [] } = {}) => {
    const form = body instanceof FormData
    const res = await fetch(origin + path, {
      method,
      headers: {
        Accept: 'application/json',
        ...(body !== undefined && !form ? { 'Content-Type': 'application/json' } : {}),
        ...(cookie ? { Cookie: cookie } : {}),
      },
      body: body === undefined ? undefined : form ? body : JSON.stringify(body),
    })
    const sc = res.headers.get('set-cookie')
    if (sc) cookie = sc.split(';')[0]
    const text = await res.text()
    const data = text ? JSON.parse(text) : undefined
    if (!res.ok && !allow.includes(res.status)) {
      throw new Error(`${username}: ${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
    }
    return allow.includes(res.status) && !res.ok ? { status: res.status, ...data } : data
  }
  await call('POST', '/api/auth/login', { username, password })
  call.cookie = () => cookie
  call.username = username
  return call
}

/** 用接口上传一页合成的 PNG（A4，带封面），返回入库结果。`opts`：dpi（默认 200，150 是低 DPI）、cover（默认 true）。 */
export async function uploadPage(call, folderId, name, seed, pageNo, { dpi = 200, cover = true, allow } = {}) {
  const p = makeScanPage({ seed, pageNo, dpi })
  const form = new FormData()
  form.append('file_name', name)
  if (cover) form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/folders/${folderId}/files`, form, allow ? { allow } : undefined)
}

/** 时间戳（文件夹名、编码里用）。 */
export function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

/**
 * 在 fn 执行期间拦截页面发出的请求：URL 符合 match 的记下来（发出时间、同时在途的最大个数），可以按 `log.delayMs`
 * 推迟放行（模拟慢接口）；其余的立即放行。fn 结束前要等在途的都回来（`waitQuiet`），再关掉拦截。
 * 用来统计看板、设置编辑器发了多少请求（M3-7 审查第 1 轮）。
 */
export async function interceptRequests(page, match, fn) {
  const log = {
    started: [],
    inflight: 0,
    maxInflight: 0,
    delayMs: 0,
    reset() {
      this.started = []
      this.maxInflight = this.inflight
    },
    /** 等到没有在途的、并且（从调用时起算）quietMs 内没有新请求。 */
    async waitQuiet(quietMs = 1500, timeout = 15000) {
      const t0 = Date.now()
      while (Date.now() - t0 < timeout) {
        const lastAt = Math.max(t0, this.started.length ? this.started[this.started.length - 1].at : 0)
        if (this.inflight === 0 && Date.now() - lastAt >= quietMs) return
        await new Promise((r) => setTimeout(r, 100))
      }
      throw new Error(`${timeout} ms 内请求没有停下来（在途 ${this.inflight} 个）`)
    },
  }
  const onRequest = (req) => {
    if (!match(req.url())) {
      req.continue().catch(() => {})
      return
    }
    log.started.push({ url: req.url(), at: Date.now() })
    log.inflight++
    log.maxInflight = Math.max(log.maxInflight, log.inflight)
    const go = () => req.continue().catch(() => {})
    if (log.delayMs > 0) setTimeout(go, log.delayMs)
    else go()
  }
  const onDone = (req) => {
    if (match(req.url())) log.inflight--
  }
  await page.setRequestInterception(true)
  page.on('request', onRequest)
  page.on('requestfinished', onDone)
  page.on('requestfailed', onDone)
  try {
    return await fn(log)
  } finally {
    log.delayMs = 0
    await log.waitQuiet(200, 10000).catch(() => {})
    page.off('request', onRequest)
    page.off('requestfinished', onDone)
    page.off('requestfailed', onDone)
    await page.setRequestInterception(false)
  }
}

/** 项目里必填的著录项的演示值（锁定前要填）：text「演示」、date「2025」、enum 第一个选项。 */
export async function requiredFieldValues(call, pid) {
  const s = await call('GET', `/api/projects/${pid}/catalog-settings`)
  const out = {}
  for (const f of s.item_fields) {
    if (!f.required) continue
    out[f.key] = f.type === 'date' ? '2025' : f.type === 'enum' ? (f.options[0] ?? '') : '演示'
  }
  return out
}
