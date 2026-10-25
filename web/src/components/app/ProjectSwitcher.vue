<script setup lang="ts">
// 项目切换器（M1-5）：业务界面侧栏顶部，显示当前项目，点开后切换。
// - 列表来自 `GET /api/projects`（当前用户能进的项目），每次打开时刷新，刚被授权的项目会出现；
// - 已归档的项目默认不显示，勾选"显示已归档"后才列出（当前项目是已归档的也照常显示）；
// - 一个能进的项目都没有时显示空状态，提示找管理员授权。
// 当前项目放在 Pinia（stores/project.ts），业务页从那里取。
// 侧栏收起时（M7-11，collapsed）：只显示项目编码的缩写（悬停提示项目名），点开照样是切换列表；没有项目、加载失败时
// 是一个图标，点了展开侧栏（看完整的说明）或重试。
import { ArchiveIcon, CheckIcon, ChevronsUpDownIcon, FolderKanbanIcon, RefreshCwIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'

import { Checkbox } from '@/components/ui/checkbox'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useProjectStore } from '@/stores/project'

const props = defineProps<{
  /** 侧栏收起了（M7-11）。 */
  collapsed?: boolean
}>()

const emit = defineEmits<{
  /** 收起时点了"没有项目"：展开侧栏看说明。 */
  expand: []
}>()

const project = useProjectStore()

/** 收起时显示的项目编码（最多 10 个字符，窄栏里折成两行，每行 5 个）。 */
const shortCode = computed(() => (project.current?.project_code ?? '').slice(0, 10))
const collapsedTitle = computed(() => {
  const p = project.current
  if (p) return `${p.project_name}（${p.project_code}）· 点击切换项目`
  return project.state === 'ready' ? '选择项目' : '正在加载项目…'
})

const open = ref(false)
const showArchived = ref(false)

const visible = computed(() => (showArchived.value ? project.projects : project.activeProjects))
/** 没有任何能进的项目（加载成功之后）。 */
const noProjects = computed(() => project.state === 'ready' && project.projects.length === 0)

onMounted(() => {
  void project.ensureLoaded()
})

function onOpenChange(v: boolean) {
  open.value = v
  if (v) void project.refresh()
}

async function choose(id: string) {
  open.value = false
  if (id === project.currentId) return
  // 页面可以拦下（例如归类页有没保存的著录，M3-5）。
  if (!(await project.confirmSwitch())) return
  project.select(id)
}
</script>

