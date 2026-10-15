// pako 1.x（utif 的依赖，不单独声明在 package.json 里）没有自带类型。cover-core.ts 只在浏览器没有
// DecompressionStream 时按需加载它的 Inflate，这里只声明用到的部分。
declare module 'pako' {
  export class Inflate {
    constructor(options?: { chunkSize?: number })
    /** 每解出一块就调一次；在里面抛错可以中止。 */
    onData: (chunk: Uint8Array) => void
    push(data: Uint8Array, last: boolean): boolean
    err: number
    msg: string
  }
}
