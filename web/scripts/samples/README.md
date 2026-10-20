# 截图脚本的样本

截图脚本和 seed 用的真实图片，`scripts/lib/upload-samples.mjs` 的 `sample(name)` 从这里读（每次运行往文件里加一段注释，图片不变、内容不重复，免得被判为重复上传）。M7-5 起 web 自成一体，不再读 server 目录里的文件。

都是从 `server/testdata/samples` 复制的（那里的 README 有格式、尺寸、DPI、方向的清单和生成方法），只留截图脚本要的几个：

| 文件 | 说明 | 谁用 |
|---|---|---|
| `jpeg-rgb.jpg` | 基线 JPEG，彩色，A4 300 DPI | seed、m2-viewer |
| `jpeg-gray.jpg` | 基线 JPEG，灰度，200 DPI | seed、m1-organize、m2-viewer |
| `jpeg-exif6.jpg` | EXIF 方向 6 | seed、m1-organize |
| `png-alpha.png` | RGBA 带透明，50 DPI | seed |
| `png-1bit.png` | 1 位灰度，300 DPI | seed |
| `tiff-none-gray8.tif` | 无压缩，8 位灰度 | seed、m1-organize |
| `tiff-lzw-rgb.tif` | LZW，8 位 RGB | seed、m1-organize、m2-viewer |
| `tiff-g4-1bit.tif` | CCITT G4，1 位 | seed、m1-organize、m2-accept、m2-viewer |

`npm run seed`（`scripts/seed.mjs`，M9-3）把上表 8 个原样（不加注释）传进 DEMO-A 的「格式样本」文件夹。

`encoding/` 是上传时被拒的罕见编码（从 server 的 `internal/platform/imagehdr/testdata/encoding` 复制）：`reject-jpeg-arith.jpg`（算术编码的 JPEG）、`reject-tiff-jpeg2000.tif`（JPEG 2000 压缩的 TIFF），验收清单准备样本时复制到 `reject/`，FILE-21 用来看上传面板里服务器给的拒收提示。

脚本要新的样本时从 server 那边复制过来，补进上表。
