// Package imagehdr 读 JPEG、PNG、TIFF 的文件头，不解码像素（requirements 5.6"文件头"，硬性规则 11）。
//
// server 上传时用它读格式、宽高、DPI、颜色模式、方向，并拒绝多页 TIFF 和超过像素上限的图；桌面客户端生成封面前
// 也用它读方向，所以两边对同一个文件得出的方向一定一致。上传（server 和客户端上传前的检查）用 [ParseUpload]，另外
// 拒绝导出 PDF 做不了的罕见编码（M7-3，encoding.go）。
//
// 规则：
//   - 格式按文件开头的魔数识别（不看扩展名）：JPEG `FF D8 FF`，PNG 8 字节签名，经典 TIFF `II*\0` / `MM\0*`。
//     其他格式（包括 BigTIFF、GIF、BMP、WebP、PDF）是 [ErrUnsupported]；空文件、比魔数还短但开头对得上的是
//     [ErrInvalid]（截断）。
//   - JPEG：读到第一个 SOF 为止，取 SOF 的宽高、精度、分量数；APP0 的 JFIF 密度；APP1 的 EXIF（只读 IFD0 的
//     Orientation、XResolution、ResolutionUnit）。取第一个以 `Exif\0\0` 开头、结构读得下去的 APP1，读不下去的跳过；
//     EXIF 坏了只当没有 EXIF，不拒绝图片。标记前的 FF 填充可以有，标记段之间的其他多余字节不支持（算读不出来）。
//   - PNG：IHDR 的宽高、位深、颜色类型；IDAT 之前的 pHYs。PNG 不看方向。
//   - TIFF：第一个 IFD 的宽高、BitsPerSample、SamplesPerPixel、PhotometricInterpretation、ExtraSamples、
//     XResolution、ResolutionUnit、Orientation；再沿 IFD 链往后看，有不是缩略图的 IFD 就是多页（[ErrMultiPage]）。
//   - DPI（四舍五入，0 表示未知）：JPEG 先看 JFIF（单位是英寸或厘米、密度不为 0），没有再看 EXIF；PNG 看 pHYs
//     （单位是米时）；TIFF 和 EXIF 看 XResolution 和 ResolutionUnit（没写单位按英寸，单位是"无"算未知）。
//   - 颜色模式（按这个顺序）：调色板（PNG 颜色类型 3、TIFF Photometric=3）→ color；1 位 → bw；单通道（包括带
//     alpha 的）→ gray；其余（包括 CMYK）→ color。JPEG 的分量数不是 1、3、4 时为空。
//   - 方向：JPEG 看 EXIF，TIFF 看 Orientation，PNG 不看；不在 1–8 里的值按 1。方向是 5–8 时交换宽高，
//     [Info] 的宽高是转正后的。
//   - 宽或高为 0 是 [ErrInvalid]；宽或高超过 int32、像素总数超过上限是 [ErrTooLarge]。
//
// 只读需要的字节：先读开头 64 KiB（常见文件的文件头都在里面），用到这段之外的字节时（TIFF 的 IFD 在文件末尾、
// JPEG 的 EXIF 缩略图很大）再按偏移读几个字节。每次读之前按文件长度检查范围，不按文件里声明的长度分配内存；
// 标记段、块、IFD 的个数和每个 IFD 的条目数都有上限，IFD 链成环时报错。所以任何输入都只会返回结果或错误，
// 不会越界、不会死循环。
package imagehdr

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// DefaultMaxPixels 是像素总数上限的默认值（`MAX_IMAGE_PIXELS`，3 亿）。
const DefaultMaxPixels = 300_000_000

// Format 是按魔数识别出的格式。
type Format string

const (
	JPEG Format = "jpeg"
	PNG  Format = "png"
	TIFF Format = "tiff" // 只有经典 TIFF，不支持 BigTIFF。
)

// MIME 是 `file_snapshots.mime_type`，也是传 S3 时的 Content-Type。
func (f Format) MIME() string {
	switch f {
	case JPEG:
		return "image/jpeg"
	case PNG:
		return "image/png"
	case TIFF:
		return "image/tiff"
	}
	return ""
}

