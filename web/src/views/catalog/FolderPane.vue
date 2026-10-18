<script setup lang="ts">
// 归类页的左栏"文件目录"（M9，requirements 5.9"界面 /catalog"，Mockup v3.1 的左栏）：浏览文件目录、选文件夹拖到右边归档。
// 只读：整理（上传、删除、调顺序）在「文件目录」页做。要 files 或 scan 菜单（页面判断，没有时不挂这个组件）。
//
// - 目录树（FolderTree，按需加载）+ 当前文件夹（地址栏 `?folder=`，没有时是根文件夹）。树上修改中的文件夹和含有它的
//   文件夹（已加载的子文件夹里有修改中的）带橙点。
// - 打开的文件夹已归档时顶部横条（SealBanner：紫色已封存、橙色修改中）。
// - 子文件夹一行一个：名称、"n 个文件 / n 个文件夹"、归档状态（未归档 / 🔒 节点名 件号 / ✎ 修改中；盒子显示
//   "已归档 n/m"，要它的子文件夹已经加载过）。单击选中、Ctrl/⌘ 加选、Shift 区间、Esc 取消、双击打开；选中的可以拖到
//   右边（drag.ts），也可以点中间的"归档 →"。只能选文件夹。
// - 文件（当前文件夹直接放的）用 FileGrid 的小格子显示（按顺序、可以双击看原图），不能选去归档、不能拖。
// - 实时：页面把 folders.changed、files.changed 转给 `onFoldersChanged`、`onFilesChanged`。
// 截图脚本用的属性：根 data-testid=folder-pane（data-folder-id、data-state）；子文件夹行 pane-folder（data-folder-id、
// data-selected、data-archive）；文件区 pane-files。
import { ExternalLinkIcon, FolderIcon, LoaderCircleIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { probeFolder } from '@/api/folders'
import { errorMessage, isApiError } from '@/api/http'
import type { FileEntry, FolderArchive, FolderChildren, FolderItem, SubfolderEntry } from '@/api/types'
import FolderArchiveBadge from '@/components/catalog/FolderArchiveBadge.vue'
import SealBanner from '@/components/catalog/SealBanner.vue'
import FolderTree from '@/components/files/FolderTree.vue'
import { type TreeFolder, useFolderTree } from '@/composables/useFolderTree'
import { useSelection } from '@/composables/useSelection'
import { useImageInfoStore } from '@/stores/image-info'
import { type Entry, fileKey, folderKey } from '@/views/files/entries'
import FileGrid from '@/views/files/FileGrid.vue'

import { FOLDERS_MIME, folderDrag, setDragLabel } from './drag'

const props = defineProps<{
  projectId: string
  rootFolderId: string
  projectName: string
  /** 地址栏的 `?folder=`（null 是根文件夹）。 */
  folderId: string | null
  itemNoWidth: number
  /** 有 files 菜单（横条上"去文件目录修改"）。 */
  canFiles: boolean
}>()

const emit = defineEmits<{
  'update:folderId': [id: string | null]
  /** 选中的子文件夹变了。 */
  selection: [folders: SubfolderEntry[]]
  unseal: [archive: FolderArchive]
  reseal: [archive: FolderArchive]
  openFiles: [folderId: string]
  openViewer: [files: FileEntry[], fileId: string]
}>()

const images = useImageInfoStore()
const tree = useFolderTree()

const folder = ref<FolderItem | null>(null)
const children = ref<FolderChildren | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading')
const loadError = ref('')

const currentId = computed(() => props.folderId ?? props.rootFolderId)
const subfolders = computed(() => children.value?.folders ?? [])
const files = computed(() => (children.value?.files ?? []).filter((f) => !images.isMissing(f.file_id)))

const selection = useSelection(() => subfolders.value.map((f) => f.id))
watch(subfolders, () => selection.prune())
const selectedFolders = computed(() => subfolders.value.filter((f) => selection.isSelected(f.id)))
watch(selectedFolders, (list) => emit('selection', list))

const fileEntries = computed<Entry[]>(() => files.value.map((file) => ({ kind: 'file', key: fileKey(file.file_id), file })))
const fileSelection = useSelection(() => fileEntries.value.map((e) => e.key))

const crumbs = computed(() =>
  folder.value ? [...folder.value.ancestors, { id: folder.value.id, folder_name: folder.value.folder_name, path_code: folder.value.path_code }] : [],
)

let openSeq = 0
let listSeq = 0

watch(
  () => props.projectId,
  () => {
    tree.reset({ id: props.rootFolderId, folder_name: props.projectName })
    folder.value = null
    children.value = null
  },
  { immediate: true },
)

async function open(id: string) {
  const my = ++openSeq
  if (folder.value?.id !== id) {
    folder.value = null
    children.value = null
    selection.clear()
    fileSelection.clear()
  }
  state.value = 'loading'
  try {
    const detail = await probeFolder(id, ['not_found', 'forbidden'])
    if (my !== openSeq) return
    if (detail.project_id !== props.projectId) throw Object.assign(new Error('不在这个项目里'), { code: 'not_found' })
    folder.value = detail
    tree.rename(props.rootFolderId, detail.ancestors[0]?.folder_name ?? detail.folder_name)
    void tree.revealPath(detail)
    await reloadList()
  } catch (e) {
    if (my !== openSeq) return
    if ((isApiError(e, 'not_found') || isApiError(e, 'forbidden') || (e as { code?: string }).code === 'not_found') && id !== props.rootFolderId) {
      toast.info('左边的文件夹不存在或已被删除，已回到根文件夹')
      emit('update:folderId', null)
      return
    }
    state.value = 'error'
    loadError.value = errorMessage(e)
  }
}

async function reloadList() {
  const id = folder.value?.id
  if (!id) return
  const my = ++listSeq
  try {
    const res = await tree.load(id)
    if (my !== listSeq || folder.value?.id !== id) return
    if (!res) {
      toast.info('左边的文件夹已被删除，已回到上一级')
      const parent = folder.value?.parent_id
      emit('update:folderId', parent && parent !== props.rootFolderId ? parent : null)
      return
    }
    children.value = res
    state.value = 'ready'
  } catch (e) {
    if (my !== listSeq) return
    if (children.value) state.value = 'ready'
    else {
      state.value = 'error'
      loadError.value = errorMessage(e)
    }
  }
}

async function reloadCrumb() {
  const id = folder.value?.id
  if (!id) return
  try {
    const detail = await probeFolder(id, ['not_found', 'forbidden'])
    if (folder.value?.id === id) folder.value = detail
  } catch {
    // 文件夹没了：reloadList 会处理。
  }
}

watch(currentId, (id) => void open(id), { immediate: true })

function go(id: string | null) {
  emit('update:folderId', id && id !== props.rootFolderId ? id : null)
}

// ---------- 归档状态 ----------

/** 子文件夹（盒子）的子文件夹已经加载过时：已归档几个、修改中几个。 */
function boxSummary(id: string): { done: number; total: number; modifying: number } | null {
  const node = tree.nodes.get(id)
  const kids = node ? tree.childrenOf(node) : undefined
  if (!kids?.length) return null
  const items = kids.filter((k) => k.archive || k.folder_count === 0)
  if (!items.length) return null
  return {
    done: items.filter((k) => !!k.archive).length,
    total: items.length,
    modifying: kids.filter((k) => k.archive?.source_status === 'modifying').length,
  }
}

/** 当前文件夹（盒子）的子文件夹归档了几个（只算能成为一件的：已归档的和没有下级文件夹的）。 */
const currentSummary = computed(() => {
  const list = subfolders.value.filter((f) => f.archive || f.folder_count === 0)
  if (!list.length) return null
  return {
    done: list.filter((f) => !!f.archive).length,
    total: list.length,
    modifying: list.filter((f) => f.archive?.source_status === 'modifying').length,
  }
})

/** 树上带橙点：自己修改中，或者已加载的子文件夹里有修改中的。 */
function treeModifying(f: TreeFolder): boolean {
  if (f.archive?.source_status === 'modifying') return true
  const kids = tree.childrenOf(f)
  return !!kids?.some((k) => k.archive?.source_status === 'modifying')
}

// ---------- 选择和拖动 ----------

function onRowClick(f: SubfolderEntry, ev: MouseEvent) {
  selection.click(f.id, { ctrl: ev.ctrlKey || ev.metaKey, shift: ev.shiftKey })
}

function onKeydown(ev: KeyboardEvent) {
  if (ev.key === 'Escape') selection.clear()
  else if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') {
    ev.preventDefault()
    selection.selectAll()
  }
}

