package bundle

import (
	"context"
	"encoding/binary"
	"errors"
	"io"

	"doclite/server/internal/platform/imagehdr"
)

// pagePlan 是一页怎么写进 PDF：原样嵌入的几个图像对象，或者转换。
type pagePlan struct {
	format imagehdr.Format
	// width、height 是原图里存的像素（没转正）；orientation 是 EXIF / TIFF 方向（1–8）。
	width, height int
	orientation   int
	// dpiX、dpiY 是文件里写的横纵 DPI（没转正，0 表示没写或读不出来）。
	dpiX, dpiY float64
	// pageW、pageH 是转正后的页面尺寸（磅）。
	pageW, pageH float64

	images  []imageObj  // 原样嵌入
	convert *conversion // 不为 nil 时转换
}

// colorSpace 是图像的颜色空间：DeviceGray / DeviceRGB / DeviceCMYK，或者 Indexed（基于 DeviceRGB，查找表 lookup）。
type colorSpace struct {
	name   string
	lookup []byte // Indexed 时每项 3 字节
}

var (
	csGray = colorSpace{name: "DeviceGray"}
	csRGB  = colorSpace{name: "DeviceRGB"}
	csCMYK = colorSpace{name: "DeviceCMYK"}
)

func indexed(lookup []byte) colorSpace { return colorSpace{name: "Indexed", lookup: lookup} }

// comps 是每个像素的样本数。
func (c colorSpace) comps() int {
	switch c.name {
	case "DeviceRGB":
		return 3
	case "DeviceCMYK":
		return 4
	}
	return 1
}

// imageObj 是一个原样嵌入的图像对象：数据是原图里的几段字节（前后可以补几个字节）。
type imageObj struct {
	width, height int
	cs            colorSpace
	bpc           int
	filter        string // DCTDecode、FlateDecode、LZWDecode、RunLengthDecode、CCITTFaxDecode，空串是不压缩
	parms         string // DecodeParms 字典的内容
	decode        string // Decode 数组的内容
	prefix        []byte // 数据前面补的字节（TIFF 的 JPEG 条带：SOI 加 JPEGTables 里的表）
	ranges        []byteRange
	reverseBits   bool   // TIFF FillOrder=2：每个字节按位反转
	trailer       []byte // 数据后面补的字节（RunLengthDecode 的 EOD）
	// col0、row0 是它在原图里的左上角（条带、分块）。分块在右边、下边超出原图的部分落在页面外，被 MediaBox 裁掉。
	col0, row0 int
}

type byteRange struct {
	off, n int64
	pngCRC bool // 这一段是 PNG 的 IDAT 块的内容，后面 4 字节是 CRC，拷贝时核对
}

// conversion 是放不进 PDF、要无损转换的页。
type conversion struct {
	reason string // 为什么转换（调试、日志用）
	memory int64  // 解码要的内存（估计）
	decode func(ctx context.Context) (*raster, error)
}

// raster 是解码后的图：颜色空间和位深尽量和原图一样（像素不变），透明度单独一个通道（写成 SMask）。
type raster struct {
	w, h     int
	cs       colorSpace
	bpc      int
	decode   string // Decode 数组的内容（WhiteIsZero 时 "1 0"）
	alphaBPC int    // 0 表示没有透明度
	matte    bool   // 颜色是预乘过 alpha 的（TIFF ExtraSamples=1），SMask 加 /Matte
	// fill 取第 y 行：color 是 w × 分量数个样本值，alpha 是 w 个透明度；哪个为 nil 就不取。
	fill func(y int, color, alpha []uint32)
}

