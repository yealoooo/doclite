// Package tiles 把一张原图切成 DZI 金字塔（requirements 第 6 节"大图切块"，5.7 的查看器）：
//
//   - 最高一级 `ceil(log2(max(宽, 高)))` 是原图转正后的原尺寸，每低一级宽高减半（向上取整），第 0 级 1×1；
//     每级由上一级 2×2 取平均（四舍五入；奇数边上的最后一列、一行按实际有的像素平均），和浏览器里的
//     web/src/lib/viewer/pyramid.ts 一致；
//   - 每块 512×512，和相邻块各重叠 1 像素（块最大 514×514）；
//   - 灰度、彩色是 JPEG（质量 90）；1 位黑白是 PNG（原尺寸一级 1 位，缩小后的各级是 8 位灰度）。
//
// 做法是整张解码（internal/imgdecode，和封面共用），按 shared/imagehdr 读出的方向转正，
// 再逐级减半、编码写盘。整张解码要占内存，所以先按文件头估算（[Plan.Estimate]），调用方据此排队、超出上限就不切；
// 时间由 ctx 控制（读文件、编码每一块之前都看一眼）。这个包不管缓存、来源和并发名额，见 internal/imagecache。
package tiles

// 块的边长、重叠、JPEG 质量（和页面 lib/viewer/pyramid.ts、OpenSeadragon 的设置一致）。
const (
	TileSize    = 512
	Overlap     = 1
	JPEGQuality = 90
)

// MaxLevel 是最高一级的级号：`ceil(log2(max(宽, 高)))`，1×1 的图是 0。
func MaxLevel(width, height int) int {
	m := max(width, height)
	level := 0
	for (1 << level) < m {
		level++
	}
	return level
}

// LevelSize 是第 level 级的宽高：原尺寸除以 2^(maxLevel − level)，向上取整。
func LevelSize(width, height, maxLevel, level int) (int, int) {
	shift := maxLevel - level
	if shift <= 0 {
		return width, height
	}
	div := 1 << shift
	return (width + div - 1) / div, (height + div - 1) / div
}

// Grid 是一级的块数（列、行）。
func Grid(levelWidth, levelHeight int) (cols, rows int) {
	return (levelWidth + TileSize - 1) / TileSize, (levelHeight + TileSize - 1) / TileSize
}

// TileSpan 是第 i 块在长度为 n 的边上覆盖的范围 [lo, hi)（含两侧的重叠）。
func TileSpan(i, n int) (lo, hi int) {
	start := i * TileSize
	return max(0, start-Overlap), min(n, start+TileSize+Overlap)
}

// TileCount 是整个金字塔的块数。
func TileCount(width, height int) int {
	maxLevel := MaxLevel(width, height)
	n := 0
	for l := 0; l <= maxLevel; l++ {
		cols, rows := Grid(LevelSize(width, height, maxLevel, l))
		n += cols * rows
	}
	return n
}
