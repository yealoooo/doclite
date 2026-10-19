<script setup lang="ts">
// 系统管理 · 用户：列表（搜索、分页）、新建、编辑资料、停用/启用、重置密码、分配角色。
// 非 admin 的操作人：按防提权规则预先禁用会返回 admin_required 的操作，鼠标移上去显示原因。
// 判断要用到角色列表（每个角色的菜单）；角色列表就绪之前、加载失败时，相关操作按"未知"一律禁用。
import { LockIcon, PlusIcon, SearchIcon } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { disableUser, enableUser, listRoles, listUsers } from '@/api/system'
import type { Page, RoleItem, UserItem } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import PageHeader from '@/components/app/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatDateTime } from '@/lib/format'
import { manageUserBlock } from '@/lib/privilege'
import { checkKeyword, MAX_KEYWORD_CHARS } from '@/lib/validate'
import { useSessionStore } from '@/stores/session'

import ResetPasswordDialog from './users/ResetPasswordDialog.vue'
import UserFormDialog from './users/UserFormDialog.vue'
import UserRolesDialog from './users/UserRolesDialog.vue'

const PAGE_SIZE = 20

const session = useSessionStore()
const route = useRoute()
const router = useRouter()

const keyword = ref('')
const page = ref(1)
const data = ref<Page<UserItem> | null>(null)
/** 列表加载失败的说明；失败时不显示旧结果。 */
const listError = ref('')
const loading = ref(false)
const roles = ref<RoleItem[]>([])
const rolesState = ref<'loading' | 'ready' | 'error'>('loading')

const roleById = computed(() => new Map(roles.value.map((r) => [r.id, r])))
const totalPages = computed(() => (data.value ? Math.max(1, Math.ceil(data.value.total / data.value.page_size)) : 1))
const keywordError = computed(() => checkKeyword(keyword.value))

/** 只采用最后一次请求的结果（输入很快时，先发的请求可能后回来）。 */
let listSeq = 0

async function loadUsers() {
  const seq = ++listSeq
  if (keywordError.value) {
    data.value = null
    listError.value = keywordError.value
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await listUsers({ keyword: keyword.value.trim(), page: page.value, page_size: PAGE_SIZE })
    if (seq !== listSeq) return
    data.value = result
    listError.value = ''
    // 列表里有角色列表里没有的角色（别人刚建的）：重新拉角色，否则防提权预判会把它当成未知。
    if (rolesState.value === 'ready' && result.items.some((u) => u.roles.some((r) => !roleById.value.has(r.id)))) {
      void loadRoles()
    }
  } catch (err) {
    if (seq !== listSeq) return
    // 错误已由请求封装提示；不再显示和条件不符的旧结果。
    data.value = null
    listError.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (seq === listSeq) loading.value = false
  }
}

async function loadRoles() {
  if (rolesState.value !== 'ready') rolesState.value = 'loading'
  try {
    roles.value = await listRoles()
    rolesState.value = 'ready'
  } catch {
    // 错误已由请求封装提示；页面上另有提示条和"重试"。
    rolesState.value = 'error'
  }
}

onMounted(() => Promise.all([loadRoles(), loadUsers()]))

watchDebounced(
  keyword,
  () => {
    page.value = 1
    void loadUsers()
  },
  { debounce: 300 },
)

function goPage(p: number) {
  page.value = Math.min(Math.max(1, p), totalPages.value)
  void loadUsers()
}

/** 角色列表还不能用时的原因（此时和角色有关的判断都按"未知"处理）；就绪时为 null。 */
const rolesBlock = computed(() => {
  if (rolesState.value === 'loading') return '正在加载角色信息，请稍候'
  if (rolesState.value === 'error') return '角色列表加载失败，无法判断权限，请重试'
  return null
})

/** 防提权规则拦下的原因（角色列表就绪后才判断）；可以修改时为 null。名字旁的锁图标用它。 */
function privilegeBlock(user: UserItem): string | null {
  if (session.isAdmin || rolesBlock.value) return null
  if (user.roles.some((r) => !roleById.value.has(r.id))) return '这个用户有尚未加载的角色，请稍候再试'
  return manageUserBlock(session.operator, user.roles.map((r) => roleById.value.get(r.id)!))
}

/** 修改这个用户（编辑、重置密码、启用）不能做的原因；可以做时为 null。持有 admin 角色的操作人不受限。 */
function manageBlock(user: UserItem): string | null {
  if (session.isAdmin) return null
  return rolesBlock.value ?? privilegeBlock(user)
}

