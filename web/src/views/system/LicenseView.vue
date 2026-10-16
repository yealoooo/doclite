<script setup lang="ts">
// 系统管理 · 许可证（M7-1，requirements 第 11 节）。
// - 状态：开发版（没编进公钥，不检查）、没导入、有效、快到期（30 天内）、已到期、无效；许可证的公司、版本、席位、
//   签发和到期时间，当前在线的登录会话数（同一账号几处登录算几个，30 分钟内有活动的才算）。
// - install_id：签发许可证要用，可复制（内网 http 不是安全上下文，复制用 execCommand 兜底）。
// - 导入：粘贴 .lic 的内容或选择 .lic 文件；签名不对、install_id 不符、已过期在原位置说明。导入后立即生效，
//   并刷新会话（顶部的到期提醒条跟着变）。
import { CopyIcon, FileUpIcon, KeyRoundIcon, ShieldCheckIcon } from '@lucide/vue'
import { computed, onMounted, ref, useTemplateRef } from 'vue'
import { toast } from 'vue-sonner'

import { errorMessage, isApiError } from '@/api/http'
import { getLicense, importLicense, LICENSE_IMPORT_CODES } from '@/api/license'
import type { LicenseState, LicenseStatusCode } from '@/api/types'
import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { formatDateTime } from '@/lib/format'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const state = ref<LicenseState | null>(null)
const loadError = ref('')

async function load() {
  try {
    state.value = await getLicense()
    loadError.value = ''
  } catch (err) {
    loadError.value = errorMessage(err)
  }
}
onMounted(load)

const STATUS_LABEL: Record<LicenseStatusCode, string> = {
  dev: '开发版',
  missing: '未导入',
  valid: '有效',
  expiring: '即将到期',
  expired: '已到期',
  invalid: '无效',
}

const STATUS_CLASS: Record<LicenseStatusCode, string> = {
  dev: 'bg-secondary text-muted-foreground',
  missing: 'bg-destructive-soft text-destructive',
  valid: 'bg-ok-soft text-ok',
  expiring: 'bg-warn-soft text-warn',
  expired: 'bg-destructive-soft text-destructive',
  invalid: 'bg-destructive-soft text-destructive',
}

const EDITION_LABEL: Record<string, string> = { trial: '试用版', standard: '标准版', enterprise: '企业版' }

const lic = computed(() => state.value?.license ?? null)
const dateOf = (v: string | null | undefined) => formatDateTime(v).slice(0, 10)

/** 状态的标题和说明。 */
const summary = computed(() => {
  const s = state.value
  if (!s) return { title: '', text: '' }
  switch (s.status) {
    case 'dev':
      return {
        title: '开发版，不检查许可证',
        text: '这个版本没有编进许可证公钥：谁都能登录，不限人数，也不用导入许可证。正式版才检查许可证。',
      }
    case 'missing':
      return {
        title: '还没有导入许可证',
        text: '现在只有管理员能登录。把下面的 install_id 发给供应商，拿到 .lic 文件后在下面导入，导入后立即生效。',
      }
    case 'invalid':
      return {
        title: '许可证无效',
        text: `${s.invalid_reason ?? '导入的许可证验不过'}。现在只有管理员能登录，请重新导入许可证。`,
      }
    case 'expired':
      return {
        title: `许可证已于 ${dateOf(lic.value?.expires_at)} 到期`,
        text: '现在除管理员以外的人都不能登录，已登录的也会被退回登录页；数据不受影响。导入新的许可证后立即恢复。',
      }
    case 'expiring':
      return {
        title: `许可证将在 ${s.days_left ?? 0} 天后到期`,
        text: '到期后除管理员以外的人都不能登录。请尽快向供应商续期，拿到新的 .lic 文件后在下面导入。',
      }
    default:
      return { title: '许可证有效', text: `有效期到 ${dateOf(lic.value?.expires_at)}，还有 ${s.days_left ?? 0} 天。` }
  }
})

// ---------- 复制 install_id ----------

