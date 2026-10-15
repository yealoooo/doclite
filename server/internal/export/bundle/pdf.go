package bundle

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math/bits"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// 最小的 PDF 1.7 写入器：对象按顺序写，偏移相对于 PDF 开头（顺序写就能算出来），最后写交叉引用表。
// 一页一张图铺满；图像数据从原图按块拷贝，不经过内存里的整张图。
//
// 对象编号：1 Catalog、2 Pages、3 Info 先占好，最后写；其余按用到的顺序分配。长度事先不知道的流（转换后 Flate
// 压缩的）用间接的 /Length，写完流再写长度对象。

const (
	objCatalog = 1
	objPages   = 2
	objInfo    = 3
)

// maxXrefOffset：经典交叉引用表的偏移是 10 位数字，一件的 PDF 超过约 9.3 GiB 时放不下（可简化：不写交叉引用流）。
const maxXrefOffset = 9_999_999_999

type pdfWriter struct {
	w        io.Writer
	off      int64
	offsets  []int64 // 下标是对象编号；-1 表示分配了还没写
	item     *Item
	now      time.Time
	pages    []int
	palettes map[string]int // 调色板（Indexed 的查找表）→ 对象编号；同一个 PDF 里一样的调色板只写一次
	id       hash.Hash      // trailer 的 ID：档号和各页的 snapshot_id、sha256
}

func newPDFWriter(w io.Writer, item *Item, now time.Time) *pdfWriter {
	p := &pdfWriter{
		w:        w,
		offsets:  []int64{0, -1, -1, -1},
		item:     item,
		now:      now,
		palettes: map[string]int{},
		id:       sha256.New(),
	}
	p.id.Write([]byte(item.ArchiveNo))
	return p
}

func (p *pdfWriter) write(b []byte) error {
	n, err := p.w.Write(b)
	p.off += int64(n)
	return err
}

func (p *pdfWriter) printf(format string, args ...any) error {
	return p.write(fmt.Appendf(nil, format, args...))
}

func (p *pdfWriter) alloc() int {
	p.offsets = append(p.offsets, -1)
	return len(p.offsets) - 1
}

func (p *pdfWriter) startObj(n int) error {
	if p.off > maxXrefOffset {
		return tooLargef("这一件的 PDF 超过 9 GB，请拆成几件再导出")
	}
	p.offsets[n] = p.off
	return p.printf("%d 0 obj\n", n)
}

// writeObj 写一个只有字典（或其他直接值）的对象。
func (p *pdfWriter) writeObj(n int, body string) error {
	if err := p.startObj(n); err != nil {
		return err
	}
	return p.printf("%s\nendobj\n", body)
}

func (p *pdfWriter) begin() error {
	// 第二行的 4 个高位字节让传输工具把文件当二进制。
	return p.write([]byte("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n"))
}

// piece 是页上的一个图像对象：整张图，或者原图里 (col0, row0) 起 cols × rows 的一块（条带、分块）。
type piece struct {
	obj                    int
	col0, row0, cols, rows int
}