/** 分配角色：对话框里要列出全部角色，所以角色列表没就绪时谁都不能用。 */
function rolesButtonBlock(user: UserItem): string | null {
  return rolesBlock.value ?? manageBlock(user)
}

function disableBlock(user: UserItem): string | null {
  if (user.id === session.me?.id) return '不能停用自己的账号'
  return manageBlock(user)
}

function replaceRow(user: UserItem) {
  if (!data.value) return
  data.value.items = data.value.items.map((u) => (u.id === user.id ? user : u))
}

// ---------- 对话框 ----------

const formOpen = ref(false)
const formUser = ref<UserItem | null>(null)
const rolesOpen = ref(false)
const resetOpen = ref(false)
const target = ref<UserItem | null>(null)
const disableOpen = ref(false)
const disabling = ref(false)

function openCreate() {
  formUser.value = null
  formOpen.value = true
}

function openEdit(user: UserItem) {
  formUser.value = user
  formOpen.value = true
}

function openRoles(user: UserItem) {
  target.value = user
  rolesOpen.value = true
}

function openReset(user: UserItem) {
  target.value = user
  resetOpen.value = true
}

function openDisable(user: UserItem) {
  target.value = user
  disableOpen.value = true
}

/** 改的是自己：重新拉当前用户和菜单（侧栏姓名、isAdmin、导航都会变）；这个页面不能进了就换成无权提示页。 */
async function refreshIfSelf(user: UserItem) {
  if (user.id !== session.me?.id) return
  try {
    await session.refreshSession()
  } catch {
    return
  }
  if (!session.canAccess(route.path)) {
    await router.replace({ name: 'forbidden', query: { from: route.fullPath } })
  }
}

async function onSaved(user: UserItem, created = false) {
  if (created) {
    toast.success(`已创建用户 ${user.display_name}`)
    await Promise.all([loadUsers(), loadRoles()])
  } else {
    replaceRow(user)
    await refreshIfSelf(user)
  }
}

async function onRolesSaved(user: UserItem) {
  replaceRow(user)
  toast.success(`已更新 ${user.display_name} 的角色`)
  void loadRoles()
  await refreshIfSelf(user)
}

async function confirmDisable() {
  if (!target.value) return
  disabling.value = true
  try {
    const user = await disableUser(target.value.id)
    replaceRow(user)
    toast.success(`已停用 ${user.display_name}`)
    disableOpen.value = false
  } catch {
    // 错误已提示（例如最后一个管理员 last_admin）。
  } finally {
    disabling.value = false
  }
}

async function enable(user: UserItem) {
  try {
    const saved = await enableUser(user.id)
    replaceRow(saved)
    toast.success(`已启用 ${saved.display_name}`)
  } catch {
    // 错误已提示。
  }
}
</script>

