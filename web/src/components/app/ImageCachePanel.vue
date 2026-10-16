<script setup lang="ts">
// 本机图片缓存的管理面板（M1-11，design §4.5），只在壳里有意义（浏览器只能用自带的缓存，看不到占用）。
// - 总占用、上限、文件数、缓存目录；各项目的占用（封面、原图张数），项目名取自 GET /api/projects，
//   当前账号进不了的项目（例如别的账号在这台电脑上看过）只显示 id；
// - 按项目清空、全部清空（都先确认）；修改上限（GB）。
// 独立组件：客户端设置页的"本机存储"里用（M7-11，原来在用户菜单的"本机图片缓存"对话框里；/scan 页头部的一行摘要
// 去掉了，缓存占用只在设置页看）。打开时加载一次，`refresh()` 可以从外面调用。
import { RefreshCwIcon } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'

import { listMyProjects } from '@/api/projects'
import type { MyProject } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { shellHas } from '@/lib/client'
import { formatBytes } from '@/lib/files'
import {
  cacheClear,
  cacheSetLimit,
  cacheStats,
  CACHE_LIMIT_MAX_BYTES,
  CACHE_LIMIT_MIN_BYTES,
  type CacheProjectUsage,
  type CacheStats,
} from '@/lib/image-cache'
import { shellErrorMessage } from '@/lib/shell'

const GB = 1024 ** 3

/** 客户端有本机图片缓存（能力 images）；浏览器里、旧客户端里都没有。 */
const inShell = shellHas('images')
const stats = ref<CacheStats | null>(null)
const loadError = ref('')
const loading = ref(false)
const projects = ref<Map<string, MyProject>>(new Map())

const limitGb = ref('')
const savingLimit = ref(false)

/** 要清空的项目；`null` 是全部，`undefined` 是没在确认。 */
const clearTarget = ref<CacheProjectUsage | null | undefined>(undefined)
const clearing = ref(false)
const confirmOpen = computed({
  get: () => clearTarget.value !== undefined,
  set: (v) => {
    if (!v && !clearing.value) clearTarget.value = undefined
  },
})

const usedPercent = computed(() => {
  const s = stats.value
  if (!s || s.limit_bytes <= 0) return 0
  return Math.min(100, (s.total_bytes / s.limit_bytes) * 100)
})

function projectLabel(id: string): { name: string; code: string | null; known: boolean } {
  const p = projects.value.get(id)
  if (p) return { name: p.project_name, code: p.project_code, known: true }
  return { name: `其他项目（${id.slice(0, 8)}…）`, code: null, known: false }
}

const confirmTitle = computed(() =>
  clearTarget.value ? `清空「${projectLabel(clearTarget.value.project_id).name}」的缓存` : '清空全部缓存',
)
const confirmDescription = computed(() => {
  const t = clearTarget.value
  const bytes = t ? t.total_bytes : (stats.value?.total_bytes ?? 0)
  const files = t ? t.file_count : (stats.value?.file_count ?? 0)
  return `将删除这台电脑上缓存的 ${files} 个图片文件（${formatBytes(bytes)}）。服务器上的图片不受影响，下次查看时会重新下载。`
})

function gbText(bytes: number): string {
  const v = bytes / GB
  return Number.isInteger(v) ? String(v) : v.toFixed(1)
}

async function loadProjects() {
  try {
    const list = await listMyProjects(true)
    projects.value = new Map(list.map((p) => [p.id, p]))
  } catch {
    // 取不到项目名时只显示 id，不影响面板。
  }
}

