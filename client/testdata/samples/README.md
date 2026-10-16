# 测试样本（客户端）

`server/testdata/samples` 的完整副本（文件、表格都一样；M7-5 起三个模块代码上互不依赖，各带各的样本），客户端的这些测试用：

- `internal/imagehdr` 的单元测试：逐个解析，结果要和下表一致（表格就是期望值，测试会读这个文件）；
- `internal/upload` 的封面测试、上传前检查和上传的集成测试，`internal/imgdecode` 的解码测试。

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

总共约 3.8 MB。

## 怎么来的

前 11 个由上一版的 `gen-samples` 生成（`../bak/doclite-v3/shell/src-tauri/examples/gen-samples.rs`，路径相对于仓库根目录；种子固定为 20261015，同样的参数输出同样的字节），按大小挑了不同的 DPI，让总量在几 MB 以内。bak 搬过目录，`cargo run` 可能因为编译缓存里的旧路径失败，这时直接运行已编译好的 `target/release/examples/gen-samples`（参数相同）：

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

方向 6 的 TIFF、BigTIFF、截断、IFD 成环这类边界情况不放文件，在 imagehdr 的测试里用代码现场拼。

## 改动时

和 `server/testdata/samples` 一起改：增删、替换文件后同步改上面的表格，再跑 `cd client && go test ./internal/imagehdr/`（server 那边跑 `cd server && go test ./internal/platform/imagehdr/`）：测试会核对目录里的每个文件都在表里、解析结果和表里一致。
