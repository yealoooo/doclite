<script setup lang="ts">
// 系统管理 · 项目（M1-5）：列表（搜索、状态筛选、分页）、新建、编辑、删除、授权。
// - 编码建好后不能改；改名时根文件夹一起改名；已归档只是标记。
// - 删除是软删除，项目里还有文件时不能删（409 project_not_empty，在确认框里提示）。
// - 授权：勾选人和角色（整体替换），见 projects/ProjectAccessDialog.vue。
import { PlusIcon, SearchIcon } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'

import { errorMessage, isApiError } from '@/api/http'
import { deleteProject, listProjects } from '@/api/projects'
import type { Page, ProjectAccess, ProjectDetail, ProjectItem, ProjectStatus } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { StatusBadge } from '@/components/ui/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { PROJECT_STATUS, PROJECT_STATUS_LABEL } from '@/lib/project'
import { checkKeyword, MAX_KEYWORD_CHARS } from '@/lib/validate'
import { useProjectStore } from '@/stores/project'

import ProjectAccessDialog from './projects/ProjectAccessDialog.vue'
import ProjectFormDialog from './projects/ProjectFormDialog.vue'

const PAGE_SIZE = 20

const projectStore = useProjectStore()

const keyword = ref('')
const status = ref<ProjectStatus | ''>('')
const page = ref(1)
const data = ref<Page<ProjectItem> | null>(null)
const listError = ref('')
const loading = ref(false)

const totalPages = computed(() => (data.value ? Math.max(1, Math.ceil(data.value.total / data.value.page_size)) : 1))
const keywordError = computed(() => checkKeyword(keyword.value))
const STATUS_FILTERS: { value: ProjectStatus | ''; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'active', label: PROJECT_STATUS_LABEL.active },
  { value: 'archived', label: PROJECT_STATUS_LABEL.archived },
]

/** 只采用最后一次请求的结果。 */
let listSeq = 0

async function load() {
  const seq = ++listSeq
  if (keywordError.value) {
    data.value = null
    listError.value = keywordError.value
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await listProjects({
      keyword: keyword.value.trim(),
      status: status.value || undefined,
      page: page.value,
      page_size: PAGE_SIZE,
    })
    if (seq !== listSeq) return
    // 当前页已经没有数据了（例如删掉了最后一页的最后一项）：退到最后一页再取一次。
    const lastPage = Math.max(1, Math.ceil(result.total / result.page_size))
    if (result.items.length === 0 && result.total > 0 && page.value > lastPage) {
      page.value = lastPage
      await load()
      return
    }
    data.value = result
    listError.value = ''
  } catch (err) {
    if (seq !== listSeq) return
    data.value = null
    listError.value = errorMessage(err)
  } finally {
    if (seq === listSeq) loading.value = false
  }
}

onMounted(load)

watchDebounced(
  keyword,
  () => {
    page.value = 1
    void load()
  },
  { debounce: 300 },
)

function setStatus(s: ProjectStatus | '') {
  status.value = s
  page.value = 1
  void load()
}

function goPage(p: number) {
  page.value = Math.min(Math.max(1, p), totalPages.value)
  void load()
}

/** 项目增删改、授权之后，业务界面的项目切换器也要跟上（改名、自己被授权……）。 */
function refreshSwitcher() {
  if (projectStore.state === 'ready') void projectStore.refresh()
}

// ---------- 对话框 ----------

const formOpen = ref(false)
const formProject = ref<ProjectItem | null>(null)
const accessOpen = ref(false)
const target = ref<ProjectItem | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
const deleteError = ref('')

function openCreate() {
  formProject.value = null
  formOpen.value = true
}

function openEdit(p: ProjectItem) {
  formProject.value = p
  formOpen.value = true
}

function openAccess(p: ProjectItem) {
  target.value = p
  accessOpen.value = true
}

function openDelete(p: ProjectItem) {
  target.value = p
  deleteError.value = ''
  deleteOpen.value = true
}

async function onSaved(p: ProjectDetail, created: boolean) {
  toast.success(created ? `已创建项目 ${p.project_name}` : `已保存 ${p.project_name}`)
  // 列表按新建时间倒序：新建的在第 1 页最前面。
  if (created) page.value = 1
  await load()
  refreshSwitcher()
}

async function onAccessSaved(access: ProjectAccess) {
  const p = target.value
  if (p && data.value) {
    data.value.items = data.value.items.map((x) =>
      x.id === p.id ? { ...x, member_count: access.users.length, role_count: access.roles.length } : x,
    )
  }
  toast.success(`已更新 ${p?.project_name ?? ''} 的授权`)
  refreshSwitcher()
}

