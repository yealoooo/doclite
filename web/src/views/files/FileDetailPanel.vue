<script setup lang="ts">
// 文件目录右侧的详情面板（M1-10）：
// - 选中一个文件：封面、file_id、位置、路径编码、当前快照（版本号、类型）、存储（名称、key）、
//   规格（宽高、DPI、颜色模式、大小）、审核状态、文件日志；"查看快照历史"打开对话框。
//   快照和日志在选中后去抖 150 ms 再取（Shift 区间选择、方向键移动时不连发）。
// - 选中多个：数量和汇总（大小、低 DPI、无封面），图片信息经 image-info store 合批获取。
// - 什么都没选：当前文件夹的信息（位置、路径编码、原件位置、子项数）。
// - 当前文件夹可以编辑名称和备注（M1-12）。
// - 单个文件的归类信息（M3-7，只读，用户决定 Q10）：CatalogPlacementInfo 自己取 files/{fid}/catalog，只给档号
//   （为空时"第 n 件"）、题名、第几页，没挂接时"未归类"；`refreshToken`、`catalogRefreshToken` 变了重新取。
import { FolderIcon, HistoryIcon, LoaderCircleIcon, PencilIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { getFileLogs, getFileSnapshots } from '@/api/files'
import { errorMessage } from '@/api/http'
import type { FileEntry, FileLog, FolderItem, SnapshotItem } from '@/api/types'
import CatalogPlacementInfo from '@/components/files/CatalogPlacementInfo.vue'
import FileCover from '@/components/files/FileCover.vue'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import {
  colorModeLabel,
  formatBytes,
  isLowDpi,
  logActionLabel,
  logSummary,
  reviewStatusOf,
  snapshotTypeLabel,
} from '@/lib/files'
import { useImageInfoStore } from '@/stores/image-info'

import type { Entry } from './entries'

const props = defineProps<{
  /** 最后点的条目（多选时也是它）。 */
  focus: Entry | null
  /** 全部选中的条目。 */
  selected: Entry[]
  /** 当前文件夹。 */
  folder: FolderItem | null
  /** 当前文件夹的位置（不含根），例如 `/2025 文书档案/盒 001`。 */
  location: string
  /** 当前文件夹的子文件夹数、文件数。 */
  counts: { folders: number; files: number }
  /** 变了就重新取快照和日志（SSE 说这个文件变了）。 */
  refreshToken: number
  /** M3-7：变了就重新取归类信息（收到 `catalog.changed`）。 */
  catalogRefreshToken?: number
}>()

const emit = defineEmits<{
  snapshots: [file: FileEntry]
  /** 编辑当前文件夹的名称和备注（M1-12；根文件夹只能改备注）。 */
  editFolder: []
}>()

const images = useImageInfoStore()

const single = computed(() => (props.selected.length === 1 ? props.focus : null))
const file = computed(() => (single.value?.kind === 'file' ? single.value.file : null))
const folderEntry = computed(() => (single.value?.kind === 'folder' ? single.value.folder : null))
const info = computed(() => (file.value ? images.get(file.value.file_id) : undefined))

// ---------- 单个文件：快照和日志 ----------

const snapshots = ref<SnapshotItem[] | null>(null)
const logs = ref<FileLog[] | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0
/** 快照和日志属于哪个文件（防止上一个文件的请求晚到、写进这个文件的面板）。 */
const loadedFor = ref<string | null>(null)

const current = computed(() =>
  loadedFor.value !== null && loadedFor.value === file.value?.file_id
    ? (snapshots.value?.[snapshots.value.length - 1] ?? null)
    : null,
)
/** 规格：快照取到之前先用 batch 的。 */
const spec = computed(() => current.value ?? info.value ?? null)

/** 清空快照和日志，并让在途的请求作废。 */
function reset() {
  seq++
  snapshots.value = null
  logs.value = null
  loadedFor.value = null
  loading.value = false
  loadError.value = null
}

async function load(fileId: string) {
  const my = ++seq
  loading.value = true
  loadError.value = null
  try {
    const [s, l] = await Promise.all([getFileSnapshots(fileId, true), getFileLogs(fileId, true)])
    if (my !== seq || file.value?.file_id !== fileId) return
    snapshots.value = s.snapshots
    logs.value = l.logs
    loadedFor.value = fileId
  } catch (e) {
    if (my !== seq || file.value?.file_id !== fileId) return
    // 失败时不留旧内容（哪怕是同一个文件刷新失败），只显示错误。
    snapshots.value = null
    logs.value = null
    loadedFor.value = null
    loadError.value = errorMessage(e)
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(
  () => [file.value?.file_id ?? null, file.value?.version_no, props.refreshToken] as const,
  ([fileId], old) => {
    if (timer) clearTimeout(timer)
    // 换了文件（或不再是单个文件）：马上清空，在途的请求作废；同一个文件刷新时先保留旧的内容。
    if (!fileId || old?.[0] !== fileId) reset()
    if (!fileId) return
    loading.value = true
    timer = setTimeout(() => void load(fileId), 150)
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  seq++
})

// ---------- 多选汇总 ----------

const selectedFiles = computed(() =>
  props.selected.filter((e): e is Extract<Entry, { kind: 'file' }> => e.kind === 'file').map((e) => e.file),
)
watch(
  () => (props.selected.length > 1 ? selectedFiles.value.map((f) => f.file_id) : []),
  (ids) => {
    if (ids.length) images.request(ids)
  },
)
const summary = computed(() => {
  let bytes = 0
  let known = 0
  let lowDpi = 0
  let noCover = 0
  for (const f of selectedFiles.value) {
    const i = images.get(f.file_id)
    if (!i) continue
    known++
    bytes += i.file_size
    if (isLowDpi(i.dpi)) lowDpi++
    if (!i.cover_url) noCover++
  }
  return { bytes, known, lowDpi, noCover, folders: props.selected.length - selectedFiles.value.length }
})
</script>

<template>
  <div class="flex flex-col gap-4 p-4 text-[13px]" data-testid="detail-panel">
    <!-- ===== 一个文件 ===== -->
    <template v-if="file">
      <FileCover
        :file-id="file.file_id"
        :info="info"
        class="mx-auto w-[124px]"
        @broken="images.reportBroken(file.file_id)"
      />
      <div class="min-w-0">
        <div class="text-sm font-semibold [overflow-wrap:anywhere] text-foreground" data-testid="detail-file-name">
          {{ file.file_name }}
        </div>
        <div class="mt-0.5 text-xs text-muted-foreground">第 {{ file.sort_no }} 页</div>
      </div>

      <dl class="kv">
        <dt>file_id</dt>
        <dd class="font-mono text-[11.5px] break-all" data-testid="detail-file-id">{{ file.file_id }}</dd>
        <dt>位置</dt>
        <dd class="[overflow-wrap:anywhere]">{{ props.location }}</dd>
        <dt>路径编码</dt>
        <dd class="font-mono text-[11.5px] break-all">{{ props.folder?.path_code }}</dd>
        <dt>当前快照</dt>
        <dd data-testid="detail-snapshot">
          <template v-if="current">
            v{{ current.version_no }} · {{ snapshotTypeLabel(current.snapshot_type) }}
            <span class="text-muted-foreground">（共 {{ snapshots!.length }} 个）</span>
          </template>
          <template v-else>v{{ file.version_no }}</template>
        </dd>
        <dt>存储</dt>
        <dd>
          <template v-if="current">
            {{ current.storage_profile_name }}
            <div class="mt-0.5 font-mono text-[11px] break-all text-muted-foreground" data-testid="detail-storage-key">
              {{ current.storage_key }}
            </div>
          </template>
          <span v-else class="text-muted-foreground">—</span>
        </dd>
        <dt>规格</dt>
        <dd>
          <template v-if="spec">
            <div>{{ formatBytes(spec.file_size) }} · {{ spec.mime_type }}</div>
            <div>
              <template v-if="spec.width_px">
                {{ spec.width_px }}×{{ spec.height_px }} ·
              </template>
              <span :class="isLowDpi(spec.dpi) ? 'font-medium text-warn' : ''">
                {{ spec.dpi != null ? `${spec.dpi} dpi` : 'DPI 未知' }}
              </span>
              · {{ colorModeLabel(spec.color_mode) }}
            </div>
            <div v-if="isLowDpi(spec.dpi)" class="text-xs text-warn">DPI 低于 200</div>
          </template>
          <span v-else class="text-muted-foreground">—</span>
        </dd>
        <dt>审核</dt>
        <dd>
          <span
            class="inline-flex items-center gap-1 rounded-full px-1.5 text-xs leading-5"
            :class="reviewStatusOf(file.review_status).badge"
          >
            {{ reviewStatusOf(file.review_status).label }}
          </span>
          <span class="ml-1.5 text-xs text-muted-foreground">第 {{ file.review_round }} 轮</span>
        </dd>
        <!-- 封面正常时不占一行；没有封面、取不到地址时写明原因。 -->
        <template v-if="info && (info.url_error || !info.cover_url)">
          <dt>封面</dt>
          <dd>
            <template v-if="info.url_error">
              <span class="text-destructive">取不到图片地址</span>
              <div class="text-xs text-muted-foreground">{{ info.url_error.message }}</div>
            </template>
            <template v-else>无封面（上传时没有带封面）</template>
          </dd>
        </template>
      </dl>

      <CatalogPlacementInfo
        :file-id="file.file_id"
        :refresh-token="props.refreshToken + (props.catalogRefreshToken ?? 0)"
      />
      <p class="-mt-2 text-xs leading-relaxed text-muted-foreground">
        移动、改名都不会影响归类和审核，它们只认 file_id。
      </p>

      <Button variant="outline" size="sm" data-testid="open-snapshots" @click="emit('snapshots', file)">
        <HistoryIcon />查看快照历史
      </Button>

      <section>
        <h3 class="mb-2 flex items-center gap-2 text-xs font-medium text-muted-foreground">
          文件日志
          <LoaderCircleIcon v-if="loading" class="size-3 animate-spin" />
        </h3>
        <p v-if="loadError" class="text-xs text-destructive">{{ loadError }}</p>
        <ol v-else-if="logs" class="flex flex-col gap-2.5" data-testid="file-logs">
          <li v-for="log in logs" :key="log.id" class="border-l-2 border-border pl-2.5">
            <div class="flex items-baseline gap-1.5">
              <span class="font-medium text-foreground">{{ logActionLabel(log.action) }}</span>
              <span class="truncate text-xs text-muted-foreground">{{ log.operator_name }}</span>
              <span class="ml-auto shrink-0 text-[11px] text-muted-foreground">{{ formatDateTime(log.created_at) }}</span>
            </div>
            <div v-if="logSummary(log)" class="mt-0.5 text-xs [overflow-wrap:anywhere] text-muted-foreground">{{ logSummary(log) }}</div>
          </li>
          <li v-if="logs.length === 0" class="text-xs text-muted-foreground">没有日志</li>
        </ol>
      </section>
    </template>

    <!-- ===== 一个文件夹 ===== -->
    <template v-else-if="folderEntry">
      <div class="flex flex-col items-center gap-2 pt-2">
        <FolderIcon class="size-16 fill-amber-300/70 text-amber-600" :stroke-width="1.25" />
        <div class="text-sm font-semibold [overflow-wrap:anywhere] text-foreground">{{ folderEntry.folder_name }}</div>
      </div>
      <dl class="kv">
        <dt>位置</dt>
        <dd class="[overflow-wrap:anywhere]">{{ props.location === '/' ? '' : props.location }}/{{ folderEntry.folder_name }}</dd>
        <dt>路径编码</dt>
        <dd class="font-mono text-[11.5px] break-all">{{ folderEntry.path_code }}</dd>
        <dt>原件</dt>
        <dd>{{ folderEntry.remark || '—' }}</dd>
        <dt>内容</dt>
        <dd>{{ folderEntry.folder_count }} 个文件夹 · {{ folderEntry.file_count }} 个文件</dd>
      </dl>
      <p class="text-xs text-muted-foreground">双击或按 Enter 打开文件夹</p>
    </template>

    <!-- ===== 多选 ===== -->
    <template v-else-if="props.selected.length > 1">
      <div class="pt-2" data-testid="selection-summary">
        <div class="text-base font-semibold text-foreground">已选 {{ props.selected.length }} 项</div>
        <div class="mt-1 text-xs text-muted-foreground">
          <template v-if="summary.folders">{{ summary.folders }} 个文件夹、</template>{{ selectedFiles.length }} 个文件
        </div>
      </div>
      <dl v-if="selectedFiles.length" class="kv">
        <dt>合计大小</dt>
        <dd>
          {{ formatBytes(summary.bytes) }}
          <span v-if="summary.known < selectedFiles.length" class="text-xs text-muted-foreground">
            （已统计 {{ summary.known }}/{{ selectedFiles.length }}）
          </span>
        </dd>
        <dt>低 DPI</dt>
        <dd :class="summary.lowDpi ? 'text-warn' : ''">{{ summary.lowDpi }} 个</dd>
        <dt>无封面</dt>
        <dd>{{ summary.noCover }} 个</dd>
      </dl>
      <p class="text-xs leading-relaxed text-muted-foreground">
        单击选一个查看详情；Shift 选区间，Ctrl/⌘ 加选，Ctrl/⌘+A 全选，Esc 取消。
      </p>
    </template>

    <!-- ===== 没选：当前文件夹 ===== -->
    <template v-else-if="props.folder">
      <div class="flex flex-col items-center gap-2 pt-2">
        <FolderIcon class="size-16 fill-amber-300/70 text-amber-600" :stroke-width="1.25" />
        <div class="text-sm font-semibold [overflow-wrap:anywhere] text-foreground">{{ props.folder.folder_name }}</div>
        <div class="text-xs text-muted-foreground">当前文件夹</div>
      </div>
      <dl class="kv" data-testid="folder-info">
        <dt>位置</dt>
        <dd class="[overflow-wrap:anywhere]">{{ props.location }}</dd>
        <dt>路径编码</dt>
        <dd class="font-mono text-[11.5px] break-all">{{ props.folder.path_code }}</dd>
        <dt>原件</dt>
        <dd>{{ props.folder.remark || '—' }}</dd>
        <dt>内容</dt>
        <dd>{{ props.counts.folders }} 个文件夹 · {{ props.counts.files }} 个文件</dd>
        <dt>创建</dt>
        <dd>{{ formatDateTime(props.folder.created_at) }}</dd>
      </dl>
      <Button variant="outline" size="sm" data-testid="edit-current-folder" @click="emit('editFolder')">
        <PencilIcon />{{ props.folder.parent_id ? '编辑名称和备注' : '修改备注' }}
      </Button>
      <p class="text-xs leading-relaxed text-muted-foreground">选中一个文件查看详情和日志；右键可以整理文件和文件夹。</p>
    </template>
  </div>
</template>

<style scoped>
.kv {
  display: grid;
  grid-template-columns: 4.5em minmax(0, 1fr);
  gap: 6px 10px;
}
.kv dt {
  color: var(--muted-foreground);
  font-size: 12px;
  line-height: 20px;
}
.kv dd {
  color: var(--foreground);
  line-height: 20px;
}
</style>