// Ext 是 S3 key 的扩展名（硬性规则 7）：jpg、png、tif，不用原文件的扩展名。
func (f Format) Ext() string {
	switch f {
	case JPEG:
		return "jpg"
	case PNG:
		return "png"
	case TIFF:
		return "tif"
	}
	return ""
}

// label 是给人看的格式名。
func (f Format) label() string {
	switch f {
	case JPEG:
		return "JPEG"
	case PNG:
		return "PNG"
	case TIFF:
		return "TIFF"
	}
	return string(f)
}

// ColorMode 是 `file_snapshots.color_mode` 的取值；空串表示不知道（库里存 NULL）。
type ColorMode string

const (
	ColorBW    ColorMode = "bw"    // 黑白（1 位）
	ColorGray  ColorMode = "gray"  // 灰度（单通道，包括带 alpha 的）
	ColorColor ColorMode = "color" // 彩色（包括调色板、CMYK）
)

// Info 是文件头读出并判定之后的结果。
type Info struct {
	Format Format
	// Width、Height 是转正后的宽高：方向为 5–8 时已经和文件里存的交换过。1 到 math.MaxInt32。
	Width, Height int
	// DPI 四舍五入取整；文件里没写、单位是"无"、读不出来时为 0（库里存 NULL，界面显示"未知"）。
	DPI int
	// ColorMode 为空表示不知道（JPEG 的分量数不是 1、3、4 这种少见的情况）。
	ColorMode ColorMode
	// Orientation 是 EXIF / TIFF 的方向，1–8；没写、不认识的值、PNG 都是 1。
	Orientation int
}

// StoredSize 是文件里存的宽高（没转正）。
func (i Info) StoredSize() (width, height int) {
	if i.Orientation >= 5 {
		return i.Height, i.Width
	}
	return i.Width, i.Height
}

// 错误的类别。用 errors.Is 判断；*Error 的 Error() 是给用户看的说明。
var (
	// ErrUnsupported：不是 JPEG、PNG、经典 TIFF（server 返回 415 unsupported_media_type）。
	ErrUnsupported = errors.New("不支持的图片格式")
	// ErrInvalid：空文件、截断、结构读不下去、宽高为 0（400 invalid_image）。
	ErrInvalid = errors.New("图片文件头读不出来")
	// ErrMultiPage：多页 TIFF（400 invalid_image，提示先拆页）。
	ErrMultiPage = errors.New("多页 TIFF")
	// ErrTooLarge：宽高超出 int32、像素总数超过上限（400 invalid_image）。
	ErrTooLarge = errors.New("图片尺寸超过上限")
	// ErrUnsupportedEncoding：格式认得，但编码方式导出 PDF 时既不能原样嵌入、也转换不了（算术编码的 JPEG、
	// JPEG 2000 压缩的 TIFF……，见 encoding.go）。只有 [ParseUpload] 返回它（400 invalid_image，提示换个保存格式重扫）。
	ErrUnsupportedEncoding = errors.New("不支持的图片编码")
)

// Error 是 [Parse]、[ParseUpload] 返回的分类错误。Kind 是上面的错误之一。
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

