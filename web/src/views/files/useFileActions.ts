// 文件目录页的整理操作（M1-12）：新建、改名、改备注、移动、复制、删除、下载原图、上传。
// 对话框的开关状态放在这里，组件在 FilesView 的模板里。操作成功后调 `afterChange(受影响的文件夹)` 立即刷新
// （SSE 的事件随后也会到，去抖后再刷新一次，结果一样）。
//
// - 多选：只能批量移动、删除文件（`POST /api/folder-files:move`、`:delete`，每次最多 500 个，超过分几次）；
//   文件夹一次只操作一个；文件夹和文件混在一起时不提供移动、删除（提示分开选）。
// - 复制：Ctrl/⌘+C 把选中的文件复制到当前文件夹（逐个 `POST /api/folder-files/{id}/copy`，名字自动加 (1)）。
// - 下载原图：浏览器里在新窗口打开 original_url；壳里用系统浏览器打开（`open_external`，只接受 http、https）。
// - 上传：浏览器里交给 stores/uploads.ts；壳里调 `pick_and_enqueue_upload`（系统对话框选文件，交给上传队列）。

import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { batchFiles } from '@/api/files'
import { copyFolderFile, deleteFolderFile, deleteFolderFiles, MAX_BULK_IDS, moveFolderFiles } from '@/api/folder-files'
import { deleteFolder, updateFolder } from '@/api/folders'
import type { FileEntry, FileImageItem, FolderItem, MyProject, SubfolderEntry } from '@/api/types'
import type { PickedFolder } from '@/components/files/FolderPicker.vue'
import type { Selection } from '@/composables/useSelection'
import { isTauri } from '@/lib/client'
import { openExternal, shellErrorMessage } from '@/lib/shell'
import { pickAndEnqueueUpload, type PickMode } from '@/lib/shell-upload'
import { useImageInfoStore } from '@/stores/image-info'
import { useUploadsStore } from '@/stores/uploads'

import { type Entry, fileKey } from './entries'
import type { EditableFolder } from './FolderFormDialog.vue'
import { opError, type OpContext } from './op-errors'

/** 选中的是什么：folders 是只选了几个文件夹（文件夹一次只能操作一个），mixed 是文件夹和文件混选。 */
export type SelectionKind = 'none' | 'file' | 'files' | 'folder' | 'folders' | 'mixed'

interface Options {
  project: () => MyProject | null
  folder: () => FolderItem | null
  selected: () => Entry[]
  selection: Selection
  /** 操作成功后：刷新这些文件夹（列表和树）。 */
  afterChange: (folderIds: string[]) => void
  openSnapshots: (file: FileEntry) => void
  /** 进入一个文件夹。 */
  navigate: (folderId: string) => void
}

/** URL 剩不到这么久就重新取再打开。 */
const URL_MARGIN_MS = 10 * 60 * 1000

function showError(e: unknown, ctx: OpContext) {
  const r = opError(e, ctx)
  toast.error(r.title, { description: r.detail })
}

function chunks<T>(list: T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < list.length; i += size) out.push(list.slice(i, i + size))
  return out
}

