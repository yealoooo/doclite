<script setup lang="ts">
// 热文件夹的设置和开关（M1-13，参考 p-upload Mockup 的"正在监听 … → 上传到 …"）。
//
// - 监控目录用系统对话框选（`hotfolder_pick_dir`，壳记住选中的目录，页面不能随便指定本机路径）；
//   目标文件夹用 FolderPicker 在当前项目里选，底部有"新建文件夹"。显示目标文件夹所在的项目、路径和备注（原件位置）。
// - 已经设置过：换目录或换目标立即保存（`hotfolder_set`），正在运行时壳按新配置继续；换目标只影响之后发现的文件。
//   还没设置：两样都选好后，"开始监听"先保存再开始。
// - 开始、暂停：`hotfolder_start`、`hotfolder_pause`（暂停 = 停止监控 + 不再开始热文件夹的项，正在传的传完）。
// - 配置里的服务器和当前服务器不一样（换过服务器）：提示重新选目标文件夹，不能开始。
// - M11-5：显示当前项目的扫描参数建议（项目设置里的；都没设时不显示），只是提示。
import { FolderInputIcon, FolderOpenIcon, FolderSyncIcon, LoaderCircleIcon, PauseIcon, PlayIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { getFolder } from '@/api/folders'
import { HANDLE_ALL, isApiError } from '@/api/http'
import type { FolderItem } from '@/api/types'
import FolderPicker, { type PickedFolder } from '@/components/files/FolderPicker.vue'
import { Button } from '@/components/ui/button'
import {
  hotfolderPause,
  hotfolderPickDir,
  hotfolderSet,
  hotfolderStart,
  type HotFolderStatus,
  type QueueStatus,
} from '@/lib/scan-queue'
import { formatClock } from '@/lib/format'
import { formatScanHint } from '@/lib/project'
import { shellErrorMessage } from '@/lib/shell'
import { useProjectStore } from '@/stores/project'

import NewFolderDialog from './NewFolderDialog.vue'

const props = defineProps<{
  hotfolder: HotFolderStatus | null
  queue: QueueStatus | null
  /** 壳的平台（`windows`、`macos`），说明文字里的路径分隔符按它来。 */
  platform: string
  /**
   * 调热文件夹的命令（useUploadQueue 的 `withHotfolder`）：命令返回的状态由它写回页面；命令执行期间收到过
   * `hotfolder:state` 事件时不用返回值覆盖（事件更新）。
   */
  apply: (run: () => Promise<HotFolderStatus>) => Promise<HotFolderStatus>
}>()

const project = useProjectStore()

const config = computed(() => props.hotfolder?.config ?? null)
const running = computed(() => props.hotfolder?.running ?? false)
const sep = computed(() => (props.platform === 'windows' ? '\\' : '/'))
/** 配置里的目标属于另一个服务器（换过服务器地址）。 */
const otherServer = computed(() => {
  const c = config.value
  const current = props.queue?.server_url
  return !!c && !!current && c.server_url !== current
})

// ---------- 目标文件夹的信息（项目、路径、备注） ----------

interface TargetInfo {
  id: string
  projectId: string
  projectName: string
  /** 从根起的路径（不含根），根是 `/`。 */
  path: string
  remark: string | null
}

/** null：还没加载；'missing'：已删除或进不去。 */
const target = ref<TargetInfo | 'missing' | null>(null)
const targetError = ref('')
let targetSeq = 0

function toTarget(f: FolderItem): TargetInfo {
  const names = [...f.ancestors.slice(1).map((a) => a.folder_name), ...(f.parent_id ? [f.folder_name] : [])]
  return {
    id: f.id,
    projectId: f.project_id,
    projectName: f.ancestors[0]?.folder_name ?? f.folder_name,
    path: `/${names.join('/')}`,
    remark: f.remark,
  }
}

async function loadTarget(id: string | null) {
  const seq = ++targetSeq
  targetError.value = ''
  if (!id || otherServer.value) {
    target.value = null
    return
  }
  try {
    const f = await getFolder(id, HANDLE_ALL)
    if (seq === targetSeq) target.value = toTarget(f)
  } catch (e) {
    if (seq !== targetSeq) return
    target.value = 'missing'
    targetError.value =
      isApiError(e) && e.status === 404
        ? '目标文件夹已被删除，请重新选择'
        : isApiError(e) && e.status === 403
          ? '你进不了目标文件夹所在的项目，请重新选择'
          : `读不到目标文件夹：${shellErrorMessage(e)}`
  }
}

watch(() => [config.value?.target_folder_id ?? null, otherServer.value] as const, ([id]) => void loadTarget(id), {
  immediate: true,
})

// ---------- 还没保存的选择 ----------

const pendingDir = ref<string | null>(null)
/** 选好、还没保存的目标文件夹，连同选的时候所在的项目。 */
const pendingTarget = ref<(PickedFolder & { projectId: string; projectName: string }) | null>(null)

// 在项目切换器里换了项目：还没保存的目标属于原来的项目，清掉（选择框也换成新项目的树）。
watch(
  () => project.current?.id ?? null,
  (id) => {
    if (pendingTarget.value && pendingTarget.value.projectId !== id) pendingTarget.value = null
  },
)

const shownDir = computed(() => pendingDir.value ?? config.value?.watch_dir ?? null)
const shownTarget = computed<{ projectName: string; path: string; remark: string | null } | null>(() => {
  if (pendingTarget.value) {
    return {
      projectName: pendingTarget.value.projectName,
      path: pendingTarget.value.path,
      remark: pendingTarget.value.remark,
    }
  }
  return target.value && target.value !== 'missing' ? target.value : null
})

/** 扫描参数建议的文字（M11-5）：当前项目的；都没设时为空串。 */
const scanHint = computed(() => formatScanHint(project.current?.scan_hint))

const busy = ref<'' | 'dir' | 'target' | 'start' | 'pause'>('')

function fail(what: string, e: unknown) {
  toast.error(`${what}：${shellErrorMessage(e)}`)
}

async function pickDir() {
  if (busy.value) return
  busy.value = 'dir'
  try {
    const dir = await hotfolderPickDir()
    if (!dir) return
    const c = config.value
    if (c && !otherServer.value) {
      await props.apply(() => hotfolderSet(dir, c.target_folder_id))
      pendingDir.value = null
      toast.success(running.value ? '监控目录已更换，正在监听新的目录' : '监控目录已更换')
    } else {
      pendingDir.value = dir
    }
  } catch (e) {
    fail('选择监控目录失败', e)
  } finally {
    busy.value = ''
  }
}

const pickerOpen = ref(false)
const pickerInitial = computed(() => {
  const t = target.value
  return t && t !== 'missing' && t.projectId === project.current?.id ? t.id : null
})

async function onPicked(picked: PickedFolder) {
  const c = config.value
  const dir = pendingDir.value ?? c?.watch_dir ?? null
  if (!c || !dir) {
    const p = project.current
    pendingTarget.value = { ...picked, projectId: p?.id ?? '', projectName: p?.project_name ?? '' }
    return
  }
  busy.value = 'target'
  try {
    await props.apply(() => hotfolderSet(dir, picked.id))
    pendingDir.value = null
    pendingTarget.value = null
    toast.success(`目标文件夹已更换：之后发现的文件传到 ${picked.path}`)
  } catch (e) {
    fail('更换目标文件夹失败', e)
  } finally {
    busy.value = ''
  }
}

// ---------- 新建文件夹（FolderPicker 的 footer-start） ----------

const newFolderOpen = ref(false)
const newFolderParent = ref<{ id: string; path: string } | null>(null)
let pickerApi: { refresh: (id: string) => Promise<void>; select: (id: string) => Promise<void> } | null = null

function askNewFolder(selected: PickedFolder | null, api: NonNullable<typeof pickerApi>) {
  const root = project.current
  if (!root) return
  newFolderParent.value = selected ? { id: selected.id, path: selected.path } : { id: root.root_folder_id, path: '/' }
  pickerApi = api
  newFolderOpen.value = true
}

async function onFolderCreated(folder: FolderItem) {
  toast.success(`已新建文件夹「${folder.folder_name}」`)
  if (pickerApi && folder.parent_id) {
    await pickerApi.refresh(folder.parent_id)
    await pickerApi.select(folder.id)
  }
}

// ---------- 开始、暂停 ----------

const canStart = computed(() => {
  if (running.value || otherServer.value) return false
  const dir = pendingDir.value ?? config.value?.watch_dir
  const targetId = pendingTarget.value?.id ?? config.value?.target_folder_id
  return !!dir && !!targetId && target.value !== 'missing'
})

async function start() {
  if (busy.value || !canStart.value) return
  busy.value = 'start'
  try {
    const dir = pendingDir.value ?? config.value?.watch_dir
    const targetId = pendingTarget.value?.id ?? config.value?.target_folder_id
    if (pendingDir.value || pendingTarget.value) {
      await props.apply(() => hotfolderSet(dir!, targetId!))
      pendingDir.value = null
      pendingTarget.value = null
    }
    await props.apply(hotfolderStart)
    toast.success('热文件夹已开始监听')
  } catch (e) {
    fail('没能开始监听', e)
  } finally {
    busy.value = ''
  }
}

async function pause() {
  if (busy.value) return
  busy.value = 'pause'
  try {
    await props.apply(hotfolderPause)
    toast.success('热文件夹已暂停：正在上传的文件传完后停下')
  } catch (e) {
    fail('暂停失败', e)
  } finally {
    busy.value = ''
  }
}

/** 热文件夹的状态还没读到（页面刚打开）。 */
const loading = computed(() => props.hotfolder === null)

const headline = computed(() => {
  if (loading.value) return '正在读取热文件夹…'
  if (!config.value && !pendingDir.value && !pendingTarget.value) return '设置热文件夹'
  if (running.value) return '正在监听'
  return config.value ? '热文件夹已暂停' : '设置热文件夹'
})
</script>

<template>
  <section
    class="rounded-lg shadow-outline"
    data-testid="hotfolder-card"
    :data-running="running ? 'true' : 'false'"
    :data-configured="config ? 'true' : 'false'"
  >
    <!-- 标题行：状态 + 开始 / 暂停 -->
    <div class="flex items-start gap-3 border-b px-4 py-3.5">
      <span
        class="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg"
        :class="running ? 'bg-ok-soft text-ok' : 'bg-muted text-muted-foreground'"
      >
        <FolderSyncIcon class="size-4.5" :class="running ? 'animate-pulse' : ''" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm font-semibold text-foreground" data-testid="hotfolder-headline">
          <span>{{ headline }}</span>
          <template v-if="running && shownDir && shownTarget">
            <code class="rounded bg-muted px-1.5 py-0.5 font-mono text-xs font-normal">{{ shownDir }}</code>
            <span class="font-normal text-muted-foreground">→ 上传到</span>
            <code class="rounded bg-muted px-1.5 py-0.5 font-mono text-xs font-normal">{{ shownTarget.path }}</code>
          </template>
        </div>
        <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
          本地子文件夹会在服务器上建成同名文件夹，例如
          <code class="font-mono">{{ shownDir ?? (sep === '\\' ? 'D:\\Scan\\Out' : '~/Scan/Out') }}{{ sep }}盒 WS-2025-004{{ sep }}</code>
          对应 <code class="font-mono">{{ (shownTarget?.path ?? '/目标文件夹').replace(/\/$/, '') }}/盒 WS-2025-004/</code>。
          客户端会等文件写完再上传，同一个子文件夹里按文件名顺序上传；失败自动重试，断网或服务器重启后接着传（整个文件重传）。
          上传后本地文件留在原处。
        </p>
        <p v-if="scanHint" class="mt-1.5 text-xs text-muted-foreground" title="项目设置里的建议，只是提示，不限制上传" data-testid="hotfolder-scan-hint">
          扫描参数建议：<span class="font-medium text-foreground">{{ scanHint }}</span>
        </p>
        <p v-if="running" class="mt-1.5 flex items-center gap-1.5 text-xs text-muted-foreground" data-testid="hotfolder-scan">
          <span class="size-1.5 rounded-full bg-ok" />
          <span>{{ props.hotfolder?.watching ? '实时监控目录' : `目录事件不可用，每 ${config?.scan_interval_secs ?? 15} 秒扫描一次` }}</span>
          <template v-if="props.hotfolder?.last_scan_at">
            <span>·</span><span>上次全量扫描 {{ formatClock(props.hotfolder.last_scan_at) }}</span>
          </template>
        </p>
      </div>
      <Button
        v-if="running"
        variant="outline"
        size="sm"
        :disabled="!!busy"
        data-testid="hotfolder-pause"
        @click="pause"
      >
        <LoaderCircleIcon v-if="busy === 'pause'" class="animate-spin" /><PauseIcon v-else />暂停
      </Button>
      <Button v-else size="sm" :disabled="loading || !canStart || !!busy" data-testid="hotfolder-start" @click="start">
        <LoaderCircleIcon v-if="busy === 'start'" class="animate-spin" /><PlayIcon v-else />开始监听
      </Button>
    </div>

    <!-- 监控目录、目标文件夹、备注 -->
    <dl class="grid grid-cols-[88px_1fr_auto] items-center gap-x-3 gap-y-2.5 px-4 py-3.5 text-sm">
      <dt class="text-muted-foreground">监控目录</dt>
      <dd class="min-w-0">
        <code v-if="shownDir" class="font-mono text-compact break-all text-foreground" data-testid="hotfolder-dir">{{ shownDir }}</code>
        <span v-else class="text-muted-foreground">还没选：扫描软件保存文件的文件夹</span>
        <span v-if="pendingDir" class="ml-2 text-xs text-warn">未保存</span>
      </dd>
      <dd class="justify-self-end">
        <Button variant="outline" size="sm" :disabled="loading || !!busy" data-testid="hotfolder-pick-dir" @click="pickDir">
          <LoaderCircleIcon v-if="busy === 'dir'" class="animate-spin" /><FolderOpenIcon v-else />{{ shownDir ? '更换…' : '选择…' }}
        </Button>
      </dd>

      <dt class="text-muted-foreground">目标文件夹</dt>
      <dd class="min-w-0" data-testid="hotfolder-target">
        <template v-if="shownTarget">
          <span v-if="shownTarget.projectName" class="text-muted-foreground">{{ shownTarget.projectName }} · </span>
          <span class="font-medium text-foreground">{{ shownTarget.path }}</span>
          <span v-if="pendingTarget" class="ml-2 text-xs text-warn">未保存</span>
        </template>
        <span v-else-if="otherServer" class="text-warn">属于另一个服务器（{{ config?.server_url }}），请重新选择</span>
        <span v-else-if="target === 'missing'" class="text-destructive">{{ targetError }}</span>
        <span v-else-if="config && target === null" class="text-muted-foreground">加载中…</span>
        <span v-else class="text-muted-foreground">还没选：服务器上的文件夹，本地子文件夹建在它下面</span>
      </dd>
      <dd class="justify-self-end">
        <Button
          variant="outline"
          size="sm"
          :disabled="loading || !!busy || !project.current"
          :title="project.current ? undefined : '先在左上角选择一个项目'"
          data-testid="hotfolder-pick-target"
          @click="pickerOpen = true"
        >
          <LoaderCircleIcon v-if="busy === 'target'" class="animate-spin" /><FolderInputIcon v-else />{{ shownTarget ? '更换…' : '选择…' }}
        </Button>
      </dd>

      <template v-if="shownTarget">
        <dt class="text-muted-foreground">原件位置</dt>
        <dd class="col-span-2 min-w-0" data-testid="hotfolder-remark">
          <span v-if="shownTarget.remark" class="whitespace-pre-line text-foreground">{{ shownTarget.remark }}</span>
          <span v-else class="text-muted-foreground">（目标文件夹没有备注）</span>
          <span class="ml-2 text-xs text-muted-foreground">文件夹备注，重扫时用来找原纸</span>
        </dd>
      </template>
    </dl>

    <p
      v-if="props.hotfolder?.error"
      class="flex items-start gap-2 border-t bg-warn-soft px-4 py-2.5 text-sm text-warn"
      role="alert"
      data-testid="hotfolder-error"
    >
      <TriangleAlertIcon class="mt-0.5 size-4 shrink-0" />{{ props.hotfolder.error }}
    </p>

    <FolderPicker
      v-if="project.current"
      v-model:open="pickerOpen"
      :root-id="project.current.root_folder_id"
      :root-name="project.current.project_name"
      :initial-folder-id="pickerInitial"
      title="选择目标文件夹"
      description="热文件夹根目录里的文件传到这个文件夹，本地子文件夹在它下面建成同名文件夹。"
      confirm-text="传到这里"
      @confirm="onPicked"
    >
      <template #footer-start="{ selected, refresh, select }">
        <Button
          variant="ghost"
          size="sm"
          data-testid="picker-new-folder"
          @click="askNewFolder(selected, { refresh, select })"
        >
          + 新建文件夹
        </Button>
        <!-- 放在插槽里：选择框打开时才挂载，弹出层排在选择框的后面（同样的 z-index 下显示在上面）。 -->
        <NewFolderDialog v-model:open="newFolderOpen" :parent="newFolderParent" @created="onFolderCreated" />
      </template>
    </FolderPicker>
  </section>
</template>
