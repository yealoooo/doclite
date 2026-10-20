<script setup lang="ts">
// 二次确认。确认按钮点下后由调用方执行操作，执行期间显示 loading，结束后由调用方关闭。
// 执行中、已经关闭（淡出动画还没放完）时确认按钮不可点，也不再发 confirm：免得同一个操作被提交两次
// （验收 END-02：删除成功后对话框淡出时又点了一次，第二次报"文件已不存在"）。
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'

const open = defineModel<boolean>('open', { required: true })

const props = withDefaults(
  defineProps<{
    title: string
    description?: string
    confirmText?: string
    destructive?: boolean
    loading?: boolean
  }>(),
  { confirmText: '确定' },
)

const emit = defineEmits<{ confirm: [] }>()

function onConfirm() {
  if (props.loading || !open.value) return
  emit('confirm')
}
</script>

<template>
  <AlertDialog v-model:open="open">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ title }}</AlertDialogTitle>
        <AlertDialogDescription v-if="description">{{ description }}</AlertDialogDescription>
      </AlertDialogHeader>
      <slot />
      <AlertDialogFooter>
        <AlertDialogCancel :disabled="loading">取消</AlertDialogCancel>
        <Button
          :variant="destructive ? 'destructive' : 'default'"
          :disabled="loading || !open"
          @click="onConfirm"
        >
          {{ loading ? '处理中…' : confirmText }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