async function confirmDelete() {
  const p = target.value
  if (!p) return
  deleting.value = true
  deleteError.value = ''
  try {
    await deleteProject(p.id, ['project_not_empty', 'not_found'])
    toast.success(`已删除项目 ${p.project_name}`)
    deleteOpen.value = false
    await load()
    refreshSwitcher()
  } catch (err) {
    if (isApiError(err, 'not_found')) {
      deleteOpen.value = false
      toast.error('项目已经被删除了')
      await load()
    } else {
      deleteError.value = errorMessage(err)
    }
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <PageHeader>
    <template #meta>
      <span v-if="data" class="text-sm text-muted-foreground">共 {{ data.total }} 个</span>
    </template>
    <template #actions>
      <div class="inline-flex rounded-md bg-muted p-0.5" role="radiogroup" data-testid="status-filter">
        <button
          v-for="f in STATUS_FILTERS"
          :key="f.value"
          type="button"
          role="radio"
          :aria-checked="status === f.value"
          class="rounded px-2.5 py-1 text-sm"
          :class="status === f.value ? 'bg-background font-medium text-foreground shadow-xs' : 'text-muted-foreground'"
          @click="setStatus(f.value)"
        >
          {{ f.label }}
        </button>
      </div>
      <div class="relative w-64">
        <SearchIcon class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          v-model="keyword"
          placeholder="搜索编码或名称"
          class="h-8 pl-8"
          :maxlength="MAX_KEYWORD_CHARS"
          :aria-invalid="!!keywordError || undefined"
          data-testid="project-search"
        />
      </div>
      <Button size="sm" data-testid="project-create" @click="openCreate"><PlusIcon />新建项目</Button>
    </template>
  </PageHeader>

  <section class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 py-5">
    <div class="rounded-lg shadow-outline">
      <Table>
        <TableHeader>
          <TableRow class="bg-muted/60 hover:bg-muted/60">
            <TableHead class="w-40 pl-4">编码</TableHead>
            <TableHead>名称</TableHead>
            <TableHead class="w-24">状态</TableHead>
            <TableHead class="w-44">授权</TableHead>
            <TableHead class="w-36">创建时间</TableHead>
            <TableHead class="w-[168px] pr-4 text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="p in data?.items ?? []" :key="p.id" :data-code="p.project_code">
            <TableCell class="pl-4 font-mono font-medium text-foreground">{{ p.project_code }}</TableCell>
            <TableCell class="whitespace-normal">
              <div class="text-foreground">{{ p.project_name }}</div>
              <div v-if="p.description" class="mt-0.5 line-clamp-1 text-xs text-muted-foreground" :title="p.description">
                {{ p.description }}
              </div>
            </TableCell>
            <TableCell>
              <StatusBadge :status="PROJECT_STATUS[p.status]" size="md" />
            </TableCell>
            <TableCell class="text-muted-foreground tabular-nums">
              <span v-if="p.member_count === 0 && p.role_count === 0" class="text-warn">未授权（仅管理员）</span>
              <span v-else>{{ p.member_count }} 人 · {{ p.role_count }} 个角色</span>
            </TableCell>
            <TableCell class="text-muted-foreground tabular-nums">{{ formatDateTime(p.created_at) }}</TableCell>
            <TableCell class="pr-4">
              <div class="flex justify-end gap-1">
                <Button size="xs" variant="ghost" @click="openEdit(p)">编辑</Button>
                <Button size="xs" variant="ghost" data-testid="project-access" @click="openAccess(p)">授权</Button>
                <Button size="xs" variant="ghost" class="text-destructive hover:text-destructive" @click="openDelete(p)">
                  删除
                </Button>
              </div>
            </TableCell>
          </TableRow>
          <TableRow v-if="data && data.items.length === 0">
            <TableCell colspan="6" class="py-10 text-center text-muted-foreground">
              {{ keyword.trim() || status ? '没有符合条件的项目' : '还没有项目，点右上角「新建项目」' }}
            </TableCell>
          </TableRow>
          <TableRow v-if="!data && loading">
            <TableCell colspan="6" class="py-10 text-center text-muted-foreground">加载中…</TableCell>
          </TableRow>
          <TableRow v-if="!data && !loading && listError">
            <TableCell colspan="6" class="py-10 text-center" data-testid="list-error">
              <span class="text-destructive">{{ listError }}</span>
              <Button v-if="!keywordError" size="sm" variant="outline" class="ml-3" @click="load">重试</Button>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>

    <div v-if="data && data.total > 0" class="flex items-center justify-end gap-3 text-sm text-muted-foreground">
      <span>第 {{ data.page }} / {{ totalPages }} 页，每页 {{ data.page_size }} 个</span>
      <Button size="sm" variant="outline" :disabled="page <= 1 || loading" @click="goPage(page - 1)">上一页</Button>
      <Button size="sm" variant="outline" :disabled="page >= totalPages || loading" @click="goPage(page + 1)">下一页</Button>
    </div>
  </section>

  <ProjectFormDialog
    v-model:open="formOpen"
    :project="formProject"
    @saved="(p) => onSaved(p, formProject === null)"
  />
  <ProjectAccessDialog v-model:open="accessOpen" :project="target" @saved="onAccessSaved" />
  <ConfirmDialog
    v-model:open="deleteOpen"
    title="删除项目"
    :description="`删除项目 ${target?.project_name}（${target?.project_code}）后，谁都进不去了，也不能恢复；编码可以给新项目再用。项目里还有文件时不能删除。`"
    confirm-text="删除"
    destructive
    :loading="deleting"
    @confirm="confirmDelete"
  >
    <p v-if="deleteError" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">
      {{ deleteError }}
    </p>
  </ConfirmDialog>
</template>
