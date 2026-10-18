// M3-7 的截图清单和断言：/system/projects 的项目编辑对话框里的"著录项和档号"（admin 操作）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-catalog-settings.mjs --base http://localhost:<preview 端口>
//
// 用一个专用项目 M37-SET「M3-7 著录设置演示」（没有就建；每次跑先把 settings 清成 {}），里面经接口建两个归类节点
// （全宗 Z109.A「全宗 A」、它下面的门类 WS「文书」）——Z109.A 在默认模板下合法（. 不是默认模板的分隔符），换成用 .
// 分隔的模板时要提示"已有编码含新模板的分隔符"。不动 DEMO-A、DEMO-B 的设置。
// 截图 m3-settings-editor、m3-settings-fields 写死到 ../docs/screenshots/m3（回归时在别的目录下跑）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-settings-saved：加 3 个著录项（文号：文本、必填、最大长度 50；成文日期：日期、默认 2025；密级：选项 公开/内部/
//   秘密、默认 内部、必填），把密级上移一位，关掉"定稿前要求全部页审核通过"，模板改成 {fonds}.{category}.{year}-{item_no:05}：
//   预览是 Z109.WS.2025-00012，提示 1 个已有编码（Z109.A）含新模板的分隔符（另存 m3-settings-editor.png、
//   m3-settings-fields.png）→ 保存 → 接口 catalog-settings 的 item_fields 按顺序和填的一样（max_length、default、
//   options 都对）、模板和 lock_requires_approved 对；项目详情的 settings 里其他键（review.reject_reasons 等）还在 →
//   重新打开对话框，三个著录项按保存的顺序显示（M3-5 合入前用接口核对"/catalog 的表单出现新字段"）。
// - m3-settings-template-error：模板改成 {fonds}{year}-{item_no:4} → 预览显示错误 → 保存 → 对话框里显示 server 的错误，
//   和预览的错误一字不差（"settings 不对："加预览的说明），模板输入框标红，库里的设置没变。另外经接口逐个提交 20 个模板
//   （合法的、各种不合法的），前端解析（src/lib/catalog.ts，Node 直接载入 TS）和 server 的结论、说明都一致。
// - m3-settings-field-error：把密级的选项清空 → 本地提示"选项至少要有一个" → 保存 → server 的错误指向
//   item_fields[1]，对话框标出第 2 行，库里的设置没变。
// 审查第 1 轮补的：
// - m3-settings-saved 里：密级的选项故意带首尾空白和空行 → 只有"选项首尾的空白会去掉"一条提示，保存的是去掉空白、
//   去掉空行的；key 写成 title 时提示"title 是题名的固定 key"。
// - m3-settings-conflict-resaved（M4-4 改写：PATCH 带版本检查 expected_updated_at，替换 M3-7 的"保存前重读比对"）：
//   两个窗口（两个浏览器上下文）打开同一个项目的编辑对话框 → 窗口 B 加著录项 other_no、改说明，先保存 → 窗口 A 改模板、
//   改状态为已归档，后保存 → PATCH 409，A 看到提示"被别人改过（说明、著录项和档号）……你对著录项和档号的修改没有保存；
//   你的其他修改还在"（另存 m3-settings-conflict.png），库里还是 B 保存的内容（先保存的不丢），A 的对话框已重新载入
//   （说明是 B 的、状态保留 A 的、显示 other_no、模板是库里的）→ A 再改模板保存成功，两边的修改都在。另外：别人只改了
//   授权时 A 保存不提示，PATCH 409 后自动用新版本重发一次（200）。after 里改回原来的设置、说明和状态。
//   M4-4 审查补的：取详情推迟 1 秒，期间名称、说明、状态和保存按钮禁用、输入不进去，取到后可用；两个窗口把说明改成
//   同一个值，后保存的提示"被别人改过（说明）"但不说"修改没有保存"，再点保存直接关掉、不发 PATCH。
// - m3-settings-requests：拦截 archive-categories 请求（每个推迟 250 ms）计数。改模板后开始逐层取已有编码，取到一半
//   关掉对话框 → 之后 2 秒不再发请求；新开对话框，改模板、来回切 4 次标签页、再改模板 → 正好取一遍。为此 context 里
//   给全宗 A 下面多建了 C01–C08 各带 3 个年度（一遍要 10 个请求）。
// - 模板对照另加 3 个 { 没闭合、片段里有制表符、引号和反斜杠、控制字符的模板（说明里的转义和 Rust 一致）。

import { mkdirSync } from 'node:fs'

