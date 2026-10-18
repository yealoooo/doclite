// `--smoke` 自检的主流程（M6-4，见 lib/smoke.ts 和 client/internal/shell/smokeflow.go）：用打包的壳把一批新页从上传
// 走到导出下载。只在设置了 DOCLITE_SMOKE_FLOW_SAMPLES 时跑；账号要有扫描、审核分配、审核、归类、导出这几个菜单（admin）。
//   flow_upload     壳里上传：壳把样本复制一份（每个文件写进本次独有的注释，内容不重复），页面经真实的
//                   pick_and_enqueue_upload（自检里用复制好的「壳里上传」文件夹代替系统对话框）加入上传队列，等全部入库；
//                   接口核对文件夹、文件名、封面（壳生成的）。服务器上的位置：<FOLDER>/<自检 时间>/壳里上传/。
//   flow_hotfolder  热文件夹：hotfolder_pick_dir（自检里是准备好的空文件夹）→ hotfolder_set → hotfolder_start，壳再往
//                   监控目录的「盒 01」里一张张放样本（smoke_flow_drop，模拟扫描软件），等全部入库后暂停热文件夹；
//                   接口核对子文件夹按名字建好、文件按名字的自然顺序排、带封面。
//   flow_review     审核看大图：把这两批页分给自己，第一页在真正的查看器（ImageViewer，壳的分块）里看到原图，再逐页带
//                   看到的 snapshot_id 通过。
//   flow_catalog    归类：按档号模板用到的层级找或建一条节点链（全宗「客户端自检」，编码见 LEVEL_CODES），把这两批页
//                   所在的两个文件夹整个归档（M9：每个文件夹一件），补上必填著录项、锁定；要求档号不空。
//   flow_export     导出下载：按条目建导出任务（DOCLITE_SMOKE_FLOW_FORMAT，默认 pdf），等 server 执行完；取下载地址后
//                   走导出页的同一段代码（lib/download.ts 的 openDownload → 壳的 open_external）。自检里壳不打开系统浏览器，
//                   只记下地址；smoke_external_check 用 HTTP 下载这个地址，核对文件名、大小、zip 的目录（目录.xlsx、
//                   清单.csv、每件的 PDF 或原图）。
// 每一步失败时后面各步照跑（依赖前一步结果的会报"前一步没做完"）。数据留在服务器上（每次一个新的「自检 <时间>」文件夹
// 和两件锁定的条目），可以在 /files、/catalog、/export 里看到。

import { createFolder, listChildren } from '@/api/folders'
import { batchFiles } from '@/api/files'
import { request } from '@/api/http'
import {
  archiveFolders,
  createArchiveCategory,
  getArchiveItem,
  getCatalogSettings,
  itemAction,
  listArchiveCategories,
  updateArchiveItem,
} from '@/api/catalog'
import { createExportTask, getExportDownload, getExportTask } from '@/api/export'
import { assignReviews, submitReview } from '@/api/reviews'
import type {
  ArchiveCategory,
  ArchiveItem,
  ExportTask,
  FileEntry,
  FileImageItem,
  FolderItem,
  Me,
  MyProject,
} from '@/api/types'
import { openDownload } from '@/lib/download'
import {
  hotfolderPause,
  hotfolderPickDir,
  hotfolderSet,
  hotfolderStart,
  uploadRecent,
  type QueueItem,
} from '@/lib/scan-queue'
import { pickAndEnqueueUpload } from '@/lib/shell-upload'
import type { SmokeConfig } from '@/lib/shell'
import type { ViewerStatus } from '@/components/viewer/ImageViewer.vue'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
import { invoke } from '@/lib/wails'

type FlowConfig = NonNullable<SmokeConfig['flow']>

