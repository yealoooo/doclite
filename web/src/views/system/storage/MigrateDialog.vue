<script setup lang="ts">
// 存储迁移（M7-2）："迁移到…"对话框：选目标存储（别的、可读写的、不是同一个位置、不在迁出的），说明迁移会做什么，
// 确认后 `POST …/{id}/migrate`。建不了（409、400）时在对话框里显示后端的说明，不弹提示。
// 截图脚本用的属性：data-testid=migrate-dialog（data-source）、migrate-target（data-profile-name、data-selected）、
// migrate-start、migrate-error。
import { CheckIcon, StarIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { errorMessage, HANDLE_ALL } from '@/api/http'
import { startStorageMigration } from '@/api/storage'
import type { StorageMigration, StorageProfileItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { migrationTargets } from '@/lib/storage'

const props = defineProps<{
  source: StorageProfileItem | null
  profiles: StorageProfileItem[]
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ started: [m: StorageMigration] }>()

const targets = computed(() => (props.source ? migrationTargets(props.source, props.profiles) : []))
const targetId = ref<string | null>(null)
const target = computed(() => targets.value.find((t) => t.id === targetId.value) ?? null)
const starting = ref(false)
const error = ref('')

watch(open, (v) => {
  if (!v) return
  error.value = ''
  // 默认选默认存储（最常见：新建存储、设为默认，再把旧存储迁过去）。
  targetId.value = (targets.value.find((t) => t.is_default) ?? targets.value[0])?.id ?? null
})

async function start() {
  const src = props.source
  if (!src || !target.value) return
  starting.value = true
  error.value = ''
  try {
    const m = await startStorageMigration(src.id, target.value.id, HANDLE_ALL)
    open.value = false
    emit('started', m)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    starting.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="flex max-h-[86vh] flex-col gap-4 sm:max-w-xl" data-testid="migrate-dialog" :data-source="props.source?.profile_name">
      <DialogHeader>
        <DialogTitle>把「{{ props.source?.profile_name }}」里的文件迁到…</DialogTitle>
        <DialogDescription>
          迁移在后台进行：把原图和封面复制到目标存储，按 sha256 核对一致后才改快照记的存放位置。
        </DialogDescription>
      </DialogHeader>

      <section class="min-h-0 overflow-y-auto">
        <div class="mb-1.5 text-xs font-medium text-muted-foreground">目标存储</div>
        <div class="flex flex-col gap-2">
          <button
            v-for="t in targets"
            :key="t.id"
            type="button"
            class="rounded-lg border px-3 py-2 text-left text-xs transition-colors hover:border-foreground/30"
            :class="targetId === t.id ? 'border-foreground bg-muted/60 ring-1 ring-foreground/20' : ''"
            data-testid="migrate-target"
            :data-profile-name="t.profile_name"
            :data-selected="targetId === t.id ? 'true' : undefined"
            @click="targetId = t.id"
          >
            <div class="flex items-center gap-1.5 font-medium text-foreground">
              {{ t.profile_name }}
              <span
                v-if="t.is_default"
                class="inline-flex items-center gap-1 rounded-md bg-primary px-1.5 py-0.5 text-2xs leading-none font-normal text-primary-foreground"
              ><StarIcon class="size-2.5" />默认</span>
              <CheckIcon v-if="targetId === t.id" class="ml-auto size-3.5" />
            </div>
            <div class="mt-0.5 break-all text-2xs text-muted-foreground">
              bucket <code class="text-foreground">{{ t.bucket }}</code> · 前缀
              <code class="text-foreground">{{ t.path_prefix || '（无）' }}</code> · {{ t.endpoint }}
            </div>
          </button>
        </div>
        <p
          v-if="target && !target.is_default"
          class="mt-2 rounded-md bg-warn-soft px-3 py-2 text-2xs text-foreground"
          data-testid="migrate-not-default"
        >
          「{{ target.profile_name }}」不是默认存储：迁移期间和之后新上传的文件仍写到默认存储。
        </p>

        <ul class="mt-4 list-disc space-y-1 pl-4 text-xs leading-relaxed text-muted-foreground">
          <li>多个快照共用的对象（复制的文件）只复制一次，引用它的快照一起改。</li>
          <li>核对不一致的（源存储里缺对象、内容和记录的 sha256 不同）不改，迁移照样继续，结束时列出来。</li>
          <li>迁移期间照常上传、审核、导出；新上传的文件写到默认存储。可以随时取消（已迁的不退回）；server 重启后从断点接着做。</li>
          <li><strong class="font-medium text-foreground">不删除源存储里的对象</strong>，删不删由运维决定；导出物不迁（7 天后自动删除）。</li>
          <li>整个系统同一时间只能有一个迁移；迁移执行时占一个后台任务的位置，导出的并发少一个。</li>
          <li>迁完、没有失败的，源存储里就没有文件了：建议先改为只读（旧导出物仍能下载），等导出物到期被清理后再停用。</li>
        </ul>
      </section>

      <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-xs text-destructive" role="alert" data-testid="migrate-error">
        {{ error }}
      </p>

      <DialogFooter>
        <Button variant="outline" :disabled="starting" @click="open = false">取消</Button>
        <Button :disabled="starting || !target" data-testid="migrate-start" @click="start">
          {{ starting ? '处理中…' : '开始迁移' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
