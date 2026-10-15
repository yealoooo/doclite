// 图片信息（M1-10，design §4.4）：按 file_id 缓存 `POST /api/files/batch` 的结果（当前版本的规格和预签名 URL）。
//
// 列表接口不返回图片地址，所有列表都按 file_id 从这里取：
// - 列表把"可见的 file_id"告诉 store（`setVisible`，每次可见范围变化都调）；不在可见范围里也要用的
//   （例如多选汇总、详情面板）用 `request(ids)`；
// - 去抖 100 ms 后（期间又有变化就重新计时），把那时仍然可见的、以及 request 过的 file_id 里还没缓存或快过期的，
//   合并成每批最多 500 个，调 batch（快速滚过的不取）；
// - 结果按 file_id 缓存；URL 剩不到 10 分钟时算过期，下一次可见或每分钟的检查时重新获取；
//   带 url_error 的项 5 分钟后重取（存储可能已恢复）；
// - batch 的 `missing`（不存在、无权访问、已删除）记进 `missing`，列表据此把它们移除；
// - `files.changed` 事件里的 file_id 用 `invalidate()` 标成过期（版本可能变了），旧图先留着，取到新的再换。
//   请求还在路上的 id 被标过期时记下来，结果写进缓存后马上再标过期、再取一次（路上那份可能是旧版本）。
// - 缓存最多 5000 项，超出时按最近使用淘汰（可见的、正在取的不淘汰）；`missing` 最多 5000 个，先进先出。
// 图片地址一律通过 `imageSrc()`（lib/image-url.ts）使用：浏览器里是预签名 URL，壳里走 img 协议（M1-11 缓存）。
//
// 用法：
//   const images = useImageInfoStore()
//   images.setVisible('files-grid', visibleFileIds)   // 列表滚动、尺寸变化时
//   images.get(fileId)                                // FileImageItem | undefined（响应式）
//   images.isMissing(fileId)
//   images.clearVisible('files-grid')                 // 组件卸载时

import { defineStore } from 'pinia'
import { shallowReactive, watch } from 'vue'
import { toast } from 'vue-sonner'

import { batchFiles, MAX_BATCH_IDS } from '@/api/files'
import { errorMessage } from '@/api/http'
import type { FileImageItem } from '@/api/types'
import { useSessionStore } from '@/stores/session'

/** 可见范围变化后等多久再发请求。 */
export const IMAGE_INFO_DEBOUNCE_MS = 100
/** URL 剩不到这么久就重新获取。 */
const REFRESH_MARGIN_MS = 10 * 60 * 1000
/** 带 url_error 的项多久后重取。 */
const ERROR_RETRY_MS = 5 * 60 * 1000
/** 刚取到的项至少这么久内不重取（防止客户端时钟比 server 快很多时反复请求）。 */
const MIN_FRESH_MS = 60 * 1000
/** 多久检查一次可见项是否快过期。 */
const EXPIRY_CHECK_MS = 60 * 1000
/** 图片加载失败时，取到不到这么久的不重取（避免死循环）。 */
const BROKEN_RETRY_MS = 30 * 1000
/** 缓存的上限（项）；超出时淘汰到 90%。 */
export const IMAGE_INFO_CACHE_LIMIT = 5000
/** `missing` 的上限。 */
const MISSING_LIMIT = 5000

interface Cached {
  item: FileImageItem
  fetchedAt: number
  /** 过了这个时间就要重取。 */
  staleAt: number
  /** 最近一次可见或被 request 的时间（淘汰用）。 */
  usedAt: number
}

function staleTime(item: FileImageItem, now: number): number {
  if (item.url_error || !item.url_expires_at) return now + ERROR_RETRY_MS
  const expires = Date.parse(item.url_expires_at)
  if (Number.isNaN(expires)) return now + ERROR_RETRY_MS
  return Math.max(expires - REFRESH_MARGIN_MS, now + MIN_FRESH_MS)
}

