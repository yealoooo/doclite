// 生成封面的 Web Worker（M1-12）：TIFF 的解码（UTIF.js，纯 JS）和缩小不占页面的主线程；解码卡死时页面可以直接
// terminate 掉它（lib/cover.ts 的超时）。消息：页面发 `{id, file}`，回 `{id, result}`（result 见 cover-core.ts）。

import { makeCoverCore, offscreenEnv } from './cover-core'

interface WorkerScope {
  onmessage: ((e: MessageEvent<{ id: number; file: Blob }>) => void) | null
  postMessage(message: unknown): void
}

const scope = self as unknown as WorkerScope

scope.onmessage = async (e) => {
  const { id, file } = e.data
  const result = await makeCoverCore(file, offscreenEnv)
  scope.postMessage({ id, result })
}
