import type { ClassValue } from "clsx"
import { clsx } from "clsx"
import { extendTailwindMerge } from "tailwind-merge"

/** style.css 里加的字号（text-2xs、text-compact）、阴影（shadow-outline）告诉 tailwind-merge，免得被当成颜色合并掉。 */
const twMerge = extendTailwindMerge({
  extend: { theme: { text: ["2xs", "compact"], shadow: ["outline"] } },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** localStorage 里的开关（'1'、'0'），给 @vueuse/core 的 useLocalStorage 用。 */
export const FLAG_SERIALIZER = {
  read: (v: string) => v === '1',
  write: (v: boolean) => (v ? '1' : '0'),
}

/** 按 `size` 个一批切开（批量接口一次有上限）。 */
export function chunk<T>(list: readonly T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < list.length; i += size) out.push(list.slice(i, i + size))
  return out
}