/** 档号模板各层级用的节点编码（全宗名称「客户端自检」，其余名称同编码）。 */
const LEVEL_CODES: Record<string, string> = {
  fonds: 'ZJ',
  category: 'WS',
  year: '2026',
  retention: 'Y',
  org: 'ZJ',
  custom: 'ZJ',
}
/** 等上传队列（壳里上传、热文件夹）全部入库的时限。 */
const UPLOAD_WAIT_MS = 90_000
/** 等导出执行完的时限；排队超过 QUEUED_HINT_MS 还没开始，多半是这个 server 没在执行后台任务。 */
const EXPORT_WAIT_MS = 180_000
const QUEUED_HINT_MS = 45_000

interface FlowPrepared {
  run: string
  upload_dir: string
  upload_files: string[]
  hot_dir: string
  hot_files: string[]
}

interface ExternalCheck {
  opened: number
  url_host: string
  url_path: string
  status: number
  content_type: string
  content_disposition: string
  file_name: string
  size: number
  sha256: string
  entries: { name: string; size: number }[]
  entry_count: number
  ms: number
}

/** 主流程各步之间传的东西。 */
export interface FlowState {
  cfg: FlowConfig
  project?: MyProject
  me?: Me
  prepared?: FlowPrepared
  runFolder?: FolderItem
  /** 壳里上传、热文件夹传上去的文件（按位置排好）和它们所在的文件夹。 */
  uploaded: FileEntry[]
  hotUploaded: FileEntry[]
  folderIds: string[]
  /** 审核时看到并通过的快照。 */
  reviewed?: FileImageItem[]
  item?: ArchiveItem
}

