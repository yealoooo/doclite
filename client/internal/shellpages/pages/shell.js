// 客户端自带的页面：连接服务器、连不上、需要升级、不能打开界面（约定见 client/BRIDGE.md 的"客户端自带的页面"）。
// 不依赖 web：用壳自己的运行时（/wails/runtime.js）调桥命令。
//
// 打开方式：
//   /shell/            启动时的关口：没配置地址 → 连接页；探测（check_server）连不上 → 连不上页；版本过低 → 升级页；
//                      都没问题 → 进界面（location.replace('/')）
//   /shell/?edit       修改服务器地址（界面里"修改服务器地址"跳过来；取消回到界面）
//   /shell/?upgrade    需要升级（界面里任何请求返回 426 时跳过来）。只信探测的结果：探测确认要升级才显示升级页，
//                      下载地址只用探测到的；探测说不用升级就回关口（短时间里反复被带过来时停在"连不上"页说明，不来回跳）。
//   导航保护没装上时，资产服务器在界面的地址上给出这一页并带 data-view="blocked"、data-reason。

const INVOKE = 'doclite/client/internal/shell.Bridge.Invoke'

let runtimePromise = null
function runtime() {
  runtimePromise ??= import('/wails/runtime.js')
  return runtimePromise
}

function isShellError(v) {
  return typeof v === 'object' && v !== null && typeof v.kind === 'string' && typeof v.message === 'string'
}

function message(e) {
  if (isShellError(e) || e instanceof Error) return e.message
  return String(e)
}

/** 调壳的命令；失败时 reject {kind, message}。 */
async function invoke(cmd, args = {}) {
  let rt
  try {
    rt = await runtime()
  } catch (e) {
    throw { kind: 'internal', message: `加载客户端运行时失败：${message(e)}` }
  }
  try {
    return await rt.Call.ByName(INVOKE, cmd, args)
  } catch (e) {
    if (isShellError(e?.cause)) throw { kind: e.cause.kind, message: e.cause.message }
    if (isShellError(e)) throw { kind: e.kind, message: e.message }
    throw { kind: 'internal', message: message(e) }
  }
}

const $ = (id) => document.getElementById(id)
const VIEWS = ['checking', 'connect', 'unreachable', 'upgrade', 'blocked']

let info = null
let editing = false
let busy = false
let attempts = 0
let seq = 0
/** 这一次加载的页面（自检报告用，壳据此丢掉同一页面先发后到的旧报告）。 */
const pageId = `${Date.now()}-${Math.random().toString(36).slice(2)}`
let reporting = Promise.resolve()

function show(view, detail = {}) {
  for (const v of VIEWS) $(`view-${v}`).hidden = v !== view
  if (info?.smoke) {
    // 自检（client/internal/shell/smoke.go）按页面报告的状态往下走；一个接一个地报，保证顺序。
    const report = {
      page: pageId,
      seq: ++seq,
      view,
      editing: !!detail.editing,
      server_url: info?.server_url ?? null,
      error: detail.error ?? null,
      attempt: detail.attempt ?? 0,
    }
    reporting = reporting.then(() => invoke('smoke_shell_view', report)).catch(() => {})
  }
}

// ---------- 关口 ----------

async function gate() {
  show('checking')
  if (!info.server_url) {
    showConnect(false)
    return
  }
  let probe
  try {
    probe = await invoke('check_server')
  } catch (e) {
    if (e.kind === 'not_configured') showConnect(false)
    else showUnreachable(message(e))
    return
  }
  if (!probe.reachable) showUnreachable(probe.error ?? '原因不明')
  else if (probe.upgrade_required) showUpgrade(probe)
  else location.replace('/')
}

// ---------- 连接服务器 ----------

function showConnect(isEdit, error = '') {
  editing = isEdit
  const input = $('server-address')
  if (isEdit && !input.value) input.value = info?.server_url ?? ''
  $('connect-title').textContent = isEdit ? '修改服务器地址' : '连接服务器'
  $('connect-cancel').hidden = !isEdit
  $('connect-error').textContent = error
  $('connect-error').hidden = !error
  $('connect-note').hidden = !isEdit || !!error
  show('connect', { editing: isEdit, error })
  input.focus()
}

