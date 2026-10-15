<script setup lang="ts">
// 系统管理 · 角色与权限：左边角色列表，右边编辑名称、说明和菜单权限（参照 plan.html §4 "角色与权限" Mockup）。
// - admin 角色的菜单固定为全部，只读；内置角色不能删除。
// - 非 admin 的操作人不能增减 system.* 菜单、不能删除含 system.* 菜单的角色（预先禁用，后端仍会拦）。
// - 当前选中的角色记在 ?role=<role_code>，刷新或直接打开链接时保持选中。
// - 有未保存的修改时，切换角色、新建角色都先确认"放弃修改"。
import { LockIcon, PlusIcon } from '@lucide/vue'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { deleteRole, listAllMenus, listRoles, setRoleMenus, updateRole, type UpdateRoleBody } from '@/api/system'
import type { MenuItem, RoleItem } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { ADMIN_ROLE_CODE, deleteRoleBlock } from '@/lib/privilege'
import { checkDescription, checkRoleName } from '@/lib/validate'
import { useSessionStore } from '@/stores/session'

import MenuTree from './roles/MenuTree.vue'
import RoleCreateDialog from './roles/RoleCreateDialog.vue'

const session = useSessionStore()
const route = useRoute()
const router = useRouter()

const roles = ref<RoleItem[]>([])
const menus = ref<MenuItem[]>([])
const loaded = ref(false)

const selectedCode = computed(() => {
  const q = route.query.role
  const code = typeof q === 'string' ? q : undefined
  return roles.value.some((r) => r.role_code === code) ? code : roles.value[0]?.role_code
})
const current = computed(() => roles.value.find((r) => r.role_code === selectedCode.value) ?? null)
const isAdminRole = computed(() => current.value?.role_code === ADMIN_ROLE_CODE)

// ---------- 编辑表单 ----------

const form = reactive({ roleName: '', description: '', menuCodes: [] as string[] })
const saving = ref(false)

function resetForm() {
  const r = current.value
  form.roleName = r?.role_name ?? ''
  form.description = r?.description ?? ''
  form.menuCodes = r ? [...r.menu_codes] : []
}

// 切换角色（或第一次加载完）时把表单换成这个角色的内容；保存后显式调用。
watch(() => current.value?.id, resetForm, { immediate: true })

function sameCodes(a: readonly string[], b: readonly string[]) {
  return a.length === b.length && a.every((c) => b.includes(c))
}

const infoChanged = computed(
  () =>
    !!current.value &&
    (form.roleName.trim() !== current.value.role_name ||
      form.description.trim() !== (current.value.description ?? '')),
)
const menusChanged = computed(() => !!current.value && !isAdminRole.value && !sameCodes(form.menuCodes, current.value.menu_codes))
const dirty = computed(() => infoChanged.value || menusChanged.value)

const treeLocked = computed(() => (isAdminRole.value ? '系统管理员角色固定拥有全部菜单，不能修改' : null))
const deleteReason = computed(() => (current.value ? deleteRoleBlock(session.operator, current.value) : null))

async function load() {
  try {
    const [r, m] = await Promise.all([listRoles(), listAllMenus()])
    roles.value = r
    menus.value = m
  } catch {
    // 错误已由请求封装提示。
  } finally {
    loaded.value = true
  }
}

onMounted(load)

// ---------- 未保存的修改 ----------

const discardOpen = ref(false)
let pendingAction: (() => void) | null = null

/** 有未保存的修改时先确认放弃，确认后（或本来就没有修改时）执行 `action`。 */
function guardDirty(action: () => void) {
  if (!dirty.value) {
    action()
    return
  }
  pendingAction = action
  discardOpen.value = true
}

function confirmDiscard() {
  discardOpen.value = false
  resetForm()
  const action = pendingAction
  pendingAction = null
  action?.()
}

watch(discardOpen, (open) => {
  if (!open) pendingAction = null
})

function select(role: RoleItem) {
  if (role.role_code === selectedCode.value) return
  guardDirty(() => void router.replace({ query: { ...route.query, role: role.role_code } }))
}

function openCreate() {
  guardDirty(() => {
    createOpen.value = true
  })
}

