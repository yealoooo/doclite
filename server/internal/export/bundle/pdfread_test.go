package bundle

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 测试用的最小 PDF 读取：只认这个包写出来的写法（经典交叉引用表、直接的对象），用来核对结构和取出图像数据。
// 读的时候顺带检查交叉引用表里每个偏移都指向"n 0 obj"。

type pdfName string
type pdfRef int
type pdfDict map[string]any
type pdfStream struct {
	dict pdfDict
	data []byte
}

type pdfDoc struct {
	t       *testing.T
	data    []byte
	offsets map[int]int
	trailer pdfDict
}

func parsePDF(t *testing.T, data []byte) *pdfDoc {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("%PDF-1.7\n")) {
		t.Fatalf("PDF 开头不对：%q", data[:min(len(data), 16)])
	}
	if !bytes.HasSuffix(data, []byte("%%EOF\n")) {
		t.Fatalf("PDF 结尾不是 %%%%EOF")
	}
	i := bytes.LastIndex(data, []byte("startxref\n"))
	if i < 0 {
		t.Fatal("没有 startxref")
	}
	end := bytes.IndexByte(data[i+10:], '\n')
	xref, err := strconv.Atoi(string(data[i+10 : i+10+end]))
	if err != nil {
		t.Fatalf("startxref：%v", err)
	}
	d := &pdfDoc{t: t, data: data, offsets: map[int]int{}}
	if !bytes.HasPrefix(data[xref:], []byte("xref\n0 ")) {
		t.Fatalf("startxref 指的位置不是 xref")
	}
	p := xref + 7
	nl := bytes.IndexByte(data[p:], '\n')
	n, _ := strconv.Atoi(string(data[p : p+nl]))
	p += nl + 1
	for obj := range n {
		entry := string(data[p : p+20])
		if len(entry) != 20 || !strings.HasSuffix(entry, "\r\n") {
			t.Fatalf("xref 第 %d 项格式不对：%q", obj, entry)
		}
		if entry[17] == 'n' {
			off, _ := strconv.Atoi(entry[:10])
			if want := fmt.Sprintf("%d 0 obj\n", obj); !bytes.HasPrefix(data[off:], []byte(want)) {
				t.Fatalf("xref 里对象 %d 的偏移 %d 不对：%q", obj, off, data[off:off+16])
			}
			d.offsets[obj] = off
		}
		p += 20
	}
	if !bytes.HasPrefix(data[p:], []byte("trailer\n")) {
		t.Fatal("xref 后面不是 trailer")
	}
	lx := &pdfLexer{b: data, p: p + 8}
	d.trailer = lx.value().(pdfDict)
	if got := d.trailer["Size"].(float64); int(got) != n {
		t.Fatalf("trailer 的 Size %v 和 xref 的个数 %d 不一致", got, n)
	}
	return d
}

// obj 读对象 n（字典后面跟 stream 时返回 *pdfStream）。
func (d *pdfDoc) obj(n int) any {
	d.t.Helper()
	off, ok := d.offsets[n]
	if !ok {
		d.t.Fatalf("没有对象 %d", n)
	}
	lx := &pdfLexer{b: d.data, p: off}
	lx.p += len(fmt.Sprintf("%d 0 obj\n", n))
	v := lx.value()
	lx.skipSpace()
	if dict, ok := v.(pdfDict); ok && bytes.HasPrefix(d.data[lx.p:], []byte("stream\n")) {
		start := lx.p + 7
		length := int(d.resolve(dict["Length"]).(float64))
		if !bytes.HasPrefix(d.data[start+length:], []byte("\nendstream\nendobj\n")) {
			d.t.Fatalf("对象 %d 的流长度 %d 不对", n, length)
		}
		return &pdfStream{dict: dict, data: d.data[start : start+length]}
	}
	return v
}

func (d *pdfDoc) resolve(v any) any {
	if r, ok := v.(pdfRef); ok {
		return d.obj(int(r))
	}
	return v
}

func (d *pdfDoc) dict(v any) pdfDict {
	d.t.Helper()
	switch x := d.resolve(v).(type) {
	case pdfDict:
		return x
	case *pdfStream:
		return x.dict
	}
	d.t.Fatalf("不是字典：%v", v)
	return nil
}

func (d *pdfDoc) info() pdfDict { return d.dict(d.trailer["Info"]) }

// pdfPlaced 是页上的一个图像：图像字典、流、放置的矩阵。
type pdfPlaced struct {
	dict   pdfDict
	data   []byte
	matrix [6]float64
}

type pdfPage struct {
	mediaBox [4]float64
	images   []pdfPlaced
}

var doRE = regexp.MustCompile(`q ([-\d. ]+) cm /(Im\d+) Do Q`)