// addPage 按 plan 写一页：图像对象、内容流、Page 对象。
func (p *pdfWriter) addPage(ctx context.Context, plan *pagePlan, ra io.ReaderAt, page *Page, buf []byte) error {
	var pieces []piece
	if plan.convert != nil {
		r, err := plan.convert.decode(ctx)
		if err != nil {
			return err
		}
		n, err := p.writeRaster(ctx, r)
		if err != nil {
			return err
		}
		pieces = append(pieces, piece{obj: n, cols: plan.width, rows: plan.height})
	} else {
		for i := range plan.images {
			img := &plan.images[i]
			n, err := p.writeImage(ctx, img, ra, buf)
			if err != nil {
				return err
			}
			pieces = append(pieces, piece{obj: n, col0: img.col0, row0: img.row0, cols: img.width, rows: img.height})
		}
	}

	// 内容流：每个图像对象从自己的单位正方形先放到原图（没转正）的单位正方形里它那一块的位置
	// [x, y, w, h]（第一行在上边），再按方向变换到页面：矩阵 = [w 0 0 h x y] × o。
	o := orientMatrix(plan.orientation, plan.pageW, plan.pageH)
	var content, xobjects strings.Builder
	W, H := float64(plan.width), float64(plan.height)
	for i, pc := range pieces {
		w, h := float64(pc.cols)/W, float64(pc.rows)/H
		x, y := float64(pc.col0)/W, 1-float64(pc.row0+pc.rows)/H
		m := [6]float64{w * o[0], w * o[1], h * o[2], h * o[3], x*o[0] + y*o[2] + o[4], x*o[1] + y*o[3] + o[5]}
		fmt.Fprintf(&content, "q %s %s %s %s %s %s cm /Im%d Do Q\n",
			num(m[0]), num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5]), i)
		fmt.Fprintf(&xobjects, "/Im%d %d 0 R ", i, pc.obj)
	}
	contentObj := p.alloc()
	if err := p.startObj(contentObj); err != nil {
		return err
	}
	if err := p.printf("<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", content.Len(), content.String()); err != nil {
		return err
	}
	pageObj := p.alloc()
	err := p.writeObj(pageObj, fmt.Sprintf(
		"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] /Resources << /XObject << %s>> >> /Contents %d 0 R >>",
		objPages, num(plan.pageW), num(plan.pageH), xobjects.String(), contentObj))
	if err != nil {
		return err
	}
	p.pages = append(p.pages, pageObj)
	p.id.Write(page.SnapshotID[:])
	p.id.Write(page.SHA256[:])
	return nil
}

// colorSpaceRef 是图像字典里 /ColorSpace 的值；Indexed 的调色板写成间接对象（同一个只写一次）。
func (p *pdfWriter) colorSpaceRef(cs colorSpace) (string, error) {
	if cs.lookup == nil {
		return "/" + cs.name, nil
	}
	key := string(cs.lookup)
	if n, ok := p.palettes[key]; ok {
		return fmt.Sprintf("%d 0 R", n), nil
	}
	n := p.alloc()
	body := fmt.Sprintf("[/Indexed /DeviceRGB %d <%s>]", len(cs.lookup)/3-1, hex.EncodeToString(cs.lookup))
	if err := p.writeObj(n, body); err != nil {
		return "", err
	}
	p.palettes[key] = n
	return fmt.Sprintf("%d 0 R", n), nil
}

// imageDict 是图像字典里 /Length 之前的部分。
func imageDict(width, height int, csRef string, bpc int, filter, parms, decode, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent %d",
		width, height, csRef, bpc)
	if filter != "" {
		fmt.Fprintf(&b, " /Filter /%s", filter)
	}
	if parms != "" {
		fmt.Fprintf(&b, " /DecodeParms << %s >>", parms)
	}
	if decode != "" {
		fmt.Fprintf(&b, " /Decode [%s]", decode)
	}
	b.WriteString(extra)
	return b.String()
}

// writeImage 写一个原样嵌入的图像对象：数据从原图的几段字节拷贝过来。
func (p *pdfWriter) writeImage(ctx context.Context, img *imageObj, ra io.ReaderAt, buf []byte) (int, error) {
	csRef, err := p.colorSpaceRef(img.cs)
	if err != nil {
		return 0, err
	}
	length := int64(len(img.prefix) + len(img.trailer))
	for _, r := range img.ranges {
		length += r.n
	}
	n := p.alloc()
	if err := p.startObj(n); err != nil {
		return 0, err
	}
	dict := imageDict(img.width, img.height, csRef, img.bpc, img.filter, img.parms, img.decode, "")
	if err := p.printf("%s /Length %d >>\nstream\n", dict, length); err != nil {
		return 0, err
	}
	if err := p.write(img.prefix); err != nil {
		return 0, err
	}
	for _, r := range img.ranges {
		if err := p.copyRange(ctx, ra, r, img.reverseBits, buf); err != nil {
			return 0, err
		}
	}
	if err := p.write(img.trailer); err != nil {
		return 0, err
	}
	if err := p.write([]byte("\nendstream\nendobj\n")); err != nil {
		return 0, err
	}
	return n, nil
}

