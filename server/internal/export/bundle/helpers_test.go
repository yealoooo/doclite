package bundle

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sample 读 server/testdata/samples 里的样本。
func sample(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "samples", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mimeOf(name string) string {
	switch filepath.Ext(name) {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	}
	return "image/tiff"
}

// memSource 是内存里的数据源：按 snapshot_id 给出内容（可以和快照记的不一样，用来测出错）。
type memSource struct {
	files   map[uuid.UUID][]byte
	openErr error
}

func newMemSource() *memSource { return &memSource{files: map[uuid.UUID][]byte{}} }

func (m *memSource) Open(ctx context.Context, p Page) (io.ReadCloser, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	b, ok := m.files[p.SnapshotID]
	if !ok {
		return nil, errors.New("没有这个对象")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memSource) ReadAt(ctx context.Context, p Page, buf []byte, off int64) (int, error) {
	if m.openErr != nil {
		return 0, m.openErr
	}
	return bytes.NewReader(m.files[p.SnapshotID]).ReadAt(buf, off)
}

// add 登记一页：快照记的是 declared 的大小和 sha256，数据源给出 served（nil 时同 declared）。
func (m *memSource) add(pageNo int, declared, served []byte, mime string) Page {
	if served == nil {
		served = declared
	}
	p := Page{
		PageNo:       pageNo,
		FileID:       uuid.New(),
		SnapshotID:   uuid.New(),
		VersionNo:    1,
		SHA256:       sha256.Sum256(declared),
		Size:         int64(len(declared)),
		MIME:         mime,
		UploadName:   "scan.jpg",
		ReviewStatus: "approved",
	}
	m.files[p.SnapshotID] = served
	return p
}

// addSample 登记一个样本文件。
func (m *memSource) addSample(t testing.TB, pageNo int, name string) Page {
	p := m.add(pageNo, sample(t, name), nil, mimeOf(name))
	p.UploadName = name
	return p
}

func testItem(archiveNo string, pages ...Page) Item {
	return Item{ArchiveNo: archiveNo, Title: "题名 " + archiveNo, Status: "locked", Pages: pages}
}

// generate 生成导出物并检查临时目录已经清空。
func generate(t *testing.T, job *Job, src PageSource, opts Options) ([]byte, Summary, error) {
	t.Helper()
	if opts.TmpDir == "" {
		opts.TmpDir = t.TempDir()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Date(2026, 10, 16, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	}
	var buf bytes.Buffer
	sum, err := Generate(context.Background(), job, src, &buf, opts, nil)
	assertEmptyDir(t, opts.TmpDir)
	return buf.Bytes(), sum, err
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("临时目录没清空：%v", left)
	}
}

// pdfOf 生成只有一件的 PDF，返回解析后的 PDF。
func pdfOf(t *testing.T, opts Options, src *memSource, pages ...Page) *pdfDoc {
	t.Helper()
	job := &Job{Format: FormatPDF, Items: []Item{testItem("A-2025-0001", pages...)}}
	z, _, err := generate(t, job, src, opts)
	if err != nil {
		t.Fatal(err)
	}
	return parsePDF(t, zipFile(t, z, "A-2025-0001.pdf"))
}

func zipFile(t *testing.T, z []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("zip 里没有 %s", name)
	return nil
}

// ---------------------------------------------------------------------------
// 独立于被测代码的小工具：取 TIFF 的条带、PNG 的 IDAT。

// tiffTag 读 TIFF 第一个 IFD 里一个标签的原始字节（按类型的长度）和类型；没有时返回 nil。
func tiffTag(b []byte, tag uint16) (raw []byte, typ uint16, bo binary.ByteOrder) {
	bo = binary.LittleEndian
	if string(b[:2]) == "MM" {
		bo = binary.BigEndian
	}
	ifd := bo.Uint32(b[4:])
	n := int(bo.Uint16(b[ifd:]))
	for i := range n {
		e := b[int(ifd)+2+12*i:]
		if bo.Uint16(e) != tag {
			continue
		}
		typ = bo.Uint16(e[2:])
		size := map[uint16]int{1: 1, 3: 2, 4: 4, 7: 1}[typ]
		count := int(bo.Uint32(e[4:]))
		if count*size <= 4 {
			return e[8 : 8+count*size], typ, bo
		}
		off := bo.Uint32(e[8:])
		return b[off : int(off)+count*size], typ, bo
	}
	return nil, 0, bo
}

// tiffUints 读一个整数数组标签（SHORT、LONG）。
func tiffUints(b []byte, tag uint16) []uint32 {
	raw, typ, bo := tiffTag(b, tag)
	var out []uint32
	for i := 0; i < len(raw); {
		if typ == 3 {
			out = append(out, uint32(bo.Uint16(raw[i:])))
			i += 2
		} else {
			out = append(out, bo.Uint32(raw[i:]))
			i += 4
		}
	}
	return out
}

func tiffStrips(t *testing.T, b []byte) (offsets, counts []uint32) {
	t.Helper()
	offsets, counts = tiffUints(b, 273), tiffUints(b, 279)
	if offsets == nil {
		offsets, counts = tiffUints(b, 324), tiffUints(b, 325)
	}
	if len(offsets) == 0 || len(offsets) != len(counts) {
		t.Fatalf("读不出条带表")
	}
	return offsets, counts
}

// fixture 读这个包的 testdata 里的文件。
//
// tiff-jpeg-*.tif 是从样本 tiff-lzw-rgb.tif 左上角裁下 400×421（100 DPI）后用 libtiff 的 tiffcp 生成的：
//
//	tiffcp -c jpeg:60 -r 64 crop.tif tiff-jpeg-ycbcr-tables.tif         # YCbCr、7 个条带、JPEGTables
//	tiffcp -c jpeg:60:r -r 128 crop.tif tiff-jpeg-rgb.tif               # RGB、4 个条带、JPEGTables
//	tiffcp -c jpeg:60 -t -w 128 -l 128 crop.tif tiff-jpeg-tiles.tif     # YCbCr、128×128 分块（右、下边补齐）
//	tiffcp -p separate -c jpeg:60:r -r 16 small.tif tiff-jpeg-planar.tif # 64×48、分平面（不支持）
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pngIDAT(b []byte) []byte {
	var out []byte
	for p := 8; p < len(b); {
		n := int(binary.BigEndian.Uint32(b[p:]))
		if string(b[p+4:p+8]) == "IDAT" {
			out = append(out, b[p+8:p+8+n]...)
		}
		p += 12 + n
	}
	return out
}

// ---------------------------------------------------------------------------
// 现场拼的 TIFF（小端，8 位样本）：用来测分平面、预测器、横纵 DPI 不同。

type synthTIFF struct {
	w, h, spp   int
	photometric int
	compression int // 1 或 8（Deflate）
	planar      int
	predictor   int
	rps         int
	xres, yres  uint32
	extra       []uint16
	pix         []byte // 每像素 spp 个 8 位样本，按行
}

func (s synthTIFF) encode() []byte {
	planes, ps := 1, s.spp
	if s.planar == 2 {
		planes, ps = s.spp, 1
	}
	var data bytes.Buffer
	data.Write(make([]byte, 8))
	var offsets, counts []uint32
	for p := range planes {
		for y0 := 0; y0 < s.h; y0 += s.rps {
			var strip []byte
			for y := y0; y < min(s.h, y0+s.rps); y++ {
				row := make([]byte, s.w*ps)
				for x := range s.w {
					for c := range ps {
						sc := c
						if planes > 1 {
							sc = p
						}
						row[x*ps+c] = s.pix[(y*s.w+x)*s.spp+sc]
					}
				}
				if s.predictor == 2 {
					for i := len(row) - 1; i >= ps; i-- {
						row[i] -= row[i-ps]
					}
				}
				strip = append(strip, row...)
			}
			if s.compression == 8 {
				var z bytes.Buffer
				zw := zlib.NewWriter(&z)
				zw.Write(strip)
				zw.Close()
				strip = z.Bytes()
			}
			offsets = append(offsets, uint32(data.Len()))
			counts = append(counts, uint32(len(strip)))
			data.Write(strip)
		}
	}
	bits := make([]uint32, s.spp)
	for i := range bits {
		bits[i] = 8
	}
	entries := []tiffEntryT{
		{256, 4, []uint32{uint32(s.w)}},
		{257, 4, []uint32{uint32(s.h)}},
		{258, 3, bits},
		{259, 3, []uint32{uint32(s.compression)}},
		{262, 3, []uint32{uint32(s.photometric)}},
		{273, 4, offsets},
		{277, 3, []uint32{uint32(s.spp)}},
		{278, 4, []uint32{uint32(s.rps)}},
		{279, 4, counts},
		{282, 5, []uint32{s.xres, 1}},
		{283, 5, []uint32{s.yres, 1}},
		{284, 3, []uint32{uint32(s.planar)}},
		{296, 3, []uint32{2}},
	}
	if s.predictor != 0 {
		entries = append(entries, tiffEntryT{317, 3, []uint32{uint32(s.predictor)}})
	}
	if len(s.extra) > 0 {
		var v []uint32
		for _, e := range s.extra {
			v = append(v, uint32(e))
		}
		entries = append(entries, tiffEntryT{338, 3, v})
	}
	return buildTIFF(data.Bytes(), entries)
}

type tiffEntryT struct {
	tag, typ uint16
	vals     []uint32
}

// buildTIFF 拼一个小端的经典 TIFF：data 的前 8 字节留给文件头，IFD 接在 data 后面。
func buildTIFF(data []byte, entries []tiffEntryT) []byte {
	sort.Slice(entries, func(i, j int) bool { return entries[i].tag < entries[j].tag })
	le := binary.LittleEndian
	ifdOff := uint32(len(data))
	extraOff := ifdOff + 2 + uint32(len(entries))*12 + 4
	var ifd, extra bytes.Buffer
	binary.Write(&ifd, le, uint16(len(entries)))
	for _, e := range entries {
		var raw []byte
		for _, v := range e.vals {
			switch e.typ {
			case 3:
				raw = le.AppendUint16(raw, uint16(v))
			default:
				raw = le.AppendUint32(raw, v)
			}
		}
		count := uint32(len(e.vals))
		if e.typ == 5 {
			count = 1
		}
		binary.Write(&ifd, le, e.tag)
		binary.Write(&ifd, le, e.typ)
		binary.Write(&ifd, le, count)
		if len(raw) <= 4 {
			ifd.Write(append(raw, make([]byte, 4-len(raw))...))
		} else {
			binary.Write(&ifd, le, extraOff+uint32(extra.Len()))
			extra.Write(raw)
		}
	}
	ifd.Write([]byte{0, 0, 0, 0})
	out := bytes.Clone(data)
	copy(out, []byte("II*\x00"))
	le.PutUint32(out[4:], ifdOff)
	out = append(out, ifd.Bytes()...)
	return append(out, extra.Bytes()...)
}