func (d *pdfDoc) pages() []pdfPage {
	d.t.Helper()
	root := d.dict(d.trailer["Root"])
	pages := d.dict(root["Pages"])
	var out []pdfPage
	kids := pages["Kids"].([]any)
	if int(pages["Count"].(float64)) != len(kids) {
		d.t.Fatalf("Pages 的 Count 和 Kids 个数不一致")
	}
	for _, k := range kids {
		pg := d.dict(k)
		var p pdfPage
		for i, v := range pg["MediaBox"].([]any) {
			p.mediaBox[i] = v.(float64)
		}
		xobj := d.dict(d.dict(pg["Resources"])["XObject"])
		content := d.resolve(pg["Contents"]).(*pdfStream)
		for _, m := range doRE.FindAllStringSubmatch(string(content.data), -1) {
			var pl pdfPlaced
			for i, f := range strings.Fields(m[1]) {
				pl.matrix[i], _ = strconv.ParseFloat(f, 64)
			}
			s := d.resolve(xobj[m[2]]).(*pdfStream)
			pl.dict, pl.data = s.dict, s.data
			p.images = append(p.images, pl)
		}
		out = append(out, p)
	}
	return out
}

// decodeImage 解一个转换后的图像（FlateDecode + Predictor 15），返回每行的样本值。
func decodeImage(t *testing.T, d *pdfDoc, dict pdfDict, data []byte) [][]uint32 {
	t.Helper()
	if dict["Filter"] != pdfName("FlateDecode") {
		t.Fatalf("Filter 是 %v，不是 FlateDecode", dict["Filter"])
	}
	parms := d.dict(dict["DecodeParms"])
	colors := int(parms["Colors"].(float64))
	bpc := int(parms["BitsPerComponent"].(float64))
	columns := int(parms["Columns"].(float64))
	if parms["Predictor"].(float64) != 15 {
		t.Fatalf("Predictor 不是 15")
	}
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	rowBytes := (columns*colors*bpc + 7) / 8
	bpp := max(1, colors*bpc/8)
	height := int(dict["Height"].(float64))
	if len(raw) != height*(rowBytes+1) {
		t.Fatalf("解出来 %d 字节，应有 %d", len(raw), height*(rowBytes+1))
	}
	prev := make([]byte, rowBytes)
	var rows [][]uint32
	for y := range height {
		line := raw[y*(rowBytes+1):]
		ft, cur := line[0], append([]byte(nil), line[1:1+rowBytes]...)
		for i := range cur {
			var a, b, c byte
			if i >= bpp {
				a, c = cur[i-bpp], prev[i-bpp]
			}
			b = prev[i]
			switch ft {
			case 0:
			case 1:
				cur[i] += a
			case 2:
				cur[i] += b
			case 3:
				cur[i] += byte((int(a) + int(b)) / 2)
			case 4:
				cur[i] += paeth(a, b, c)
			default:
				t.Fatalf("PNG 过滤类型 %d 不对", ft)
			}
		}
		vals := make([]uint32, columns*colors)
		for i := range vals {
			vals[i] = getSample(cur, i, bpc)
		}
		rows = append(rows, vals)
		prev = cur
	}
	return rows
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---------------------------------------------------------------------------
// 词法

type pdfLexer struct {
	b []byte
	p int
}

func (l *pdfLexer) skipSpace() {
	for l.p < len(l.b) && strings.IndexByte(" \t\r\n\f\x00", l.b[l.p]) >= 0 {
		l.p++
	}
}

func isDelim(c byte) bool { return strings.IndexByte(" \t\r\n\f\x00()<>[]{}/%", c) >= 0 }

func (l *pdfLexer) token() string {
	l.skipSpace()
	s := l.p
	for l.p < len(l.b) && !isDelim(l.b[l.p]) {
		l.p++
	}
	return string(l.b[s:l.p])
}

func (l *pdfLexer) value() any {
	l.skipSpace()
	switch c := l.b[l.p]; {
	case c == '<' && l.b[l.p+1] == '<':
		l.p += 2
		d := pdfDict{}
		for {
			l.skipSpace()
			if l.b[l.p] == '>' {
				l.p += 2
				return d
			}
			k := l.value().(pdfName)
			d[string(k)] = l.value()
		}
	case c == '<':
		e := bytes.IndexByte(l.b[l.p:], '>')
		s := string(l.b[l.p+1 : l.p+e])
		l.p += e + 1
		return "hex:" + s
	case c == '[':
		l.p++
		var a []any
		for {
			l.skipSpace()
			if l.b[l.p] == ']' {
				l.p++
				return a
			}
			a = append(a, l.value())
		}
	case c == '/':
		l.p++
		return pdfName(l.token())
	case c == '(':
		depth, s := 0, l.p+1
		for {
			switch l.b[l.p] {
			case '\\':
				l.p++
			case '(':
				depth++
			case ')':
				depth--
			}
			l.p++
			if depth == 0 {
				return string(l.b[s : l.p-1])
			}
		}
	}
	tok := l.token()
	switch tok {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		panic(fmt.Sprintf("PDF 里认不出的记号 %q", tok))
	}
	// "n 0 R"
	save := l.p
	if g := l.token(); g == "0" {
		if r := l.token(); r == "R" {
			return pdfRef(int(f))
		}
	}
	l.p = save
	return f
}
