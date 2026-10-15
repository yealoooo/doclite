// 标签页是否在前台（M4-5 审查第 1 轮）：`document.visibilityState` 不是 hidden。切到后台时暂停按事件的重取
// （交付后变更、预检），回到前台时补取一次。组件卸载时移除监听。
//
//   const visible = usePageVisible()
//   watch(visible, (v) => { if (v) catchUp() })

import { getCurrentScope, onScopeDispose, ref } from 'vue'

export function usePageVisible() {
  const read = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  const visible = ref(read())
  if (typeof document === 'undefined') return visible
  const onChange = () => {
    visible.value = read()
  }
  document.addEventListener('visibilitychange', onChange)
  if (getCurrentScope()) onScopeDispose(() => document.removeEventListener('visibilitychange', onChange))
  return visible
}
