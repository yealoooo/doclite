// 挂接工作台的页序操作（M3-6）：每个操作都写成"在一份有序的 file_id 列表上做什么"，提交时算出整份新列表，
// 经 `PUT /api/archive-items/{id}/files {row_version, file_ids}` 一次提交（M3-3）。
//
// 位置按"挨着哪个文件"记（不只记下标）：409 `conflict`（别人刚改过这个条目）之后，在最新的页序上重做同一个操作，
// 别人加了、移出了页，位置也尽量对（挨着的那个文件不在了才按下标）。重做的结果先给用户看（未保存），确认后再提交。
//
// - move：把这些页（按它们现在的先后）挪到目标位置；
// - insert：把这些文件（新挂上的）插到目标位置，已经在这个条目里的跳过；
// - remove：移出这些页。

/**
 * 目标位置。`index` 是"去掉要挪的页之后"的列表里的插入下标，挨着的文件不在了才用它：`before` 是那个文件原来的
 * 下标（放在它原来的位置上），`after` 也是那个文件原来的下标（它不在了，就放在它原来的位置，不是它后面一位）。
 */
export type PageTarget =
  | { at: 'start' | 'end' }
  | { before: string; index: number }
  | { after: string; index: number }
  /** 移到第 n 页（0 起）："移到第 n 页"用，按位置不按挨着的文件。 */
  | { position: number }

/** 新挂上的文件的信息（重做结果的预览、"已在别的条目里"的提示要用文件名）。 */
export interface OpFile {
  file_id: string
  file_name: string
  folder_id?: string
  folder_path?: string
  review_status: string
  review_round: number
  version_no: number
}

export type PageOp =
  | { kind: 'move'; fileIds: string[]; target: PageTarget; label: string }
  | { kind: 'insert'; files: OpFile[]; target: PageTarget; label: string }
  | { kind: 'remove'; fileIds: string[]; label: string }

export interface OpResult {
  /** 新的整份列表。 */
  list: string[]
  /** 和原来的列表不一样。 */
  changed: boolean
  /** 这次真正挪动、挂上、移出的 file_id（选中、高亮用）。 */
  affected: string[]
  /** 不适用的：insert 里已经在这个条目里的，move、remove 里已经不在这个条目里的。 */
  skipped: string[]
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, n))
}

/** 目标位置在 `rest`（已经去掉要挪的页）里的下标。 */
export function resolveTarget(target: PageTarget, rest: readonly string[]): number {
  if ('at' in target) return target.at === 'start' ? 0 : rest.length
  if ('position' in target) return clamp(target.position, 0, rest.length)
  if ('before' in target) {
    const i = rest.indexOf(target.before)
    return i >= 0 ? i : clamp(target.index, 0, rest.length)
  }
  const i = rest.indexOf(target.after)
  return i >= 0 ? i + 1 : clamp(target.index, 0, rest.length)
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

/** 在 `list` 上做 `op`，返回新列表（不改 `list`）。 */
export function applyOp(op: PageOp, list: readonly string[]): OpResult {
  if (op.kind === 'remove') {
    const drop = new Set(op.fileIds)
    const next = list.filter((id) => !drop.has(id))
    const present = new Set(list)
    return {
      list: next,
      changed: next.length !== list.length,
      affected: op.fileIds.filter((id) => present.has(id)),
      skipped: op.fileIds.filter((id) => !present.has(id)),
    }
  }
  if (op.kind === 'move') {
    const moving = new Set(op.fileIds)
    const moved = list.filter((id) => moving.has(id))
    const rest = list.filter((id) => !moving.has(id))
    const at = resolveTarget(op.target, rest)
    const next = [...rest.slice(0, at), ...moved, ...rest.slice(at)]
    const present = new Set(list)
    return {
      list: next,
      changed: !sameList(next, list),
      affected: moved,
      skipped: op.fileIds.filter((id) => !present.has(id)),
    }
  }
  const present = new Set(list)
  const seen = new Set<string>()
  const adding: string[] = []
  const skipped: string[] = []
  for (const f of op.files) {
    if (present.has(f.file_id) || seen.has(f.file_id)) skipped.push(f.file_id)
    else {
      seen.add(f.file_id)
      adding.push(f.file_id)
    }
  }
  const at = resolveTarget(op.target, list)
  const next = [...list.slice(0, at), ...adding, ...list.slice(at)]
  return { list: next, changed: adding.length > 0, affected: adding, skipped }
}

/**
 * 在显示的列表 `list` 的第 `gap` 个空隙（0 = 第 1 页前面，list.length = 最后）放下 `moving` 这些页（拖放、键盘"放下"）：
 * 挨着的是空隙后面第一个不在 `moving` 里的页，下标按去掉 `moving` 之后算。
 */
export function targetAtGap(list: readonly string[], gap: number, moving: ReadonlySet<string> = new Set()): PageTarget {
  let index = 0
  for (let i = 0; i < Math.min(gap, list.length); i++) if (!moving.has(list[i]!)) index++
  for (let j = gap; j < list.length; j++) {
    if (!moving.has(list[j]!)) return { before: list[j]!, index }
  }
  return { at: 'end' }
}

/** 一组文件的说明：`IMG_0001.png` / `IMG_0001.png 等 30 个文件`。 */
export function filesText(names: string[]): string {
  if (!names.length) return '0 个文件'
  return names.length === 1 ? names[0]! : `${names[0]} 等 ${names.length} 个文件`
}