<template>
  <div class="mb-2" data-testid="project-switcher" :data-collapsed="props.collapsed ? 'true' : 'false'">
    <!-- 收起时没有项目、加载失败：一个图标。 -->
    <Tooltip v-if="props.collapsed && (noProjects || project.state === 'error')">
      <TooltipTrigger as-child>
        <button
          type="button"
          class="flex h-10 w-full items-center justify-center rounded-lg text-muted-foreground hover:bg-sidebar-accent"
          :class="project.state === 'error' ? 'text-destructive' : ''"
          :aria-label="noProjects ? '没有可进入的项目' : '项目列表加载失败，点击重试'"
          @click="noProjects ? emit('expand') : project.refresh()"
        >
          <FolderKanbanIcon v-if="noProjects" class="size-4" />
          <TriangleAlertIcon v-else class="size-4" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="right" :side-offset="8">
        {{ noProjects ? '没有可进入的项目' : '项目列表加载失败，点击重试' }}
      </TooltipContent>
    </Tooltip>

    <!-- 一个能进的项目都没有：空状态。 -->
    <div
      v-else-if="noProjects"
      class="rounded-lg border border-dashed border-sidebar-border px-3 py-3 text-center"
      data-testid="project-empty"
    >
      <FolderKanbanIcon class="mx-auto mb-1.5 size-5 text-muted-foreground" />
      <div class="text-sm font-medium text-foreground">没有可进入的项目</div>
      <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
        请联系管理员在「系统管理 › 项目」里给你授权
      </p>
      <button
        type="button"
        class="mt-2 inline-flex items-center gap-1 text-xs text-info hover:underline"
        data-testid="project-reload"
        @click="project.refresh()"
      >
        <RefreshCwIcon class="size-3" />重新检查
      </button>
    </div>

    <div
      v-else-if="project.state === 'error'"
      class="flex items-center gap-2 rounded-lg bg-destructive-soft px-3 py-2 text-xs text-destructive"
    >
      <span class="flex-1">项目列表加载失败</span>
      <button type="button" class="underline" @click="project.refresh()">重试</button>
    </div>

    <Popover v-else :open="open" @update:open="onOpenChange">
      <PopoverTrigger v-if="props.collapsed" as-child>
        <button
          type="button"
          class="flex h-10 w-full items-center justify-center rounded-lg bg-background px-0.5 shadow-outline hover:bg-sidebar-accent"
          :disabled="project.state !== 'ready'"
          :title="collapsedTitle"
          :aria-label="collapsedTitle"
          data-testid="project-switcher-trigger"
        >
          <span
            v-if="project.current"
            class="line-clamp-2 text-center font-mono text-2xs leading-tight font-semibold break-all text-foreground"
            data-testid="project-current-code"
          >{{ shortCode }}</span>
          <FolderKanbanIcon v-else class="size-4 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverTrigger v-else as-child>
        <button
          type="button"
          class="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left hover:bg-sidebar-accent"
          :disabled="project.state !== 'ready'"
          data-testid="project-switcher-trigger"
        >
          <span class="min-w-0 flex-1">
            <template v-if="project.current">
              <span class="flex items-center gap-1.5">
                <span class="truncate text-sm font-semibold text-foreground" data-testid="project-current">
                  {{ project.current.project_name }}
                </span>
                <ArchiveIcon
                  v-if="project.current.status === 'archived'"
                  class="size-3.5 shrink-0 text-muted-foreground"
                  aria-label="已归档"
                />
              </span>
              <span class="block truncate text-xs text-muted-foreground">
                项目 <span class="font-mono">{{ project.current.project_code }}</span>
              </span>
            </template>
            <template v-else-if="project.state === 'ready'">
              <span class="block text-sm font-medium text-foreground">选择项目</span>
              <span class="block text-xs text-muted-foreground">没有进行中的项目</span>
            </template>
            <span v-else class="block text-sm text-muted-foreground">正在加载项目…</span>
          </span>
          <ChevronsUpDownIcon class="size-4 shrink-0 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" side="right" :side-offset="10" class="w-72 gap-0 p-1">
        <div class="px-2.5 pt-2 pb-1.5 text-xs font-medium text-muted-foreground">切换项目</div>
        <div class="flex max-h-80 flex-col overflow-y-auto" role="listbox" data-testid="project-options">
          <button
            v-for="p in visible"
            :key="p.id"
            type="button"
            role="option"
            :aria-selected="p.id === project.currentId"
            class="flex items-center gap-2 rounded-md px-2.5 py-2 text-left hover:bg-muted"
            :data-code="p.project_code"
            data-testid="project-option"
            @click="choose(p.id)"
          >
            <span class="min-w-0 flex-1">
              <span class="flex items-center gap-1.5">
                <span class="truncate text-sm text-foreground">{{ p.project_name }}</span>
                <span
                  v-if="p.status === 'archived'"
                  class="shrink-0 rounded bg-muted px-1 text-2xs text-muted-foreground"
                >已归档</span>
              </span>
              <span class="block truncate font-mono text-xs text-muted-foreground">{{ p.project_code }}</span>
            </span>
            <CheckIcon v-if="p.id === project.currentId" class="size-4 shrink-0 text-foreground" />
          </button>
          <p v-if="visible.length === 0" class="px-2.5 py-3 text-sm text-muted-foreground">
            没有进行中的项目{{ project.archivedProjects.length ? '，可以勾选下面的「显示已归档」' : '' }}
          </p>
        </div>
        <label
          v-if="project.archivedProjects.length > 0"
          class="mt-1 flex cursor-pointer items-center gap-2 border-t px-2.5 pt-2 pb-1.5 text-xs text-muted-foreground"
        >
          <Checkbox :model-value="showArchived" @update:model-value="(v) => (showArchived = v === true)" />
          显示已归档（{{ project.archivedProjects.length }}）
        </label>
      </PopoverContent>
    </Popover>
  </div>
</template>