// copyRange 把原图的一段字节拷进流里；reverse 时每个字节按位反转（TIFF FillOrder=2）；r.pngCRC 时核对 PNG 块的 CRC。
func (p *pdfWriter) copyRange(ctx context.Context, ra io.ReaderAt, r byteRange, reverse bool, buf []byte) error {
	var crc uint32
	if r.pngCRC {
		crc = crc32.Update(0, crc32.IEEETable, []byte("IDAT"))
	}
	for done := int64(0); done < r.n; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buf[:min(int64(len(buf)), r.n-done)]
		if err := readFull(ra, chunk, r.off+done); err != nil {
			return err
		}
		if r.pngCRC {
			crc = crc32.Update(crc, crc32.IEEETable, chunk)
		}
		if reverse {
			for i, c := range chunk {
				chunk[i] = bits.Reverse8(c)
			}
		}
		if err := p.write(chunk); err != nil {
			return err
		}
		done += int64(len(chunk))
	}
	if r.pngCRC {
		var stored [4]byte
		if err := readFull(ra, stored[:], r.off+r.n); err != nil {
			return err
		}
		if crc != be32(stored[:]) {
			return corruptf("PNG 的 IDAT 块 CRC 不对，文件已损坏")
		}
	}
	return nil
}

// writeRaster 写一个转换后的图像：Flate 压缩，PNG 预测器（每行都用 Up），有透明时另写一个 SMask。
func (p *pdfWriter) writeRaster(ctx context.Context, r *raster) (int, error) {
	csRef, err := p.colorSpaceRef(r.cs)
	if err != nil {
		return 0, err
	}
	n := p.alloc()
	smask := 0
	extra := ""
	if r.alphaBPC > 0 {
		smask = p.alloc()
		extra = fmt.Sprintf(" /SMask %d 0 R", smask)
	}
	ncomp := r.cs.comps()
	dict := imageDict(r.w, r.h, csRef, r.bpc, "FlateDecode", predictorParms(ncomp, r.bpc, r.w), r.decode, extra)
	if err := p.writeFlateRows(ctx, n, dict, r, ncomp, r.bpc, false); err != nil {
		return 0, err
	}
	if smask != 0 {
		matte := ""
		if r.matte {
			matte = " /Matte [" + strings.TrimSpace(strings.Repeat("0 ", ncomp)) + "]"
		}
		dict := imageDict(r.w, r.h, "/DeviceGray", r.alphaBPC, "FlateDecode", predictorParms(1, r.alphaBPC, r.w), "", matte)
		if err := p.writeFlateRows(ctx, smask, dict, r, 1, r.alphaBPC, true); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func predictorParms(colors, bpc, columns int) string {
	return fmt.Sprintf("/Predictor 15 /Colors %d /BitsPerComponent %d /Columns %d", colors, bpc, columns)
}

// writeFlateRows 写对象 n：字典 dict（不含 /Length），流是 r 的颜色（alpha 为 false）或透明度的各行，
// 每行前面一个 PNG 过滤类型字节（2 = Up），再 zlib 压缩。长度事先不知道，用间接对象。
func (p *pdfWriter) writeFlateRows(ctx context.Context, n int, dict string, r *raster, ncomp, bpc int, alpha bool) error {
	lenObj := p.alloc()
	if err := p.startObj(n); err != nil {
		return err
	}
	if err := p.printf("%s /Length %d 0 R >>\nstream\n", dict, lenObj); err != nil {
		return err
	}
	start := p.off
	zw, err := zlib.NewWriterLevel(pdfStreamWriter{p}, zlib.DefaultCompression)
	if err != nil {
		return err
	}
	rowBytes := (r.w*ncomp*bpc + 7) / 8
	prev := make([]byte, rowBytes)
	cur := make([]byte, rowBytes)
	out := make([]byte, 1+rowBytes)
	out[0] = 2 // Up
	vals := make([]uint32, r.w*ncomp)
	for y := 0; y < r.h; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if alpha {
			r.fill(y, nil, vals)
		} else {
			r.fill(y, vals, nil)
		}
		packSamples(cur, vals, bpc)
		for i := range cur {
			out[1+i] = cur[i] - prev[i]
		}
		if _, err := zw.Write(out); err != nil {
			return err
		}
		prev, cur = cur, prev
	}
	if err := zw.Close(); err != nil {
		return err
	}
	length := p.off - start
	if err := p.write([]byte("\nendstream\nendobj\n")); err != nil {
		return err
	}
	return p.writeObj(lenObj, strconv.FormatInt(length, 10))
}

// pdfStreamWriter 让 zlib 直接写进 PDF（计偏移）。
type pdfStreamWriter struct{ p *pdfWriter }

func (s pdfStreamWriter) Write(b []byte) (int, error) {
	if err := s.p.write(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// packSamples 把样本值按 bpc 位紧挨着放进 dst（一行，末尾补 0 到整字节）。
func packSamples(dst []byte, vals []uint32, bpc int) {
	switch bpc {
	case 8:
		for i, v := range vals {
			dst[i] = byte(v)
		}
		return
	case 16:
		for i, v := range vals {
			dst[2*i] = byte(v >> 8)
			dst[2*i+1] = byte(v)
		}
		return
	}
	clear(dst)
	perByte := 8 / bpc
	for i, v := range vals {
		shift := 8 - bpc*(i%perByte+1)
		dst[i/perByte] |= byte(v) << shift
	}
}

func (p *pdfWriter) finish() error {
	var kids strings.Builder
	for _, n := range p.pages {
		fmt.Fprintf(&kids, "%d 0 R ", n)
	}
	if err := p.writeObj(objPages, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.TrimSpace(kids.String()), len(p.pages))); err != nil {
		return err
	}
	if err := p.writeObj(objCatalog, fmt.Sprintf(
		"<< /Type /Catalog /Pages %d 0 R /ViewerPreferences << /DisplayDocTitle true >> >>", objPages)); err != nil {
		return err
	}
	date := pdfDate(p.now)
	if err := p.writeObj(objInfo, fmt.Sprintf(
		"<< /Title %s /Subject %s /Producer (Doclite) /Creator (Doclite) /CreationDate (%s) /ModDate (%s) >>",
		pdfText(p.item.Title), pdfText(p.item.ArchiveNo), date, date)); err != nil {
		return err
	}

	xref := p.off
	var b bytes.Buffer
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\r\n", len(p.offsets))
	for _, off := range p.offsets[1:] {
		if off < 0 {
			b.WriteString("0000000000 65535 f\r\n")
		} else {
			fmt.Fprintf(&b, "%010d 00000 n\r\n", off)
		}
	}
	id := hex.EncodeToString(p.id.Sum(nil)[:16])
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(p.offsets), objCatalog, objInfo, id, id, xref)
	return p.write(b.Bytes())
}

// pdfText 是 PDF 的文本串：UTF-16BE 带 BOM，写成十六进制串（中文、括号都不用转义）。
func pdfText(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

// pdfDate 是 PDF 的日期 D:YYYYMMDDHHmmSS+08'00'（按 t 的时区）。
func pdfDate(t time.Time) string {
	_, offset := t.Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("D:%s%c%02d'%02d'", t.Format("20060102150405"), sign, offset/3600, offset%3600/60)
}

// num 写一个数：最多 4 位小数，去掉末尾的 0。
func num(f float64) string {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// orientMatrix 是把原图（没转正）的单位正方形变换到 pw × ph 页面上、并按 EXIF / TIFF 方向转正的矩阵
// [a b c d e f]（x' = a·x + c·y + e，y' = b·x + d·y + f；图像第一行在单位正方形的上边）。
func orientMatrix(o int, pw, ph float64) [6]float64 {
	switch o {
	case 2: // 左右镜像
		return [6]float64{-pw, 0, 0, ph, pw, 0}
	case 3: // 转 180°
		return [6]float64{-pw, 0, 0, -ph, pw, ph}
	case 4: // 上下镜像
		return [6]float64{pw, 0, 0, -ph, 0, ph}
	case 5: // 沿主对角线翻转
		return [6]float64{0, -ph, -pw, 0, pw, ph}
	case 6: // 顺时针转 90°
		return [6]float64{0, -ph, pw, 0, 0, ph}
	case 7: // 沿副对角线翻转
		return [6]float64{0, ph, pw, 0, 0, 0}
	case 8: // 逆时针转 90°
		return [6]float64{0, ph, -pw, 0, pw, 0}
	}
	return [6]float64{pw, 0, 0, ph, 0, 0}
}
