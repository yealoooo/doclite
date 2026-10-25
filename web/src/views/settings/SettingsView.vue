<script setup lang="ts">
// 客户端的设置页 /settings（M7-11，只在客户端里有入口：侧栏用户区的"设置"、托盘菜单的"设置…"）。
// 和客户端本身有关的都放在这里，取代原来的"关于""本机图片缓存"两个对话框。按客户端的能力显示（client/BRIDGE.md），
// 比网页旧的客户端没有的功能整组或整行隐藏：
// - 服务器：当前地址；连接状态（经客户端调 GET /api/health，通不通、响应时间）；服务端版本；"修改服务器地址"
//   （客户端自带的修改地址页，能力 pages；已经配置过地址时客户端弹系统确认框）；
// - 账号：当前登录的用户（姓名、登录名、角色）、退出登录；
// - 本机存储（StorageSection）：数据目录（大小、在访达中打开，能力 settings）、本机图片缓存（images）、上传队列
//   （upload）、日志目录（settings）；
// - 热文件夹（HotFolderSection，能力 upload）：当前监控的目录和状态，去扫描上传页设置；
// - 关于：客户端版本、界面版本、运行环境；"复制诊断信息"（diagnostics.ts，不带令牌、Cookie、密码）。
// 上传队列、热文件夹的状态随壳的事件更新（upload:state、hotfolder:state）。
// 浏览器里只显示一句说明（交付只有客户端，浏览器只在开发时用）。
import {
  CircleUserIcon,
  CopyIcon,
  FolderSyncIcon,
  HardDriveIcon,
  InfoIcon,
  LoaderCircleIcon,
  LogOutIcon,
  MonitorIcon,
  RefreshCwIcon,
  ServerIcon,
} from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'

import { fetchHealth } from '@/api/health'
import { errorMessage } from '@/api/http'
import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { useLogout } from '@/composables/useLogout'
import { copyText } from '@/lib/clipboard'
import { cacheStats } from '@/lib/image-cache'
import {
  hotfolderStatus,
  type HotFolderStatus,
  listenQueueEvents,
  type QueueStatus,
  uploadStatus,
} from '@/lib/scan-queue'
import { localDirs, type LocalDirs, shellErrorMessage } from '@/lib/shell'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'

import { formatDiagnostics, type HealthResult, platformLabel } from './diagnostics'
import HotFolderSection from './HotFolderSection.vue'
import SettingsSection from './SettingsSection.vue'
import StorageSection from './StorageSection.vue'

const shell = useShellStore()
const session = useSessionStore()
const { loggingOut, logout } = useLogout()

const inShell = shell.inShell
const info = computed(() => shell.info)
const hasSettings = shell.has('settings')
const hasImages = shell.has('images')
const hasUpload = shell.has('upload')

const runtime = computed(() => platformLabel(inShell ? info.value : null))
const roleNames = computed(() => session.me?.roles.map((r) => r.role_name) ?? [])

// ---------- 服务器：连接状态 ----------

const health = ref<HealthResult | null>(null)
const checking = ref(false)

/** 经客户端调 GET /api/health，记下通不通和响应时间（含客户端转发的时间）。 */
async function measureHealth(): Promise<HealthResult> {
  const t0 = performance.now()
  try {
    const h = await fetchHealth()
    return { ok: true, ms: Math.max(1, Math.round(performance.now() - t0)), version: h.version }
  } catch (e) {
    return { ok: false, ms: Math.round(performance.now() - t0), error: errorMessage(e) }
  }
}

async function checkHealth() {
  if (checking.value) return
  checking.value = true
  try {
    health.value = await measureHealth()
  } finally {
    checking.value = false
  }
}

// ---------- 本机目录、上传队列、热文件夹 ----------

const dirs = ref<LocalDirs | null>(null)
const dirsError = ref('')
const dirsLoaded = ref(!hasSettings)

async function loadDirs() {
  if (!hasSettings) return
  try {
    dirs.value = await localDirs()
    dirsError.value = ''
  } catch (e) {
    dirsError.value = `读取失败：${shellErrorMessage(e)}`
  } finally {
    dirsLoaded.value = true
  }
}

const queue = ref<QueueStatus | null>(null)
const queueError = ref('')
const hotfolder = ref<HotFolderStatus | null>(null)
const hotfolderError = ref('')
const uploadLoaded = ref(!hasUpload)

