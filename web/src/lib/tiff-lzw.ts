// TIFF 的 LZW 解码（M1-12）：替换 UTIF.js 3.1.0 自带的 `UTIF.decode._decodeLZW`。
//
// UTIF 的实现码宽不封顶：编码器在表满（4094）之后才发 Clear 时（libtiff、Rust 的 tiff/weezl 都容忍这种写法），
// 它会把码宽加到 13 位、之后全部读错，最后抛异常（gen-samples 的 tiff-lzw-rgb.tif 就是这样）。它的字符串表也是
// 固定 2 MB，长串多时会写出界。这里按 TIFF 6.0 的规则重写：高位在前、提前一个码加宽（early change），
// 码宽最多 12 位；表满后不再加新串；坏数据（码超出表、读到数据末尾）就停下，已经解出的保留，不抛出、不死循环。

const CLEAR = 256
const EOI = 257
const MAX_BITS = 12
const TABLE_SIZE = 1 << MAX_BITS

/**
 * 解一个 LZW 条带：从 `data[off]` 开始读，写到 `tgt[toff…]`，写满 `tgt` 或读到 EOI、数据末尾就停。
 * 参数和 UTIF 的 `_decodeLZW(data, off, tgt, toff)` 一样（UTIF 不传条带长度）。
 */
export function decodeTiffLzw(data: Uint8Array, off: number, tgt: Uint8Array, toff: number): void {
  const prefix = new Int16Array(TABLE_SIZE)
  const suffix = new Uint8Array(TABLE_SIZE)
  const length = new Uint16Array(TABLE_SIZE)
  const first = new Uint8Array(TABLE_SIZE)
  for (let i = 0; i < 256; i++) {
    prefix[i] = -1
    suffix[i] = i
    length[i] = 1
    first[i] = i
  }
  const limitBits = data.length * 8
  let bitPos = off * 8
  let bits = 9
  let next = 258
  let prev = -1
  let out = toff
  const outEnd = tgt.length

  /** 把码对应的串写到 out（从后往前沿着前缀链写），返回写了多少。 */
  const emit = (code: number): boolean => {
    const n = length[code]!
    if (out + n > outEnd) {
      // 写不下：写能写的部分然后停。
      let c = code
      let pos = out + n - 1
      while (c >= 0) {
        if (pos < outEnd) tgt[pos] = suffix[c]!
        pos--
        c = prefix[c]!
      }
      out = outEnd
      return false
    }
    let c = code
    let pos = out + n - 1
    while (c >= 0) {
      tgt[pos--] = suffix[c]!
      c = prefix[c]!
    }
    out += n
    return true
  }

  while (bitPos + bits <= limitBits && out < outEnd) {
    // 高位在前读 bits 位（最多跨 3 个字节）。
    const byte = bitPos >>> 3
    const v = ((data[byte]! << 16) | ((data[byte + 1] ?? 0) << 8) | (data[byte + 2] ?? 0)) >>> 0
    const code = (v >>> (24 - (bitPos & 7) - bits)) & ((1 << bits) - 1)
    bitPos += bits

    if (code === EOI) break
    if (code === CLEAR) {
      bits = 9
      next = 258
      prev = -1
      continue
    }
    if (prev < 0) {
      if (code > 255) break
      if (!emit(code)) break
      prev = code
      continue
    }
    let firstChar: number
    if (code < next) {
      firstChar = first[code]!
    } else if (code === next && next < TABLE_SIZE) {
      firstChar = first[prev]!
    } else {
      break
    }
    if (next < TABLE_SIZE) {
      prefix[next] = prev
      suffix[next] = firstChar
      length[next] = length[prev]! + 1
      first[next] = first[prev]!
      next++
    }
    if (!emit(code)) break
    prev = code
    // early change：下一个要分配的码到 2^bits - 1 时加宽，最多 12 位。
    if (next + 1 >= 1 << bits && bits < MAX_BITS) bits++
  }
}
