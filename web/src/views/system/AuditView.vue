<script setup lang="ts">
// 系统管理 · 审计日志（M7-3，菜单 system.audit）：只读列表。
//
// - 筛选：日期（本机时区的整天）、操作人、项目、对象类型、操作；条件放在地址栏的查询参数里（刷新、复制地址后还是
//   同样的条件）。改条件立即重新查（只用最后一次请求的结果）。
// - 列表按时间倒序（server 按 id 倒序、游标分页），每页 50 条，底下"加载更多"。
// - 点一行展开：变更前后的差异（detail 的 before、after）和其他信息。类型、动作、字段都译成中文（lib/audit.ts），
//   不认识的原样显示。
import { ChevronRightIcon, RotateCcwIcon } from '@lucide/vue'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { AUDIT_PAGE_SIZE, type AuditQuery, getAuditFilterOptions, listAuditLogs } from '@/api/audit'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import type { AuditFilterOptions, AuditLog } from '@/api/types'
import PageHeader from '@/components/app/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  actionLabel,
  actionOptions,
  describe,
  detailRows,
  formatAuditTime,
  knownNames,
  objectName,
  TARGET_LABELS,
  targetLabel,
} from '@/lib/audit'

const route = useRoute()
const router = useRouter()

// ---------- 筛选条件（和地址栏同步） ----------

const FILTER_KEYS = ['from', 'to', 'operator', 'project', 'type', 'action'] as const
type FilterKey = (typeof FILTER_KEYS)[number]
type Filters = Record<FilterKey, string>

function readQuery(): Filters {
  const out = {} as Filters
  for (const k of FILTER_KEYS) {
    const v = route.query[k]
    out[k] = typeof v === 'string' ? v : ''
  }
  return out
}

const filters = reactive<Filters>(readQuery())
const hasFilters = computed(() => FILTER_KEYS.some((k) => filters[k] !== ''))

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/

/** 本机时区某天 0 点（加 days 天）的 RFC 3339；不是日期时为 null。 */
function dayStart(date: string, days = 0): string | null {
  if (!DATE_RE.test(date)) return null
  const [y, m, d] = date.split('-').map(Number) as [number, number, number]
  const t = new Date(y, m - 1, d + days)
  return Number.isNaN(t.getTime()) ? null : t.toISOString()
}

const rangeError = computed(() =>
  filters.from && filters.to && filters.from > filters.to ? '开始日期不能晚于结束日期' : '',
)

/** 接口的查询参数（结束日期那一天也算在内）。 */
function apiQuery(): AuditQuery {
  return {
    from: filters.from ? dayStart(filters.from) : null,
    to: filters.to ? dayStart(filters.to, 1) : null,
    operator_id: filters.operator || null,
    project_id: filters.project || null,
    target_type: filters.type || null,
    action: filters.action || null,
  }
}

function resetFilters() {
  for (const k of FILTER_KEYS) filters[k] = ''
}

// ---------- 筛选栏的选项 ----------

const options = ref<AuditFilterOptions | null>(null)

const operatorOptions = computed(() => {
  const list = (options.value?.users ?? []).map((u) => ({
    value: u.id,
    label: `${u.display_name}（${u.username}）${u.status === 'disabled' ? ' · 已停用' : ''}`,
  }))
  if (filters.operator && !list.some((o) => o.value === filters.operator)) {
    list.unshift({ value: filters.operator, label: filters.operator })
  }
  return list
})

const projectOptions = computed(() => {
  const list = (options.value?.projects ?? []).map((p) => ({
    value: p.id,
    label: `${p.project_code} · ${p.project_name}${p.deleted ? '（已删除）' : ''}`,
  }))
  if (filters.project && !list.some((o) => o.value === filters.project)) {
    list.unshift({ value: filters.project, label: filters.project })
  }
  return list
})

const typeOptions = computed(() => {
  const list = Object.entries(TARGET_LABELS).map(([value, label]) => ({ value, label }))
  if (filters.type && !(filters.type in TARGET_LABELS)) list.push({ value: filters.type, label: filters.type })
  return list
})

const actionChoices = computed(() => {
  const list = actionOptions(filters.type || null)
  if (filters.action && !list.some((o) => o.value === filters.action)) {
    list.push({ value: filters.action, label: actionLabel(filters.action, filters.type) })
  }
  return list
})

// 换了对象类型、原来选的操作这个类型没有时清掉。
watch(
  () => filters.type,
  (type) => {
    if (filters.action && type && !actionOptions(type).some((o) => o.value === filters.action)) filters.action = ''
  },
)

// ---------- 列表 ----------

const items = ref<AuditLog[]>([])
const nextCursor = ref<string | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading')
const listError = ref('')
const loadingMore = ref(false)
const moreError = ref('')
const expanded = ref(new Set<number>())
/** 只用最后一次请求的结果（条件改得快时，先发的可能后回来）。 */
let seq = 0

const rows = computed(() => {
  const known = knownNames(items.value)
  return items.value.map((log) => ({
    log,
    time: formatAuditTime(log.created_at),
    object: objectName(log, known),
    action: actionLabel(log.action, log.target_type),
    summary: describe(log),
  }))
})