// analyze 读文件头，定下这一页怎么写进 PDF。ra 是整个原图（导出时是临时文件，Inspect 时是按范围读的数据源）。
// inspect 为 true 时只做只看文件头就能做的检查（不扫描 PackBits 条带、PNG 只看 IDAT 之前的块和文件末尾）。
func analyze(ra io.ReaderAt, page *Page, opts Options, inspect bool) (*pagePlan, error) {
	head := make([]byte, min(page.Size, 8))
	if err := readFull(ra, head, 0); err != nil {
		return nil, err
	}
	format, err := checkMagic(head, page.MIME)
	if err != nil {
		return nil, err
	}
	// 上传时已经按像素上限查过，这里不再限制像素数（转换另有内存上限）。
	info, err := imagehdr.Parse(ra, page.Size, 0)
	if err != nil {
		return nil, err
	}
	var plan *pagePlan
	switch format {
	case imagehdr.JPEG:
		plan, err = analyzeJPEG(ra, page.Size)
	case imagehdr.PNG:
		plan, err = analyzePNG(ra, page.Size, opts, inspect)
	case imagehdr.TIFF:
		plan, err = analyzeTIFF(ra, page.Size, opts, inspect)
	}
	if err != nil {
		return nil, err
	}
	plan.format = format
	if w, h := info.StoredSize(); w != plan.width || h != plan.height {
		return nil, corruptf("文件头里的宽高前后不一致（%d×%d 和 %d×%d）", w, h, plan.width, plan.height)
	}
	plan.orientation = info.Orientation
	plan.pageW, plan.pageH = pageSize(plan.width, plan.height, plan.dpiX, plan.dpiY, plan.orientation)
	if c := plan.convert; c != nil && c.memory > opts.ConvertMemoryLimit {
		return nil, tooLargef("这一页要转换（%s），需要约 %.1f MiB 内存，超过上限 %.1f MiB",
			c.reason, float64(c.memory)/(1<<20), float64(opts.ConvertMemoryLimit)/(1<<20))
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// 页面尺寸

// 单个方向的 DPI 在这个范围内算合理（requirements 5.11）。
const (
	minDPI      = 100
	maxDPI      = 2400
	minOtherDPI = 50  // 一个方向合理时，另一个不低于这个也照用（传真 204×98 不压扁）
	defaultDPI  = 300 // 两个方向都不合理时
)

// chooseDPI 按规则定横纵 DPI：两个都合理各用各的；一个合理、另一个在 50–2400 也各用各的；否则都用合理的那个；
// 都不合理（包括没写）按 300。
func chooseDPI(x, y float64) (float64, float64) {
	ok := func(v float64) bool { return v >= minDPI && v <= maxDPI }
	usable := func(v float64) bool { return v >= minOtherDPI && v <= maxDPI }
	switch {
	case ok(x) && usable(y), ok(y) && usable(x):
		return x, y
	case ok(x):
		return x, x
	case ok(y):
		return y, y
	}
	return defaultDPI, defaultDPI
}

// PDF 页面边长的范围（磅，PDF 1.7 的实现限制）。超出时两个方向一起缩放，长宽比不变。
const (
	minPageSide = 3
	maxPageSide = 14400
)

// pageSize 是转正后的页面尺寸（磅）：像素 × 72 / DPI，横纵分别算；方向 5–8 时页面的宽是原图的高。
func pageSize(w, h int, dpiX, dpiY float64, orientation int) (pw, ph float64) {
	dx, dy := chooseDPI(dpiX, dpiY)
	pw, ph = float64(w)*72/dx, float64(h)*72/dy
	if orientation >= 5 {
		pw, ph = ph, pw
	}
	if m := max(pw, ph); m > maxPageSide {
		pw, ph = pw*maxPageSide/m, ph*maxPageSide/m
	} else if m := min(pw, ph); m < minPageSide {
		s := min(minPageSide/m, maxPageSide/max(pw, ph))
		pw, ph = pw*s, ph*s
	}
	return pw, ph
}

// ---------------------------------------------------------------------------
// 小工具

// readFull 从 off 读满 b；读到文件末尾算文件结构不对（偏移越界）。
func readFull(ra io.ReaderAt, b []byte, off int64) error {
	if len(b) == 0 {
		return nil
	}
	n, err := ra.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return corruptf("要读第 %d 字节起的 %d 个字节，超出了文件末尾", off, len(b))
	}
	return err
}

func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