/** 角色的名称、菜单变了：当前用户可能有这个角色，重新拉当前用户和菜单；这个页面不能进了就换成无权提示页。 */
async function refreshAfterChange(): Promise<boolean> {
  try {
    await session.refreshSession()
  } catch {
    return true
  }
  if (!session.canAccess(route.path)) {
    await router.replace({ name: 'forbidden', query: { from: route.fullPath } })
    return false
  }
  return true
}

function replaceRole(role: RoleItem) {
  roles.value = roles.value.map((r) => (r.id === role.id ? role : r))
}

async function save() {
  const role = current.value
  if (!role) return
  const name = form.roleName.trim()
  const invalid = checkRoleName(form.roleName) || checkDescription(form.description)
  if (invalid) {
    toast.error(invalid)
    return
  }
  const info: UpdateRoleBody = {}
  if (name !== role.role_name) info.role_name = name
  if (form.description.trim() !== (role.description ?? '')) info.description = form.description.trim()
  const menuCodes = menusChanged.value ? [...form.menuCodes] : null

  saving.value = true
  try {
    if (Object.keys(info).length) replaceRole(await updateRole(role.id, info))
    if (menuCodes) replaceRole(await setRoleMenus(role.id, menuCodes))
    if (!(await refreshAfterChange())) return
    resetForm()
    toast.success(`已保存「${current.value?.role_name}」`)
  } catch {
    // 错误已提示（例如 admin_required）。已经保存成功的部分已更新到列表，表单里保留没保存的修改。
  } finally {
    saving.value = false
  }
}

// ---------- 新建、删除 ----------

const createOpen = ref(false)
const deleteOpen = ref(false)
const deleting = ref(false)

async function onCreated(role: RoleItem) {
  roles.value = [...roles.value, role]
  toast.success(`已创建角色「${role.role_name}」`)
  await router.replace({ query: { ...route.query, role: role.role_code } })
}

async function confirmDelete() {
  const role = current.value
  if (!role) return
  deleting.value = true
  try {
    await deleteRole(role.id)
    roles.value = roles.value.filter((r) => r.id !== role.id)
    deleteOpen.value = false
    toast.success(`已删除角色「${role.role_name}」`)
    await router.replace({ query: { ...route.query, role: roles.value[0]?.role_code } })
    // 删掉的可能是自己的角色。
    await refreshAfterChange()
  } catch {
    // 错误已提示。
  } finally {
    deleting.value = false
  }
}

function badgeOf(role: RoleItem): { text: string; cls: string } {
  if (role.role_code === ADMIN_ROLE_CODE) return { text: '内置 · 锁定', cls: 'bg-secondary text-muted-foreground' }
  if (role.is_builtin) return { text: '内置', cls: 'bg-secondary text-muted-foreground' }
  return { text: '自定义', cls: 'bg-info-soft text-info' }
}
</script>

