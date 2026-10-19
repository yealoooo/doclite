<script setup lang="ts">
// 项目授权：勾选人和角色，整体替换（PUT /api/projects/{pid}/access）。
// - 选人用 GET /api/system/projects/access-candidates（全部用户的精简信息和全部角色），不依赖用户管理的菜单权限；
// - 勾选角色后，显示这个角色会带进哪些人；已停用的用户标出来（授权保留，启用后就能进）；
// - 系统管理员（admin 角色）本来就能进所有项目，不用授权。
import { SearchIcon, UserXIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { errorMessage, HANDLE_ALL } from '@/api/http'
import { getAccessCandidates, getProjectAccess, setProjectAccess } from '@/api/projects'
import type { AccessCandidates, CandidateUser, ProjectAccess, ProjectItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ADMIN_ROLE_CODE } from '@/lib/privilege'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{ project: ProjectItem | null }>()

const emit = defineEmits<{ saved: [access: ProjectAccess] }>()

const candidates = ref<AccessCandidates | null>(null)
const original = ref<ProjectAccess | null>(null)
const userIds = ref<string[]>([])
const roleIds = ref<string[]>([])
const keyword = ref('')
const loadError = ref('')
const error = ref('')
const saving = ref(false)
/** 只采用最后一次打开时的加载结果。 */
let loadSeq = 0

async function load() {
  const p = props.project
  if (!p) return
  const seq = ++loadSeq
  candidates.value = null
  original.value = null
  loadError.value = ''
  try {
    const [c, a] = await Promise.all([getAccessCandidates(HANDLE_ALL), getProjectAccess(p.id, HANDLE_ALL)])
    if (seq !== loadSeq) return
    candidates.value = c
    original.value = a
    userIds.value = a.users.map((u) => u.id)
    roleIds.value = a.roles.map((r) => r.id)
  } catch (err) {
    if (seq !== loadSeq) return
    loadError.value = errorMessage(err)
  }
}

watch(open, (v) => {
  if (!v) return
  keyword.value = ''
  error.value = ''
  void load()
})

const userById = computed(() => new Map(candidates.value?.users.map((u) => [u.id, u]) ?? []))
const roleName = computed(() => new Map(candidates.value?.roles.map((r) => [r.id, r.role_name]) ?? []))

/** 拥有这个角色的用户（按登录名）。 */
function membersOf(roleId: string): CandidateUser[] {
  return candidates.value?.users.filter((u) => u.role_ids.includes(roleId)) ?? []
}

/** 用户通过哪些已勾选的角色进得来（角色名）。 */
function viaRoles(user: CandidateUser): string[] {
  return user.role_ids.filter((id) => roleIds.value.includes(id)).map((id) => roleName.value.get(id) ?? '')
}

const filteredUsers = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  const users = candidates.value?.users ?? []
  if (!k) return users
  return users.filter((u) => u.username.toLowerCase().includes(k) || u.display_name.toLowerCase().includes(k))
})

/** admin 角色的 id（候选里的角色按 role_code 找）。 */
const adminRoleId = computed(() => candidates.value?.roles.find((r) => r.role_code === ADMIN_ROLE_CODE)?.id ?? null)

/**
 * 合计：因为这次授权能进这个项目的人（个人授权 ∪ 角色带进的）。持有 admin 角色的用户本来就能进所有项目，
 * 不算在内（和文案"不含系统管理员"一致）。
 */
const summary = computed(() => {
  const all = new Set(userIds.value)
  for (const u of candidates.value?.users ?? []) {
    if (u.role_ids.some((id) => roleIds.value.includes(id))) all.add(u.id)
  }
  const counted = [...all].filter((id) => {
    const u = userById.value.get(id)
    return !(adminRoleId.value && u?.role_ids.includes(adminRoleId.value))
  })
  const disabled = counted.filter((id) => userById.value.get(id)?.status === 'disabled').length
  return { total: counted.length, disabled }
})

const changed = computed(() => {
  const o = original.value
  if (!o) return false
  const same = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x))
  return !same(userIds.value, o.users.map((u) => u.id)) || !same(roleIds.value, o.roles.map((r) => r.id))
})

function toggleUser(id: string, on: boolean) {
  userIds.value = on ? [...userIds.value, id] : userIds.value.filter((x) => x !== id)
}

function toggleRole(id: string, on: boolean) {
  roleIds.value = on ? [...roleIds.value, id] : roleIds.value.filter((x) => x !== id)
}