async function load() {
  const my = ++seq
  expanded.value = new Set()
  moreError.value = ''
  if (rangeError.value) {
    items.value = []
    nextCursor.value = null
    state.value = 'error'
    listError.value = rangeError.value
    return
  }
  state.value = 'loading'
  try {
    const res = await listAuditLogs({ ...apiQuery(), limit: AUDIT_PAGE_SIZE }, HANDLE_ALL)
    if (my !== seq) return
    items.value = res.items
    nextCursor.value = res.next_cursor
    state.value = 'ready'
  } catch (err) {
    if (my !== seq) return
    items.value = []
    nextCursor.value = null
    listError.value = errorMessage(err)
    state.value = 'error'
  }
}

async function loadMore() {
  if (!nextCursor.value || loadingMore.value) return
  const my = seq
  loadingMore.value = true
  moreError.value = ''
  try {
    const res = await listAuditLogs({ ...apiQuery(), limit: AUDIT_PAGE_SIZE, cursor: nextCursor.value }, HANDLE_ALL)
    if (my !== seq) return
    items.value = [...items.value, ...res.items]
    nextCursor.value = res.next_cursor
  } catch (err) {
    if (my === seq) moreError.value = `加载失败：${errorMessage(err)}`
  } finally {
    loadingMore.value = false
  }
}

function toggle(id: number) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

watch(
  filters,
  () => {
    const query: Record<string, string> = {}
    for (const k of FILTER_KEYS) if (filters[k]) query[k] = filters[k]
    void router.replace({ query })
    void load()
  },
  { deep: true },
)

onMounted(async () => {
  void load()
  try {
    options.value = await getAuditFilterOptions(HANDLE_ALL)
  } catch {
    // 选项取不到时下拉框只有"全部"和当前的条件；列表照常。
  }
})

function actionClass(action: string): string {
  if (action === 'create') return 'bg-ok-soft text-ok'
  if (action === 'delete' || action === 'disable') return 'bg-destructive-soft text-destructive'
  if (action === 'lock' || action === 'unlock') return 'bg-info-soft text-info'
  return ''
}

const CONTROL =
  'h-8 rounded-md border bg-background px-2 text-sm text-foreground outline-none focus-visible:ring-3 focus-visible:ring-ring/50'
</script>