<template>
  <PageHeader>
    <template #meta>
      <span class="rounded-md bg-secondary px-2 py-0.5 text-xs text-muted-foreground">权限目前只做到菜单级</span>
    </template>
    <template #actions>
      <Button size="sm" data-testid="role-create" @click="openCreate"><PlusIcon />新建角色</Button>
    </template>
  </PageHeader>

  <div class="grid min-h-0 flex-1 grid-cols-[260px_minmax(0,1fr)]">
    <!-- 角色列表 -->
    <aside class="flex min-h-0 flex-col gap-1 overflow-y-auto border-r p-3">
      <div class="px-2 pb-1 text-xs font-semibold text-muted-foreground">角色</div>
      <button
        v-for="role in roles"
        :key="role.id"
        type="button"
        class="flex flex-col gap-0.5 rounded-md px-2.5 py-2 text-left transition-colors"
        :class="
          role.role_code === selectedCode
            ? 'bg-secondary shadow-[0_0_0_1px_rgba(0,0,0,0.12)]'
            : 'hover:bg-muted'
        "
        :data-role="role.role_code"
        @click="select(role)"
      >
        <span class="flex items-center justify-between gap-2">
          <span class="truncate text-sm font-medium text-foreground">{{ role.role_name }}</span>
          <span class="shrink-0 rounded px-1.5 py-0.5 text-[11px] leading-none" :class="badgeOf(role).cls">
            {{ badgeOf(role).text }}
          </span>
        </span>
        <span class="flex items-center justify-between text-xs text-muted-foreground">
          <code>{{ role.role_code }}</code>
          <span>{{ role.user_count }} 人</span>
        </span>
      </button>
      <p v-if="loaded && roles.length === 0" class="px-2 py-2 text-sm text-muted-foreground">没有角色</p>
    </aside>

    <!-- 编辑区 -->
    <section v-if="current" class="flex min-h-0 flex-col overflow-y-auto" data-testid="role-editor">
      <div class="flex max-w-3xl flex-col gap-5 px-6 py-5">
        <div class="grid grid-cols-2 gap-4">
          <div class="grid gap-2">
            <Label for="re-name">角色名称</Label>
            <Input id="re-name" v-model="form.roleName" autocomplete="off" />
          </div>
          <div class="grid gap-2">
            <Label>角色编码</Label>
            <div class="flex h-9 items-center rounded-md bg-muted px-2.5 text-sm text-muted-foreground">
              <code class="text-foreground">{{ current.role_code }}</code>
              <span class="ml-1.5">（{{ current.is_builtin ? '内置，' : '' }}不能改）</span>
            </div>
          </div>
          <div class="col-span-2 grid gap-2">
            <Label for="re-desc">说明</Label>
            <Textarea id="re-desc" v-model="form.description" rows="2" placeholder="这个角色负责什么（可选）" />
          </div>
        </div>

        <div class="flex flex-col gap-2">
          <div class="flex items-baseline justify-between">
            <h2 class="text-sm font-semibold text-foreground">菜单权限</h2>
            <span class="text-xs text-muted-foreground">
              {{ current.user_count }} 个用户拥有这个角色；用户的菜单 = 他所有角色的菜单并集
            </span>
          </div>
          <p
            v-if="treeLocked"
            class="flex items-center gap-2 rounded-md bg-secondary px-3 py-2 text-xs text-muted-foreground"
          >
            <LockIcon class="size-3.5" />{{ treeLocked }}
          </p>
          <p
            v-else-if="!session.isAdmin"
            class="flex items-center gap-2 rounded-md bg-info-soft px-3 py-2 text-xs text-foreground"
            data-testid="non-admin-note"
          >
            <LockIcon class="size-3.5 text-info" />你不是系统管理员：系统管理下的菜单（system.*）不能勾选或取消，只能由系统管理员修改。
          </p>
          <MenuTree v-model="form.menuCodes" :menus="menus" :locked="treeLocked" />
          <p class="text-xs text-muted-foreground">
            菜单权限同时约束后端接口：没有 <code>review</code> 的用户调用审核接口会返回 403，不只是把前端入口藏起来。
          </p>
        </div>

        <div class="flex items-center gap-2">
          <Button :disabled="!dirty || saving" data-testid="role-save" @click="save">{{ saving ? '保存中…' : '保存' }}</Button>
          <Button variant="ghost" :disabled="!dirty || saving" @click="resetForm">取消</Button>
          <div class="flex-1" />
          <GuardedButton variant="destructive" :reason="deleteReason" @click="deleteOpen = true">删除角色</GuardedButton>
        </div>
      </div>
    </section>
    <section v-else class="flex items-center justify-center text-sm text-muted-foreground">
      {{ loaded ? '选择左侧的角色' : '加载中…' }}
    </section>
  </div>

  <RoleCreateDialog v-model:open="createOpen" @created="onCreated" />
  <ConfirmDialog
    v-model:open="discardOpen"
    title="放弃未保存的修改？"
    :description="`「${current?.role_name}」的名称、说明或菜单权限还没有保存，继续会丢失这些修改。`"
    confirm-text="放弃修改"
    destructive
    @confirm="confirmDiscard"
  />
  <ConfirmDialog
    v-model:open="deleteOpen"
    title="删除角色"
    :description="`删除「${current?.role_name}」后，拥有这个角色的 ${current?.user_count ?? 0} 个用户会失去它带来的菜单。这个操作不能撤销。`"
    confirm-text="删除"
    destructive
    :loading="deleting"
    @confirm="confirmDelete"
  />
</template>