async function refresh() {
  if (!inShell) return
  loading.value = true
  loadError.value = ''
  try {
    const s = await cacheStats()
    stats.value = s
    limitGb.value = gbText(s.limit_bytes)
  } catch (e) {
    loadError.value = shellErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function confirmClear() {
  const target = clearTarget.value
  if (target === undefined) return
  clearing.value = true
  try {
    const r = await cacheClear(target?.project_id ?? null)
    const what = target ? `「${projectLabel(target.project_id).name}」的缓存` : '全部缓存'
    if (r.failed_files > 0) {
      toast.warning(`已清空${what}：删除 ${r.removed_files} 个文件；有 ${r.failed_files} 个文件被占用没删掉，稍后再试`)
    } else {
      toast.success(`已清空${what}：删除 ${r.removed_files} 个文件，释放 ${formatBytes(r.removed_bytes)}`)
    }
    clearing.value = false
    clearTarget.value = undefined
    await refresh()
  } catch (e) {
    toast.error(`清空失败：${shellErrorMessage(e)}`)
  } finally {
    clearing.value = false
  }
}

const limitError = computed(() => {
  const v = Number(limitGb.value)
  if (!limitGb.value.trim() || !Number.isFinite(v)) return '请填写数字'
  const bytes = Math.round(v * GB)
  if (bytes < CACHE_LIMIT_MIN_BYTES || bytes > CACHE_LIMIT_MAX_BYTES) return '上限要在 0.1 GB 到 1024 GB 之间'
  return ''
})
const limitChanged = computed(
  () => !!stats.value && !limitError.value && Math.round(Number(limitGb.value) * GB) !== stats.value.limit_bytes,
)

async function saveLimit() {
  if (!limitChanged.value || savingLimit.value) return
  savingLimit.value = true
  try {
    const s = await cacheSetLimit(Math.round(Number(limitGb.value) * GB))
    stats.value = s
    limitGb.value = gbText(s.limit_bytes)
    toast.success(`上限已改为 ${formatBytes(s.limit_bytes)}`)
  } catch (e) {
    toast.error(`修改上限失败：${shellErrorMessage(e)}`)
  } finally {
    savingLimit.value = false
  }
}

onMounted(() => {
  void loadProjects()
  void refresh()
})

defineExpose({ refresh })
</script>

<template>
  <div class="flex flex-col gap-4 text-sm" data-testid="image-cache-panel">
    <p v-if="!inShell" class="text-muted-foreground">本机图片缓存只在桌面客户端里有；浏览器使用自带的缓存。</p>

    <template v-else>
      <!-- 总占用 -->
      <div class="rounded-lg p-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
        <div class="flex items-end justify-between gap-4">
          <div>
            <div class="text-xs text-muted-foreground">总占用</div>
            <div class="text-2xl font-semibold text-foreground tabular-nums" data-testid="cache-total">
              {{ stats ? formatBytes(stats.total_bytes) : '—' }}
            </div>
          </div>
          <div class="text-right text-xs text-muted-foreground tabular-nums">
            <div>上限 {{ stats ? formatBytes(stats.limit_bytes) : '—' }}</div>
            <div>{{ stats ? `${stats.file_count} 个文件` : '' }}</div>
          </div>
        </div>
        <div class="mt-3 h-1.5 overflow-hidden rounded-full bg-muted">
          <div
            class="h-full rounded-full transition-[width]"
            :class="usedPercent >= 90 ? 'bg-warn' : 'bg-primary'"
            :style="{ width: `${usedPercent}%` }"
          />
        </div>
        <div v-if="stats" class="mt-3 text-xs text-muted-foreground">
          超过上限后自动删除最久没看过的图片。位置：
          <span class="font-mono break-all text-foreground" data-testid="cache-dir">{{ stats.dir }}</span>
        </div>
        <p v-if="loadError" class="mt-3 text-destructive" role="alert">读取缓存信息失败：{{ loadError }}</p>
        <p
          v-if="stats && !stats.available"
          class="mt-3 rounded-md bg-warn-soft px-3 py-2 text-warn"
          role="alert"
          data-testid="cache-unavailable"
        >
          本机图片缓存不可用，图片照常显示，只是不存到这台电脑上{{ stats.error ? `（${stats.error}）` : '' }}。
        </p>
      </div>

      <!-- 各项目 -->
      <div class="rounded-lg shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
        <Table>
          <TableHeader>
            <TableRow class="bg-muted/60 hover:bg-muted/60">
              <TableHead class="pl-4">项目</TableHead>
              <TableHead class="w-16 text-right">封面</TableHead>
              <TableHead class="w-16 text-right">原图</TableHead>
              <TableHead class="w-24 text-right">占用</TableHead>
              <TableHead class="w-20 pr-4 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow
              v-for="u in stats?.projects ?? []"
              :key="u.project_id"
              :data-project-id="u.project_id"
              data-testid="cache-project-row"
            >
              <TableCell class="pl-4 whitespace-normal">
                <div
                  :class="projectLabel(u.project_id).known ? 'text-foreground' : 'text-muted-foreground'"
                  :title="projectLabel(u.project_id).known ? undefined : `当前账号进不了这个项目（项目 id ${u.project_id}）`"
                >
                  {{ projectLabel(u.project_id).name }}
                </div>
                <div v-if="projectLabel(u.project_id).code" class="font-mono text-xs text-muted-foreground">
                  {{ projectLabel(u.project_id).code }}
                </div>
              </TableCell>
              <TableCell class="text-right text-muted-foreground tabular-nums">{{ u.cover_count }}</TableCell>
              <TableCell class="text-right text-muted-foreground tabular-nums">{{ u.original_count }}</TableCell>
              <TableCell class="text-right font-medium text-foreground tabular-nums">{{ formatBytes(u.total_bytes) }}</TableCell>
              <TableCell class="pr-4 text-right">
                <Button
                  size="xs"
                  variant="ghost"
                  class="text-destructive hover:text-destructive"
                  data-testid="cache-clear-project"
                  @click="clearTarget = u"
                >
                  清空
                </Button>
              </TableCell>
            </TableRow>
            <TableRow v-if="stats && stats.projects.length === 0">
              <TableCell colspan="5" class="py-8 text-center text-muted-foreground" data-testid="cache-empty">
                缓存是空的：查看过的封面和原图会存在这里，下次打开不用重新下载。
              </TableCell>
            </TableRow>
            <TableRow v-if="!stats && loading">
              <TableCell colspan="5" class="py-8 text-center text-muted-foreground">加载中…</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>

      <!-- 上限和操作 -->
      <div class="flex flex-wrap items-center justify-between gap-3">
        <form class="flex items-center gap-2" @submit.prevent="saveLimit">
          <label for="cache-limit" class="text-muted-foreground">上限</label>
          <Input
            id="cache-limit"
            v-model="limitGb"
            inputmode="decimal"
            class="h-8 w-20 text-right tabular-nums"
            :aria-invalid="!!limitError"
            data-testid="cache-limit"
          />
          <span class="text-muted-foreground">GB</span>
          <Button
            type="submit"
            size="sm"
            variant="outline"
            :disabled="!limitChanged || savingLimit || !stats?.available"
          >
            {{ savingLimit ? '保存中…' : '保存' }}
          </Button>
          <span v-if="limitError && limitGb.trim()" class="text-xs text-destructive">{{ limitError }}</span>
        </form>
        <div class="flex items-center gap-2">
          <Button size="sm" variant="ghost" :disabled="loading" @click="refresh">
            <RefreshCwIcon :class="loading ? 'animate-spin' : ''" />刷新
          </Button>
          <Button
            size="sm"
            variant="outline"
            class="text-destructive hover:text-destructive"
            :disabled="!stats || !stats.available || stats.file_count === 0"
            data-testid="cache-clear-all"
            @click="clearTarget = null"
          >
            全部清空
          </Button>
        </div>
      </div>
    </template>
  </div>

  <ConfirmDialog
    v-model:open="confirmOpen"
    :title="confirmTitle"
    :description="confirmDescription"
    confirm-text="清空"
    destructive
    :loading="clearing"
    @confirm="confirmClear"
  />
</template>