async function copyText(text: string): Promise<boolean> {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 落到下面的兜底。
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  return ok
}

async function copyInstallId() {
  if (!state.value) return
  if (await copyText(state.value.install_id)) toast.success('已复制 install_id')
  else toast.error('复制失败，请手动选中复制')
}

// ---------- 导入 ----------

const text = ref('')
const fileName = ref('')
const importing = ref(false)
const importError = ref('')
const importErrorCode = ref('')
const fileInput = useTemplateRef<HTMLInputElement>('fileInput')

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > 64 * 1024) {
    importError.value = '这个文件太大，不像许可证文件（.lic 一般只有 1 KB 左右）'
    importErrorCode.value = 'too_large'
    return
  }
  text.value = await file.text()
  fileName.value = file.name
  importError.value = ''
  importErrorCode.value = ''
}

async function submit() {
  if (importing.value || !text.value.trim()) return
  importing.value = true
  importError.value = ''
  importErrorCode.value = ''
  try {
    const replaced = state.value?.license !== null && state.value?.license !== undefined
    state.value = await importLicense(text.value, LICENSE_IMPORT_CODES)
    text.value = ''
    fileName.value = ''
    toast.success(replaced ? '已替换许可证，立即生效' : '许可证已导入，立即生效')
    // 顶部的到期提醒条来自 GET /api/me。
    session.refreshSession().catch(() => {})
  } catch (err) {
    if (isApiError(err)) {
      importErrorCode.value = err.code
      importError.value = importErrorText(err.code, err.message)
    }
  } finally {
    importing.value = false
  }
}

function importErrorText(code: string, message: string): string {
  switch (code) {
    case 'invalid_license':
      return `导入失败：${message}`
    case 'license_mismatch':
      return '导入失败：这份许可证是为另一套系统签发的（install_id 不符）。请把本页的 install_id 发给供应商重新签发。'
    case 'license_file_expired':
      return `导入失败：${message}`
    default:
      return message
  }
}
</script>

