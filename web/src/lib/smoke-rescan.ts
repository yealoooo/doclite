// `--smoke` 自检的重扫一步（M2-7，见 lib/smoke.ts 和 client/internal/shell/smoke.go；Wails 壳在 M6-2 才有）。
//
// 只在设置了 DOCLITE_SMOKE_RESCAN_PATH 时跑：自检里的 `pick_and_rescan` 不弹系统对话框，用那个本机文件。
// 1. 找要重扫的文件：DOCLITE_SMOKE_RESCAN_FILE_ID，或项目（DOCLITE_SMOKE_RESCAN_PROJECT，默认第一个能进的）待重扫列表的
//    第一个；记下它现在的版本号、轮次、最近一次驳回的审核人；
// 2. 经真实的壳命令重扫（lib/rescan.ts 的 rescanInShell，和页面上的"上传重扫件"同一段代码）；
// 3. 核对：新版本号 = 原来 + 1、轮次 + 1、待审核且自动分回驳回它的审核人、带了封面（壳生成的）、上传的文件名；
//    待重扫列表里没有它了；batch 的当前快照是这次的、有封面，封面经 img 协议能显示；快照历史最后一个是 rescan；
//    最新一条文件日志是 rescan（版本号、轮次、审核人）；壳发过 hashing、cover、uploading 三个阶段的进度事件，
//    上传的最后一个事件 sent = size。
// DOCLITE_SMOKE_RESCAN_EXPECT=incomplete 时给的是写了一半的文件：要求壳以"选的文件不完整或还在写入"拒绝（不发请求），
// 文件仍在待重扫列表里、版本不变、没有新的日志。
// 账号要有 scan（或 files）菜单，看日志要 files 菜单（demo_scanner 两个都有）。

import { batchFiles, getFileLogs, getFileSnapshots } from '@/api/files'
import { request } from '@/api/http'
import { listRescans } from '@/api/rescans'
import type { MyProject, RescanItem } from '@/api/types'
import { imageSrc } from '@/lib/image-url'
import { MSG_INCOMPLETE, type RescanBytes, rescanInShell, type RescanPhase } from '@/lib/rescan'
import { isShellError, type SmokeConfig } from '@/lib/shell'

function loadCover(src: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.alt = '重扫后的封面'
    img.style.cssText = 'width:24px;height:34px;object-fit:contain;margin:1px;border:1px solid #eee'
    img.onload = () => resolve(img.naturalWidth)
    img.onerror = () => reject(new Error(`重扫后的封面加载失败：${src}`))
    img.src = src
    document.getElementById('smoke')?.appendChild(img)
  })
}

