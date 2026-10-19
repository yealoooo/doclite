// 取原图交给 OpenSeadragon（M2-4，design §4.4 的"看原图"，用户决定 Q1）：只在客户端里，用 M2-3 的分块（img 协议的
// dzi 描述 + 按块取），界面只取看得到的块；第一次打开要等壳下载原图、切块。浏览器里（只是开发工具）不看原图，查看器
// 只显示封面，提示"请在客户端中使用"（ViewerLoadError `shell_only`）。切不了块（too_large、unsupported 等）时抛
// ViewerLoadError，查看器只显示封面并提示下载原图。`signal` 取消时（换图、关掉）请求一起停掉。
//
// 换文件之后先只显示封面，停留 `settleMs`（约 250 毫秒，由查看器给）再取分块描述；这段时间里又换了就不发请求
// （M2-4 审查第 1 轮）：壳那边的下载原图、切块一旦开始，页面取消请求也会做完（M2-3 的设计），快速翻页时不停下来会越积越多。

import type { SnapshotImageFields } from '@/api/types'

import { SHELL_ONLY, shellImages } from '../client'
import { loadTileSource, TileSourceError } from '../image-url'
import { ViewerLoadError } from './errors'
import { type OpenSeadragon, ShellTileSource, type TileRequestHook } from './osd'

/** 查看器用到的图片信息（batch 的一项或 snapshots 的一个版本）。 */
export type ViewerImageInfo = Pick<
  SnapshotImageFields,
  | 'snapshot_id'
  | 'version_no'
  | 'width_px'
  | 'height_px'
  | 'dpi'
  | 'color_mode'
  | 'file_size'
  | 'mime_type'
  | 'cover_url'
  | 'original_url'
  | 'url_expires_at'
  | 'url_error'
>

/** 加载的步骤：tiles 等壳准备分块；render 取块、画出来。 */
export type LoadStep = 'tiles' | 'render'

export interface LoadHooks {
  onStep?: (step: LoadStep) => void
  /** 每取一块调一次。 */
  onTile?: TileRequestHook
}

export interface LoadOptions {
  /** 先停留这么久（毫秒）再取分块描述（查看器刚换了文件时给，见文件开头）；取消了就不取。 */
  settleMs?: number
}

export interface LoadedOriginal {
  tileSource: OpenSeadragon.TileSource
  /** 原图（转正后）的宽高。 */
  width: number
  height: number
  /** 分块描述的几项。 */
  detail: Record<string, unknown>
}

/** 取原图，返回交给 OpenSeadragon 的图源。失败抛 ViewerLoadError（signal 取消时 code 为 aborted）。 */
export async function loadOriginal(
  fileId: string,
  info: ViewerImageInfo,
  signal: AbortSignal,
  hooks: LoadHooks = {},
  options: LoadOptions = {},
): Promise<LoadedOriginal> {
  if (!shellImages()) throw new ViewerLoadError('shell_only', `${SHELL_ONLY}（浏览器里只显示封面）`)
  if (!info.original_url) {
    throw new ViewerLoadError('no_url', info.url_error?.message ?? '取不到原图地址')
  }
  return loadShellTiles(fileId, info, signal, hooks, options.settleMs ?? 0)
}

function aborted(): ViewerLoadError {
  return new ViewerLoadError('aborted', '已取消')
}

/** 等 ms 毫秒；signal 取消时 reject aborted。 */
function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(aborted())
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      reject(aborted())
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

async function loadShellTiles(fileId: string, info: ViewerImageInfo, signal: AbortSignal, hooks: LoadHooks, settleMs: number) {
  hooks.onStep?.('tiles')
  // 刚换了文件：先停一下（只显示封面），用户接着翻页时这一张就不发请求（见文件开头）。
  if (settleMs > 0) await pause(settleMs, signal)
  let d
  try {
    d = await loadTileSource({ file_id: fileId, snapshot_id: info.snapshot_id, cover_url: info.cover_url, original_url: info.original_url }, signal)
  } catch (e) {
    if (signal.aborted) throw aborted()
    if (e instanceof TileSourceError) {
      throw new ViewerLoadError(e.code, shellMessage(e), e.status)
    }
    throw new ViewerLoadError('internal', e instanceof Error ? e.message : String(e))
  }
  if (signal.aborted) throw aborted()
  hooks.onStep?.('render')
  return {
    tileSource: new ShellTileSource(d, signal, hooks.onTile),
    width: d.width,
    height: d.height,
    detail: {
      levels: d.levels,
      tile_count: d.tile_count,
      format: d.format,
      mode: d.mode,
      streamed: d.streamed,
      elapsed_ms: d.elapsed_ms,
      hit: d.hit,
    },
  }
}

function shellMessage(e: TileSourceError): string {
  switch (e.code) {
    case 'too_large':
      return '原图太大，客户端切不了块'
    case 'timeout':
      return '原图太复杂，客户端切块超时'
    case 'unsupported_format':
    case 'unsupported':
      return '这种格式客户端切不了块'
    case 'orientation':
      return '原图的方向标记客户端处理不了'
    case 'corrupt':
      return '原图已损坏，客户端解不了'
    case 'not_found':
      return '客户端没有这张图的下载记录（图片信息可能过期了），请重试'
    case 'unavailable':
      return '客户端的图片缓存暂时不可用，请稍后重试'
    case 'upstream':
    case 'integrity':
      return '客户端下载原图失败（存储连不上或内容不对），请稍后重试'
    default:
      return e.message || `客户端返回 ${e.status}`
  }
}