async function submitConnect(ev) {
  ev?.preventDefault()
  const url = $('server-address').value.trim()
  if (!url || busy) return
  busy = true
  const button = $('connect-submit')
  button.disabled = true
  button.textContent = '正在连接…'
  try {
    const probe = await invoke('probe_server', { url })
    if (!probe.reachable) {
      showConnect(editing, `连不上 ${probe.server_url}：${probe.error ?? '原因不明'}`)
      return
    }
    // 版本过低也先保存：回到关口显示升级提示。地址变了会清空本机的会话（已经配置过地址时壳先弹确认框）。
    await invoke('set_server', { url: probe.server_url })
    location.replace('/shell/')
  } catch (e) {
    showConnect(editing, e.kind === 'invalid_request' ? e.message : `保存失败：${message(e)}`)
  } finally {
    busy = false
    button.disabled = false
    button.textContent = '连接'
  }
}

// ---------- 连不上 ----------

function showUnreachable(reason) {
  attempts++
  $('unreachable-server').textContent = info?.server_url ?? ''
  $('unreachable-reason').textContent = reason
  show('unreachable', { error: reason, attempt: attempts })
}

async function retry() {
  const button = $('unreachable-retry')
  button.disabled = true
  button.textContent = '正在重试…'
  try {
    await gate()
  } finally {
    button.disabled = false
    button.textContent = '重试'
  }
}

// ---------- 需要升级 ----------

function showUpgrade(probe) {
  $('upgrade-current').textContent = probe.client_version ?? info?.version ?? '—'
  $('upgrade-min').textContent = probe.min_version ?? '—'
  $('upgrade-latest').textContent = probe.latest_version ?? '—'
  $('upgrade-url').textContent = probe.download_url ?? '—'
  $('upgrade-download').hidden = !probe.download_url
  $('upgrade-download').dataset.url = probe.download_url ?? ''
  $('upgrade-no-url').hidden = !!probe.download_url
  $('upgrade-error').hidden = true
  show('upgrade', {})
}

/** 短时间里被界面带到升级页、探测却说不用升级的次数（防止 / 和 /shell/?upgrade 之间来回跳）。 */
function upgradeBounces() {
  try {
    const key = 'doclite_upgrade_bounce'
    const now = Date.now()
    const list = JSON.parse(sessionStorage.getItem(key) || '[]').filter((t) => now - t < 60_000)
    list.push(now)
    sessionStorage.setItem(key, JSON.stringify(list))
    return list.length
  } catch {
    return 1
  }
}

async function upgradePage() {
  show('checking')
  let probe
  try {
    probe = await invoke('check_server')
  } catch (e) {
    showUnreachable(message(e))
    return
  }
  if (!probe.reachable) {
    showUnreachable(probe.error ?? '原因不明')
  } else if (probe.upgrade_required) {
    showUpgrade(probe)
  } else if (upgradeBounces() >= 3) {
    showUnreachable('服务器拒绝了这个版本的客户端，但探测时又说不用升级。请稍后重试，或联系系统管理员。')
  } else {
    location.replace('/')
  }
}

async function download() {
  const url = $('upgrade-download').dataset.url
  if (!url) return
  try {
    await invoke('open_external', { url })
  } catch (e) {
    $('upgrade-error').textContent = `打开下载地址失败：${message(e)}`
    $('upgrade-error').hidden = false
  }
}

// ---------- 不能打开界面 ----------

function showBlocked(reason) {
  $('blocked-reason').textContent = reason
  show('blocked', { error: reason })
}

// ---------- 启动 ----------

function cancelEdit() {
  location.replace('/')
}

async function init() {
  $('view-connect').addEventListener('submit', submitConnect)
  $('connect-cancel').addEventListener('click', cancelEdit)
  $('unreachable-edit').addEventListener('click', () => showConnect(true))
  $('unreachable-retry').addEventListener('click', retry)
  $('upgrade-download').addEventListener('click', download)
  $('upgrade-edit').addEventListener('click', () => showConnect(true))
  $('upgrade-recheck').addEventListener('click', gate)

  try {
    info = await invoke('shell_info')
  } catch (e) {
    showUnreachable(`客户端内部出错：${message(e)}`)
    return
  }
  $('client-version').textContent = info.version
  if (info.smoke) {
    // 自检用（只在 --smoke 时有）：壳用 ExecJS 操作这一页。
    window.docliteShell = {
      connect(url) {
        $('server-address').value = url
        void submitConnect()
      },
      retry: () => void retry(),
      edit: () => showConnect(true),
      cancel: cancelEdit,
    }
  }

  const forced = document.body.dataset.view
  const params = new URLSearchParams(location.search)
  if (forced === 'blocked') showBlocked(document.body.dataset.reason || '客户端不能打开界面')
  else if (params.has('edit')) showConnect(!!info.server_url)
  else if (params.has('upgrade')) await upgradePage()
  else await gate()
}

void init()
