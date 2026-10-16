<script setup lang="ts">
// 扫描上传页 /scan（M1-13，参考 plan.html 的 p-upload Mockup）。
//
// 在壳里：
// - 头部：本机图片缓存的摘要（ImageCachePanel 的 compact 模式："本机图片缓存 1.3 GB / 2 GB · 清空"）。
// - 队列因 401 暂停：提示重新登录（登录成功后壳自动继续，M1-9）；因 426 暂停：显示升级提示。
// - 热文件夹：监控目录（系统对话框）、目标文件夹（FolderPicker，可以新建文件夹）、备注、开始和暂停（HotFolderCard）。
// - 各项计数：等待写入完成、排队、上传中、已入库、内容重复已跳过、失败；点一格只看这种状态的文件，再点一次取消。
// - 最近文件（QueueList）：大小、进度、状态、低 DPI 提示、无封面标记，失败项可以重试。
// 数据来自壳的上传队列命令和事件（useUploadQueue）。不做：扫描参数建议、同时运行多个热文件夹。
//
// 在浏览器里：热文件夹只能在桌面客户端里用（要读本机的文件夹），引导到文件目录页上传。
//
// 右侧是待重扫列表（M2-7，RescanList；壳和浏览器里都显示）：驳回的页、原件位置、驳回原因，"上传重扫件"单独上传
// （壳里走 `pick_and_rescan`，不进上传队列；浏览器里生成封面后 upload()）。
import { DownloadIcon, FolderSyncIcon, LoaderCircleIcon, LogInIcon, MonitorIcon, RotateCcwIcon, TriangleAlertIcon, UploadIcon } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { request } from '@/api/http'
import ImageCachePanel from '@/components/app/ImageCachePanel.vue'
import { Button } from '@/components/ui/button'
import { useUploadQueue } from '@/composables/useUploadQueue'
import { type HotFolderStatus, type ItemStatus, type QueueItem, uploadResume, uploadRetryFailed } from '@/lib/scan-queue'
import { openExternal, shellErrorMessage } from '@/lib/shell'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'

import HotFolderCard from './HotFolderCard.vue'
import QueueList from './QueueList.vue'
import RescanList from './RescanList.vue'
import { STATUS_TILES } from './status'

const shell = useShellStore()
const session = useSessionStore()
const project = useProjectStore()
const router = useRouter()
const inShell = shell.inShell
/** 客户端有上传队列和热文件夹（客户端比网页旧、没有这项能力时提示升级客户端）。 */
const queueAvailable = inShell && shell.has('upload')

// ---------- 壳里 ----------

const q = queueAvailable
  ? useUploadQueue({
      onAuthRequired: () => toast.warning('登录已失效，上传已暂停。重新登录后自动继续。', { id: 'upload-auth' }),
    })
  : null

const paused = computed(() => q?.status.value?.paused ?? null)
const stats = computed(() => q?.status.value?.stats ?? null)

/** 热文件夹卡片调命令时用（命令期间收到过事件就不用返回值覆盖，见 useUploadQueue）。 */
function applyHotfolder(run: () => Promise<HotFolderStatus>) {
  return q ? q.withHotfolder(run) : run()
}

/** 会话失效：清掉页面的登录状态，去登录页；登录成功后回到这里，壳自动让队列继续。 */
async function relogin() {
  session.clear()
  await router.replace({ name: 'login', query: { redirect: '/scan' } })
}

const upgrade = computed(() => {
  const d = paused.value?.reason === 'upgrade_required' ? (paused.value.details ?? {}) : null
  if (!d) return null
  const s = (v: unknown) => (typeof v === 'string' && v ? v : null)
  return {
    client: s(d.client_version) ?? shell.clientVersion ?? '—',
    min: s(d.min_version),
    latest: s(d.latest_version),
    url: s(d.download_url),
  }
})

async function download() {
  if (!upgrade.value?.url) return
  try {
    await openExternal(upgrade.value.url)
  } catch (e) {
    toast.error(`打开下载地址失败：${shellErrorMessage(e)}`)
  }
}

const resuming = ref(false)
async function resume() {
  if (!q || resuming.value) return
  resuming.value = true
  try {
    await q.withStatus(uploadResume)
  } catch (e) {
    toast.error(`继续失败：${shellErrorMessage(e)}`)
  } finally {
    resuming.value = false
  }
}

function toggleFilter(s: ItemStatus) {
  if (!q) return
  q.filter.value = q.filter.value === s ? null : s
}

const retrying = ref(new Set<number>())
async function retryOne(item: QueueItem) {
  if (retrying.value.has(item.id)) return
  retrying.value = new Set([...retrying.value, item.id])
  try {
    const n = await uploadRetryFailed([item.id])
    if (n === 0) toast.info('这个文件已经不是失败状态了')
  } catch (e) {
    toast.error(`重试失败：${shellErrorMessage(e)}`)
  } finally {
    const next = new Set(retrying.value)
    next.delete(item.id)
    retrying.value = next
  }
}

