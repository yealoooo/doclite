# 界面（web）和客户端的约定（桥）

web（界面）和 client（桌面客户端，壳）是两个独立的模块（requirements 1.3），代码上互不依赖。客户端打包时嵌入某个版本的 web 产物包（[界面从哪来](#界面从哪来)），两边之间只有两样约定：

- **HTTP 接口**：界面经壳调 server 的 `/api`（requirements 第 8 节），壳只代理，不改内容；
- **这份文档**：界面能调壳的哪些命令、收哪些事件、用哪些地址，以及双方各自要遵守的规则。

改了这里的任何一项，两边要一起看。新增功能按[兼容规则](#兼容规则)加能力，不要改已有命令的意思。

## 目录

1. [界面从哪来](#界面从哪来)
2. [界面要遵守的](#界面要遵守的)
3. [调用方式](#调用方式)
4. [桥的白名单](#桥的白名单)
5. [能力](#能力)
6. [命令](#命令)
7. [事件](#事件)
8. [流](#流)
9. [取图地址](#取图地址)
10. [客户端自带的页面](#客户端自带的页面)
11. [兼容规则](#兼容规则)
12. [信任边界](#信任边界)
13. [自检用的命令](#自检用的命令)

## 界面从哪来

**界面随客户端打包，版本跟客户端走**（requirements 1.3，参照 Element）。web 构建出带版本号的产物包（`npm run release` → `web/release/doclite-web-<版本>.tar.gz`，包里是 `manifest.json` 和 `dist/`）；客户端用 `client/WEB_VERSION` 记要用的 web 版本，打包时取这个产物包（`DOCLITE_WEB_ARTIFACT` 指定的文件，没给就找 `../web/release/doclite-web-<WEB_VERSION>.tar.gz`），核对清单里的版本后嵌进可执行文件（`cmd/webartifact`）。客户端不读 web 的源码；拆仓库以后改成从发布地址下载这个包。改界面要升级客户端。

客户端里配置的"服务器地址"是 **server 的接口地址**（本地开发是 `http://127.0.0.1:9080`）：客户端只经它访问 `/api`，不依赖网页服务。

WebView 里页面的地址是壳的资产服务器（macOS `wails://localhost`，Windows `http://wails.localhost`），按路径分：

| 路径 | 谁处理 |
|---|---|
| `/wails/` | Wails 自己：运行时 `/wails/runtime.js`、命令调用 `/wails/runtime`（先过[白名单](#桥的白名单)）、流、事件。其余 `/wails/` 地址 404 |
| `/shell/img/` | 取图、本机图片缓存、切块（[取图地址](#取图地址)） |
| `/shell/` | [客户端自带的页面](#客户端自带的页面)：启动关口、连接服务器、连不上、需要升级、不能打开界面 |
| `/api/` | 404。界面的接口请求一律走命令 `api_fetch` 和流 `doclite.api_stream`，由壳的会话代理带上 Cookie 和客户端头 |
| 其余 | 嵌入的界面（web 产物的 `dist/`）；没有扩展名的路径回退到 `index.html` |

启动时窗口先打开 `/shell/`（客户端的关口页）：没配置地址就停在连接页；探测（`check_server`）连不上、版本过低就停在对应的页；都没问题才 `location.replace('/')` 进界面。

**开发构建**（不带 production 标签，`wails3 task build:dev`）设了 `FRONTEND_DEVSERVER_URL` 时，"其余"由 Wails 转给 vite 的开发服务器（热更新，页面地址带 vite 的端口，CSP 另外放行本机的 WebSocket）。正式构建（`wails3 package`）只用嵌入的产物，不认这个环境变量。

## 界面要遵守的

- **入口**：`dist/index.html`；前端路由（没有扩展名的路径）由壳回退到它；
- **CSP**（壳加，界面不能放宽）：`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-src 'none'`。所以：没有内联脚本和 `eval`，不引用外部资源，不能直连 server 或存储，不能用 iframe；
- **不能离开资产服务器**：导航到别的地址会被拦下（macOS 已有，Windows 在 M8）。下载、外部链接用命令 `open_external` 交给系统浏览器；
- **判断在不在壳里**：`location.protocol === 'wails:'` 或 `location.hostname === 'wails.localhost'`；在壳里用到本机功能一律按[能力](#能力)判断；
- **运行时**：在壳里用 `import('/wails/runtime.js')`（壳给的，和壳里的 Wails 同一个版本），web 不打包 Wails 的运行时；浏览器里不加载。只用 `Call.ByName`（以及它自带的取消）、`Events.On`、`JSONStream`，运行时的其他对象在壳里被拦下；
- 界面不改壳的设置，换服务器地址要去客户端自带的页面（`/shell/?edit`）。

## 调用方式

壳只绑定了一个方法，按命令名分发：

```js
const rt = await import('/wails/runtime.js')
const result = await rt.Call.ByName('doclite/client/internal/shell.Bridge.Invoke', '命令名', { /* 参数 */ })
```

- 参数是 JSON 对象，键名照下面的表；没有参数的传 `{}`；
- **命名：参数 camelCase，结果和事件的数据 snake_case**（沿用上一版，新命令照此）；
- 失败时运行时抛出 `RuntimeError`，壳给的错误在它的 `cause` 里：`{kind, message}`。`message` 是给人看的中文，可以直接提示；
- `kind`：

| kind | 意思 |
|---|---|
| `invalid_request` | 参数不对、不允许（例如路径不在 `/api/` 下、地址格式不对、用户在确认框里取消了） |
| `not_configured` | 还没配置服务器地址 |
| `network` | 连不上 server、超时、连接中断 |
| `internal` | 壳内部出错（本机文件读写、队列库打不开等）。`message` 只有一句概括，带本机路径的细节只写进客户端日志 |
| `unsupported` | **这个客户端没有这个命令**（比界面旧），界面隐藏相应功能或提示升级客户端 |

## 桥的白名单

Wails 的运行时在 `POST /wails/runtime` 上不只有绑定方法，还有 Browser.OpenURL、Clipboard、Dialog、Window、Application.Quit 等对象，而且 Wails 不看请求方法、请求体为空时从查询串取参数。壳在 Wails 之前收紧（`internal/shell/runtimeguard.go`）：

- 只许 `POST`、`Content-Type: application/json`、不带查询串、请求体非空；
- 请求体按 Wails 自己的结构解析（同样的 Go 结构体和 `encoding/json`：重复的键以最后一个为准，键名不分大小写，和 Wails 看到的一致），只放行：
  - `Call`（object 0）的 CallBinding（method 0），`methodName` 是 `doclite/client/internal/shell.Bridge.Invoke`——所有命令都走它；
  - `CancelCall`（object 10，method 0）——运行时取消一个还在进行的调用；
- 运行时把超过 512 KB 的请求体分块发（`x-wails-chunk-id`、`-index`、`-total`）：壳先把各块收齐拼好再按上面校验，放行时当作一个普通请求交给 Wails；
- 其余一律 403（日志里记一条"拦下了不在白名单里的运行时调用"）。`--smoke` 的 `runtime_guard` 一步在打包的客户端里核对（页面自己发带查询串的 GET、text/plain、System.Environment、别的绑定方法，都要 403）。

运行时另有一条不经 HTTP 的消息通道（macOS 的 `webkit.messageHandlers`、Windows 的 `chrome.webview.postMessage`），Wails 只认窗口拖动、改大小、`wails:runtime:ready` 几种消息（壳没有打开 `AllowSimpleEventEmit`），碰不到命令和本机功能。

## 能力

`shell_info` 的 `capabilities` 列出这个客户端有哪些功能。同一份 web 代码在浏览器里、在嵌入它的不同版本的客户端里都要能跑，所以界面按能力决定显示哪些本机功能：没有的隐藏，或者在用到时提示"请升级客户端"，不报错。界面已经知道没有的能力，对应的命令不发出去（web 的 `lib/wails.ts` 直接当 `unsupported` 处理）。

| 能力 | 命令 | 事件、流、地址 |
|---|---|---|
| `api` | `api_fetch` | 流 `doclite.api_stream` |
| `server` | `probe_server`、`check_server`、`set_server` | |
| `pages` | | 客户端自带的页面 `/shell/`、`/shell/?edit`、`/shell/?upgrade` |
| `external` | `open_external` | |
| `tray` | `tray_status` | `tray:error` |
| `upload` | `upload_status`、`upload_recent`、`upload_retry_failed`、`upload_resume`、`pick_and_enqueue_upload`、`hotfolder_status`、`hotfolder_pick_dir`、`hotfolder_set`、`hotfolder_start`、`hotfolder_pause` | `upload:*`、`hotfolder:state` |
| `rescan` | `pick_and_rescan` | `rescan:progress` |
| `images` | `cache_stats`、`cache_clear`、`cache_set_limit`、`prepare_tiles` | 取图地址 `/shell/img/…` |
| `settings` | `local_dirs`、`open_local_dir` | `tray:open-settings` |
| `smoke` | `smoke_*`（只在 `--smoke` 时有） | |

0.2.0（第一个按能力协商的客户端）有上面除 `settings`、`smoke` 以外的全部能力；`settings` 是 M7-11（客户端的设置页）加的。`shell_info` 本身不属于任何能力，总是有。

## 命令

### 基础

| 命令 | 参数 | 结果 | 说明 |
|---|---|---|---|
| `shell_info` | 无 | `ShellInfo` | 见下 |
| `probe_server` | `{url}` | `ProbeResult` | 探测一个还没保存的地址（规整成 origin：没写协议按 http，只许 http、https，不能带用户名、路径、查询串）。格式不对 `invalid_request` |
| `check_server` | 无 | `ProbeResult` | 探测已配置的地址；没配置 `not_configured` |
| `set_server` | `{url}` | 规整后的地址（字符串） | 保存地址。**已经配置过、要改成别的地址时，壳先弹系统确认框**（"从 A 改为 B"，默认按钮是"取消"），用户取消时 `invalid_request`"已取消，服务器地址没有改"；首次配置、地址没变、`--smoke` 时不弹。地址变了：清空会话（Cookie 罐），图片缓存的来源记录、上传队列按服务器隔离各自重来 |
| `open_external` | `{url}` | `null` | 用系统浏览器打开：只许 http、https，不能带用户名密码，≤ 16 KB，没有空白和控制字符 |

`ShellInfo`：

```ts
{
  version: string              // 客户端的版本（client/VERSION），也就是 X-Client-Version
  web_version: string | null   // 嵌入的界面的版本（web 产物包的清单，client/WEB_VERSION），没嵌入界面时 null
  server_url: string | null    // 已配置的地址（server 的接口地址），没配置时 null
  data_dir: string             // 本机数据目录（只用来显示）
  platform: string             // macos、windows……
  arch?: string                // 处理器架构 amd64、arm64……（M7-11 起有）
  os_version?: string | null   // 系统的名字和版本，例如 "Windows 10 Pro 2009 (Build: 19045)"、"MacOS 15.6"；取不到时 null（M7-11 起有）
  smoke: boolean               // 在跑 --smoke
  capabilities: string[]       // 见"能力"；不认识的忽略
}
```

`ProbeResult`：`GET /api/health` 要 200 和 `{status, version}`（只要它通就算连得上）；再查 `GET /api/shell/latest`（最低、最新版本和下载地址，server 没有这个接口时 404，跳过）；最后用**不带会话**的 `GET /api/me` 问 server 收不收这个客户端版本——server 的版本检查在所有 `/api`（health、shell 除外）最外层、鉴权之前，版本过低回 426 `client_upgrade_required`（details 带最低版本、最新版本、下载地址），收的话是 401。

```ts
{
  server_url: string
  reachable: boolean           // /api/health 通
  error: string | null         // 连不上时给人看的原因
  server_version: string | null
  min_version: string | null   // 来自 /api/shell/latest，没有时用 426 的 details
  latest_version: string | null
  download_url: string | null
  client_version: string
  upgrade_required: boolean    // 客户端版本低于 min_version，或 /api/me 回 426
}
```

### 会话代理

`api_fetch {req}` → `{status, headers, body}`：

```ts
req = {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: string                 // 以 /api/ 开头，不带查询串；不能有 .、..、空段、%2e、%2f、%5c、%00
  query: [string, string][]    // 查询参数，顺序保留
  headers: Record<string, string>  // 只能是 accept、content-type、if-match、if-none-match、last-event-id
  body?: string                // ≤ 16 MB（上传原图走上传队列，不经这里）
}
// 响应：status 照常（4xx、5xx 不算命令失败）；headers 只有 content-type、content-disposition、etag、
// last-modified、cache-control、retry-after（名字小写）；body 是文本，≤ 64 MB。不跟随跳转。
```

只能访问已配置地址上 `/api/` 下的路径。壳加 `X-Client-Type: desktop`、`X-Client-Version` 和会话 Cookie；页面拿不到 `Set-Cookie`。`POST /api/auth/logout`（不论结果）清空 Cookie 罐。连不上、超时是 `network`。

### 上传队列、热文件夹（能力 `upload`）

| 命令 | 参数 | 结果 |
|---|---|---|
| `upload_status` | 无 | `QueueStatus` |
| `upload_recent` | `{limit, status: ItemStatus[] \| null}` | `QueueItem[]`（最近的，新的在前） |
| `upload_retry_failed` | `{ids: number[] \| null}`（null 表示全部失败的） | 重试了几个（number） |
| `upload_resume` | 无 | `QueueStatus`（401、426 暂停后手动再试） |
| `pick_and_enqueue_upload` | `{folderId, mode: 'files' \| 'folder'}` | `{cancelled, added: number[], skipped: {path, reason}[]}` |
| `hotfolder_status` | 无 | `HotFolderStatus` |
| `hotfolder_pick_dir` | 无 | 选的目录（字符串），取消时 `null` |
| `hotfolder_set` | `{watchDir, targetFolderId}` | `HotFolderStatus` |
| `hotfolder_start`、`hotfolder_pause` | 无 | `HotFolderStatus` |

- 本机的文件和目录**只能由用户在系统对话框里选**：`pick_and_enqueue_upload` 自己弹对话框，页面给不了路径；`hotfolder_set` 的 `watchDir` 只接受 `hotfolder_pick_dir` 选过的目录或者现有配置里的目录；
- `folderId`、`targetFolderId` 是服务器上的文件夹 id（UUID），传到已配置的服务器；
- 类型（字段和 web 的 `web/src/lib/scan-queue.ts` 一致）：
  - `ItemStatus`：`waiting`、`queued`、`uploading`、`done`、`duplicate`、`failed`；
  - `QueueStatus`：`{server_url, paused: {reason: 'auth_required' | 'upgrade_required', message, details, since} | null, paused_sources: string[], stats: {waiting, queued, uploading, done, duplicate, failed, retrying, total}, running}`；
  - `QueueItem`：`{id, source, server_url, local_path, file_name, rel_dir, target_folder_id, folder_id, size, mtime_ns, sha256, status, attempts, next_attempt_at, file_id, server_file_name, sort_no, cover_status: 'included' | 'not_generated' | 'rejected' | null, cover_reason, error_code, error_message, warnings: string[], duplicate_of: {file_id?, folder_path?, file_name?} | null, created_at, updated_at, finished_at}`（时间是 Unix 毫秒）；
  - `HotFolderStatus`：`{config: {watch_dir, target_folder_id, server_url, scan_interval_secs, running} | null, running, watching, error, last_scan_at, scans}`。`config.running` 是"壳重启后自动开始"（Go 里叫 `AutoStart`，JSON 沿用旧名）；外面的 `running` 是现在在不在运行。`config.scan_interval_secs` 总是 15：目录事件不可用（`watching` 为 false）时每 15 秒全量扫描一次（有目录事件时只重新列出有变化的目录，另外每 60 秒全量扫描兜底）。

### 重扫件（能力 `rescan`）

`pick_and_rescan {fileId}` → `RescanOutcome`：系统对话框选一个文件 → 查文件尾 → 算 sha → 封面 → `POST /api/files/{fileId}/rescan`，同步返回；同时只做一个。

```ts
{
  cancelled: boolean
  local_name: string | null
  size: number | null
  sha256: string | null
  cover: { status: 'included' | 'not_generated' | 'rejected', reason: string | null, ms: number | null } | null
  attempts: number
  result: object | null        // server 的重扫响应
  error: { status, code, message, details } | null   // server 拒绝时
  elapsed_ms: number
}
```

### 托盘（能力 `tray`）

`tray_status` → `{created, error, close_to_tray, tooltip, status_line, counts_line, toggle: 'pause' | 'start' | 'unavailable', close_check: {ok, error} | null}`。

### 图片缓存和切块（能力 `images`）

| 命令 | 参数 | 结果 |
|---|---|---|
| `cache_stats` | 无 | `{dir, available, error, total_bytes, file_count, limit_bytes, projects: [{project_id, total_bytes, file_count, cover_count, original_count, tiles_count}], counters: {hits, misses, downloads, download_bytes, rejected, errors, tilings, tiling_failures}}` |
| `cache_clear` | `{projectId: string \| null}`（null 表示全部） | `{removed_files, removed_bytes, failed_files}` |
| `cache_set_limit` | `{limitBytes}`（100 MB 到 1 TB） | 同 `cache_stats` |
| `prepare_tiles` | `{fileId, snapshotId}` | `{status: 'ready' \| 'generating' \| 'queued' \| 'failed', code, message}`；不在来源记录里 `invalid_request` |

### 设置页（能力 `settings`）

客户端的设置页（web 的 `/settings`，只在客户端里出现）用：

| 命令 | 参数 | 结果 |
|---|---|---|
| `local_dirs` | 无 | `{data, logs, cache}`，每项 `{path, size_bytes, file_count}` |
| `open_local_dir` | `{dir: 'data' \| 'logs' \| 'cache'}` | `null` |

- `local_dirs`：`data` 是整个数据目录（同 `shell_info.data_dir`，大小含日志、图片缓存、上传队列库），`logs` 是日志目录（按天滚动，留 14 天），`cache` 是本机图片缓存的目录（大小取缓存自己的统计，同 `cache_stats`）。路径只用来显示；
- `open_local_dir`：用系统的文件管理器（macOS 访达、Windows 资源管理器）打开其中一个目录。**`dir` 只能是这三个名字**（区分大小写，不能带空白、斜杠、`..`），其余一律 `invalid_request`；**页面给不了路径**，路径由壳按名字算（都在数据目录里）。目录不在、打不开时 `internal`（不带路径）。`--smoke` 时不打开（不抢焦点），照常校验参数、返回 `null`；
- 托盘菜单的"设置…"：壳显示主窗口，再发事件 `tray:open-settings`（没有数据），页面收到后跳到 `/settings`（没登录时先到登录页，登录后回来）。页面停在客户端自带的页面上时没人订阅，只是显示窗口。

## 事件

页面用运行时的 `Events.On(名字, ev => …)` 订阅，数据在 `ev.data`。

| 事件 | 数据 | 能力 |
|---|---|---|
| `upload:item` | `QueueItem` | `upload` |
| `upload:item-removed` | `{id}` | `upload` |
| `upload:progress` | `{id, sent, size}`（上传中约每 250 毫秒一次） | `upload` |
| `upload:state` | `QueueStatus` | `upload` |
| `upload:auth-required` | 无 | `upload` |
| `upload:upgrade-required` | `{details}`（426 的 details） | `upload` |
| `hotfolder:state` | `HotFolderStatus` | `upload` |
| `rescan:progress` | `{file_id, phase: 'hashing' \| 'cover' \| 'uploading', sent, size}` | `rescan` |
| `tray:error` | `{message}`（托盘上"继续热文件夹"失败） | `tray` |
| `tray:open-settings` | 无（托盘菜单的"设置…"，页面跳到设置页） | `settings` |

## 流

`doclite.api_stream`（能力 `api`）：SSE 等流式接口。页面用运行时的 `JSONStream('doclite.api_stream')` 打开，连上后先发一帧 `{req}`（同 `api_fetch` 的 `req`），壳逐条发回：

```ts
{ event: 'open', status: number, headers: Record<string, string> }
{ event: 'chunk', data: string }        // 按 UTF-8 字符边界切好的文本
{ event: 'end' }
{ event: 'error', kind: string, message: string }
```

`end`、`error` 之后壳关掉流。页面关流、页面重载、壳退出时壳断开到 server 的连接。4xx、5xx 也是 `open` → `chunk`（错误体）→ `end`。

## 取图地址

能力 `images`。和页面同源，`<img>`、`fetch`、OpenSeadragon 直接用；**只认来源记录**：图片信息要先经 `POST /api/files/batch` 或 `GET /api/files/{fid}/snapshots`（经 `api_fetch`）取得，壳记下其中的预签名地址，登录、退出、换服务器清空。

| 地址 | 内容 |
|---|---|
| `/shell/img/cover/<file_id>/<snapshot_id>` | 封面（JPEG） |
| `/shell/img/original/<file_id>/<snapshot_id>` | 原图（核对大小和 sha256） |
| `/shell/img/dzi/<file_id>/<snapshot_id>` | 分块描述（DZI 的 JSON 写法，块地址在 `Image.Url`） |
| `/shell/img/tile/<file_id>/<snapshot_id>/<级>/<x>_<y>.<jpg\|png>` | 一块（512×512、重叠 1） |
| `/shell/img/test/probe.png` | 自检的测试图（64×64） |

响应带 `Cache-Control: no-store`；封面、原图、分块描述带 `X-Doclite-Cache: hit|miss`。失败时是 JSON `{code, message}` 并带 `X-Doclite-Error: <code>`：404 `bad_path`、`not_found`（不在来源记录里）、`no_url`、`out_of_range`；422 切不了（`too_large`、`timeout`、`unsupported_format`、`unsupported`、`corrupt`），页面只显示封面；502 下载失败（`upstream`、`integrity`）；503 `unavailable`（可以重试）；500 `io`。

## 客户端自带的页面

能力 `pages`。简单的 HTML、CSS、JS，embed 在客户端里（`internal/shellpages`），不依赖 web，也用这份约定里的命令。

| 地址 | 用途 | 界面什么时候跳过来 |
|---|---|---|
| `/shell/` | 启动时的关口：没配置 → 连接页；连不上 → 连不上页（重试、修改地址）；版本过低 → 升级页；都没问题 → `location.replace('/')` | 不用跳（壳启动时自己打开） |
| `/shell/?edit` | 修改服务器地址（带上原地址；"取消"回 `/`）。已经配置过地址时壳会弹系统确认框 | 设置页、登录页里的"修改服务器地址"：`location.assign('/shell/?edit')` |
| `/shell/?upgrade` | 需要升级：**只信探测的结果**——`check_server` 确认 `upgrade_required` 才显示升级页，版本和下载地址只用探测到的（查询串里的一概不用）；探测说不用升级就回 `/`（一分钟里被带过来 3 次以上时停在"连不上"页说明，不来回跳） | 任何请求返回 426（`client_upgrade_required`）：`location.replace('/shell/?upgrade')` |

连接页先 `probe_server`，连得上才 `set_server`，再回 `/shell/`。macOS 上导航保护装不上时，资产服务器不给界面，在界面的地址上给出这一页的"客户端不能打开界面"（失败就关闭）。客户端没有 `pages` 能力时，界面隐藏"修改服务器地址"，426 时只提示。

## 兼容规则

- **server 和客户端**：server 的接口只增不改，兼容旧一版客户端；页面离不开的变化（例如 `api_fetch` 的约定变了、界面要用新接口而旧客户端嵌入的界面不会用）靠 server 的 `MIN_CLIENT_VERSION` 强制升级（低于它的客户端整页提示升级，自动更新在 M8）；
- **界面和客户端**：客户端嵌入哪个版本的 web 由 `client/WEB_VERSION` 定，打包时就配好。界面仍按能力判断本机功能：用到新命令前先看 `capabilities`，没有就隐藏或提示升级客户端；万一发出去了，客户端回 `unsupported`，界面照常提示，不报错；
- **客户端**不删、不改已有命令的参数和结果的意思；加字段可以（界面忽略不认识的字段）；
- **加功能**：新命令归进已有的一项能力（这项能力的含义没变时），或者加一项新能力；同时更新这份文档；
- 客户端的版本号在 `client/VERSION`，嵌入的界面的版本在 `client/WEB_VERSION`（来自 `web/package.json`），各自独立，设置页的"关于"里都显示。

## 信任边界

**谁能调桥**：只有资产服务器给出的同源页面——也就是**随客户端发布的界面**（嵌入的 web 产物）和**客户端自带的页面**（`/shell/`）。页面不来自网络（开发构建接 vite 时除外），所以信任它们就是信任客户端的发布包本身。

- 页面离不开资产服务器：macOS 的导航保护只许 `wails://localhost` 和 `about:`；**装不上时失败就关闭**（不给界面，只显示说明页）。Windows 的导航保护在 M8；CSP `frame-src 'none'`，没有 iframe；
- 资产服务器最外层的中间件拦下主机不对、`Origin` 不是资产服务器自己的（协议和主机不对；端口不管）、`Sec-Fetch-Site` 是 cross-site 或 same-site 的请求（包括桥的 `/wails/runtime`）。实测 macOS 的 WebKit 对自定义协议的 fetch 不带 `Origin`，这一道主要在 Windows（WebView2）上起作用；
- 桥的入口只放行 `Bridge.Invoke` 和取消调用（[桥的白名单](#桥的白名单)），Wails 运行时的其他对象（打开任意地址、剪贴板、系统对话框、窗口、退出）页面一概调不到；
- 界面调 server 只经会话代理：页面拿不到会话 Cookie，只能访问已配置地址上 `/api/` 下的路径。

**页面通过桥能做什么**（都限在已配置的服务器和用户自己的操作上）：

| 能力 | 能做的 | 限制 |
|---|---|---|
| `api` | 以当前登录的用户调已配置服务器的 `/api/` | 只许 `/api/` 下的路径和少数请求头；拿不到会话 Cookie；server 照常按菜单、项目授权检查 |
| `server` | 探测地址、换服务器地址 | 已经配置过地址时换地址要用户在系统确认框里点"改为新地址"；换了清空会话 |
| `external` | 用系统浏览器打开 http、https 地址 | 不能是 file:、自定义协议，不能带用户名密码 |
| `upload`、`rescan` | 把本机文件传到已配置服务器的某个文件夹 | 文件、监控目录只能由用户在系统对话框里选，页面给不了本机路径 |
| `images` | 读、清空本机图片缓存，切块 | 只认来源记录里的图；上限 100 MB–1 TB |
| `tray` | 读托盘状态 | |
| `settings` | 读数据、日志、图片缓存三个目录的路径和大小；用系统的文件管理器打开其中一个 | 只认 `data`、`logs`、`cache` 三个名字，路径由壳算，页面给不了路径 |
| `smoke` | 自检用的命令 | 只在 `--smoke` 进程里登记，正常启动时调了是 `unsupported` |

**会把本机路径告诉页面的**（都是为了显示）：`shell_info.data_dir`、`local_dirs` 的三个 `path`（设置页）；`cache_stats.dir`，缓存不可用时 `cache_stats.error` 也可能带路径（设置页的"本机图片缓存"）；`hotfolder_pick_dir` 的结果、`hotfolder_status.config.watch_dir`、`upload_recent` 的 `local_path`、`pick_and_enqueue_upload` 的 `skipped[].path`、`pick_and_rescan` 的 `local_name`（扫描上传页、上传结果）；`upload_status`、热文件夹的 `error` 和上传失败的 `error_message` 可能带用户选的目录。`internal` 错误不带：原始错误只写进客户端日志。

## 自检用的命令

只在 `--smoke` 时有（能力 `smoke`），说明见 `internal/shell/smoke.go`、`smokeflow.go`：`smoke_config`（`stage: 'main' | 'page'`）、`smoke_report {report}`（完整的自检交回后回 `{next: 'edit_server'}`，界面随即去 `/shell/?edit`）、`smoke_shell_view`（客户端自带的页面报告状态）、`smoke_flow_prepare`、`smoke_flow_drop`、`smoke_external_check {url, fileName, size}`。自检里 `open_external`、两个系统对话框不弹出，改走自检准备好的文件和目录；`open_local_dir` 不打开文件管理器。