func newErr(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

func invalidf(format string, args ...any) *Error { return newErr(ErrInvalid, format, args...) }

// isHeaderErr：是不是内容的问题（*Error），而不是读文件本身出错。
func isHeaderErr(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Parse 读文件头并按规则判定。r 是整个文件，size 是它的字节数；maxPixels 是像素总数上限，<= 0 表示不限。
//
// 返回的错误：分类错误（errors.Is 判断 [ErrUnsupported]、[ErrInvalid]、[ErrMultiPage]、[ErrTooLarge]，
// Error() 是给用户看的中文说明）；或者读文件本身出错（r.ReadAt 返回的错误，包了一层），server 按 500 处理。
//
// 用 io.ReaderAt 是因为 TIFF 的 IFD 可以在文件任何位置（很多扫描软件写在文件末尾），按偏移读才能不读整个文件。
// 上传时的临时文件（*os.File）、内存里的字节（bytes.Reader）都直接可用。
func Parse(r io.ReaderAt, size int64, maxPixels int64) (Info, error) {
	info, _, err := parse(r, size, maxPixels)
	return info, err
}

// ParseUpload 是上传时用的 [Parse]：另外拒绝导出 PDF 时做不了的罕见编码（[ErrUnsupportedEncoding]，规则见
// encoding.go）。server 的上传、重扫和桌面客户端上传前的检查都用它，两边对同一个文件的结论一致。
//
// 已经入库的文件不受影响：看图、生成封面、导出仍用 [Parse]。
func ParseUpload(r io.ReaderAt, size int64, maxPixels int64) (Info, error) {
	info, h, err := parse(r, size, maxPixels)
	if err != nil {
		return Info{}, err
	}
	if err := checkEncoding(h); err != nil {
		return Info{}, err
	}
	return info, nil
}

func parse(r io.ReaderAt, size int64, maxPixels int64) (Info, *header, error) {
	s, err := newSource(r, size)
	if err != nil {
		return Info{}, nil, err
	}
	format, err := detect(s)
	if err != nil {
		return Info{}, nil, err
	}
	var h header
	switch format {
	case JPEG:
		h, err = readJPEG(s)
	case PNG:
		h, err = readPNG(s)
	case TIFF:
		h, err = readTIFF(s)
	}
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Kind == ErrInvalid {
			return Info{}, nil, invalidf("%s 文件头读不出来：%s", format.label(), e.Msg)
		}
		return Info{}, nil, err
	}
	h.format = format
	info, err := judge(h, maxPixels)
	if err != nil {
		return Info{}, nil, err
	}
	return info, &h, nil
}

// header 是各格式读出的原始值，由 judge 按规则判定。
type header struct {
	format        Format
	width, height uint32
	palette       bool // 调色板图
	bits          int  // 每个样本的位数
	channels      int  // 颜色通道数，不含 alpha；0 表示不知道
	orientation   int  // 文件里写的方向；0 表示没写
	dpi           int  // 已按格式的规则换算好；0 表示未知
	multiPage     bool // TIFF 第一个 IFD 之后还有不是缩略图的 IFD

	// 给 [ParseUpload] 查编码方式用的：JPEG 的 SOF 标记和分量数（不限于 1、3、4），TIFF 的结构和第一个 IFD。
	sof   byte
	comps int
	tiff  *tiffBlock
	ifd0  ifd
}

func judge(h header, maxPixels int64) (Info, error) {
	if h.multiPage {
		return Info{}, newErr(ErrMultiPage, "这是多页 TIFF，不支持，请先拆成单页再上传")
	}
	w, ht := h.width, h.height
	if w == 0 || ht == 0 {
		return Info{}, invalidf("%s 文件头里的宽高是 %d×%d，文件可能已损坏", h.format.label(), w, ht)
	}
	if w > math.MaxInt32 || ht > math.MaxInt32 {
		return Info{}, newErr(ErrTooLarge, "图片尺寸 %d×%d 超出范围", w, ht)
	}
	if pixels := uint64(w) * uint64(ht); maxPixels > 0 && pixels > uint64(maxPixels) {
		return Info{}, newErr(ErrTooLarge, "图片尺寸 %d×%d 太大：像素总数 %d，超过上限 %d", w, ht, pixels, maxPixels)
	}
	orientation := h.orientation
	if orientation < 1 || orientation > 8 {
		orientation = 1
	}
	width, height := int(w), int(ht)
	if orientation >= 5 {
		width, height = height, width
	}
	return Info{
		Format:      h.format,
		Width:       width,
		Height:      height,
		DPI:         h.dpi,
		ColorMode:   colorMode(h.palette, h.bits, h.channels),
		Orientation: orientation,
	}, nil
}

func colorMode(palette bool, bits, channels int) ColorMode {
	switch {
	case palette:
		return ColorColor
	case bits == 1:
		return ColorBW
	case channels == 0:
		return ""
	case channels == 1:
		return ColorGray
	default:
		return ColorColor
	}
}

const cmPerInch = 2.54

// roundDPI 四舍五入取整；小于 1 或超出 int32 时算未知（0）。
func roundDPI(v float64) int {
	r := math.Round(v)
	if math.IsNaN(r) || r < 1 || r > math.MaxInt32 {
		return 0
	}
	return int(r)
}

// ---------------------------------------------------------------------------
// 按偏移读

// headBytes：开头一次读这么多，常见文件的文件头都在里面。
const headBytes = 64 << 10

// source 是可以按偏移读的文件：记下总长度和开头的一段，每次读之前先检查范围。
type source struct {
	r    io.ReaderAt
	size int64
	head []byte
}

func newSource(r io.ReaderAt, size int64) (*source, error) {
	if size < 0 {
		return nil, fmt.Errorf("imagehdr: 文件大小不能是负数（%d）", size)
	}
	head := make([]byte, min(size, headBytes))
	if err := readFull(r, head, 0); err != nil {
		return nil, err
	}
	return &source{r: r, size: size, head: head}, nil
}

// bytes 返回从 off 起的 n 个字节（不要修改）。超出文件末尾时返回 ErrInvalid（文件不完整），不去读。
func (s *source) bytes(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > s.size || int64(n) > s.size-off {
		return nil, invalidf("要读第 %d 字节起的 %d 个字节，超出了文件末尾（文件共 %d 字节，可能不完整）", off, n, s.size)
	}
	end := off + int64(n)
	if end <= int64(len(s.head)) {
		return s.head[off:end], nil
	}
	buf := make([]byte, n)
	if err := readFull(s.r, buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}

func readFull(r io.ReaderAt, buf []byte, off int64) error {
	if len(buf) == 0 {
		return nil
	}
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	// 读之前按 size 检查过范围，读到末尾只会是文件比给的 size 短（或者读的过程中变短了），按截断算。
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return invalidf("文件比声明的短，不完整")
	}
	return fmt.Errorf("读图片文件失败：%w", err)
}

// ---------------------------------------------------------------------------
// 格式识别

var (
	magicJPEG      = []byte{0xFF, 0xD8, 0xFF}
	magicPNG       = []byte("\x89PNG\r\n\x1a\n")
	magicTIFFLE    = []byte("II*\x00")
	magicTIFFBE    = []byte("MM\x00*")
	magicBigTIFFLE = []byte("II+\x00")
	magicBigTIFFBE = []byte("MM\x00+")
)

var supported = []struct {
	magic  []byte
	format Format
}{
	{magicJPEG, JPEG},
	{magicPNG, PNG},
	{magicTIFFLE, TIFF},
	{magicTIFFBE, TIFF},
}

func hasPrefix(b, prefix []byte) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == string(prefix)
}

func detect(s *source) (Format, error) {
	if s.size == 0 {
		return "", invalidf("文件是空的")
	}
	head := s.head[:min(len(s.head), 8)]
	for _, m := range supported {
		if hasPrefix(head, m.magic) {
			return m.format, nil
		}
	}
	if hasPrefix(head, magicBigTIFFLE) || hasPrefix(head, magicBigTIFFBE) {
		return "", newErr(ErrUnsupported, "不支持 BigTIFF，只接受 JPEG、PNG 和经典 TIFF")
	}
	// 比魔数还短，但开头对得上：是被截断的 JPEG、PNG 或 TIFF。
	for _, m := range supported {
		if len(head) < len(m.magic) && hasPrefix(m.magic, head) {
			return "", invalidf("文件只有 %d 字节，不完整", len(head))
		}
	}
	return "", newErr(ErrUnsupported, "不支持这种格式，只接受 JPEG、PNG 和 TIFF（按文件开头的字节判断，不看扩展名）")
}