<template>
  <PageHeader>
    <template #meta>
      <span
        v-if="state"
        class="rounded-md px-2 py-0.5 text-xs"
        :class="STATUS_CLASS[state.status]"
        data-testid="license-status"
        :data-status="state.status"
      >{{ STATUS_LABEL[state.status] }}</span>
    </template>
  </PageHeader>

  <section class="min-h-0 flex-1 overflow-y-auto px-6 py-5" data-testid="license-page">
    <div class="flex max-w-3xl flex-col gap-4">
      <div
        v-if="loadError"
        class="flex items-center gap-3 rounded-lg bg-destructive-soft px-4 py-3 text-sm text-destructive"
        role="alert"
      >
        <p class="flex-1">许可证信息加载失败：{{ loadError }}</p>
        <Button size="sm" variant="outline" @click="load">重试</Button>
      </div>

      <template v-if="state">
        <!-- 状态和许可证内容 -->
        <article class="flex flex-col gap-3 rounded-lg px-4 py-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
          <div class="flex items-start gap-3">
            <ShieldCheckIcon class="mt-0.5 size-5 shrink-0 text-muted-foreground" />
            <div class="flex min-w-0 flex-col gap-1">
              <h2 class="font-semibold text-foreground" data-testid="license-title">{{ summary.title }}</h2>
              <p class="text-sm text-muted-foreground">{{ summary.text }}</p>
            </div>
          </div>
          <dl class="grid grid-cols-[7rem_minmax(0,1fr)] gap-x-4 gap-y-2 border-t pt-3 text-sm">
            <template v-if="lic">
              <dt class="text-muted-foreground">公司</dt>
              <dd class="text-foreground" data-testid="license-company">{{ lic.company }}</dd>
              <dt class="text-muted-foreground">版本</dt>
              <dd class="text-foreground">{{ EDITION_LABEL[lic.edition] ?? lic.edition }}</dd>
              <dt class="text-muted-foreground">席位</dt>
              <dd class="text-foreground">{{ lic.max_users }} 个（同时在线的登录数；同一账号几处登录算几个）</dd>
            </template>
            <dt class="text-muted-foreground">当前在线</dt>
            <dd class="text-foreground" data-testid="license-online">
              {{ state.online_sessions }} 个登录<span v-if="lic" class="text-muted-foreground"> / 席位 {{ lic.max_users }}</span>
              <span class="text-muted-foreground">（30 分钟内有活动的才算，含管理员）</span>
            </dd>
            <template v-if="lic">
              <dt class="text-muted-foreground">签发时间</dt>
              <dd class="text-foreground">{{ formatDateTime(lic.issued_at) }}</dd>
              <dt class="text-muted-foreground">到期时间</dt>
              <dd class="text-foreground" data-testid="license-expires">
                {{ formatDateTime(lic.expires_at) }}
                <span v-if="state.days_left !== null" class="text-muted-foreground">（还有 {{ state.days_left }} 天）</span>
              </dd>
              <dt class="text-muted-foreground">许可证编号</dt>
              <dd class="font-mono text-xs leading-5 break-all text-muted-foreground">{{ lic.license_id }}</dd>
              <dt class="text-muted-foreground">导入</dt>
              <dd class="text-muted-foreground">
                {{ formatDateTime(state.imported_at) }}<span v-if="state.imported_by"> · {{ state.imported_by }}</span>
              </dd>
            </template>
          </dl>
        </article>

        <!-- install_id -->
        <article class="flex flex-col gap-2 rounded-lg px-4 py-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
          <div class="flex items-center gap-2">
            <KeyRoundIcon class="size-4 shrink-0 text-muted-foreground" />
            <h2 class="text-sm font-semibold text-foreground">install_id</h2>
            <span class="text-xs text-muted-foreground">系统首次启动时生成，以后不变；签发许可证时要用到它</span>
          </div>
          <div class="flex items-center gap-2">
            <code
              class="min-w-0 flex-1 rounded-md bg-muted px-3 py-2 font-mono text-xs break-all text-foreground select-all"
              data-testid="install-id"
            >{{ state.install_id }}</code>
            <Button size="sm" variant="outline" data-testid="copy-install-id" @click="copyInstallId">
              <CopyIcon />复制
            </Button>
          </div>
        </article>

        <!-- 导入 -->
        <article
          v-if="state.status !== 'dev'"
          class="flex flex-col gap-3 rounded-lg px-4 py-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]"
          data-testid="license-import"
        >
          <div class="flex items-center gap-2">
            <FileUpIcon class="size-4 shrink-0 text-muted-foreground" />
            <h2 class="text-sm font-semibold text-foreground">{{ lic ? '替换许可证' : '导入许可证' }}</h2>
            <span class="text-xs text-muted-foreground">粘贴 .lic 文件的内容，或选择 .lic 文件</span>
          </div>
          <Textarea
            v-model="text"
            class="min-h-36 font-mono text-xs"
            placeholder="-----BEGIN DOCLITE LICENSE-----&#10;…&#10;-----END DOCLITE LICENSE-----"
            spellcheck="false"
            data-testid="license-text"
            @input="importError = ''"
          />
          <p
            v-if="importError"
            class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
            role="alert"
            data-testid="license-import-error"
            :data-code="importErrorCode"
          >
            {{ importError }}
          </p>
          <div class="flex items-center gap-2">
            <input ref="fileInput" type="file" accept=".lic,text/plain" class="hidden" @change="onFile" />
            <Button size="sm" variant="outline" @click="fileInput?.click()"><FileUpIcon />选择 .lic 文件</Button>
            <span v-if="fileName" class="truncate text-xs text-muted-foreground">{{ fileName }}</span>
            <div class="flex-1" />
            <Button size="sm" :disabled="importing || !text.trim()" data-testid="license-submit" @click="submit">
              {{ importing ? '导入中…' : lic ? '替换' : '导入' }}
            </Button>
          </div>
        </article>
      </template>
    </div>
  </section>
</template>
