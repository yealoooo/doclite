import type { ClassValue } from "clsx"
import { clsx } from "clsx"
import { twMerge } from "tailwind-merge"

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