export function newFlowState(cfg: FlowConfig): FlowState {
  return { cfg, uploaded: [], hotUploaded: [], folderIds: [] }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function need<T>(v: T | undefined | null, what: string): T {
  if (v === undefined || v === null) throw new Error(`前一步没做完（没有${what}）`)
  return v
}

async function findProject(cfg: FlowConfig): Promise<MyProject> {
  const projects = await request<MyProject[]>('/api/projects', { handle: true })
  const p = cfg.project ? projects.find((x) => x.project_code === cfg.project) : (projects.find((x) => x.status === 'active') ?? projects[0])
  if (!p) throw new Error(cfg.project ? `进不了项目 ${cfg.project}` : '这个账号没有能进的项目')
  return p
}

/** 从项目根起按路径找或建文件夹。 */
async function ensureFolderPath(rootId: string, path: string): Promise<FolderItem> {
  let parent = rootId
  let folder: FolderItem | null = null
  for (const name of path.split('/').filter(Boolean)) {
    folder = await createFolder({ parent_id: parent, folder_name: name, if_exists: 'return' }, true)
    parent = folder.id
  }
  return need(folder, '文件夹路径')
}

async function childFolder(parentId: string, name: string) {
  const ch = await listChildren(parentId, true)
  const f = ch.folders.find((x) => x.folder_name === name)
  if (!f) throw new Error(`服务器上没有「${name}」文件夹`)
  return f
}

/** 等这些队列项都结束（入库、重复、失败）；有不是"已入库"的就报错。match 返回 true 的项才算。 */
async function waitQueue(match: (it: QueueItem) => boolean, count: number, what: string): Promise<QueueItem[]> {
  const started = Date.now()
  let last: QueueItem[] = []
  for (;;) {
    last = (await uploadRecent(500)).filter(match)
    const finished = last.filter((i) => i.status === 'done' || i.status === 'duplicate' || i.status === 'failed')
    if (last.length >= count && finished.length === last.length) break
    if (Date.now() - started > UPLOAD_WAIT_MS) {
      throw new Error(`${what}：${UPLOAD_WAIT_MS / 1000} 秒内没有全部入库（${last.map((i) => `${i.file_name}=${i.status}`).join('、') || '队列里还没有'}）`)
    }
    await sleep(500)
  }
  const bad = last.filter((i) => i.status !== 'done')
  if (bad.length > 0) {
    throw new Error(`${what}：${bad.map((i) => `${i.file_name} ${i.status}（${i.error_code ?? ''} ${i.error_message ?? ''}）`).join('；')}`)
  }
  return last
}

/** 核对服务器上的文件：名字、顺序、都有封面。 */
async function checkServerFiles(folderId: string, names: string[], what: string) {
  const files = (await listChildren(folderId, true)).files
  const got = files.map((f) => f.file_name)
  if (got.join('|') !== names.join('|')) throw new Error(`${what}：服务器上是 ${got.join('、')}，应为 ${names.join('、')}`)
  const batch = await batchFiles(files.map((f) => f.file_id), true)
  const noCover = batch.items.filter((i) => !i.cover_url).map((i) => files.find((f) => f.file_id === i.file_id)?.file_name)
  if (noCover.length > 0) throw new Error(`${what}：${noCover.join('、')} 没有封面（壳应该生成）`)
  return files
}

/** flow_upload：壳里上传（见文件开头）。 */
export async function checkFlowUpload(st: FlowState) {
  const started = performance.now()
  st.project = await findProject(st.cfg)
  st.me = await request<Me>('/api/me', { handle: true })
  const prep = await invoke<FlowPrepared>('smoke_flow_prepare')
  st.prepared = prep
  const parent = await ensureFolderPath(st.project.root_folder_id, st.cfg.folder)
  st.runFolder = await createFolder(
    { parent_id: parent.id, folder_name: prep.run, remark: '客户端自检（--smoke 的主流程：壳里上传、热文件夹、审核、归类、导出）' },
    true,
  )
  const pick = await pickAndEnqueueUpload(st.runFolder.id, 'folder')
  if (pick.cancelled || pick.added.length !== prep.upload_files.length || pick.skipped.length > 0) {
    throw new Error(`pick_and_enqueue_upload 的结果不对：${JSON.stringify(pick)}`)
  }
  const ids = new Set(pick.added)
  const items = await waitQueue((i) => ids.has(i.id), ids.size, '壳里上传')
  const sub = await childFolder(st.runFolder.id, prep.upload_dir)
  st.uploaded = await checkServerFiles(sub.id, prep.upload_files, '壳里上传')
  st.folderIds.push(sub.id)
  const covers = items.map((i) => i.cover_status)
  if (covers.some((c) => c !== 'included')) throw new Error(`封面没有都带上：${covers.join('、')}`)
  return {
    project_code: st.project.project_code,
    folder: `${st.cfg.folder}/${prep.run}/${prep.upload_dir}`,
    files: prep.upload_files,
    queue_ids: pick.added,
    covers: 'included',
    ms: Math.round(performance.now() - started),
  }
}

/** flow_hotfolder：热文件夹（见文件开头）。 */
export async function checkFlowHotfolder(st: FlowState) {
  const started = performance.now()
  const prep = need(st.prepared, '准备好的样本')
  const runFolder = need(st.runFolder, '本次的文件夹')
  const dir = await hotfolderPickDir()
  if (dir !== prep.hot_dir) throw new Error(`hotfolder_pick_dir 返回 ${dir}，应为准备好的 ${prep.hot_dir}`)
  const target = await createFolder({ parent_id: runFolder.id, folder_name: '热文件夹', if_exists: 'return' }, true)
  await hotfolderSet(dir, target.id)
  const running = await hotfolderStart()
  if (!running.running) throw new Error(`热文件夹没有开始：${JSON.stringify(running)}`)
  try {
    const drop = await invoke<{ sub: string; files: string[] }>('smoke_flow_drop')
    const inDir = (i: QueueItem) => i.source === 'hotfolder' && i.local_path.startsWith(dir)
    const items = await waitQueue(inDir, drop.files.length, '热文件夹')
    const sub = await childFolder(target.id, drop.sub)
    // 同一子文件夹按文件名的自然顺序串行上传，服务器上的顺序也是这样（样本名字本身就是这个顺序）。
    st.hotUploaded = await checkServerFiles(sub.id, drop.files, '热文件夹')
    st.folderIds.push(sub.id)
    if (items.some((i) => i.cover_status !== 'included')) throw new Error(`封面没有都带上：${items.map((i) => i.cover_status).join('、')}`)
    return {
      folder: `${st.cfg.folder}/${prep.run}/热文件夹/${drop.sub}`,
      files: drop.files,
      watching: running.watching,
      ms: Math.round(performance.now() - started),
    }
  } finally {
    await hotfolderPause().catch(() => {})
  }
}

/** flow_review：审核看大图（见文件开头）。mountViewer 是 smoke.ts 里挂查看器的那一段（避免互相 import）。 */
export async function checkFlowReview(
  st: FlowState,
  mountViewer: (fileId: string, info: ViewerImageInfo) => Promise<{ status: ViewerStatus; drawn: number; ms: number; unmount: () => void }>,
) {
  const project = need(st.project, '项目')
  const me = need(st.me, '当前用户')
  const files = [...st.uploaded, ...st.hotUploaded]
  if (files.length === 0) throw new Error('前一步没做完（没有传上去的文件）')
  const ids = files.map((f) => f.file_id)
  const assigned = await assignReviews(project.id, { reviewer_id: me.id, file_ids: ids }, true)
  const batch = await batchFiles(ids, true)
  // 第一页在真正的查看器里看原图（壳的分块）。
  const first = need(batch.items[0], '第一页的图片信息')
  const v = await mountViewer(first.file_id, first)
  const viewed = { mode: v.status.mode, tile_requests: v.status.tileRequests, tiles_drawn: v.drawn, ms: v.ms }
  v.unmount()
  if (viewed.mode !== 'tiles' || viewed.tiles_drawn === 0) throw new Error(`查看器没有用分块画出原图：${JSON.stringify(viewed)}`)
  for (const it of batch.items) {
    await submitReview(it.file_id, { result: 'approved', snapshot_id: it.snapshot_id }, true)
  }
  const after = (await Promise.all(st.folderIds.map((id) => listChildren(id, true)))).flatMap((c) => c.files)
  const notApproved = after.filter((f) => ids.includes(f.file_id) && f.review_status !== 'approved')
  if (notApproved.length > 0) throw new Error(`没有都通过：${notApproved.map((f) => `${f.file_name}=${f.review_status}`).join('、')}`)
  st.reviewed = batch.items
  return { assigned: assigned.assigned, approved: batch.items.length, viewer: viewed }
}

/** 按档号模板用到的层级找或建节点链，返回最后一个节点（模板不用节点时是根节点）。 */
async function ensureCategoryChain(projectId: string, levels: string[]): Promise<ArchiveCategory> {
  let list = await listArchiveCategories(projectId, null, true)
  let parent = list.parent
  for (const type of levels) {
    const code = LEVEL_CODES[type] ?? 'ZJ'
    let node = list.items.find((c) => c.category_code === code)
    if (node && node.category_type !== type) throw new Error(`「${parent.category_name}」下已有编码 ${code} 的 ${node.category_type} 节点，和要建的 ${type} 冲突`)
    if (!node) {
      node = await createArchiveCategory(
        projectId,
        { parent_id: parent.id, category_type: type, category_code: code, category_name: type === 'fonds' ? '客户端自检' : code },
        true,
      )
    }
    parent = node
    list = await listArchiveCategories(projectId, node.id, true)
  }
  return parent
}

/** flow_catalog：归类（见文件开头）。 */
export async function checkFlowCatalog(st: FlowState) {
  const project = need(st.project, '项目')
  const prep = need(st.prepared, '准备好的样本')
  const reviewed = need(st.reviewed, '审核通过的页')
  const settings = await getCatalogSettings(project.id, true)
  if (settings.archive_no_template_error) throw new Error(`项目的档号模板不对：${settings.archive_no_template_error}`)
  const leaf = await ensureCategoryChain(project.id, settings.archive_no_levels)
  const extra: Record<string, string> = {}
  for (const f of settings.item_fields) {
    if (f.required) extra[f.key] = f.default ?? (f.type === 'date' ? '2026' : f.type === 'enum' ? (f.options[0] ?? '') : '客户端自检')
  }
  const res = await archiveFolders(
    leaf.id,
    { folder_ids: st.folderIds, titles: Object.fromEntries(st.folderIds.map((id, i) => [id, `客户端自检 ${prep.run}（${i + 1}）`])) },
    true,
  )
  if (res.items.length !== st.folderIds.length) throw new Error(`应归档 ${st.folderIds.length} 件，实际 ${res.items.length} 件：${JSON.stringify(res.skipped)}`)
  const locked: ArchiveItem[] = []
  for (const planned of res.items) {
    let it = await getArchiveItem(need(planned.item_id, '新件的 id'), true)
    if (Object.keys(extra).length) it = await updateArchiveItem(it.id, { row_version: it.row_version, extra_fields: extra }, true)
    it = await itemAction(it.id, 'lock', it.row_version, true)
    if (it.status !== 'locked') throw new Error(`条目没有锁定：${it.status}`)
    if (!it.archive_no) throw new Error(`条目没有档号：${JSON.stringify(it.archive_no_reason)}`)
    locked.push(it)
  }
  const pages = locked.reduce((n, it) => n + it.page_count, 0)
  if (pages !== reviewed.length) throw new Error(`归档了 ${pages} 页，应为 ${reviewed.length} 页`)
  const item = locked[0]!
  st.item = item
  return { category: item.category_path, archive_no: item.archive_no, item_no: item.item_no, items: locked.length, pages, status: item.status }
}

/** zip 里的文件名（服务端 export 的规则：Windows 非法字符换成 _）。只用来核对，结尾的点、保留名这些少见情况不管。 */
function zipName(archiveNo: string) {
  return archiveNo.replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_')
}

/** flow_export：导出下载（见文件开头）。 */
export async function checkFlowExport(st: FlowState) {
  const project = need(st.project, '项目')
  const item = need(st.item, '锁定的条目')
  const started = performance.now()
  let task: ExportTask = await createExportTask(
    project.id,
    { scope_type: 'item', scope_ids: [item.id], export_format: st.cfg.format, only_approved: true },
    true,
  )
  for (;;) {
    if (task.status === 'done') break
    if (task.status === 'failed' || task.status === 'canceled') throw new Error(`导出任务 ${task.status}：${JSON.stringify(task)}`)
    const waited = performance.now() - started
    if (waited > EXPORT_WAIT_MS) throw new Error(`导出任务 ${EXPORT_WAIT_MS / 1000} 秒内没做完（${task.status}，${task.progress_done}/${task.progress_total}）`)
    if (task.status === 'queued' && waited > QUEUED_HINT_MS) {
      throw new Error(`导出任务排队 ${Math.round(waited / 1000)} 秒还没开始：这个 server 多半没在执行后台任务（JOB_CONCURRENCY=0，或同一个库上别的 server 拿着执行器锁）`)
    }
    await sleep(1000)
    task = await getExportTask(task.id, true)
  }
  const exportMs = Math.round(performance.now() - started)
  const dl = await getExportDownload(task.id, true)
  // 和导出页的"下载"同一段代码：壳里交给 open_external（自检里壳只记下地址）。
  const via = await openDownload(dl.url, () => {})
  if (via !== 'shell') throw new Error(`下载应该交给壳（open_external），实际 ${via}`)
  const check = await invoke<ExternalCheck>('smoke_external_check', { url: dl.url, fileName: dl.file_name, size: dl.size })
  const names = check.entries.map((e) => e.name)
  const want = ['目录.xlsx', '清单.csv']
  const base = zipName(need(item.archive_no, '档号'))
  if (st.cfg.format === 'pdf') want.push(`${base}.pdf`)
  else want.push(`${base}/0001.`)
  const missing = want.filter((w) => !names.some((n) => (w.endsWith('.') ? n.startsWith(w) : n === w)))
  if (missing.length > 0) throw new Error(`zip 里没有 ${missing.join('、')}：${names.join('、')}`)
  return {
    task_id: task.id,
    format: st.cfg.format,
    pages: task.page_count,
    export_ms: exportMs,
    file_name: dl.file_name,
    via,
    download: { host: check.url_host, status: check.status, size: check.size, ms: check.ms, content_type: check.content_type },
    entries: names,
  }
}