export const useImageInfoStore = defineStore('imageInfo', () => {
  const cache = shallowReactive(new Map<string, Cached>())
  const missing = shallowReactive(new Set<string>())

  /** 各个列表当前可见的 file_id（键是列表的名字）。 */
  const visible = new Map<string, string[]>()
  /** 等着下一次合批的 file_id。 */
  const pending = new Set<string>()
  /** 正在请求的 file_id。 */
  const inflight = new Set<string>()
  /** 请求还在路上时被 invalidate 的 id：结果写进缓存后要再标过期。 */
  const dirtyWhileInflight = new Set<string>()
  let timer: ReturnType<typeof setTimeout> | null = null
  let ticker: ReturnType<typeof setInterval> | null = null
  /** 清空缓存时加一，丢弃之前发出的请求的结果。 */
  let generation = 0

  function needsFetch(id: string, now = Date.now()): boolean {
    if (missing.has(id) || inflight.has(id)) return false
    const c = cache.get(id)
    return !c || c.staleAt <= now
  }

  function schedule() {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => void flush(), IMAGE_INFO_DEBOUNCE_MS)
  }

  function touch(ids: Iterable<string>, now: number) {
    for (const id of ids) {
      const c = cache.get(id)
      if (c) c.usedAt = now
    }
  }

  /**
   * 要用这些文件的图片信息（不管是否可见，例如详情面板、多选汇总）：没缓存或快过期的，去抖后合批获取。
   */
  function request(ids: Iterable<string>) {
    const now = Date.now()
    let added = false
    for (const id of ids) {
      const c = cache.get(id)
      if (c) c.usedAt = now
      if (needsFetch(id, now)) {
        pending.add(id)
        added = true
      }
    }
    if (added) schedule()
  }

  function allVisible(): string[] {
    const out: string[] = []
    for (const ids of visible.values()) out.push(...ids)
    return out
  }

  /** 可见的里有要取的：去抖后取（发请求时只取那时仍然可见的，快速滚过的不取）。 */
  function scheduleVisible() {
    const now = Date.now()
    if (allVisible().some((id) => needsFetch(id, now))) schedule()
  }

  /** 某个列表当前可见（含预渲染）的 file_id。可见范围每次变化都调一次。 */
  function setVisible(owner: string, ids: string[]) {
    visible.set(owner, ids)
    touch(ids, Date.now())
    scheduleVisible()
    ticker ??= setInterval(scheduleVisible, EXPIRY_CHECK_MS)
  }

  /** 列表卸载时调。没有任何可见的列表时，停掉过期检查。 */
  function clearVisible(owner: string) {
    visible.delete(owner)
    if (visible.size === 0 && ticker) {
      clearInterval(ticker)
      ticker = null
    }
  }

  async function flush() {
    timer = null
    const now = Date.now()
    const want = new Set([...pending, ...allVisible()])
    pending.clear()
    const ids = [...want].filter((id) => needsFetch(id, now))
    const chunks: string[][] = []
    for (let i = 0; i < ids.length; i += MAX_BATCH_IDS) chunks.push(ids.slice(i, i + MAX_BATCH_IDS))
    await Promise.all(chunks.map(fetchChunk))
  }

  async function fetchChunk(ids: string[]) {
    const gen = generation
    for (const id of ids) inflight.add(id)
    try {
      const res = await batchFiles(ids, true)
      if (gen !== generation) return
      const now = Date.now()
      for (const item of res.items) {
        cache.set(item.file_id, { item, fetchedAt: now, staleAt: staleTime(item, now), usedAt: now })
      }
      for (const id of res.missing) {
        addMissing(id)
        cache.delete(id)
      }
    } catch (e) {
      // 401、403、426 的全局处理照常；其余只提示一次（同一个 id 的 toast 会合并），下次可见时再试。
      if (gen === generation) toast.error(`图片信息加载失败：${errorMessage(e)}`, { id: 'image-info-failed' })
    } finally {
      if (gen === generation) {
        let redo = false
        for (const id of ids) {
          inflight.delete(id)
          // 在路上时被标过期：刚写进缓存的可能是旧版本，再标过期、再取一次。
          if (dirtyWhileInflight.delete(id)) {
            const c = cache.get(id)
            if (c) c.staleAt = 0
            missing.delete(id)
            pending.add(id)
            redo = true
          }
        }
        if (redo) schedule()
        prune()
      }
    }
  }

  function addMissing(id: string) {
    missing.add(id)
    if (missing.size > MISSING_LIMIT) {
      // Set 按加入顺序遍历：先进先出。
      const drop = missing.size - MISSING_LIMIT
      let n = 0
      for (const old of missing) {
        if (n++ >= drop) break
        missing.delete(old)
      }
    }
  }

  /** 缓存超过上限时，按最近使用淘汰到 90%；可见的、等着取的、正在取的不淘汰。 */
  function prune() {
    if (cache.size <= IMAGE_INFO_CACHE_LIMIT) return
    const keep = new Set([...allVisible(), ...pending, ...inflight])
    const candidates = [...cache.entries()].filter(([id]) => !keep.has(id)).sort((a, b) => a[1].usedAt - b[1].usedAt)
    let excess = cache.size - Math.floor(IMAGE_INFO_CACHE_LIMIT * 0.9)
    for (const [id] of candidates) {
      if (excess-- <= 0) break
      cache.delete(id)
    }
  }

  function get(id: string): FileImageItem | undefined {
    return cache.get(id)?.item
  }

  function isMissing(id: string): boolean {
    return missing.has(id)
  }

  /**
   * 这些文件可能变了（`files.changed`）：标成过期，可见的马上重取（旧的信息先留着显示）。
   * 不传 ids 表示全部（整体刷新时）。请求还在路上的，等结果回来后再取一次。
   */
  function invalidate(ids?: Iterable<string>) {
    const targets = ids ? new Set(ids) : null
    for (const [id, c] of cache) {
      if (!targets || targets.has(id)) c.staleAt = 0
    }
    for (const id of inflight) {
      if (!targets || targets.has(id)) dirtyWhileInflight.add(id)
    }
    if (!targets) missing.clear()
    else for (const id of targets) missing.delete(id)
    scheduleVisible()
  }

  /** `<img>` 加载失败（例如 URL 已经过期）：取到有一会儿了就重取一次（可见与否都取）。 */
  function reportBroken(id: string) {
    const c = cache.get(id)
    if (!c || Date.now() - c.fetchedAt < BROKEN_RETRY_MS) return
    c.staleAt = 0
    request([id])
  }

  function clear() {
    generation++
    cache.clear()
    missing.clear()
    pending.clear()
    inflight.clear()
    dirtyWhileInflight.clear()
    visible.clear()
    if (timer) clearTimeout(timer)
    timer = null
    if (ticker) clearInterval(ticker)
    ticker = null
  }

  // 换了登录用户：缓存作废（能进的项目不同）。
  const session = useSessionStore()
  watch(
    () => session.me?.id ?? null,
    () => clear(),
  )

  return { get, isMissing, request, setVisible, clearVisible, invalidate, reportBroken, clear }
})