<template>
  <PageHeader>
    <template #meta>
      <span v-if="data" class="text-sm text-muted-foreground">共 {{ data.total }} 个</span>
    </template>
    <template #actions>
      <div class="relative w-64">
        <SearchIcon class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          v-model="keyword"
          placeholder="搜索登录名或姓名"
          class="h-8 pl-8"
          :maxlength="MAX_KEYWORD_CHARS"
          :aria-invalid="!!keywordError || undefined"
          data-testid="user-search"
        />
      </div>
      <GuardedButton size="sm" :reason="rolesBlock" data-testid="user-create" @click="openCreate">
        <PlusIcon />新建用户
      </GuardedButton>
    </template>
  </PageHeader>

  <section class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 py-5">
    <div
      v-if="rolesState === 'error'"
      class="flex items-center gap-3 rounded-lg bg-destructive-soft px-4 py-3 text-sm text-destructive"
      role="alert"
      data-testid="roles-error"
    >
      <p class="flex-1">角色列表加载失败：新建用户、分配角色{{ session.isAdmin ? '' : '和修改用户' }}的操作暂时禁用。</p>
      <Button size="sm" variant="outline" @click="loadRoles">重试</Button>
    </div>
    <div
      v-if="!session.isAdmin"
      class="flex items-start gap-2.5 rounded-lg bg-info-soft px-4 py-3 text-sm text-foreground"
      data-testid="non-admin-note"
    >
      <LockIcon class="mt-0.5 size-4 shrink-0 text-info" />
      <p>
        你不是系统管理员。系统管理员账号、有你没有的系统管理权限的账号（名字旁带<LockIcon
          class="mx-0.5 inline size-3.5 align-[-2px] text-muted-foreground"
        />）只能由系统管理员修改；系统管理员角色、含有你没有的系统管理菜单的角色也不能分配。相关按钮已禁用，鼠标移上去可以看到原因。
      </p>
    </div>

    <div class="rounded-lg shadow-outline">
      <Table>
        <TableHeader>
          <TableRow class="bg-muted/60 hover:bg-muted/60">
            <TableHead class="w-48 pl-4">登录名</TableHead>
            <TableHead class="w-36">姓名</TableHead>
            <TableHead>角色</TableHead>
            <TableHead class="w-20">状态</TableHead>
            <TableHead class="w-36">最近登录</TableHead>
            <TableHead class="w-36">创建时间</TableHead>
            <TableHead class="w-[272px] pr-4 text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="user in data?.items ?? []" :key="user.id" :data-username="user.username">
            <TableCell class="pl-4 font-medium text-foreground">
              <span class="inline-flex items-center gap-1.5">
                {{ user.username }}
                <Tooltip v-if="privilegeBlock(user)">
                  <TooltipTrigger as-child>
                    <LockIcon class="size-3.5 text-muted-foreground" tabindex="0" data-testid="row-lock" />
                  </TooltipTrigger>
                  <TooltipContent>{{ privilegeBlock(user) }}</TooltipContent>
                </Tooltip>
                <Badge v-if="user.id === session.me?.id" variant="outline" class="h-4.5 px-1.5 text-2xs">我</Badge>
              </span>
            </TableCell>
            <TableCell>{{ user.display_name }}</TableCell>
            <TableCell class="whitespace-normal">
              <div class="flex flex-wrap gap-1">
                <Badge
                  v-for="r in user.roles"
                  :key="r.id"
                  variant="secondary"
                  class="rounded-md font-normal"
                >{{ r.role_name }}</Badge>
                <span v-if="user.roles.length === 0" class="text-muted-foreground">—</span>
              </div>
            </TableCell>
            <TableCell>
              <Badge
                :class="user.status === 'active' ? 'bg-ok-soft text-ok' : 'bg-destructive-soft text-destructive'"
                class="rounded-md"
              >{{ user.status === 'active' ? '启用' : '停用' }}</Badge>
            </TableCell>
            <TableCell class="text-muted-foreground tabular-nums">{{ formatDateTime(user.last_login_at) }}</TableCell>
            <TableCell class="text-muted-foreground tabular-nums">{{ formatDateTime(user.created_at) }}</TableCell>
            <TableCell class="pr-4">
              <div class="flex justify-end gap-1">
                <GuardedButton size="xs" variant="ghost" :reason="manageBlock(user)" @click="openEdit(user)">编辑</GuardedButton>
                <GuardedButton size="xs" variant="ghost" :reason="rolesButtonBlock(user)" @click="openRoles(user)">角色</GuardedButton>
                <GuardedButton size="xs" variant="ghost" :reason="manageBlock(user)" @click="openReset(user)">重置密码</GuardedButton>
                <GuardedButton
                  v-if="user.status === 'active'"
                  size="xs"
                  variant="ghost"
                  class="text-destructive hover:text-destructive"
                  :reason="disableBlock(user)"
                  @click="openDisable(user)"
                >停用</GuardedButton>
                <GuardedButton v-else size="xs" variant="ghost" :reason="manageBlock(user)" @click="enable(user)">启用</GuardedButton>
              </div>
            </TableCell>
          </TableRow>
          <TableRow v-if="data && data.items.length === 0">
            <TableCell colspan="7" class="py-10 text-center text-muted-foreground">
              {{ keyword.trim() ? '没有符合条件的用户' : '还没有用户' }}
            </TableCell>
          </TableRow>
          <TableRow v-if="!data && loading">
            <TableCell colspan="7" class="py-10 text-center text-muted-foreground">加载中…</TableCell>
          </TableRow>
          <TableRow v-if="!data && !loading && listError">
            <TableCell colspan="7" class="py-10 text-center" data-testid="list-error">
              <span class="text-destructive">{{ listError }}</span>
              <Button v-if="!keywordError" size="sm" variant="outline" class="ml-3" @click="loadUsers">重试</Button>
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

  <UserFormDialog
    v-model:open="formOpen"
    :user="formUser"
    :roles="roles"
    @saved="(u) => onSaved(u, formUser === null)"
  />
  <UserRolesDialog v-model:open="rolesOpen" :user="target" :roles="roles" @saved="onRolesSaved" />
  <ResetPasswordDialog v-model:open="resetOpen" :user="target" />
  <ConfirmDialog
    v-model:open="disableOpen"
    title="停用用户"
    :description="`停用 ${target?.display_name}（${target?.username}）后，他会被立即强制下线，不能再登录。之后可以重新启用。`"
    confirm-text="停用"
    destructive
    :loading="disabling"
    @confirm="confirmDisable"
  />
</template>