async function loadQueue() {
  try {
    queue.value = await uploadStatus()
    queueError.value = ''
  } catch (e) {
    queueError.value = shellErrorMessage(e)
  }
}

async function loadUploads() {
  if (!hasUpload) return
  await Promise.all([
    loadQueue(),
    hotfolderStatus()
      .then((s) => {
        hotfolder.value = s
        hotfolderError.value = ''
      })
      .catch((e) => {
        hotfolderError.value = shellErrorMessage(e)
      }),
  ])
  uploadLoaded.value = true
}

let stopEvents: (() => void) | null = null
let alive = true

onMounted(async () => {
  if (!inShell) return
  void checkHealth()
  void loadDirs()
  if (hasUpload) {
    try {
      const stop = await listenQueueEvents({
        state: (s) => (queue.value = s),
        hotfolder: (s) => (hotfolder.value = s),
      })
      if (alive) stopEvents = stop
      else stop()
    } catch {
      // 订阅不了：状态只在打开页面时读一次。
    }
    void loadUploads()
  }
})

onBeforeUnmount(() => {
  alive = false
  stopEvents?.()
  stopEvents = null
})

/** 第一次读取都结束了（截图脚本、自检据此等待）。 */
const ready = computed(() => health.value !== null && dirsLoaded.value && uploadLoaded.value)

// ---------- 关于：复制诊断信息 ----------

const copying = ref(false)

async function copyDiagnostics() {
  if (copying.value) return
  copying.value = true
  try {
    // 连接状态现测一次；缓存的占用现读（面板里的可能是打开页面时的）。
    const [h, cache] = await Promise.all([
      measureHealth(),
      hasImages ? cacheStats().catch(() => null) : Promise.resolve(null),
    ])
    health.value = h
    const me = session.me
    const text = formatDiagnostics({
      at: new Date(),
      clientVersion: shell.clientVersion,
      webVersion: shell.webVersion,
      runtime: runtime.value,
      serverUrl: shell.serverUrl,
      health: h,
      user: me ? { username: me.username, displayName: me.display_name, roles: roleNames.value } : null,
      dataDir: info.value?.data_dir ?? null,
      dirs: dirs.value,
      cache: cache
        ? { totalBytes: cache.total_bytes, limitBytes: cache.limit_bytes, fileCount: cache.file_count, available: cache.available }
        : null,
      queue: queue.value,
      hotfolder: hotfolder.value,
      capabilities: info.value?.capabilities ?? [],
    })
    if (await copyText(text)) toast.success('已复制诊断信息，可以直接粘贴给维护人员')
    else toast.error('复制失败，请稍后再试')
  } finally {
    copying.value = false
  }
}
</script>

