# 测试样本

常见扫描格式的真实图片文件（都能正常解码），server 的这些测试用：

- `internal/platform/imagehdr` 的单元测试：逐个解析，结果要和下表一致（表格就是期望值，测试会读这个文件）；
- API 测试（上传、多页 TIFF 拒绝）和导出测试（`internal/export/bundle`：原样嵌入、转换）。

三个模块代码上互不依赖（M7-5），各带各的样本：`client/testdata/samples` 是这个目录的完整副本（客户端的 imagehdr、封面、解码测试用，表格一样），`web/scripts/samples` 是截图脚本要的几个（`web/scripts/lib/upload-samples.mjs` 读）。

内容是合成的 A4 页面（页码、标题、模拟文字行、纸张噪点，左上角有一块深色"方向块"），彩色页另有蓝色页眉、黄色高亮和红色印章。

## 清单

宽×高是**转正后**的（方向 5–8 时和文件里存的交换）；DPI 四舍五入；颜色是 `file_snapshots.color_mode` 的取值；"结果"是 `imagehdr.Parse` 应有的结果。

| 文件 | 格式 | 宽×高 | DPI | 颜色 | 方向 | 结果 | 说明 |
|---|---|---|---|---|---|---|---|
| `jpeg-rgb.jpg` | JPEG | 2480×3508 | 300 | color | 1 | 通过 | 基线 JPEG，彩色（YCbCr），JFIF 写 DPI；A4 300 DPI 的正常扫描件 |
| `jpeg-gray.jpg` | JPEG | 1654×2339 | 200 | gray | 1 | 通过 | 基线 JPEG，灰度（1 个分量）；200 DPI 正好不算低 |
| `jpeg-exif6.jpg` | JPEG | 1240×1754 | 150 | color | 6 | 通过 | 像素存成横的 1754×1240，EXIF 方向 6（顺时针转 90° 显示），转正后是竖的 A4 |
| `jpeg-progressive.jpg` | JPEG | 595×842 | 72 | color | 1 | 通过 | 渐进式 JPEG（SOF2） |
| `jpeg-cmyk.jpg` | JPEG | 595×842 | 72 | color | 1 | 通过 | CMYK（4 个分量，Adobe 反相） |
| `png-alpha.png` | PNG | 413×585 | 50 | color | 1 | 通过 | RGBA 带透明，pHYs 写 DPI |
| `png-1bit.png` | PNG | 2480×3508 | 300 | bw | 1 | 通过 | 1 位灰度 |
| `tiff-none-gray8.tif` | TIFF | 595×842 | 72 | gray | 1 | 通过 | 无压缩，8 位灰度（BlackIsZero）；IFD 在文件末尾 |
| `tiff-lzw-rgb.tif` | TIFF | 595×842 | 72 | color | 1 | 通过 | LZW，8 位 RGB；IFD 在文件末尾 |
| `tiff-g4-1bit.tif` | TIFF | 2480×3508 | 300 | bw | 1 | 通过 | CCITT G4，1 位（WhiteIsZero），最常见的黑白扫描件 |
| `tiff-jpeg-ycbcr.tif` | TIFF | 595×842 | 72 | color | 1 | 通过 | JPEG 压缩（Compression=7），YCbCr |
| `tiff-multipage-2p.tif` | TIFF | 827×1169 | 100 | gray | 1 | 多页 | 两页，LZW，8 位灰度（WhiteIsZero）；上传应 400 并提示拆页 |
| `bmp-rgb24.bmp` | BMP | 40×56 | 200 | color | 1 | 通过 | 24 位，BITMAPINFOHEADER，像素/米 7874（M11-7） |
| `bmp-1bit.bmp` | BMP | 40×56 | 300 | bw | 1 | 通过 | 1 位，调色板是黑白两色 |
| `bmp-gray4.bmp` | BMP | 40×56 | 150 | gray | 1 | 通过 | 4 位调色板，16 级灰 |
| `gif-single.gif` | GIF | 40×56 | 0 | color | 1 | 通过 | 单帧，16 色调色板；GIF 没有 DPI |
| `gif-multipage-2f.gif` | GIF | 40×56 | 0 | color | 1 | 多页 | 两帧的动画；上传应 400 并提示保存成单张 |
| `webp-lossy.webp` | WebP | 40×56 | 0 | color | 1 | 通过 | 有损（VP8）；WebP 没有 DPI |
| `webp-lossy-alpha.webp` | WebP | 40×56 | 0 | color | 1 | 通过 | 有损带透明（VP8X + ALPH + VP8） |
| `webp-lossless-alpha.webp` | WebP | 40×56 | 0 | color | 1 | 通过 | 无损带透明（VP8L） |
| `webp-multipage-anim.webp` | WebP | 40×56 | 0 | color | 1 | 多页 | 两帧的动画（VP8X + ANIM + ANMF）；上传应 400 并提示保存成单张 |

