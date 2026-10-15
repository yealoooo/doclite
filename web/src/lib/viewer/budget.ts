// 浏览器里看原图的内存预算（M2-4 审查第 1 轮）：一个页面里所有查看器共用一份（对比时两侧共用），大小和封面的
// TIFF 解码预算相同（cover-core 的 MAX_TIFF_DECODE_BYTES，1 GiB；这里不引入 cover-core，免得把 UTIF 打进主包）。
//
// - 开始下载之前，按 batch 里的宽高、颜色、大小估一个峰值，占下这么多（acquireBudget）：
//   - 现在就够：马上占下；
//   - 现在不够，但别的查看器解完、回落到常驻的大小以后就够：等着（这时还不下载），它们回落或放掉时再判断；
//   - 别的都回落也不够：拒绝（BudgetError），这一侧只显示封面，提示下载原图或改用客户端。
// - Worker 读了 TIFF 的文件头后，按实际的压缩方式、位深重算峰值（resize）：变小直接改；变大时现在够就占，
//   现在不够但等别的解完就够时，先把自己占的放掉再排队等（不解码），怎么都不够就拒绝。
//   等着的一律不占额度，所以两侧不会互相等死；按先来后到放行。
// - 解完以后回落到常驻的大小（settle：金字塔各级的像素）；换图、关掉、失败时放掉（release）。
// 这里只是记账（估算），不是真的限制分配；Worker 里真的分配不出来时另有 RangeError → too_large。

/** 浏览器里看原图的内存预算（字节）：页面里所有查看器共用。 */
export const VIEWER_BUDGET_BYTES = 1024 * 1024 * 1024

const MB = 1024 * 1024

/** 预算不够（code 为 over_budget，查看器按"超出预算"提示）。 */
export class BudgetError extends Error {
  /** 这一张要占的字节数。 */
  readonly need: number
  /** 别的查看器占着（或解完后仍会占着）的字节数。 */
  readonly others: number

  constructor(need: number, others: number) {
    super(
      others > 0
        ? `同时打开的原图共用浏览器里的内存预算（${VIEWER_BUDGET_BYTES / MB / 1024} GB）：另一张占着约 ${Math.round(others / MB)} MB，这一张约要 ${Math.round(need / MB)} MB，放不下`
        : `这一张解码约要 ${Math.round(need / MB)} MB，超出浏览器里的内存预算（${VIEWER_BUDGET_BYTES / MB / 1024} GB）`,
    )
    this.need = need
    this.others = others
  }
}

interface LeaseState {
  /** 现在占着的字节数（解码中是峰值，settle 之后是常驻的）。 */
  bytes: number
  /** 解完以后常驻的字节数（估算；settle 时换成实际的）。 */
  resident: number
  settled: boolean
  released: boolean
}

interface Waiter {
  peak: number
  resident: number
  /** resize 时排队的：已有的那一项（等着的时候占 0）；acquire 的为 null（放行时新建）。 */
  lease: LeaseState | null
  resolve: (s: LeaseState) => void
  reject: (e: Error) => void
  cleanup: () => void
}

const leases = new Set<LeaseState>()
const waiters: Waiter[] = []

/** 别的查看器现在占着的。 */
function usedNow(except?: LeaseState): number {
  let n = 0
  for (const l of leases) if (l !== except) n += l.bytes
  return n
}

/** 别的查看器都解完、回落以后仍会占着的。 */
function usedEventually(except?: LeaseState): number {
  let n = 0
  for (const l of leases) if (l !== except) n += l.settled ? l.bytes : Math.min(l.bytes, l.resident)
  return n
}

/** 一张原图占着的预算。 */
export class BudgetLease {
  private readonly s: LeaseState

  constructor(s: LeaseState) {
    this.s = s
  }

  /** 现在占着的字节数。 */
  get bytes(): number {
    return this.s.bytes
  }