<template>
  <PageHeader title="设置" />

  <section
    class="min-h-0 flex-1 overflow-y-auto px-6 py-5"
    data-testid="settings-page"
    :data-state="!inShell ? 'browser' : ready ? 'ready' : 'loading'"
  >
    <div v-if="!inShell" class="flex max-w-xl items-start gap-3 rounded-lg p-5 shadow-outline">
      <MonitorIcon class="mt-0.5 size-5 shrink-0 text-muted-foreground" />
      <p class="text-sm leading-relaxed text-muted-foreground">
        设置页只在 Doclite 桌面客户端里有：服务器地址、本机的数据目录、图片缓存、上传队列和热文件夹都是客户端的。
      </p>
    </div>

    <div v-else class="flex max-w-3xl flex-col gap-4">
      <!-- 服务器 -->
      <SettingsSection title="服务器" :icon="ServerIcon" data-testid="settings-server">
        <template #actions>
          <Button v-if="shell.canEditServer" size="sm" variant="outline" data-testid="settings-edit-server" @click="shell.editServer()">
            修改服务器地址
          </Button>
        </template>
        <dl class="grid grid-cols-[88px_1fr] items-baseline gap-x-3 gap-y-2.5 text-sm">
          <dt class="text-muted-foreground">地址</dt>
          <dd class="font-mono break-all text-foreground" data-testid="settings-server-url">{{ shell.serverUrl ?? '—' }}</dd>
          <dt class="text-muted-foreground">连接</dt>
          <dd class="flex flex-wrap items-center gap-2" data-testid="settings-health" :data-ok="health ? String(health.ok) : undefined">
            <template v-if="health === null">
              <LoaderCircleIcon class="size-3.5 animate-spin text-muted-foreground" /><span class="text-muted-foreground">正在测试…</span>
            </template>
            <template v-else-if="health.ok">
              <span class="size-2 rounded-full bg-ok" />
              <span class="text-foreground">正常</span>
              <span class="text-muted-foreground tabular-nums" data-testid="settings-health-ms">响应 {{ health.ms }} ms</span>
            </template>
            <template v-else>
              <span class="size-2 rounded-full bg-destructive" />
              <span class="text-destructive">连不上：{{ health.error }}</span>
            </template>
            <Button size="xs" variant="ghost" :disabled="checking" data-testid="settings-health-retry" @click="checkHealth">
              <RefreshCwIcon :class="checking ? 'animate-spin' : ''" />重新测试
            </Button>
          </dd>
          <dt class="text-muted-foreground">服务端版本</dt>
          <dd class="font-mono text-foreground" data-testid="settings-server-version">{{ health?.ok ? health.version : '—' }}</dd>
        </dl>
      </SettingsSection>

      <!-- 账号 -->
      <SettingsSection title="账号" :icon="CircleUserIcon" data-testid="settings-account">
        <template #actions>
          <Button size="sm" variant="outline" :disabled="loggingOut" data-testid="settings-logout" @click="logout">
            <LogOutIcon />退出登录
          </Button>
        </template>
        <dl class="grid grid-cols-[88px_1fr] items-baseline gap-x-3 gap-y-2.5 text-sm">
          <dt class="text-muted-foreground">姓名</dt>
          <dd class="text-foreground" data-testid="settings-user-name">{{ session.me?.display_name ?? '—' }}</dd>
          <dt class="text-muted-foreground">登录名</dt>
          <dd class="font-mono text-foreground" data-testid="settings-username">{{ session.me?.username ?? '—' }}</dd>
          <dt class="text-muted-foreground">角色</dt>
          <dd class="text-foreground" data-testid="settings-roles">{{ roleNames.length ? roleNames.join('、') : '没有角色' }}</dd>
        </dl>
      </SettingsSection>

      <!-- 本机存储 -->
      <SettingsSection title="本机存储" :icon="HardDriveIcon" description="这台电脑上客户端自己的文件。" data-testid="settings-storage">
        <StorageSection
          :platform="info?.platform ?? null"
          :data-dir="info?.data_dir ?? null"
          :can-open="hasSettings"
          :dirs="dirs"
          :dirs-error="dirsError"
          :has-images="hasImages"
          :has-upload="hasUpload"
          :queue="queue"
          :queue-error="queueError"
          @queue-changed="loadQueue"
        />
      </SettingsSection>

      <!-- 热文件夹 -->
      <SettingsSection
        v-if="hasUpload"
        title="热文件夹"
        :icon="FolderSyncIcon"
        description="盯着扫描软件的输出目录，写完一页就自动上传。"
        data-testid="settings-hotfolder"
      >
        <HotFolderSection :hotfolder="hotfolder" :error="hotfolderError" :queue-server="queue?.server_url ?? null" />
      </SettingsSection>

      <!-- 关于 -->
      <SettingsSection title="关于" :icon="InfoIcon" description="Doclite 纸质档案数字化系统" data-testid="settings-about">
        <template #actions>
          <Button size="sm" variant="outline" :disabled="copying" data-testid="settings-copy-diagnostics" @click="copyDiagnostics">
            <LoaderCircleIcon v-if="copying" class="animate-spin" /><CopyIcon v-else />复制诊断信息
          </Button>
        </template>
        <dl class="grid grid-cols-[88px_1fr] items-baseline gap-x-3 gap-y-2.5 text-sm">
          <dt class="text-muted-foreground">客户端版本</dt>
          <dd class="font-mono text-foreground" data-testid="settings-client-version">{{ shell.clientVersion ?? '—' }}</dd>
          <dt class="text-muted-foreground">界面版本</dt>
          <dd class="font-mono text-foreground" data-testid="settings-web-version">{{ shell.webVersion }}</dd>
          <dt class="text-muted-foreground">运行环境</dt>
          <dd class="text-foreground" data-testid="settings-runtime">{{ runtime }}</dd>
        </dl>
        <p class="mt-3 text-xs text-muted-foreground">
          诊断信息包括上面这些版本、服务器地址和连接、账号、本机目录、缓存和上传队列的统计，不含密码和登录凭据。
        </p>
      </SettingsSection>
    </div>
  </section>
</template>
