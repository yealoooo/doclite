// 运行环境：Tauri 壳（WebView2）还是普通浏览器。

declare global {
  interface Window {
    /** Tauri 2 注入的内部对象；有它说明页面运行在壳里。 */
    __TAURI_INTERNALS__?: unknown
  }
}

/** 是否运行在 Tauri 壳里。 */
export function isTauri(): boolean {
  return typeof window !== 'undefined' && window.__TAURI_INTERNALS__ !== undefined
}
