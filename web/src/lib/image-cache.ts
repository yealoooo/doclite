// 本机图片缓存（M1-11，design §4.5）的管理命令，只在 isShell() 时调用。Wails 壳的实现在 client/internal/imagecache（M6-3）。
//
// - 图片本身不经过这里：页面照常用 `imageSrc()`（壳里是同源的取图地址），壳先查本机缓存，没有再用预签名 URL 下载。
// - 缓存放在本机数据目录的 `image-cache/` 下，按 file_id、snapshot_id 存；默认上限 2 GB，超出后按最近访问淘汰。
// - 命令失败时 reject `{kind, message}`（ShellError，见 lib/shell.ts）。

import { invoke } from './wails'

/** 壳启动以来的计数（`--smoke` 用它判断命中还是下载）。 */
export interface CacheCounters {
  /** 命中缓存的请求数。 */
  hits: number
  /** 没命中（要下载，或等着别的请求下载同一张）的请求数。 */
  misses: number
  /** 实际完成的下载次数（同一张图的并发请求只算一次）。 */
  downloads: number
  download_bytes: number
  /** 不在记录里、没有下载地址的请求（取图地址返回 404）。 */
  rejected: number
  /** 其他失败（存储连不上、sha 不一致、读写文件出错）。 */
  errors: number
  /** 完成的切块次数（M2-3；同一张图的并发请求只算一次）。 */
  tilings: number
  /** 切块失败的次数。 */
  tiling_failures: number
}

export interface CacheProjectUsage {
  project_id: string
  total_bytes: number
  file_count: number
  cover_count: number
  original_count: number
  /** 原图分块（一套算一项，M2-3）。 */
  tiles_count: number
}

export interface CacheStats {
  /** 缓存目录，例如 `C:\Users\…\AppData\Local\Doclite\image-cache`。 */
  dir: string
  /** 缓存是否可用。不可用（索引打不开）时图片照常显示，只是不存到本机，占用都是 0。 */
  available: boolean
  /** 不可用的原因。 */
  error: string | null
  total_bytes: number
  file_count: number
  limit_bytes: number
  /** 占用大的在前。 */
  projects: CacheProjectUsage[]
  counters: CacheCounters
}

export interface CacheClearReport {
  removed_files: number
  removed_bytes: number
  /** 删不掉的文件数（例如被别的程序占着），下次再试。 */
  failed_files: number
}

const GB = 1024 ** 3
/** 上限的范围（和壳一致）：100 MB – 1 TB。 */
export const CACHE_LIMIT_MIN_BYTES = 100 * 1024 ** 2
export const CACHE_LIMIT_MAX_BYTES = 1024 * GB

export function cacheStats() {
  return invoke<CacheStats>('cache_stats')
}

/** 清空某个项目的缓存；不传表示全部。 */
export function cacheClear(projectId?: string | null) {
  return invoke<CacheClearReport>('cache_clear', { projectId: projectId ?? null })
}

/** 修改上限（字节），超出时立即淘汰。返回修改后的统计。 */
export function cacheSetLimit(limitBytes: number) {
  return invoke<CacheStats>('cache_set_limit', { limitBytes })
}

/** `prepareTiles()` 的结果：ready 已经切好；generating 正在切；queued 排上了（只留最新的一张）；
 * failed 这张图切不了（code 如 too_large、unsupported，见 lib/image-url.ts）。 */
export interface PrepareTilesResult {
  status: 'ready' | 'generating' | 'queued' | 'failed'
  code: string | null
  message: string | null
}

/** 提前给一张图切块（M2-3）：页面看当前这张时给下一张切，马上返回。只在壳里调用；info 要来自 batch 或
 * snapshots 接口（不在壳的来源记录里的 reject `invalid_request`）。 */
export function prepareTiles(info: { file_id: string; snapshot_id: string }) {
  return invoke<PrepareTilesResult>('prepare_tiles', { fileId: info.file_id, snapshotId: info.snapshot_id })
}