async function submit() {
  const p = props.project
  if (!p) return
  error.value = ''
  saving.value = true
  try {
    const saved = await setProjectAccess(p.id, userIds.value, roleIds.value, HANDLE_ALL)
    emit('saved', saved)
    open.value = false
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-3xl" data-testid="access-dialog">
      <DialogHeader>
        <DialogTitle>项目授权 · {{ project?.project_name }}</DialogTitle>
        <DialogDescription>
          能进这个项目的人 = 勾选的人 + 拥有所勾选角色的人。系统管理员能进所有项目，不用授权；能用哪些菜单仍由各自的角色决定。
        </DialogDescription>
      </DialogHeader>

      <div v-if="loadError" class="flex items-center gap-3 rounded-lg bg-destructive-soft px-4 py-3 text-sm text-destructive">
        <p class="flex-1">加载失败：{{ loadError }}</p>
        <Button size="sm" variant="outline" @click="load">重试</Button>
      </div>
      <p v-else-if="!candidates" class="py-10 text-center text-sm text-muted-foreground">加载中…</p>

      <div v-else class="grid grid-cols-2 gap-4">
        <!-- 授权给人 -->
        <section class="flex min-h-0 flex-col gap-2">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium text-foreground">授权给人</h3>
            <span class="text-xs text-muted-foreground">已选 {{ userIds.length }} 人</span>
          </div>
          <div class="relative">
            <SearchIcon class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input v-model="keyword" placeholder="搜索登录名或姓名" class="h-8 pl-8" data-testid="access-search" />
          </div>
          <div class="flex h-80 flex-col overflow-y-auto rounded-lg p-1 shadow-outline">
            <label
              v-for="u in filteredUsers"
              :key="u.id"
              class="flex cursor-pointer items-start gap-2.5 rounded-md px-2.5 py-2 hover:bg-muted"
              :data-username="u.username"
              data-testid="access-user"
            >
              <Checkbox
                class="mt-0.5"
                :model-value="userIds.includes(u.id)"
                @update:model-value="(v) => toggleUser(u.id, v === true)"
              />
              <span class="min-w-0 flex-1">
                <span class="flex items-center gap-2">
                  <span class="truncate text-sm text-foreground" :class="u.status === 'disabled' ? 'opacity-60' : ''">
                    {{ u.display_name }}
                  </span>
                  <span class="truncate font-mono text-xs text-muted-foreground">{{ u.username }}</span>
                  <span
                    v-if="u.status === 'disabled'"
                    class="inline-flex shrink-0 items-center gap-0.5 rounded bg-destructive-soft px-1 text-2xs text-destructive"
                    data-testid="disabled-badge"
                  ><UserXIcon class="size-3" />已停用</span>
                </span>
                <span v-if="viaRoles(u).length" class="mt-0.5 block text-xs text-info">
                  已通过角色「{{ viaRoles(u).join('、') }}」授权
                </span>
              </span>
            </label>
            <p v-if="filteredUsers.length === 0" class="px-2.5 py-2 text-sm text-muted-foreground">没有符合条件的用户</p>
          </div>
        </section>

        <!-- 授权给角色 -->
        <section class="flex min-h-0 flex-col gap-2">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium text-foreground">授权给角色</h3>
            <span class="text-xs text-muted-foreground">已选 {{ roleIds.length }} 个</span>
          </div>
          <div class="flex h-[364px] flex-col overflow-y-auto rounded-lg p-1 shadow-outline">
            <div
              v-for="r in candidates.roles"
              :key="r.id"
              class="rounded-md px-2.5 py-2 hover:bg-muted"
              :data-role="r.role_code"
              data-testid="access-role"
            >
              <label class="flex cursor-pointer items-start gap-2.5">
                <Checkbox
                  class="mt-0.5"
                  :model-value="roleIds.includes(r.id)"
                  @update:model-value="(v) => toggleRole(r.id, v === true)"
                />
                <span class="min-w-0 flex-1">
                  <span class="flex items-center gap-2">
                    <span class="text-sm text-foreground">{{ r.role_name }}</span>
                    <span class="font-mono text-xs text-muted-foreground">{{ r.role_code }}</span>
                    <span class="ml-auto shrink-0 text-xs text-muted-foreground">{{ membersOf(r.id).length }} 人</span>
                  </span>
                  <span v-if="r.role_code === ADMIN_ROLE_CODE" class="mt-0.5 block text-xs text-muted-foreground">
                    系统管理员本来就能进所有项目
                  </span>
                </span>
              </label>
              <!-- 勾选后显示会带进哪些人。 -->
              <div
                v-if="roleIds.includes(r.id)"
                class="mt-1.5 ml-7 flex flex-wrap gap-1"
                data-testid="role-members"
              >
                <span v-if="membersOf(r.id).length === 0" class="text-xs text-muted-foreground">目前没有人拥有这个角色</span>
                <span
                  v-for="m in membersOf(r.id)"
                  :key="m.id"
                  class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-xs"
                  :class="m.status === 'disabled' ? 'bg-destructive-soft text-destructive' : 'bg-info-soft text-foreground'"
                  :title="m.status === 'disabled' ? '已停用' : m.username"
                >
                  <UserXIcon v-if="m.status === 'disabled'" class="size-3" />{{ m.display_name }}
                </span>
              </div>
            </div>
          </div>
        </section>
      </div>

      <p v-if="candidates" class="text-sm text-muted-foreground" data-testid="access-summary">
        合计 <b class="text-foreground">{{ summary.total }}</b> 人能进这个项目（不含系统管理员）<template v-if="summary.disabled">，其中
          <span class="text-destructive">{{ summary.disabled }} 人已停用</span>（启用后才能登录）</template>。
      </p>
      <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">{{ error }}</p>

      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button :disabled="saving || !changed" data-testid="access-save" @click="submit">
          {{ saving ? '保存中…' : '保存' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
