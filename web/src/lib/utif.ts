// UTIF.js（npm 包 utif，MIT，package.json 里锁定 3.1.0）加上本项目的修正（M1-12）。要解 TIFF 的地方都从这里引入，
// 不直接 import 'utif'（M2 看图也用它）：
//   import UTIF, { resetUtifState } from '@/lib/utif'
//
// - LZW 解码换成 lib/tiff-lzw.ts（UTIF 3.1.0 的码宽不封顶，遇到表满后才发 Clear 的写法会解错）。
//   替换不了（utif 换了版本、内部函数改名）时在引入这个模块时就抛错，不静默地用回有问题的实现。
// - 不要把整个文件交给 `UTIF.decode`：它不限制标签的 count，还会递归读 SubIFD、EXIF、MakerNote（可以成环），
//   特制的文件能吃掉几个 GB 内存。用法见 cover-core.ts 的 tiffCover：自己读 IFD、自己拼 IFD 对象，只调 decodeImage。
// - Deflate：UTIF 用 `require('pako')`。打包后 `typeof require` 不是函数，它退回读全局的 `self.pako`（没有），
//   所以 UTIF 自己解不了 Deflate；cover-core.ts 先自己解开（inflateLimited）再交给它。

import UTIF from 'utif'

import { decodeTiffLzw } from './tiff-lzw'

type UtifInternals = typeof UTIF.decode & {
  _decodeLZW?: unknown
  _decodeG3?: { allow2D?: unknown }
}

const internals = UTIF.decode as UtifInternals
if (typeof internals._decodeLZW !== 'function' || typeof internals._decodeG3 !== 'function') {
  throw new Error('utif 的版本不对：找不到 UTIF.decode._decodeLZW / _decodeG3，LZW 补丁没生效（package.json 里要锁定 utif 3.1.0）')
}
internals._decodeLZW = decodeTiffLzw

/**
 * 解一张图之前重置 UTIF 的全局状态。G3 解码器记着"这个文件允不允许二维编码"（`_decodeG3.allow2D`），
 * 原本由 `UTIF.decode` 清掉；不调 `UTIF.decode` 时要自己清，否则上一个文件的结论会带到下一个。
 */
export function resetUtifState() {
  internals._decodeG3!.allow2D = null
}

export default UTIF