function onDragStart(f: SubfolderEntry, ev: DragEvent) {
  if (!selection.isSelected(f.id)) selection.set([f.id])
  const list = selectedFolders.value.length ? selectedFolders.value : [f]
  folderDrag.value = { ids: list.map((x) => x.id), names: list.map((x) => x.folder_name) }
  const dt = ev.dataTransfer
  if (dt) {
    dt.effectAllowed = 'copy'
    dt.setData(FOLDERS_MIME, list.map((x) => x.id).join(','))
    dt.setData('text/plain', list.map((x) => x.folder_name).join('\n'))
  }
  setDragLabel(ev, list.length === 1 ? `「${list[0]!.folder_name}」` : `${list.length} 个文件夹`)
}

function onDragEnd() {
  folderDrag.value = null
}

function onFileOpen(entry: Entry) {
  if (entry.kind === 'file') emit('openViewer', files.value, entry.file.file_id)
}

// ---------- 给页面用 ----------

/** folders.changed：树上已加载的、当前文件夹的列表、面包屑。`ids` 为 null 时不知道范围，全部刷新。 */
function onFoldersChanged(ids: Set<string> | null) {
  const cur = folder.value
  const listAffected = !!cur && (!ids || ids.has(cur.id) || subfolders.value.some((f) => ids.has(f.id)))
  if (ids) void tree.refresh(ids, listAffected ? cur?.id : null)
  else void tree.refreshAll(cur?.id)
  if (listAffected) void reloadList()
  if (cur && (!ids || crumbs.value.some((c) => ids.has(c.id)))) void reloadCrumb()
}