export function useFileActions(opts: Options) {
  const router = useRouter()
  const images = useImageInfoStore()
  const uploads = useUploadsStore()

  const files = computed(() =>
    opts.selected().filter((e): e is Extract<Entry, { kind: 'file' }> => e.kind === 'file').map((e) => e.file),
  )
  const folders = computed(() =>
    opts.selected().filter((e): e is Extract<Entry, { kind: 'folder' }> => e.kind === 'folder').map((e) => e.folder),
  )
  const kind = computed<SelectionKind>(() => {
    const f = files.value.length
    const d = folders.value.length
    if (f + d === 0) return 'none'
    if (d === 0) return f === 1 ? 'file' : 'files'
    if (f === 0) return d === 1 ? 'folder' : 'folders'
    return 'mixed'
  })

  // ---------- 对话框状态 ----------

  const folderForm = reactive<{
    open: boolean
    parent: { id: string; folder_name: string } | null
    folder: EditableFolder | null
    focus: 'name' | 'remark'
  }>({ open: false, parent: null, folder: null, focus: 'name' })

  const rename = reactive<{ open: boolean; file: FileEntry | null }>({ open: false, file: null })

  const move = reactive<{
    open: boolean
    /** 移动的是选中的文件，还是一个文件夹。 */
    what: 'files' | 'folder'
    files: FileEntry[]
    folder: SubfolderEntry | null
    title: string
    exclude: { path_code: string }[]
    disabledIds: string[]
  }>({ open: false, what: 'files', files: [], folder: null, title: '', exclude: [], disabledIds: [] })

  const remove = reactive<{
    open: boolean
    loading: boolean
    title: string
    description: string
    files: FileEntry[]
    folder: SubfolderEntry | null
  }>({ open: false, loading: false, title: '', description: '', files: [], folder: null })

  // ---------- 新建、改名、改备注 ----------

  function newFolder() {
    const cur = opts.folder()
    if (!cur) return
    Object.assign(folderForm, { open: true, parent: { id: cur.id, folder_name: cur.folder_name }, folder: null, focus: 'name' })
  }

  function editFolder(f: SubfolderEntry, focus: 'name' | 'remark' = 'name') {
    Object.assign(folderForm, {
      open: true,
      parent: null,
      folder: { id: f.id, folder_name: f.folder_name, remark: f.remark, isRoot: false },
      focus,
    })
  }

  function editCurrentFolder() {
    const cur = opts.folder()
    if (!cur) return
    Object.assign(folderForm, {
      open: true,
      parent: null,
      folder: { id: cur.id, folder_name: cur.folder_name, remark: cur.remark, isRoot: cur.parent_id === null },
      focus: cur.parent_id === null ? 'remark' : 'name',
    })
  }

  function onFolderSaved(saved: FolderItem, created: boolean) {
    const cur = opts.folder()
    if (created) toast.success(`已新建文件夹「${saved.folder_name}」`)
    const ids = [saved.id]
    if (saved.parent_id) ids.push(saved.parent_id)
    if (cur) ids.push(cur.id)
    opts.afterChange(ids)
  }

  function renameSelected() {
    if (kind.value === 'file') {
      rename.file = files.value[0]!
      rename.open = true
    } else if (kind.value === 'folder') {
      editFolder(folders.value[0]!, 'name')
    }
  }

  function onFileRenamed() {
    const cur = opts.folder()
    if (cur) opts.afterChange([cur.id])
  }

  // ---------- 移动 ----------

  function moveSelected() {
    const cur = opts.folder()
    if (!cur) return
    if (kind.value === 'file' || kind.value === 'files') {
      Object.assign(move, {
        open: true,
        what: 'files',
        files: files.value.slice(),
        folder: null,
        title: files.value.length === 1 ? `把「${files.value[0]!.file_name}」移动到…` : `把 ${files.value.length} 个文件移动到…`,
        exclude: [],
        disabledIds: [cur.id],
      })
    } else if (kind.value === 'folder') {
      const f = folders.value[0]!
      Object.assign(move, {
        open: true,
        what: 'folder',
        files: [],
        folder: f,
        title: `把文件夹「${f.folder_name}」移动到…`,
        // 不能移进它自己和它的子文件夹；移到现在的上级等于不动。
        exclude: [{ path_code: f.path_code }],
        disabledIds: [cur.id],
      })
    } else if (kind.value === 'folders') {
      toast.info('文件夹一次只能移动一个，请只选一个文件夹')
    } else if (kind.value === 'mixed') {
      toast.info('文件夹和文件要分开移动：文件可以多选一起移，文件夹一次移一个')
    }
  }

  async function onMoveTarget(target: PickedFolder) {
    const cur = opts.folder()
    if (!cur) return
    if (move.what === 'folder' && move.folder) {
      const f = move.folder
      try {
        await updateFolder(f.id, { parent_id: target.id }, true)
        toast.success(`已把文件夹「${f.folder_name}」移动到 ${target.path}`)
        opts.selection.clear()
        opts.afterChange([cur.id, target.id, f.id])
      } catch (e) {
        showError(e, { kind: 'folder' })
        opts.afterChange([cur.id])
      }
      return
    }
    const list = move.files
    let moved = 0
    let renamed = 0
    try {
      for (const part of chunks(list, MAX_BULK_IDS)) {
        const res = await moveFolderFiles(
          part.map((f) => f.folder_file_id),
          target.id,
          true,
        )
        moved += res.items.filter((i) => i.moved).length
        renamed += res.items.filter((i) => i.renamed).length
      }
      toast.success(`已移动 ${moved} 个文件到 ${target.path}`, {
        description: renamed ? `其中 ${renamed} 个和目标里的文件重名，已自动改名（加 (1)、(2)…）` : undefined,
      })
      opts.selection.clear()
    } catch (e) {
      showError(e, { kind: list.length > 1 ? 'files' : 'file' })
      if (moved) toast.info(`前面已移动 ${moved} 个文件（每批最多 ${MAX_BULK_IDS} 个，出错的那一批没有移动）`)
    } finally {
      opts.afterChange([cur.id, target.id])
    }
  }

  // ---------- 复制 ----------

  /** 正在复制（按住 Ctrl/⌘+C 或连按时不重复复制）。 */
  const copying = ref(false)

  async function copySelected() {
    const cur = opts.folder()
    if (!cur || copying.value) return
    if (kind.value !== 'file' && kind.value !== 'files') {
      if (kind.value !== 'none') toast.info('只能复制文件（复制到当前文件夹，名字自动加 (1)）')
      return
    }
    copying.value = true
    const list = files.value.slice()
    let done = 0
    const names: string[] = []
    try {
      for (const f of list) {
        const res = await copyFolderFile(f.folder_file_id, undefined, true)
        names.push(res.file_name)
        done++
      }
      toast.success(list.length === 1 ? `已复制为「${names[0]}」` : `已复制 ${done} 个文件到当前文件夹`, {
        description: '副本是新的文件（新的 file_id），需要单独审核和归类；不占用额外的存储空间。',
      })
    } catch (e) {
      showError(e, { kind: 'file' })
      if (done) toast.info(`前面已复制 ${done} 个`)
    } finally {
      copying.value = false
      opts.afterChange([cur.id])
    }
  }

  // ---------- 删除 ----------

  function deleteSelected() {
    if (kind.value === 'file' || kind.value === 'files') {
      const list = files.value.slice()
      Object.assign(remove, {
        open: true,
        loading: false,
        files: list,
        folder: null,
        title: list.length === 1 ? `删除「${list[0]!.file_name}」？` : `删除选中的 ${list.length} 个文件？`,
        description:
          '删除后文件从文件目录里消失，进行中的审核分配会取消，操作记进文件日志。已归类的文件不能删除，要先从档案条目里移出。',
      })
    } else if (kind.value === 'folder') {
      const f = folders.value[0]!
      const parts = []
      if (f.folder_count) parts.push(`${f.folder_count} 个子文件夹`)
      if (f.file_count) parts.push(`${f.file_count} 个文件`)
      Object.assign(remove, {
        open: true,
        loading: false,
        files: [],
        folder: f,
        title: `删除文件夹「${f.folder_name}」？`,
        description: `${
          parts.length ? `它里面的 ${parts.join('、')}（以及更深的各层）会一起删除。` : '这是一个空文件夹。'
        }子树里有已归类的文件时不能删除，要先从档案条目里移出。`,
      })
    } else if (kind.value === 'folders') {
      toast.info('文件夹一次只能删除一个，请只选一个文件夹')
    } else if (kind.value === 'mixed') {
      toast.info('文件夹和文件要分开删除：文件可以多选一起删，文件夹一次删一个')
    }
  }

  async function confirmDelete() {
    const cur = opts.folder()
    if (!cur) return
    remove.loading = true
    let deleted = 0
    try {
      if (remove.folder) {
        await deleteFolder(remove.folder.id, true)
        toast.success(`已删除文件夹「${remove.folder.folder_name}」`)
      } else if (remove.files.length === 1) {
        await deleteFolderFile(remove.files[0]!.folder_file_id, true)
        toast.success(`已删除「${remove.files[0]!.file_name}」`)
      } else {
        // 每批最多 500 个，一批是一个事务（有已归类的整批不删）。
        const total = remove.files.length
        for (const part of chunks(remove.files.slice(), MAX_BULK_IDS)) {
          await deleteFolderFiles(
            part.map((f) => f.folder_file_id),
            true,
          )
          deleted += part.length
        }
        toast.success(`已删除 ${total} 个文件`)
      }
      opts.selection.clear()
      remove.open = false
    } catch (e) {
      showError(e, { kind: remove.folder ? 'folder' : remove.files.length > 1 ? 'files' : 'file' })
      if (deleted) {
        // 前面几批已经删掉了：从待删列表里去掉，对话框里只剩没删的（再点"删除"不会因为它们 404）。
        toast.info(`前面已删除 ${deleted} 个文件（每批最多 ${MAX_BULK_IDS} 个，出错的那一批没有删除）`)
        remove.files = remove.files.slice(deleted)
        remove.title = `删除剩下的 ${remove.files.length} 个文件？`
        opts.selection.set(remove.files.map((f) => fileKey(f.file_id)))
      }
    } finally {
      remove.loading = false
      opts.afterChange([cur.id])
    }
  }

  // ---------- 打开、快照、下载原图 ----------

  /** 右键"打开"：文件夹进入；文件选中它（右侧详情面板显示它）。 */
  function openEntry(entry: Entry) {
    if (entry.kind === 'folder') {
      opts.navigate(entry.folder.id)
    } else {
      opts.selection.set([entry.key])
    }
  }

  function snapshotsOfSelected() {
    if (kind.value === 'file') opts.openSnapshots(files.value[0]!)
  }

  /** 取这个文件当前版本的原图地址（缓存里的快过期了就重新取）。 */
  async function originalUrl(fileId: string): Promise<FileImageItem | null> {
    const cached = images.get(fileId)
    const fresh =
      cached?.original_url &&
      cached.url_expires_at &&
      Date.parse(cached.url_expires_at) - Date.now() > URL_MARGIN_MS
    if (fresh) return cached
    const res = await batchFiles([fileId], true)
    return res.items[0] ?? null
  }

  async function downloadOriginal() {
    if (kind.value !== 'file') return
    const f = files.value[0]!
    let info: FileImageItem | null
    try {
      info = await originalUrl(f.file_id)
    } catch (e) {
      showError(e, { kind: 'file' })
      return
    }
    if (!info) {
      toast.error('文件已不存在（可能刚被删除）')
      return
    }
    const url = info.original_url
    if (!url) {
      toast.error('取不到原图地址', { description: info.url_error?.message })
      return
    }
    // 只打开 http、https（预签名 URL）；不让页面借此打开别的协议。
    let parsed: URL
    try {
      parsed = new URL(url)
    } catch {
      toast.error('原图地址格式不对')
      return
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      toast.error('原图地址不是 http 或 https，不能打开')
      return
    }
    if (isTauri()) {
      try {
        await openExternal(parsed.href)
        toast.success('已在系统浏览器里打开原图', { description: '另存时的文件名是 v1.jpg 这样的形式。' })
      } catch (e) {
        toast.error(shellErrorMessage(e))
      }
    } else {
      window.open(parsed.href, '_blank', 'noopener,noreferrer')
    }
  }

  // ---------- 上传 ----------

  /** 浏览器：交给上传 store。壳：用系统对话框选，加入壳的上传队列。 */
  function startUpload(mode: PickMode, pickedFiles?: File[]) {
    const p = opts.project()
    const cur = opts.folder()
    if (!p || !cur) return
    if (isTauri()) {
      void shellUpload(cur, mode)
      return
    }
    if (!pickedFiles?.length) return
    const target = { id: cur.id, name: cur.folder_name, projectId: p.id }
    if (mode === 'folder') uploads.uploadFolder(target, pickedFiles)
    else uploads.uploadFiles(target, pickedFiles)
  }

  async function shellUpload(cur: FolderItem, mode: PickMode) {
    try {
      const res = await pickAndEnqueueUpload(cur.id, mode)
      if (res.cancelled) return
      const skipped = res.skipped.length ? `；跳过 ${res.skipped.length} 个（${res.skipped[0]!.reason}${res.skipped.length > 1 ? ' 等' : ''}）` : ''
      if (res.added.length) {
        toast.success(`已加入上传队列：${res.added.length} 个文件，传到「${cur.folder_name}」`, {
          description: `上传进度在「扫描上传」页查看${skipped}`,
          action: { label: '查看', onClick: () => void router.push('/scan') },
        })
      } else {
        toast.info(`没有加入上传队列的文件${skipped}`)
      }
    } catch (e) {
      toast.error(`加入上传队列失败：${shellErrorMessage(e)}`)
    }
  }

  return {
    kind,
    files,
    folders,
    folderForm,
    rename,
    move,
    remove,
    newFolder,
    editFolder,
    editCurrentFolder,
    onFolderSaved,
    renameSelected,
    onFileRenamed,
    moveSelected,
    onMoveTarget,
    copySelected,
    copying,
    deleteSelected,
    confirmDelete,
    openEntry,
    snapshotsOfSelected,
    downloadOriginal,
    startUpload,
  }
}
