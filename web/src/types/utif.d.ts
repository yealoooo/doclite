// UTIF.js（npm 包 utif，MIT）没有自带类型，这里只声明用到的部分（M1-12 浏览器上传生成封面；M2 看图也用它）。
declare module 'utif' {
  /** 一个 IFD：标签按 `t<编号>` 存成数组（例如 `t256` 是宽），decodeImage 之后有 width、height、data。 */
  export interface IFD {
    [tag: `t${number}`]: number[] | Uint8Array | string[] | undefined
    width?: number
    height?: number
    /** decodeImage 之后：解压后的像素数据（按原来的位深和通道排列）。 */
    data?: Uint8Array
    subIFD?: IFD[]
    exifIFD?: IFD
  }

  interface UTIFStatic {
    /** 读文件头和 IFD 链（不解像素）。 */
    decode(buffer: ArrayBuffer | Uint8Array): IFD[]
    /** 解出一个 IFD 的像素（写到 `ifd.data`、`ifd.width`、`ifd.height`）。 */
    decodeImage(buffer: ArrayBuffer | Uint8Array, ifd: IFD, ifds?: IFD[]): void
    /** 转成 RGBA，每像素 4 字节。 */
    toRGBA8(ifd: IFD): Uint8Array
    /** 把 RGBA 编成无压缩的 TIFF（截图脚本造样本用）。 */
    encodeImage(rgba: ArrayBuffer | Uint8Array, w: number, h: number, metadata?: Record<string, unknown>): ArrayBuffer
  }

  const UTIF: UTIFStatic
  export default UTIF
}