<template>
  <PageHeader>
    <template #meta>
      <span class="text-sm text-muted-foreground" data-testid="audit-count">
        按时间倒序<template v-if="state === 'ready'">，已加载 {{ items.length }} 条{{ nextCursor ? '' : '（全部）' }}</template>
      </span>
    </template>
  </PageHeader>

  <section class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 py-5">
    <div class="flex flex-wrap items-end gap-3" data-testid="audit-filters">
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        开始日期
        <input v-model="filters.from" type="date" :class="CONTROL" data-testid="audit-from" />
      </label>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        结束日期
        <input v-model="filters.to" type="date" :class="CONTROL" data-testid="audit-to" />
      </label>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        操作人
        <select v-model="filters.operator" :class="[CONTROL, 'w-52']" data-testid="audit-operator">
          <option value="">全部</option>
          <option v-for="o in operatorOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        项目
        <select v-model="filters.project" :class="[CONTROL, 'w-56']" data-testid="audit-project">
          <option value="">全部</option>
          <option v-for="o in projectOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        对象
        <select v-model="filters.type" :class="[CONTROL, 'w-32']" data-testid="audit-type">
          <option value="">全部</option>
          <option v-for="o in typeOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        操作
        <select v-model="filters.action" :class="[CONTROL, 'w-44']" data-testid="audit-action">
          <option value="">全部</option>
          <option v-for="o in actionChoices" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
      <Button size="sm" variant="ghost" :disabled="!hasFilters" data-testid="audit-reset" @click="resetFilters">
        <RotateCcwIcon />清空条件
      </Button>
    </div>

    <div class="rounded-lg shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
      <Table class="table-fixed">
        <TableHeader>
          <TableRow class="bg-muted/60 hover:bg-muted/60">
            <TableHead class="w-9 pl-3" />
            <TableHead class="w-40">时间</TableHead>
            <TableHead class="w-36">操作人</TableHead>
            <TableHead class="w-48">项目</TableHead>
            <TableHead class="w-64">对象</TableHead>
            <TableHead class="w-36">操作</TableHead>
            <TableHead class="pr-4">说明</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <template v-for="r in rows" :key="r.log.id">
            <TableRow
              class="cursor-pointer"
              data-testid="audit-row"
              :data-audit-id="r.log.id"
              :data-target-type="r.log.target_type"
              :data-target-id="r.log.target_id"
              :data-action="r.log.action"
              @click="toggle(r.log.id)"
            >
              <TableCell class="pl-3">
                <button
                  type="button"
                  class="flex size-5 items-center justify-center rounded text-muted-foreground hover:text-foreground"
                  :aria-expanded="expanded.has(r.log.id)"
                  aria-label="展开详情"
                  @click.stop="toggle(r.log.id)"
                >
                  <ChevronRightIcon class="size-4 transition-transform" :class="{ 'rotate-90': expanded.has(r.log.id) }" />
                </button>
              </TableCell>
              <TableCell class="text-muted-foreground tabular-nums">{{ r.time }}</TableCell>
              <TableCell class="truncate" :title="r.log.operator_username ?? '系统自动'">
                <span v-if="r.log.operator_name">{{ r.log.operator_name }}</span>
                <span v-else class="text-muted-foreground">系统</span>
              </TableCell>
              <TableCell class="truncate" :title="r.log.project_name ?? ''">
                <template v-if="r.log.project_code">
                  <span class="font-mono text-xs text-muted-foreground">{{ r.log.project_code }}</span>
                  {{ r.log.project_name }}
                </template>
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell class="truncate" :title="r.object.title">
                <span class="text-muted-foreground">{{ targetLabel(r.log.target_type) }}</span>
                {{ r.object.name }}
              </TableCell>
              <TableCell>
                <Badge variant="secondary" class="rounded-md font-normal" :class="actionClass(r.log.action)">
                  {{ r.action }}
                </Badge>
              </TableCell>
              <TableCell class="truncate pr-4 text-muted-foreground" :title="r.summary">{{ r.summary || '—' }}</TableCell>
            </TableRow>
            <TableRow
              v-if="expanded.has(r.log.id)"
              class="bg-muted/30 hover:bg-muted/30"
              data-testid="audit-detail"
              :data-audit-id="r.log.id"
            >
              <TableCell colspan="7" class="px-4 py-3 whitespace-normal">
                <div class="flex flex-col gap-3 pl-6">
                  <table v-if="detailRows(r.log).changes.length" class="w-full max-w-4xl table-fixed text-xs">
                    <thead>
                      <tr class="text-left text-muted-foreground">
                        <th class="w-40 py-1 pr-3 font-medium">字段</th>
                        <th v-if="detailRows(r.log).changes.some((c) => c.before !== undefined)" class="py-1 pr-3 font-medium">
                          之前
                        </th>
                        <th class="py-1 font-medium">{{ detailRows(r.log).changes.some((c) => c.before !== undefined) ? '之后' : '值' }}</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="c in detailRows(r.log).changes" :key="c.key" class="border-t border-border/60 align-top">
                        <td class="py-1 pr-3 text-muted-foreground" :title="c.key">{{ c.label }}</td>
                        <td
                          v-if="c.before !== undefined"
                          class="py-1 pr-3 break-all"
                          :class="c.before === c.after ? 'text-muted-foreground' : 'text-destructive/80'"
                        >
                          {{ c.before }}
                        </td>
                        <td class="py-1 break-all" :class="c.before === c.after ? 'text-muted-foreground' : 'text-foreground'">
                          {{ c.after }}
                        </td>
                      </tr>
                    </tbody>
                  </table>
                  <dl v-if="detailRows(r.log).others.length" class="grid max-w-4xl grid-cols-[10rem_1fr] gap-x-3 gap-y-1 text-xs">
                    <template v-for="o in detailRows(r.log).others" :key="o.key">
                      <dt class="text-muted-foreground" :title="o.key">{{ o.label }}</dt>
                      <dd class="break-all text-foreground">{{ o.after }}</dd>
                    </template>
                  </dl>
                  <p class="font-mono text-[11px] text-muted-foreground">
                    #{{ r.log.id }} · {{ r.log.target_type }} / {{ r.log.action }} · {{ r.log.target_id }}
                    <template v-if="r.log.operator_username"> · {{ r.log.operator_username }}</template>
                  </p>
                </div>
              </TableCell>
            </TableRow>
          </template>
          <TableRow v-if="state === 'ready' && items.length === 0">
            <TableCell colspan="7" class="py-10 text-center text-muted-foreground" data-testid="audit-empty">
              {{ hasFilters ? '没有符合条件的记录' : '还没有审计记录' }}
            </TableCell>
          </TableRow>
          <TableRow v-if="state === 'loading'">
            <TableCell colspan="7" class="py-10 text-center text-muted-foreground">加载中…</TableCell>
          </TableRow>
          <TableRow v-if="state === 'error'">
            <TableCell colspan="7" class="py-10 text-center" data-testid="audit-error">
              <span class="text-destructive">{{ listError }}</span>
              <Button v-if="!rangeError" size="sm" variant="outline" class="ml-3" @click="load">重试</Button>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>

    <div v-if="state === 'ready' && items.length" class="flex items-center justify-center gap-3 text-sm text-muted-foreground">
      <Button v-if="nextCursor" size="sm" variant="outline" :disabled="loadingMore" data-testid="audit-more" @click="loadMore">
        {{ loadingMore ? '加载中…' : '加载更多' }}
      </Button>
      <span v-else data-testid="audit-end">没有更早的记录了</span>
      <span v-if="moreError" class="text-destructive">{{ moreError }}</span>
    </div>
  </section>
</template>