import { apiLogin, interceptRequests } from '../lib/api.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
// 另存的截图和清单的截图放在同一个目录：--out，默认 docs/screenshots/m3。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  return i > 0 ? process.argv[i + 1] : '../docs/screenshots/m3'
})()
const CODE = 'M37-SET'
const NEW_TEMPLATE = '{fonds}.{category}.{year}-{item_no:05}'

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    let project = (await api('GET', `/api/system/projects?keyword=${CODE}&page_size=100`)).items.find((p) => p.project_code === CODE)
    if (!project) {
      project = await api('POST', '/api/system/projects', {
        project_code: CODE,
        project_name: 'M3-7 著录设置演示',
        description: 'M3-7 截图脚本用：著录项和档号的编辑。每次跑会把 settings 清空重来',
      })
      console.log(`  新建了项目 ${CODE}`)
    }
    const pid = project.id
    const fonds =
      (await api('GET', `/api/projects/${pid}/archive-categories`)).items.find((c) => c.category_code === 'Z109.A') ??
      (await api('POST', `/api/projects/${pid}/archive-categories`, { category_type: 'fonds', category_code: 'Z109.A', category_name: '全宗 A' }))
    const under = async (parentId) => (await api('GET', `/api/projects/${pid}/archive-categories?parent_id=${parentId}`)).items
    const ensure = async (parentId, type, code, name) =>
      (await under(parentId)).find((c) => c.category_code === code) ??
      (await api('POST', `/api/projects/${pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name }))
    await ensure(fonds.id, 'category', 'WS', '文书')
    // 再多几层（审查第 1 轮：数逐层取编码的请求、关掉对话框后是否停下）：全宗 A 下 8 个自定义节点 C01–C08，各带 3 个年度。
    for (let i = 1; i <= 8; i++) {
      const c = await ensure(fonds.id, 'custom', `C0${i}`, `测试分支 ${i}`)
      for (const y of ['2022', '2023', '2024']) await ensure(c.id, 'year', y, `${y} 年`)
    }
    // 每次从默认设置开始（PATCH settings {}：库里存 {}，读取时补默认值）。
    await api('PATCH', `/api/system/projects/${pid}`, { settings: {} })
    const catalogLib = await import('../../src/lib/catalog.ts')
    return { origin, api, pid, catalogLib }
  })()
  return ctxPromise
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function openEditor(page) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/system/projects`, { waitUntil: 'networkidle0' })
  await page.waitForSelector(`tr[data-code="${CODE}"]`, { timeout: 10000 })
  const buttons = await page.$$(`tr[data-code="${CODE}"] button`)
  await buttons[0].click() // 编辑
  await page.waitForSelector('[data-testid=project-form][data-settings=ready]', { timeout: 10000 })
  await page.click('[data-testid=project-form-tab][data-tab=catalog]')
  await page.waitForSelector('[data-testid=catalog-editor]', { timeout: 5000 })
}

/** 给原生输入框设值（v-model 听 input 事件）。 */
async function setValue(page, selector, value) {
  await page.$eval(
    selector,
    (el, v) => {
      el.value = v
      el.dispatchEvent(new Event('input', { bubbles: true }))
    },
    value,
  )
}

/** 另一个窗口：新的浏览器上下文（不共享 Cookie），同样用 admin 登录（M4-4）。 */
async function otherWindow(page) {
  const ctx = await context(page)
  const bc = await page.browser().createBrowserContext()
  const other = await bc.newPage()
  await other.setViewport({ width: 1440, height: 900 })
  await other.goto(`${ctx.origin}/login`, { waitUntil: 'networkidle0' })
  await other.type('#username', admin)
  await other.type('#password', adminPassword)
  await Promise.all([
    other.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }),
    other.click('[data-testid=login-submit]'),
  ])
  return { other, close: () => bc.close() }
}

/** 点基本信息里的状态按钮（进行中、已归档）。 */
async function clickStatus(page, label) {
  const found = await page.$$eval(
    '[data-testid=project-form] [role=radiogroup] button[role=radio]',
    (els, l) => {
      const b = els.find((e) => e.textContent.trim() === l)
      b?.click()
      return !!b
    },
    label,
  )
  assert(found, `找不到状态按钮「${label}」`)
}

async function statusChecked(page, label) {
  return page.$$eval(
    '[data-testid=project-form] [role=radiogroup] button[role=radio]',
    (els, l) => els.some((e) => e.textContent.trim() === l && e.getAttribute('aria-checked') === 'true'),
    label,
  )
}

/** 记下页面发出的 PATCH /api/system/projects/{pid} 的响应状态码（M4-4）。 */
function recordPatches(page, pid) {
  const statuses = []
  const onResponse = (res) => {
    if (res.request().method() === 'PATCH' && new URL(res.url()).pathname === `/api/system/projects/${pid}`) statuses.push(res.status())
  }
  page.on('response', onResponse)
  return { list: () => [...statuses], stop: () => page.off('response', onResponse) }
}