const retryingAll = ref(false)
async function retryAll() {
  if (retryingAll.value) return
  retryingAll.value = true
  try {
    const n = await uploadRetryFailed()
    toast.success(n > 0 ? `已重新排队 ${n} 个失败的文件` : '没有失败的文件')
  } catch (e) {
    toast.error(`重试失败：${shellErrorMessage(e)}`)
  } finally {
    retryingAll.value = false
  }
}

const filterLabel = computed(() => STATUS_TILES.find((t) => t.status === q?.filter.value)?.label ?? null)

// ---------- 浏览器里 ----------

const canFiles = computed(() => session.canAccess('/files'))
const downloadUrl = ref<string | null>(null)

onMounted(() => {
  if (inShell) {
    void project.ensureLoaded()
    return
  }
  // 桌面客户端的下载地址（server 配置了才有）；取不到就不显示。
  request<{ download_url: string | null }>('/api/shell/latest', { handle: true })
    .then((r) => (downloadUrl.value = r.download_url || null))
    .catch(() => {})
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="scan-page"
    :data-mode="inShell ? 'shell' : 'browser'"
    :data-state="q?.state.value ?? 'browser'"
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="text-base font-semibold tracking-tight text-foreground">扫描上传</h1>
      <span v-if="inShell" class="rounded-full bg-info-soft px-2 py-0.5 text-xs text-info">桌面客户端</span>
      <div class="flex-1" />
      <ImageCachePanel v-if="shell.has('images')" compact />
    </header>

    <div class="flex min-h-0 flex-1">
      <!-- ===== 浏览器：热文件夹只能在客户端里用 ===== -->
      <section v-if="!inShell" class="flex min-w-0 flex-1 items-start justify-center overflow-y-auto p-6">
        <div class="mt-10 w-full max-w-xl rounded-lg p-6 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]" data-testid="scan-browser-notice">
          <div class="flex items-start gap-3">
            <span class="flex size-10 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
              <MonitorIcon class="size-5" />
            </span>
            <div class="min-w-0">
              <h2 class="text-base font-semibold text-foreground">热文件夹只能在桌面客户端里使用</h2>
              <p class="mt-1.5 text-sm leading-relaxed text-muted-foreground">
                热文件夹会盯着扫描软件的输出目录：扫描仪每写完一页就自动上传，本地子文件夹在服务器上建成同名文件夹，断网后自动续传。浏览器读不了这台电脑上的文件夹，所以要在
                Doclite 桌面客户端里使用。
              </p>
            </div>
          </div>
          <div class="mt-5 rounded-md bg-muted px-4 py-3 text-sm">
            <div class="font-medium text-foreground">在浏览器里上传扫描件</div>
            <p class="mt-1 text-muted-foreground">
              到「文件目录」打开要传到的文件夹，用"上传文件"或"上传文件夹"选择扫描好的图片（JPEG、PNG、TIFF）。
            </p>
          </div>
          <div class="mt-5 flex flex-wrap items-center gap-2">
            <Button v-if="canFiles" data-testid="scan-goto-files" @click="router.push('/files')">
              <UploadIcon />前往文件目录上传
            </Button>
            <span v-else class="text-sm text-muted-foreground">你的账号没有「文件目录」菜单，请联系管理员开通，或使用桌面客户端。</span>
            <Button v-if="downloadUrl" variant="outline" as="a" :href="downloadUrl" target="_blank" rel="noopener">
              <DownloadIcon />下载桌面客户端
            </Button>
          </div>
        </div>
      </section>

      <!-- ===== 壳里，但客户端没有上传队列（比网页旧） ===== -->
      <section v-else-if="!queueAvailable" class="flex min-w-0 flex-1 items-start justify-center p-6">
        <div class="mt-10 w-full max-w-xl rounded-lg bg-warn-soft px-4 py-3 text-sm" role="alert" data-testid="scan-client-old">
          <div class="font-medium text-warn">这个版本的客户端没有上传队列和热文件夹</div>
          <div class="mt-1 text-foreground/80">请升级桌面客户端（当前版本 <span class="font-mono">{{ shell.clientVersion ?? '—' }}</span>）。</div>
        </div>
      </section>

      <!-- ===== 壳里 ===== -->
      <div v-else-if="q" class="min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div class="mx-auto flex max-w-6xl flex-col gap-4 p-5">
          <!-- 401：重新登录后继续 -->
          <div
            v-if="paused?.reason === 'auth_required'"
            class="flex items-center gap-3 rounded-lg bg-warn-soft px-4 py-3 text-sm"
            role="alert"
            data-testid="paused-auth"
          >
            <TriangleAlertIcon class="size-4 shrink-0 text-warn" />
            <div class="min-w-0 flex-1">
              <div class="font-medium text-warn">登录已失效，上传已暂停</div>
              <div class="mt-0.5 text-foreground/80">重新登录后自动继续上传，没传完的文件不会丢，也不会重复。</div>
            </div>
            <Button size="sm" data-testid="paused-relogin" @click="relogin"><LogInIcon />重新登录</Button>
          </div>

          <!-- 426：客户端要升级 -->
          <div
            v-else-if="upgrade"
            class="flex items-start gap-3 rounded-lg bg-destructive-soft px-4 py-3 text-sm"
            role="alert"
            data-testid="paused-upgrade"
          >
            <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-destructive" />
            <div class="min-w-0 flex-1">
              <div class="font-medium text-destructive">客户端版本过低，上传已暂停</div>
              <div class="mt-0.5 text-foreground/80">
                当前版本 <span class="font-mono">{{ upgrade.client }}</span>，服务器要求至少
                <span class="font-mono">{{ upgrade.min ?? '—' }}</span>
                <template v-if="upgrade.latest">（最新 <span class="font-mono">{{ upgrade.latest }}</span>）</template>。
                请安装新版本后重新打开客户端；没传完的文件会接着传。
              </div>
            </div>
            <div class="flex shrink-0 gap-2">
              <Button v-if="upgrade.url" size="sm" data-testid="paused-download" @click="download"><DownloadIcon />下载新版本</Button>
              <Button size="sm" variant="outline" :disabled="resuming" title="服务器已调低最低版本时用" @click="resume">再试一次</Button>
            </div>
          </div>

          <div v-if="q.state.value === 'error'" class="rounded-lg bg-destructive-soft px-4 py-3 text-sm text-destructive" role="alert">
            读不到上传队列：{{ q.loadError.value }}
            <Button size="xs" variant="outline" class="ml-2" @click="q.load()">重试</Button>
          </div>

          <HotFolderCard
            :hotfolder="q.hotfolder.value"
            :queue="q.status.value"
            :platform="shell.info?.platform ?? ''"
            :apply="applyHotfolder"
          />

          <!-- 各项计数（点一格只看这种状态） -->
          <div class="grid grid-cols-6 gap-2" data-testid="queue-stats">
            <button
              v-for="t in STATUS_TILES"
              :key="t.status"
              type="button"
              class="rounded-lg px-3 py-2.5 text-left transition-colors shadow-[0_0_0_1px_rgba(0,0,0,0.08)] hover:bg-muted/60"
              :class="q.filter.value === t.status ? 'bg-muted shadow-[0_0_0_1.5px_var(--color-foreground)]' : ''"
              :aria-pressed="q.filter.value === t.status"
              :data-testid="`tile-${t.status}`"
              :title="q.filter.value === t.status ? '再点一次显示全部' : `只看「${t.label}」的文件`"
              @click="toggleFilter(t.status)"
            >
              <div
                class="text-xl font-semibold tabular-nums"
                :class="
                  t.status === 'failed' && (stats?.failed ?? 0) > 0
                    ? 'text-destructive'
                    : t.status === 'done'
                      ? 'text-ok'
                      : 'text-foreground'
                "
                data-testid="tile-count"
              >
                {{ stats ? stats[t.status] : '—' }}
              </div>
              <div class="mt-0.5 truncate text-xs text-muted-foreground">
                {{ t.label }}<template v-if="t.status === 'queued' && (stats?.retrying ?? 0) > 0">（{{ stats!.retrying }} 个等重试）</template>
              </div>
            </button>
          </div>

          <!-- 最近文件 -->
          <section class="rounded-lg shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
            <div class="flex h-11 items-center gap-2 border-b px-4 text-sm">
              <FolderSyncIcon class="size-4 text-muted-foreground" />
              <span class="font-medium text-foreground">{{ filterLabel ? `「${filterLabel}」的文件` : '最近的文件' }}</span>
              <span class="text-xs text-muted-foreground">按发现的先后，新的在上面；最多显示 200 个，上传中和失败的另外都显示</span>
              <button
                v-if="q.filter.value"
                type="button"
                class="text-xs text-info hover:underline"
                data-testid="filter-clear"
                @click="q.filter.value = null"
              >
                显示全部
              </button>
              <div class="flex-1" />
              <LoaderCircleIcon v-if="q.listLoading.value" class="size-4 animate-spin text-muted-foreground" />
              <Button
                v-if="(stats?.failed ?? 0) > 0"
                size="xs"
                variant="outline"
                :disabled="retryingAll"
                data-testid="retry-all"
                @click="retryAll"
              >
                <RotateCcwIcon />全部失败项重试
              </Button>
            </div>
            <QueueList
              v-if="q.rows.value.length > 0"
              :rows="q.rows.value"
              :progress-of="q.progressOf"
              :now="q.now.value"
              :retrying="retrying"
              @retry="retryOne"
            />
            <div
              v-else
              class="flex h-28 items-center justify-center text-sm text-muted-foreground"
              data-testid="queue-empty"
            >
              <template v-if="q.state.value === 'loading' || q.state.value === 'idle'">
                <LoaderCircleIcon class="mr-2 size-4 animate-spin" />正在读取上传队列…
              </template>
              <template v-else-if="q.filter.value">没有「{{ filterLabel }}」的文件</template>
              <template v-else>还没有文件：开始监听后，扫描软件写进监控目录的图片会出现在这里</template>
            </div>
          </section>
        </div>
      </div>

      <!-- ===== 待重扫（M2-7，壳和浏览器都有） ===== -->
      <RescanList />
    </div>
  </div>
</template>