/** files.changed：当前文件夹里的文件变了就重取列表（页数、文件）。 */
function onFilesChanged(folderIds: Set<string> | null) {
  const cur = folder.value
  if (!cur) return
  if (!folderIds || folderIds.has(cur.id) || subfolders.value.some((f) => folderIds.has(f.id))) void reloadList()
}

function refresh() {
  void tree.refreshAll(folder.value?.id)
  void reloadList()
  void reloadCrumb()
}

defineExpose({
  onFoldersChanged,
  onFilesChanged,
  refresh,
  clearSelection: () => selection.clear(),
  /** 当前文件夹（横条、"去文件目录修改"用）。 */
  current: () => folder.value,
})

const folderKeyOf = folderKey
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="folder-pane" :data-folder-id="folder?.id" :data-state="state">
    <div class="flex h-10 shrink-0 items-center gap-2 border-b bg-muted/30 px-3">
      <span class="shrink-0 text-[13px] font-semibold text-foreground">文件目录</span>
      <nav class="flex min-w-0 items-center gap-0.5 truncate text-xs text-muted-foreground" data-testid="pane-crumb">
        <template v-for="(c, i) in crumbs" :key="c.id">
          <span v-if="i > 0" class="px-0.5">/</span>
          <b v-if="i === crumbs.length - 1" class="truncate font-medium text-foreground">{{ c.folder_name }}</b>
          <button v-else type="button" class="max-w-28 truncate text-info hover:underline" :title="c.folder_name" @click="go(c.id)">{{ c.folder_name }}</button>
        </template>
      </nav>
    </div>
    <div class="flex min-h-0 flex-1">
      <aside class="w-44 shrink-0 overflow-y-auto border-r bg-sidebar/60 px-1.5 py-2 xl:w-52">
        <FolderTree :tree="tree" :selected-id="folder?.id ?? currentId" @select="go">
          <template #suffix="{ folder: f }">
            <span v-if="treeModifying(f)" class="size-1.5 shrink-0 rounded-full bg-orange-500" title="里面有修改中的文件夹（改完要重新归档）" data-testid="tree-modifying-dot" />
          </template>
        </FolderTree>
      </aside>
      <div class="flex min-h-0 min-w-0 flex-1 flex-col">
        <SealBanner
          v-if="folder?.archive"
          :archive="folder.archive"
          where="catalog"
          can-catalog
          :can-files="props.canFiles"
          :item-no-width="props.itemNoWidth"
          @unseal="folder?.archive && emit('unseal', folder.archive)"
          @reseal="folder?.archive && emit('reseal', folder.archive)"
          @open-files="folder && emit('openFiles', folder.id)"
        />
        <div class="flex h-8 shrink-0 items-center gap-2 border-b px-3 text-[11px] text-muted-foreground">
          <template v-if="subfolders.length">
            <span class="min-w-0 truncate">双击打开 · 选中文件夹（Ctrl/⌘、Shift 多选）拖到右边归档</span>
            <span class="flex-1" />
            <span
              v-if="currentSummary"
              class="shrink-0 rounded-full px-1.5 leading-[18px]"
              :class="currentSummary.done === currentSummary.total ? 'bg-violet-100 text-violet-700' : 'bg-muted text-muted-foreground'"
              data-testid="pane-summary"
            >已归档 {{ currentSummary.done }}/{{ currentSummary.total }}</span>
            <span v-if="currentSummary?.modifying" class="shrink-0 rounded-full border border-orange-300 bg-orange-50 px-1.5 leading-4 text-orange-700">修改中 {{ currentSummary.modifying }}</span>
          </template>
          <template v-else-if="folder">
            <span class="min-w-0 truncate">{{ files.length }} 个文件（按顺序）<template v-if="folder.archive?.source_status === 'sealed'"> · 已封存，只能重扫</template></span>
            <span class="flex-1" />
          </template>
          <button
            v-if="props.canFiles && folder"
            type="button"
            class="flex shrink-0 items-center gap-0.5 text-info hover:underline"
            title="在「文件目录」页打开这个文件夹（上传、整理在那里做）"
            data-testid="pane-open-files"
            @click="emit('openFiles', folder.id)"
          ><ExternalLinkIcon class="size-3" />文件目录</button>
        </div>

        <div v-if="state === 'error' && !children" class="flex flex-1 flex-col items-center justify-center gap-2 p-4 text-xs">
          <p class="text-destructive">{{ loadError }}</p>
          <button type="button" class="underline" @click="open(currentId)">重试</button>
        </div>
        <div v-else-if="!children" class="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
          <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载…
        </div>
        <div v-else-if="!subfolders.length && !files.length" class="flex flex-1 items-center justify-center text-xs text-muted-foreground" data-testid="pane-empty">
          文件夹是空的
        </div>
        <template v-else>
          <div
            v-if="subfolders.length"
            class="min-h-0 overflow-y-auto outline-none"
            :class="files.length ? 'max-h-[45%] shrink-0 border-b' : 'flex-1'"
            tabindex="0"
            data-testid="pane-folders"
            @keydown="onKeydown"
            @click.self="selection.clear()"
          >
            <div
              v-for="f in subfolders"
              :key="f.id"
              class="grid cursor-default grid-cols-[18px_minmax(0,1fr)_auto] items-center gap-2 border-b border-border/50 px-3 py-1.5 select-none"
              :class="selection.isSelected(f.id) ? 'bg-info-soft' : 'hover:bg-muted/60'"
              draggable="true"
              data-testid="pane-folder"
              :data-key="folderKeyOf(f.id)"
              :data-folder-id="f.id"
              :data-selected="selection.isSelected(f.id) ? 'true' : undefined"
              :data-archive="f.archive ? f.archive.source_status : f.folder_count ? 'box' : 'none'"
              @click="onRowClick(f, $event)"
              @dblclick="go(f.id)"
              @dragstart="onDragStart(f, $event)"
              @dragend="onDragEnd"
            >
              <FolderIcon class="size-4 fill-amber-300/70 text-amber-600" :stroke-width="1.5" />
              <span class="flex min-w-0 items-baseline gap-1.5">
                <span class="truncate text-[13px] text-foreground" :title="f.remark ? `${f.folder_name}（${f.remark}）` : f.folder_name">{{ f.folder_name }}</span>
                <span class="shrink-0 text-[11px] text-muted-foreground">
                  {{ f.folder_count ? `${f.folder_count} 个文件夹` : `${f.file_count} 个文件` }}
                </span>
              </span>
              <span class="flex min-w-0 items-center justify-end gap-1">
                <template v-if="f.folder_count && !f.archive">
                  <template v-for="s in [boxSummary(f.id)]" :key="s ? 'sum' : 'none'">
                    <span
                      v-if="s"
                      class="shrink-0 rounded-full px-1.5 text-[11px] leading-[18px]"
                      :class="s.done === s.total ? 'bg-violet-100 text-violet-700' : 'bg-muted text-muted-foreground'"
                    >已归档 {{ s.done }}/{{ s.total }}</span>
                    <span v-if="s?.modifying" class="shrink-0 rounded-full border border-orange-300 bg-orange-50 px-1.5 text-[11px] leading-4 text-orange-700">修改中 {{ s.modifying }}</span>
                  </template>
                </template>
                <FolderArchiveBadge v-else :archive="f.archive" :item-no-width="props.itemNoWidth" show-none />
              </span>
            </div>
          </div>
          <div v-if="files.length" class="flex min-h-0 flex-1 flex-col" data-testid="pane-files">
            <FileGrid
              :entries="fileEntries"
              view="icons"
              dense
              :selection="fileSelection"
              visible-owner="catalog-pane"
              @open="onFileOpen"
            />
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