const last = '[data-testid=item-field]:last-child'
async function addField(page, { key, label, type = 'text', required = false, maxLength, options, def }) {
  await page.click('[data-testid=add-field]')
  await page.waitForSelector(`${last} [data-testid=field-key]`)
  await page.type(`${last} [data-testid=field-key]`, key)
  await page.type(`${last} [data-testid=field-label]`, label)
  if (type !== 'text') await page.click(`${last} [data-testid=field-type] button[data-value=${type}]`)
  if (required) await page.click(`${last} [data-testid=field-required]`)
  if (maxLength !== undefined) await setValue(page, `${last} [data-testid=field-max-length]`, String(maxLength))
  if (options) await setValue(page, `${last} [data-testid=field-options]`, Array.isArray(options) ? options.join('\n') : options)
  if (def !== undefined) {
    if (type === 'enum') await page.select(`${last} [data-testid=field-default]`, def)
    else await setValue(page, `${last} [data-testid=field-default]`, def)
  }
}

async function catalogSettings(ctx) {
  return ctx.api('GET', `/api/projects/${ctx.pid}/catalog-settings`)
}

async function saveAndExpectError(page) {
  await page.click('[data-testid=project-form-submit]')
  await page.waitForSelector('[data-testid=project-form-error][data-scope=settings]', { timeout: 5000 })
  return page.$eval('[data-testid=project-form-error][data-scope=settings]', (el) => el.textContent.trim())
}

const EXPECTED_FIELDS = [
  { key: 'doc_no', label: '文号', type: 'text', required: true, options: [], max_length: 50, default: null },
  { key: 'secret', label: '密级', type: 'enum', required: true, options: ['公开', '内部', '秘密'], max_length: null, default: '内部' },
  { key: 'doc_date', label: '成文日期', type: 'date', required: false, options: [], max_length: null, default: '2025' },
]

/** 前端解析和 server 校验对照的模板。 */
const PARITY_TEMPLATES = [
  '{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}',
  NEW_TEMPLATE,
  '{item_no:010}',
  '{fonds}·{custom}_{item_no:03}',
  '{fonds}{year}-{item_no:4}',
  '{fonds}{year}-{item_no}',
  '{fonds}-{item_no}-{item_no:04}',
  '{fonds}-{fonds}-{item_no}',
  '{fonds}-{foo}-{item_no}',
  '{fonds}-{year:04}-{item_no}',
  '{fonds} {item_no}',
  '{fonds}/{item_no}',
  '{fonds-{item_no}',
  '{fonds}-{item_no}}',
  '',
  '{item_no:0010}',
  'A{item_no:00}',
  '{fonds}ab{item_no}',
  '{fonds}中{item_no}',
  `${'x'.repeat(196)}{item_no}`,
  // { 没闭合时说明里引用的片段按 Rust 的 Debug 转义（引号、反斜杠、制表符、其他控制字符）。
  '{fonds\t-{item_no',
  '{fo"n\\ds-{item_no}',
  '{a\u0001b',
]