export async function checkRescan(cfg: SmokeConfig['rescan']) {
  if (!cfg?.enabled) return { skipped: '没有设置 DOCLITE_SMOKE_RESCAN_PATH' }
  const projects = await request<MyProject[]>('/api/projects', { handle: true })
  const project = cfg.project
    ? projects.find((p) => p.project_code === cfg.project)
    : (projects.find((p) => p.status === 'active') ?? projects[0])
  if (!project) throw new Error(cfg.project ? `进不了项目 ${cfg.project}` : '这个账号没有能进的项目')

  const before = await listRescans(project.id, { limit: 2000 }, true)
  const target: RescanItem | undefined = cfg.file_id
    ? before.items.find((i) => i.file_id === cfg.file_id)
    : before.items[0]
  if (!target) {
    throw new Error(
      cfg.file_id
        ? `文件 ${cfg.file_id} 不在 ${project.project_code} 的待重扫列表里（不是已驳回？）`
        : `${project.project_code} 没有待重扫的文件（先驳回一页）`,
    )
  }
  const lastReviewer = target.last_reject

  if (cfg.expect === 'incomplete') {
    const logsBefore = (await getFileLogs(target.file_id, true)).logs.length
    let err: unknown = null
    try {
      const out = await rescanInShell(target.file_id)
      throw new Error(`写了一半的文件应该被拒绝，实际 ${out.kind}${out.kind === 'failed' ? `（${out.error.code}）` : ''}`)
    } catch (e) {
      err = e
    }
    if (!isShellError(err) || err.kind !== 'invalid_request' || err.message !== MSG_INCOMPLETE) {
      throw err instanceof Error ? err : new Error(`拒绝的原因不对：${JSON.stringify(err)}`)
    }
    const after = await listRescans(project.id, { limit: 2000 }, true)
    const still = after.items.find((i) => i.file_id === target.file_id)
    const logsAfter = (await getFileLogs(target.file_id, true)).logs.length
    if (!still || still.version_no !== target.version_no || after.total !== before.total || logsAfter !== logsBefore) {
      throw new Error(`拒绝后文件应该原样留在待重扫列表里：still=${!!still} v${still?.version_no} total ${before.total}→${after.total} 日志 ${logsBefore}→${logsAfter}`)
    }
    return {
      expect: 'incomplete',
      file_id: target.file_id,
      file_name: target.file_name,
      rejected: { kind: err.kind, message: err.message },
      still_rejected: `v${still.version_no} 第 ${still.review_round} 轮`,
      rescans_total: `${before.total} → ${after.total}`,
      logs: `${logsBefore} → ${logsAfter}`,
    }
  }

  const events: { phase: RescanPhase; sent: number | null }[] = []
  const out = await rescanInShell(target.file_id, (phase: RescanPhase, b?: RescanBytes) => events.push({ phase, sent: b?.sent ?? null }))
  if (out.kind === 'cancelled') throw new Error('pick_and_rescan 返回"已取消"（自检里应该用环境变量的文件，不弹对话框）')
  if (out.kind === 'failed') throw new Error(`重扫没有成功：${out.error.status} ${out.error.code} ${out.error.message}`)
  const r = out.result
  const problems: string[] = []
  const expect = (ok: boolean, what: string) => {
    if (!ok) problems.push(what)
  }
  expect(r.file_id === target.file_id, `file_id 变了：${r.file_id}`)
  expect(r.version_no === target.version_no + 1, `版本号应该是 v${target.version_no + 1}，实际 v${r.version_no}`)
  expect(r.review_round === target.review_round + 1, `轮次应该是 ${target.review_round + 1}，实际 ${r.review_round}`)
  expect(r.review_status === 'pending', `状态应该是待审核，实际 ${r.review_status}`)
  if (lastReviewer) expect(r.reviewer_id === lastReviewer.reviewer_id, `应该自动分回 ${lastReviewer.reviewer_name}，实际 ${r.reviewer_id}`)
  expect(r.has_cover && out.cover.status === 'included', `应该带封面（壳生成），实际 ${out.cover.status} ${out.cover.reason ?? ''}`)
  expect(!cfg.file_name || out.localName === cfg.file_name, `上传的文件名应该是 ${cfg.file_name}，实际 ${out.localName}`)
  const phases = [...new Set(events.map((e) => e.phase))]
  expect(['hashing', 'cover', 'uploading'].every((p) => phases.includes(p as RescanPhase)), `进度事件的阶段：${phases.join(',')}`)
  const lastUpload = events.filter((e) => e.phase === 'uploading').pop()
  expect(lastUpload?.sent === r.file_size, `上传的最后一个进度事件应该是传完（${r.file_size}），实际 ${lastUpload?.sent}`)

  const after = await listRescans(project.id, { limit: 2000 }, true)
  expect(!after.items.some((i) => i.file_id === target.file_id), '重扫后它还在待重扫列表里')
  expect(after.total === before.total - 1, `待重扫数应该从 ${before.total} 变成 ${before.total - 1}，实际 ${after.total}`)

  const batch = await batchFiles([target.file_id], true)
  const info = batch.items[0]
  expect(!!info && info.snapshot_id === r.snapshot_id && info.version_no === r.version_no, 'batch 的当前快照不是这次重扫的')
  let coverWidth = 0
  if (info) {
    const src = imageSrc(info, 'cover')
    if (!src) problems.push('batch 里没有封面地址')
    else coverWidth = await loadCover(src)
  }

  const snaps = await getFileSnapshots(target.file_id, true)
  const last = snaps.snapshots[snaps.snapshots.length - 1]
  expect(
    !!last && last.snapshot_type === 'rescan' && last.version_no === r.version_no && last.upload_name === out.localName,
    `快照历史的最后一个应该是 v${r.version_no} rescan（${out.localName}），实际 ${last ? `v${last.version_no} ${last.snapshot_type} ${last.upload_name}` : '无'}`,
  )

  const logs = await getFileLogs(target.file_id, true)
  const log = logs.logs[0]
  const d = (log?.detail ?? {}) as Record<string, unknown>
  expect(log?.action === 'rescan', `最新一条日志应该是 rescan，实际 ${log?.action}`)
  expect(d.version_no === r.version_no && d.review_round === r.review_round, `rescan 日志的版本号、轮次不对：${JSON.stringify(d)}`)
  if (lastReviewer) expect(d.reviewer === lastReviewer.reviewer_name, `rescan 日志的审核人应该是 ${lastReviewer.reviewer_name}：${JSON.stringify(d)}`)

  if (problems.length) throw new Error(problems.join('；'))
  return {
    project_code: project.project_code,
    file_id: r.file_id,
    file_name: target.file_name,
    folder: target.folder_path,
    local_name: out.localName,
    from: `v${target.version_no} 第 ${target.review_round} 轮`,
    to: `v${r.version_no} 第 ${r.review_round} 轮`,
    review_status: r.review_status,
    reviewer: lastReviewer?.reviewer_name ?? null,
    cover: out.cover.status,
    cover_width: coverWidth,
    attempts: out.attempts,
    ms: out.ms,
    progress: { phases, events: events.length, last_sent: lastUpload?.sent ?? null },
    rescans_total: `${before.total} → ${after.total}`,
    log: { action: log?.action, version_no: d.version_no, review_round: d.review_round, reviewer: d.reviewer },
  }
}
