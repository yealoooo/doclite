// 浏览器上传前的整理（M1-12）：哪些文件要传、各传到哪个子文件夹、按什么顺序。规则和壳一致：
// - 只收 jpg、jpeg、tif、tiff、png（不区分大小写），按扩展名挑；以 `.`、`~$` 开头、`.tmp` 结尾、Thumbs.db、
//   desktop.ini、.DS_Store 这类系统文件直接忽略（不进结果列表），其他扩展名的列为"已忽略"；
// - 上传文件夹时按 `webkitRelativePath` 的目录逐层建子文件夹（连同选中的文件夹本身，和壳的上传按钮一样）；
//   以 `.`、`~$` 开头的目录、`$RECYCLE.BIN`、`System Volume Information` 整个跳过；
// - 同一个文件夹里按文件名的自然顺序上传（IMG_2 在 IMG_10 前，和壳的 natural.rs 相同），server 按到达顺序
//   给 sort_no，所以 sort_no 就是文件名顺序。目录名按不区分大小写合并（server 的同级文件夹名不区分大小写）。

/** 上传接受的扩展名（和壳的 `upload::EXTENSIONS` 相同）。 */
export const UPLOAD_EXTENSIONS = ['jpg', 'jpeg', 'tif', 'tiff', 'png'] as const

/** `<input type=file>` 的 accept。 */
export const UPLOAD_ACCEPT = '.jpg,.jpeg,.tif,.tiff,.png,image/jpeg,image/png,image/tiff'

function codePointCompare(a: string, b: string): number {
  const ia = a[Symbol.iterator]()
  const ib = b[Symbol.iterator]()
  for (;;) {
    const x = ia.next()
    const y = ib.next()
    if (x.done || y.done) return x.done === y.done ? 0 : x.done ? -1 : 1
    const d = x.value.codePointAt(0)! - y.value.codePointAt(0)!
    if (d !== 0) return d
  }
}

const isDigit = (c: string) => c >= '0' && c <= '9'

/**
 * 文件名的自然顺序（同壳的 `upload::natural::natural_cmp`）：逐个字符比，两边都是 ASCII 数字时取整段按数值比
 * （数值相同时前导 0 少的在前）；其他字符不区分大小写；一边是另一边的前缀时短的在前；都相同再按原文比。
 */
export function naturalCompare(a: string, b: string): number {
  const ca = [...a]
  const cb = [...b]
  let i = 0
  let j = 0
  for (;;) {
    if (i >= ca.length || j >= cb.length) {
      if (i >= ca.length && j >= cb.length) break
      return i >= ca.length ? -1 : 1
    }
    const x = ca[i]!
    const y = cb[j]!
    if (isDigit(x) && isDigit(y)) {
      let ea = i
      while (ea < ca.length && isDigit(ca[ea]!)) ea++
      let eb = j
      while (eb < cb.length && isDigit(cb[eb]!)) eb++
      const ra = ca.slice(i, ea).join('')
      const rb = cb.slice(j, eb).join('')
      const ta = ra.replace(/^0+/, '')
      const tb = rb.replace(/^0+/, '')
      const d = ta.length - tb.length || (ta < tb ? -1 : ta > tb ? 1 : 0) || ra.length - rb.length
      if (d !== 0) return d
      i = ea
      j = eb
    } else {
      const d = codePointCompare(x.toLowerCase(), y.toLowerCase())
      if (d !== 0) return d
      i++
      j++
    }
  }
  return codePointCompare(a, b)
}

/** 文件扩展名（小写，不带点）。 */
function extensionOf(name: string): string {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

/** 系统文件、临时文件：直接忽略（同壳的 `accept_file_name` 里扩展名之前的部分）。 */
function isJunkFile(name: string): boolean {
  const lower = name.toLowerCase()
  return (
    name.startsWith('.') ||
    name.startsWith('~$') ||
    lower.endsWith('.tmp') ||
    lower === 'thumbs.db' ||
    lower === 'desktop.ini'
  )
}

/** 要跳过的目录（同壳的 `accept_dir_name`）。 */
function isSkippedDir(name: string): boolean {
  const lower = name.toLowerCase()
  return name.startsWith('.') || name.startsWith('~$') || lower === '$recycle.bin' || lower === 'system volume information'
}

export function isSupportedUploadName(name: string): boolean {
  return (UPLOAD_EXTENSIONS as readonly string[]).includes(extensionOf(name))
}

/** 一个要上传的文件。 */
export interface PlannedFile {
  file: File
  name: string
  /** 相对目标文件夹的子目录（逐层的名字）；空数组表示目标文件夹本身。 */
  dir: string[]
}

/** 同一个子文件夹里的文件（按自然顺序）。 */
export interface PlannedGroup {
  /** 子目录（显示用的原文，取第一次出现的写法）。 */
  dir: string[]
  /** 不区分大小写的键（`a/b`）。 */
  key: string
  files: PlannedFile[]
}

export interface PlannedSkip {
  file: File
  name: string
  dir: string[]
  reason: string
}

export interface UploadPlan {
  /** 要建的子文件夹（父在前，按自然顺序）。 */
  dirs: string[][]
  /** 按子文件夹分组，组按路径的自然顺序；组内按文件名的自然顺序。 */
  groups: PlannedGroup[]
  /** 不支持的格式（列进结果）。 */
  skipped: PlannedSkip[]
  /** 直接忽略的系统文件、隐藏文件和目录的个数。 */
  ignored: number
}

export function dirKey(dir: readonly string[]): string {
  return dir.map((d) => d.toLowerCase()).join('/')
}

function comparePaths(a: readonly string[], b: readonly string[]): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    const d = naturalCompare(a[i]!, b[i]!)
    if (d !== 0) return d
  }
  return a.length - b.length
}

/**
 * 整理要上传的文件。`mode` 为 folder 时按每个文件的 `webkitRelativePath`（`选中的文件夹/子文件夹/…/文件名`）
 * 分到子文件夹，否则都传到目标文件夹本身。
 */
export function planUpload(files: readonly File[], mode: 'files' | 'folder'): UploadPlan {
  const groups = new Map<string, PlannedGroup>()
  const dirs = new Map<string, string[]>()
  const skipped: PlannedSkip[] = []
  let ignored = 0

  for (const file of files) {
    const rel = mode === 'folder' ? (file.webkitRelativePath || file.name) : file.name
    const parts = rel.split(/[\\/]/).filter((p) => p !== '')
    const name = parts.pop() ?? file.name
    const dir = mode === 'folder' ? parts : []
    if (dir.some(isSkippedDir) || isJunkFile(name)) {
      ignored++
      continue
    }
    if (!isSupportedUploadName(name)) {
      skipped.push({ file, name, dir, reason: '不支持的格式（只收 jpg、jpeg、png、tif、tiff）' })
      continue
    }
    const key = dirKey(dir)
    let group = groups.get(key)
    if (!group) {
      group = { dir, key, files: [] }
      groups.set(key, group)
    }
    group.files.push({ file, name, dir: group.dir })
    // 每一层都要建（父在前）。
    for (let i = 1; i <= dir.length; i++) {
      const sub = group.dir.slice(0, i)
      const k = dirKey(sub)
      if (!dirs.has(k)) dirs.set(k, sub)
    }
  }

  for (const g of groups.values()) g.files.sort((a, b) => naturalCompare(a.name, b.name))
  return {
    dirs: [...dirs.values()].sort(comparePaths),
    groups: [...groups.values()].sort((a, b) => comparePaths(a.dir, b.dir)),
    skipped,
    ignored,
  }
}