  /**
   * 按准确的估算改占用（Worker 读了文件头之后）：变小直接改；变大时现在够就占；现在不够但等别的解完就够时，先放掉
   * 自己占的、调 `onWait`，排队等到放行；怎么都不够返回 false（占用不变，调用方按拒绝处理，随后 release）。
   * 等的时候 release 了（换图、关掉）就 reject。
   */
  async resize(peak: number, resident: number, onWait?: () => void): Promise<boolean> {
    const s = this.s
    if (s.released) return false
    if (peak <= s.bytes || usedNow(s) + peak <= VIEWER_BUDGET_BYTES) {
      s.bytes = peak
      s.resident = resident
      wake()
      return true
    }
    if (usedEventually(s) + peak > VIEWER_BUDGET_BYTES) return false
    s.bytes = 0
    s.resident = resident
    onWait?.()
    await new Promise<LeaseState>((resolve, reject) => {
      waiters.push({ peak, resident, lease: s, resolve, reject, cleanup: () => {} })
      wake()
    })
    return true
  }

  /** 解完了：回落到常驻的字节数。 */
  settle(resident: number) {
    const s = this.s
    if (s.released) return
    s.settled = true
    s.resident = resident
    s.bytes = Math.min(s.bytes, resident)
    wake()
  }

  /** 不再用了（换图、关掉、失败）。可以重复调。 */
  release() {
    const s = this.s
    if (s.released) return
    s.released = true
    leases.delete(s)
    // 自己还在 resize 里排队的，一起去掉。
    for (let i = waiters.length - 1; i >= 0; i--) {
      const w = waiters[i]!
      if (w.lease !== s) continue
      waiters.splice(i, 1)
      w.reject(new Error('已取消'))
    }
    wake()
  }

  /** 拒绝时的错误（resize 返回 false 之后用）。 */
  refusal(peak: number): BudgetError {
    return new BudgetError(peak, usedNow(this.s))
  }
}

function grant(peak: number, resident: number): LeaseState {
  const s: LeaseState = { bytes: peak, resident, settled: false, released: false }
  leases.add(s)
  return s
}

/** 有占用变了：按先来后到看等着的能不能占上、是不是已经不可能了。 */
function wake() {
  for (let i = 0; i < waiters.length; ) {
    const w = waiters[i]!
    const except = w.lease ?? undefined
    if (usedNow(except) + w.peak <= VIEWER_BUDGET_BYTES) {
      waiters.splice(i, 1)
      w.cleanup()
      if (w.lease) {
        w.lease.bytes = w.peak
        w.resolve(w.lease)
      } else {
        w.resolve(grant(w.peak, w.resident))
      }
      // 占用变多了，后面的从头再看（usedNow 变了）。
      i = 0
    } else if (usedEventually(except) + w.peak > VIEWER_BUDGET_BYTES) {
      waiters.splice(i, 1)
      w.cleanup()
      w.reject(new BudgetError(w.peak, usedEventually(except)))
    } else {
      i++
    }
  }
}

/**
 * 占下 `peak` 字节（解完以后常驻约 `resident` 字节）。现在不够但等别的解完就够时，先调 `onWait` 再等；
 * 不可能够时 reject BudgetError；`signal` 取消时 reject 它的 reason（等着的那一项一起去掉）。
 */
export function acquireBudget(peak: number, resident: number, signal: AbortSignal, onWait?: () => void): Promise<BudgetLease> {
  if (signal.aborted) return Promise.reject(signal.reason)
  if (usedNow() + peak <= VIEWER_BUDGET_BYTES) return Promise.resolve(new BudgetLease(grant(peak, resident)))
  if (usedEventually() + peak > VIEWER_BUDGET_BYTES) return Promise.reject(new BudgetError(peak, usedEventually()))
  onWait?.()
  return new Promise<BudgetLease>((resolve, reject) => {
    const onAbort = () => {
      const i = waiters.indexOf(w)
      if (i >= 0) waiters.splice(i, 1)
      reject(signal.reason)
    }
    const w: Waiter = {
      peak,
      resident,
      lease: null,
      resolve: (s) => resolve(new BudgetLease(s)),
      reject,
      cleanup: () => signal.removeEventListener('abort', onAbort),
    }
    signal.addEventListener('abort', onAbort, { once: true })
    waiters.push(w)
  })
}

/** 现在的占用（截图脚本、测试看）。 */
export function budgetUsage(): { used: number; leases: number; waiting: number } {
  return { used: usedNow(), leases: leases.size, waiting: waiters.length }
}