总共约 3.8 MB。

## 怎么来的

JPEG、PNG、TIFF 的前 11 个由上一版的 `gen-samples` 生成（`../bak/doclite-v3/shell/src-tauri/examples/gen-samples.rs`，路径相对于仓库根目录；种子固定为 20261015，同样的参数输出同样的字节），按大小挑了不同的 DPI，让总量在几 MB 以内。bak 搬过目录，`cargo run` 可能因为编译缓存里的旧路径失败，这时直接运行已编译好的 `target/release/examples/gen-samples`（参数相同）：

```sh
cd ../bak/doclite-v3/shell/src-tauri
cargo run --release --example gen-samples -- --out /tmp/s300 --seed 20261015 --dpi 300   # jpeg-rgb、png-1bit、tiff-g4-1bit
cargo run --release --example gen-samples -- --out /tmp/s200 --seed 20261015 --dpi 200   # jpeg-gray
cargo run --release --example gen-samples -- --out /tmp/s150 --seed 20261015 --dpi 150   # jpeg-exif6
cargo run --release --example gen-samples -- --out /tmp/s72  --seed 20261015 --dpi 72    # jpeg-progressive、jpeg-cmyk、tiff-none-gray8、tiff-lzw-rgb、tiff-jpeg-ycbcr
cargo run --release --example gen-samples -- --out /tmp/s50  --seed 20261015 --dpi 50    # png-alpha
```

`tiff-multipage-2p.tif`：用 gen-samples 生成两页 100 DPI 的 G4（`--dpi 100 --pages 2`），再用 macOS 自带的 `tiffutil` 合成一个文件（`tiffutil -cat` 会解压成 8 位灰度，所以再 `tiffutil -lzw` 压一下）：

```sh
cargo run --release --example gen-samples -- --out /tmp/mp --seed 20261015 --dpi 100 --pages 2
tiffutil -cat /tmp/mp/tiff-g4-1bit-p1.tif /tmp/mp/tiff-g4-1bit-p2.tif -out /tmp/mp/cat.tif
tiffutil -lzw /tmp/mp/cat.tif -out tiff-multipage-2p.tif
```

BMP、GIF、WebP 的 9 个（M11-7，40×56 的小页面，共约 11 KB）用本机的 ImageMagick 7 和 libwebp（`cwebp`、`img2webp`）生成：

```sh
magick -size 40x56 xc:white -fill '#1f4e9c' -draw 'rectangle 0,0 39,7' -fill black -draw 'rectangle 2,10 9,17' \
  -fill gray30 -draw 'rectangle 12,12 36,13' -draw 'rectangle 4,22 36,23' -draw 'rectangle 4,28 34,29' \
  -draw 'rectangle 4,34 30,35' -fill '#cc2222' -draw 'circle 30,47 30,42' base.png
magick base.png -fill '#2a8a2a' -draw 'rectangle 4,40 16,52' base2.png
magick base.png -alpha set -channel A -fx 'j<8?0.4:(i>28&&j>40?0:1)' +channel alpha.png
magick base.png -type TrueColor -density 200 -units PixelsPerInch BMP3:bmp-rgb24.bmp
magick base.png -colorspace Gray -threshold 60% -type Bilevel -density 300 -units PixelsPerInch BMP3:bmp-1bit.bmp
magick base.png -colorspace Gray -colors 16 -type Palette -density 150 -units PixelsPerInch BMP3:bmp-gray4.bmp
magick base.png -colors 16 gif-single.gif
magick -delay 50 base.png base2.png -loop 0 gif-multipage-2f.gif
cwebp -quiet -q 80 base.png -o webp-lossy.webp
cwebp -quiet -q 80 alpha.png -o webp-lossy-alpha.webp
cwebp -quiet -lossless alpha.png -o webp-lossless-alpha.webp
img2webp -loop 0 -lossy base.png base2.png -o webp-multipage-anim.webp
```

方向 6 的 TIFF、BigTIFF、截断、IFD 成环、少见写法的 BMP 这类边界情况不放文件，在 imagehdr 的测试里用代码现场拼。

## 改动时

增删、替换文件后同步改上面的表格，再跑 `cd server && go test ./internal/platform/imagehdr/`：测试会核对目录里的每个文件都在表里、解析结果和表里一致。客户端、web 的副本要一起改（客户端的副本连表格一起换，再跑 `cd client && go test ./internal/imagehdr/`）。