export default {
  out: OUT,
  accounts: { [admin]: adminPassword },
  shots: [
    {
      name: 'm3-settings-saved',
      user: admin,
      path: '/system/projects',
      prepare: async (page) => {
        const ctx = await context(page)
        await openEditor(page)
        await addField(page, { key: 'doc_no', label: '文号', required: true, maxLength: 50 })
        await addField(page, { key: 'doc_date', label: '成文日期', type: 'date', def: '2025' })
        // 选项故意带首尾空白和空行：保存的是去掉空白、去掉空行的（M3-2 起 server 拒绝首尾带空白的选项和空选项）。
        await addField(page, { key: 'secret', label: '密级', type: 'enum', required: true, options: ' 公开 \n内部\n\n秘密 ', def: '内部' })
        await page.click('[data-testid=item-field][data-index="2"] [data-testid=field-up]')
        await page.waitForSelector('[data-testid=item-field][data-index="1"][data-key=secret]', { timeout: 2000 })
        await page.click('[data-testid=lock-requires-approved]')
        await setValue(page, '[data-testid=archive-template]', NEW_TEMPLATE)
        await page.waitForSelector('[data-testid=template-preview][data-ok=true]')
        const preview = await page.$eval('[data-testid=template-preview]', (el) => el.dataset.value)
        assert(preview === 'Z109.WS.2025-00012', `预览应为 Z109.WS.2025-00012，是 ${preview}`)
        await page.waitForSelector('[data-testid=template-codes-warning][data-state=ready]', { timeout: 5000 })
        const conflicts = await page.$eval('[data-testid=template-codes-warning]', (el) => ({ n: Number(el.dataset.count), text: el.textContent }))
        assert(conflicts.n === 1 && conflicts.text.includes('Z109.A'), `应提示 1 个已有编码（Z109.A）含分隔符，是 ${conflicts.n}：${conflicts.text}`)
        const problems = await page.$$eval('[data-testid=field-problem]', (els) => els.map((e) => e.textContent.trim()))
        assert(
          JSON.stringify(problems) === JSON.stringify(['选项首尾的空白会去掉']),
          `本地提示应只有"选项首尾的空白会去掉"，实际：${problems.join('；')}`,
        )
        // key 写成 title：本地提示（M3-2 起 server 拒绝），只提示不挡；改回来。
        await setValue(page, '[data-testid=item-field][data-index="0"] [data-testid=field-key]', 'title')
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=item-field][data-index="0"] [data-testid=field-problem]')].some((e) => e.textContent.includes('title 是题名的固定 key')),
          { timeout: 2000 },
        )
        await setValue(page, '[data-testid=item-field][data-index="0"] [data-testid=field-key]', 'doc_no')
        console.log(`  预览 ${preview}；提示已有编码 Z109.A 含新模板的分隔符`)
        await sleep(300)
        mkdirSync(OUT, { recursive: true })
        await page.screenshot({ path: `${OUT}/m3-settings-editor.png` })
        await page.$eval('[data-testid=add-field]', (el) => el.scrollIntoView({ block: 'start' }))
        await sleep(200)
        await page.screenshot({ path: `${OUT}/m3-settings-fields.png` })

        // 保存。
        await page.click('[data-testid=project-form-submit]')
        await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
        const cs = await catalogSettings(ctx)
        assert(JSON.stringify(cs.item_fields) === JSON.stringify(EXPECTED_FIELDS), `catalog-settings 的著录项不对：${JSON.stringify(cs.item_fields)}`)
        assert(cs.archive_no_template === NEW_TEMPLATE, `模板不对：${cs.archive_no_template}`)
        assert(cs.archive_no_template_error === null, '模板应能解析')
        assert(cs.lock_requires_approved === false, 'lock_requires_approved 应为 false')
        assert(JSON.stringify(cs.archive_no_levels) === JSON.stringify(['fonds', 'category', 'year']), `层级不对：${cs.archive_no_levels}`)
        const detail = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
        assert(detail.settings.review?.reject_reasons?.length === 8, 'settings 里的 review.reject_reasons 应原样保留')
        assert(detail.settings.export?.require_locked_item === true, 'settings 里的 export.require_locked_item 应原样保留')
        console.log(`  保存后 catalog-settings：${cs.item_fields.map((f) => `${f.label}(${f.type}${f.required ? '，必填' : ''})`).join('、')}；模板 ${cs.archive_no_template}；定稿不要求全部通过`)
        // 再读一次原样写回（M3-1 验收过）：PATCH 同样的 item_fields 不报错、不变。
        await ctx.api('PATCH', `/api/system/projects/${ctx.pid}`, { settings: { ...detail.settings, item_fields: cs.item_fields } })
        assert(JSON.stringify((await catalogSettings(ctx)).item_fields) === JSON.stringify(EXPECTED_FIELDS), '原样写回后著录项变了')

        // 重新打开：按保存的顺序显示。
        await openEditor(page)
        const keys = await page.$$eval('[data-testid=item-field]', (els) => els.map((e) => e.dataset.key))
        assert(JSON.stringify(keys) === JSON.stringify(['doc_no', 'secret', 'doc_date']), `重新打开后的顺序不对：${keys}`)
        const tpl = await page.$eval('[data-testid=archive-template]', (el) => el.value)
        assert(tpl === NEW_TEMPLATE, `重新打开后的模板不对：${tpl}`)
        const dirty = await page.$eval('[data-testid=project-form-tab][data-tab=catalog]', (el) => !!el.querySelector('.bg-info'))
        assert(!dirty, '刚打开就显示"有修改"')
        await page.$eval('[data-testid=add-field]', (el) => el.scrollIntoView({ block: 'start' }))
      },
    },
    {
      name: 'm3-settings-template-error',
      user: admin,
      path: '/system/projects',
      prepare: async (page) => {
        const ctx = await context(page)
        const before = JSON.stringify((await ctx.api('GET', `/api/system/projects/${ctx.pid}`)).settings)
        await openEditor(page)
        await setValue(page, '[data-testid=archive-template]', '{fonds}{year}-{item_no:4}')
        await page.waitForSelector('[data-testid=template-preview][data-ok=false]')
        const local = await page.$eval('[data-testid=template-error]', (el) => el.textContent.trim())
        const shown = await saveAndExpectError(page)
        assert(shown === `没有保存：settings 不对：${local}`, `server 的错误和预览不一致：预览「${local}」，对话框「${shown}」`)
        assert(await page.$eval('[data-testid=archive-template]', (el) => el.getAttribute('aria-invalid') === 'true'), '模板输入框应标红')
        const after = JSON.stringify((await ctx.api('GET', `/api/system/projects/${ctx.pid}`)).settings)
        assert(after === before, '保存失败后库里的设置变了')
        console.log(`  预览和 server 都说：${local}`)

        // 前端解析和 server 校验对照（经接口逐个提交；最后恢复原来的设置）。
        const base = JSON.parse(before)
        const mismatches = []
        try {
          for (const t of PARITY_TEMPLATES) {
            const res = await ctx.api('PATCH', `/api/system/projects/${ctx.pid}`, { settings: { ...base, archive_no: { template: t } } }, { allow: [400] })
            const server = res?.status === 400 ? { ok: false, error: res.message.replace(/^settings 不对：/, '') } : { ok: true }
            const p = ctx.catalogLib.parseArchiveNoTemplate(t)
            const client = p.ok ? { ok: true } : { ok: false, error: p.error }
            if (JSON.stringify(client) !== JSON.stringify(server)) mismatches.push(`${JSON.stringify(t)}：前端 ${JSON.stringify(client)}，server ${JSON.stringify(server)}`)
          }
        } finally {
          await ctx.api('PATCH', `/api/system/projects/${ctx.pid}`, { settings: base })
        }
        assert(mismatches.length === 0, `前端解析和 server 不一致：${mismatches.join('；')}`)
        console.log(`  ${PARITY_TEMPLATES.length} 个模板（${PARITY_TEMPLATES.filter((t) => ctx.catalogLib.parseArchiveNoTemplate(t).ok).length} 个合法）前端解析和 server 的结论、说明都一致`)
        await page.$eval('[data-testid=project-form-error][data-scope=settings]', (el) => el.scrollIntoView({ block: 'start' }))
      },
    },
    {
      name: 'm3-settings-field-error',
      user: admin,
      path: '/system/projects',
      prepare: async (page) => {
        const ctx = await context(page)
        const before = JSON.stringify((await ctx.api('GET', `/api/system/projects/${ctx.pid}`)).settings)
        await openEditor(page)
        await setValue(page, '[data-testid=item-field][data-key=secret] [data-testid=field-options]', '')
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=item-field][data-key=secret] [data-testid=field-problem]')].some((e) => e.textContent.includes('选项至少要有一个')),
          { timeout: 2000 },
        )
        const shown = await saveAndExpectError(page)
        assert(shown.includes('item_fields[1]'), `server 的错误应指向 item_fields[1]：${shown}`)
        await page.waitForSelector('[data-testid=item-field][data-index="1"][data-highlight=true]', { timeout: 2000 })
        const after = JSON.stringify((await ctx.api('GET', `/api/system/projects/${ctx.pid}`)).settings)
        assert(after === before, '保存失败后库里的设置变了')
        console.log(`  ${shown}；第 2 行标红`)
        await page.$eval('[data-testid=project-form-error][data-scope=settings]', (el) => el.scrollIntoView({ block: 'start' }))
        await sleep(200)
      },
    },
    {
      // M4-4（替换 M3-7 审查第 1 轮的"保存前重读比对"）：PATCH 带版本检查 expected_updated_at。两个窗口（两个浏览器
      // 上下文，都是 admin）同时打开同一个项目的编辑对话框：窗口 B 先保存（加著录项 other_no、改说明）；窗口 A（改了
      // 模板和状态）后保存 → 409，A 看到提示、对话框重新载入，库里是 B 保存的内容；A 核对后再保存，两边的修改都在。
      // 另外：别人只改了对话框里看不到的东西（授权）时，A 保存不提示，自动用新版本重发（PATCH 两次：409、200）。
      name: 'm3-settings-conflict-resaved',
      user: admin,
      path: '/system/projects',
      prepare: async (page) => {
        const ctx = await context(page)
        const detail0 = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
        conflictRestore = { settings: detail0.settings, description: detail0.description ?? '', status: detail0.status }

        // M4-4 审查：取详情期间（推迟 1 秒）名称、说明、状态和保存按钮禁用，输入不进去；取到后可用，值是详情里的。
        await interceptRequests(page, (u) => new URL(u).pathname === `/api/system/projects/${ctx.pid}`, async (log) => {
          await page.goto(`${ctx.origin}/system/projects`, { waitUntil: 'networkidle0' })
          await page.waitForSelector(`tr[data-code="${CODE}"]`, { timeout: 10000 })
          log.delayMs = 1000
          const t0 = Date.now()
          await (await page.$$(`tr[data-code="${CODE}"] button`))[0].click() // 编辑
          await page.waitForSelector('[data-testid=project-form][data-settings=loading]', { timeout: 5000 })
          const lockedState = () =>
            page.evaluate(() => {
              const radios = [...document.querySelectorAll('[data-testid=project-form] [role=radiogroup] button[role=radio]')].filter((b) =>
                ['进行中', '已归档'].includes(b.textContent.trim()),
              )
              return {
                name: document.querySelector('#pf-name').disabled,
                description: document.querySelector('#pf-desc').disabled,
                status: radios.length === 2 && radios.every((b) => b.disabled),
                submit: document.querySelector('[data-testid=project-form-submit]').disabled,
              }
            })
          const during = await lockedState()
          assert(Object.values(during).every(Boolean), `取详情期间应全部禁用：${JSON.stringify(during)}`)
          await page.type('#pf-name', '偷偷改名').catch(() => {})
          await page.waitForSelector('[data-testid=project-form][data-settings=ready]', { timeout: 5000 })
          const waited = Date.now() - t0
          const ready = await lockedState()
          assert(Object.values(ready).every((v) => v === false), `取到详情后应全部可用：${JSON.stringify(ready)}`)
          const shownName = await page.$eval('#pf-name', (el) => el.value)
          assert(shownName === detail0.project_name, `取详情期间不应输入进去，名称应是详情里的：${shownName}`)
          console.log(`  取详情推迟 1 秒：期间名称、说明、状态、保存按钮都禁用、输入不进去，约 ${waited} ms 后可用`)
          await page.click('[data-testid=project-form-cancel]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
        })

        const { other, close } = await otherWindow(page)
        try {
          // 两个窗口都打开编辑对话框（都停在"著录项和档号"）。
          await openEditor(page)
          await openEditor(other)
          const versionA = detail0.updated_at

          // 窗口 B 先保存：加一个著录项、改说明。
          const bDescription = `窗口 B 改的说明 ${Date.now()}`
          await addField(other, { key: 'other_no', label: '另一人加的编号' })
          await other.click('[data-testid=project-form-tab][data-tab=basic]')
          await setValue(other, '#pf-desc', bDescription)
          await other.click('[data-testid=project-form-submit]')
          await other.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          const afterB = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
          assert(afterB.updated_at !== versionA, '窗口 B 保存后版本应变了')
          assert(afterB.settings.item_fields.some((f) => f.key === 'other_no') && afterB.description === bDescription, '窗口 B 应保存成功')

          // 窗口 A（还是打开时的版本）：改模板、改状态为已归档，保存 → 409，看到提示，对话框还开着。
          const mine = '{fonds}.{year}-{item_no:04}'
          await setValue(page, '[data-testid=archive-template]', mine)
          await page.click('[data-testid=project-form-tab][data-tab=basic]')
          await clickStatus(page, '已归档')
          const patches = recordPatches(page, ctx.pid)
          await page.click('[data-testid=project-form-submit]')
          await page.waitForSelector('[data-testid=project-form-conflict]', { timeout: 5000 })
          const notice = await page.$eval('[data-testid=project-form-conflict]', (el) => el.textContent.trim())
          assert(
            notice.includes('被别人改过（说明、著录项和档号）') && notice.includes('你对著录项和档号的修改没有保存') && notice.includes('你的其他修改还在'),
            `提示不对：${notice}`,
          )
          assert(!!(await page.$('[data-testid=project-form]')), '409 时对话框应还开着')
          assert(JSON.stringify(patches.list()) === JSON.stringify([409]), `应只发一次 PATCH 且 409：${patches.list()}`)
          // 先保存的内容不丢：库里还是窗口 B 保存的。
          const kept = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
          assert(kept.updated_at === afterB.updated_at, '409 时库里不该有任何变化')
          assert(kept.settings.item_fields.some((f) => f.key === 'other_no'), '窗口 B 加的著录项被覆盖掉了')
          assert(kept.settings.archive_no.template === NEW_TEMPLATE, `409 时模板不该改：${kept.settings.archive_no.template}`)
          assert(kept.description === bDescription && kept.status === 'active', '409 时说明、状态不该改')
          // 窗口 A 已重新载入：说明是 B 改的；状态保留 A 的修改；著录项和档号换成库里的（A 改的模板放弃）。
          assert((await page.$eval('#pf-desc', (el) => el.value)) === bDescription, '重新载入后说明应是窗口 B 改的')
          assert(await statusChecked(page, '已归档'), '别人没改状态，A 改的状态应保留')
          const tab = await page.$eval('[data-testid=project-form]', (el) => el.dataset.tab)
          assert(tab === 'catalog', `著录项和档号被别人改过，应切到这个标签页：${tab}`)
          const keys = await page.$$eval('[data-testid=item-field]', (els) => els.map((e) => e.dataset.key))
          assert(keys.includes('other_no'), `重新载入后应显示别人加的著录项：${keys}`)
          const tpl = await page.$eval('[data-testid=archive-template]', (el) => el.value)
          assert(tpl === NEW_TEMPLATE, `重新载入后模板应是库里的（本地修改放弃）：${tpl}`)
          console.log(`  窗口 B 先保存（加 other_no、改说明），窗口 A 后保存：409，提示"${notice}"；库里是 B 的内容；A 已重新载入`)
          await sleep(200)
          mkdirSync(OUT, { recursive: true })
          await page.screenshot({ path: `${OUT}/m3-settings-conflict.png` })

          // 核对后重新改模板再保存：这次成功，两边的修改都在。
          await setValue(page, '[data-testid=archive-template]', mine)
          await page.click('[data-testid=project-form-submit]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          await sleep(200) // 等 puppeteer 的 response 事件到齐
          assert(JSON.stringify(patches.list()) === JSON.stringify([409, 200]), `再保存应 200：${patches.list()}`)
          const both = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
          assert(
            both.settings.archive_no.template === mine &&
              both.settings.item_fields.some((f) => f.key === 'other_no') &&
              both.description === bDescription &&
              both.status === 'archived',
            '重新保存后应同时有 A 的模板和状态、B 的著录项和说明',
          )
          console.log('  窗口 A 核对后再保存：成功，A 的模板和状态、B 的著录项和说明都在')
          patches.stop()

          // 别人只改了授权（对话框里看不到）：A 保存不提示，自动用新版本重发。授权要真的变了才换版本（没变化不写库），
          // 所以临时多授权一个角色，after 里改回去。
          await openEditor(page)
          const access = await ctx.api('GET', `/api/projects/${ctx.pid}/access`)
          accessRestore = { user_ids: access.users.map((u) => u.id), role_ids: access.roles.map((r) => r.id) }
          const extraRole = (await ctx.api('GET', '/api/system/projects/access-candidates')).roles.find(
            (r) => !accessRestore.role_ids.includes(r.id),
          )
          assert(extraRole, '找不到还没授权的角色')
          await ctx.api('PUT', `/api/projects/${ctx.pid}/access`, { ...accessRestore, role_ids: [...accessRestore.role_ids, extraRole.id] })
          const silent = '{fonds}-{year}-{item_no:04}'
          await setValue(page, '[data-testid=archive-template]', silent)
          const retried = recordPatches(page, ctx.pid)
          await page.click('[data-testid=project-form-submit]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          await sleep(200)
          retried.stop()
          assert(JSON.stringify(retried.list()) === JSON.stringify([409, 200]), `只改了授权时应自动重发一次：${retried.list()}`)
          const after = await ctx.api('GET', `/api/system/projects/${ctx.pid}`)
          assert(after.settings.archive_no.template === silent && after.settings.item_fields.some((f) => f.key === 'other_no'), '自动重发后模板应保存、别的不丢')
          console.log('  别人只改了授权：A 保存时 409 后自动用新版本重发（PATCH 409 → 200），不提示')

          // M4-4 审查：两个窗口把说明改成同一个值。后保存的看到"被别人改过（说明）"，但不说"修改没有保存"
          // （改的和别人一样，不算放弃）；再点保存没有修改，直接关掉、不发请求。
          await openEditor(page)
          await openEditor(other)
          const same = `两边改成一样的说明 ${Date.now()}`
          for (const p of [other, page]) {
            await p.click('[data-testid=project-form-tab][data-tab=basic]')
            await setValue(p, '#pf-desc', same)
          }
          await other.click('[data-testid=project-form-submit]')
          await other.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          const samePatches = recordPatches(page, ctx.pid)
          await page.click('[data-testid=project-form-submit]')
          await page.waitForSelector('[data-testid=project-form-conflict]', { timeout: 5000 })
          const sameNotice = await page.$eval('[data-testid=project-form-conflict]', (el) => el.textContent.trim())
          assert(
            sameNotice.includes('被别人改过（说明）') && !sameNotice.includes('没有保存') && !sameNotice.includes('其他修改'),
            `改成同一个值时不应提示"没有保存"：${sameNotice}`,
          )
          assert((await page.$eval('#pf-desc', (el) => el.value)) === same, '说明应是两边一样的值')
          await page.click('[data-testid=project-form-submit]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          await sleep(200)
          samePatches.stop()
          assert(JSON.stringify(samePatches.list()) === JSON.stringify([409]), `再点保存不应再发 PATCH：${samePatches.list()}`)
          assert((await ctx.api('GET', `/api/system/projects/${ctx.pid}`)).description === same, '库里的说明应是两边一样的值')
          console.log(`  两边把说明改成同一个值：后保存的提示"${sameNotice}"，不说"没有保存"；再点保存直接关掉`)
        } finally {
          await close()
        }
        await openEditor(page)
      },
      after: async () => {
        if (!ctxPromise || !conflictRestore) return
        const ctx = await ctxPromise
        await ctx.api('PATCH', `/api/system/projects/${ctx.pid}`, conflictRestore)
        conflictRestore = null
        console.log('  已把 M37-SET 的设置、说明、状态改回这一张之前的样子')
        if (accessRestore) {
          await ctx.api('PUT', `/api/projects/${ctx.pid}/access`, accessRestore)
          accessRestore = null
          console.log('  已把 M37-SET 的授权改回去')
        }
      },
    },
    {
      // 审查第 1 轮：逐层取已有编码，一个对话框里最多取一遍；关掉对话框后不再发请求。
      name: 'm3-settings-requests',
      user: admin,
      path: '/system/projects',
      prepare: async (page) => {
        const ctx = await context(page)
        // 取一遍要多少个请求：根下一层 1 个，加上每个有子节点的节点各 1 个。
        let expected = 0
        const queue = [null]
        while (queue.length) {
          const id = queue.shift()
          expected++
          const items = (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories${id ? `?parent_id=${id}` : ''}`)).items
          for (const c of items) if (c.child_count > 0) queue.push(c.id)
        }
        const tab = async (t) => {
          await page.click(`[data-testid=project-form-tab][data-tab=${t}]`)
          await sleep(150)
        }
        await interceptRequests(page, (u) => u.includes('/archive-categories'), async (log) => {
          log.delayMs = 250

          // 关掉对话框：取到一半就停，之后不再发。
          await openEditor(page)
          log.reset()
          await setValue(page, '[data-testid=archive-template]', '{fonds}.{category}-{item_no}')
          const t0 = Date.now()
          while (log.started.length === 0 && Date.now() - t0 < 5000) await sleep(20)
          assert(log.started.length > 0, '改模板后应开始取已有编码')
          await page.click('[data-testid=project-form-cancel]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=project-form]'), { timeout: 5000 })
          const atClose = log.started.length
          await sleep(2000)
          const afterClose = log.started.length - atClose
          assert(afterClose === 0, `关掉对话框后又发了 ${afterClose} 个请求`)
          assert(atClose < expected, `关掉时应还没取完（${atClose} / ${expected}）`)
          console.log(`  取到一半关掉对话框：关掉时已发 ${atClose} 个（取完要 ${expected} 个），之后 2 秒没有再发`)

          // 新对话框：改模板、来回切标签页、再改模板，只取一遍。
          await openEditor(page)
          await log.waitQuiet(300)
          log.reset()
          await setValue(page, '[data-testid=archive-template]', '{fonds}.{category}-{item_no}')
          for (let i = 0; i < 3; i++) {
            await tab('basic')
            await tab('catalog')
          }
          await page.waitForSelector('[data-testid=template-codes-warning][data-state=ready]', { timeout: 15000 })
          await setValue(page, '[data-testid=archive-template]', '{fonds}.{year}-{item_no}')
          await tab('basic')
          await tab('catalog')
          await log.waitQuiet(1000)
          assert(log.started.length === expected, `一个对话框里应只取一遍（${expected} 个请求），实际 ${log.started.length} 个`)
          const n = await page.$eval('[data-testid=template-codes-warning]', (el) => Number(el.dataset.count))
          assert(n === 1, `应提示 1 个已有编码（Z109.A）含分隔符，是 ${n}`)
          console.log(`  改模板、来回切 4 次标签页、再改模板：一共 ${log.started.length} 个请求（正好一遍），提示 1 个编码冲突`)
        })
        await page.$eval('[data-testid=template-codes-warning]', (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
  ],
}

/** m3-settings-conflict-resaved 开始前的设置（after 里改回去）。 */
let conflictRestore = null
/** m3-settings-conflict-resaved 临时改授权之前的授权（after 里改回去）。 */
let accessRestore = null
