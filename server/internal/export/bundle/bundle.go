// Package bundle 生成导出物（requirements 5.11"导出物"）：输入冻结的条目和页、读原图的接口，输出写进一个
// writer 的 zip。纯函数式，不碰数据库和 S3；导出任务、执行器、上传在 M5-2。
//
// zip（不压缩、UTF-8 名、超过 4 GB 时 Go 自动用 zip64）里的条目：
//   - PDF：每件一个 `<名字>.pdf`（平铺），然后 `清单.csv`、`目录.xlsx`；
//   - 原图包：每件一个文件夹 `<名字>/`，里面 `0001.<jpg|png|tif>`……（字节原样拷贝），然后 `清单.csv`、`目录.xlsx`。
//
// `<名字>` 一般就是档号，规则见 [ItemNames]。
//
// 流式：一次处理一页。PDF 的页先从数据源读到临时文件（边读边核对大小和 sha256），读文件头、定下原样嵌入还是转换，
// 再写进 PDF；PDF 直接写进 zip 条目，不在本机生成整个 PDF。原图包的页从数据源直接拷进 zip（边拷边核对）。
// 临时文件在 Options.TmpDir 下的 `export-*` 目录里，成功、出错、取消时都删掉。
//
// PDF 的规则：页面尺寸见 plan.go，方向和写法见 pdf.go，各格式原样嵌入还是转换见 jpeg.go、png.go、tiff.go、
// tiffjpeg.go、tiffconv.go。硬性规则 11 的例外（server 解码像素，只在转换时）只在这个包里。
package bundle

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
)

// Format 是导出物的格式，取值和 export_tasks.export_format 一致。
type Format string

const (
	FormatPDF    Format = "pdf"
	FormatImages Format = "images"
)

// Job 是一次导出：建任务时冻结的条目和页。
type Job struct {
	Format Format
	// FieldNames 是著录项的列名（按设置的顺序），每件的 FieldValues 与它一一对应。
	FieldNames []string
	// Items 已经排好序（节点 path_code、件号、id）。
	Items []Item
}

// Item 是一件（档案条目），字段是建任务时冻结的值。
type Item struct {
	ArchiveNo   string
	Title       string
	FieldValues []string
	// CategoryNames 是从归类根往下的节点名称链（不含根），目录.xlsx 里用 " / " 连接。
	CategoryNames []string
	// Status 是条目状态：draft、done、locked。
	Status string
	// Pages 按页号排好（页号严格递增）。
	Pages []Page
}

// Page 是一页：挂接的文件和建任务时定下的快照。
type Page struct {
	PageNo     int
	FileID     uuid.UUID
	SnapshotID uuid.UUID
	VersionNo  int
	SHA256     [32]byte
	Size       int64
	// MIME 是快照的 mime_type：image/jpeg、image/png、image/tiff。
	MIME string
	// UploadName 是原文件名（清单.csv 里的"原文件名"）。
	UploadName string
	// ReviewStatus 是审核状态：unassigned、pending、approved、rejected。
	ReviewStatus string
	// StorageProfileID、StorageKey 给 PageSource 用，这个包不看。
	StorageProfileID uuid.UUID
	StorageKey       string
}

// PageSource 读原图。实现要能被多次调用（出错后用 Inspect 再看剩下的页时）。
type PageSource interface {
	// Open 返回整个原图，按顺序读到 EOF。导出时每页调用一次。读失败（包括连接中断）算 KindSource。
	Open(ctx context.Context, p Page) (io.ReadCloser, error)
	// ReadAt 读原图从 off 起的 len(b) 个字节，语义同 io.ReaderAt（n < len(b) 时 err 不为 nil，读到末尾是 io.EOF）。
	// 只有 Inspect 用（S3 用 Range 请求）。
	ReadAt(ctx context.Context, p Page, b []byte, off int64) (n int, err error)
}

// DefaultConvertMemoryLimit 是转换时内存上限的默认值。
const DefaultConvertMemoryLimit = 512 << 20

// Options 是生成时的选项。
type Options struct {
	// TmpDir 是临时目录，空串用 os.TempDir()。这里面建 `export-*` 目录，结束时删掉。
	TmpDir string
	// Now 是导出时间：zip 条目的时间（按它的时区写 DOS 时间）、PDF 的 CreationDate。零值用 time.Now()。
	Now time.Time
	// ConvertMemoryLimit 是转换一页时解码像素用的内存上限（字节），<= 0 用 DefaultConvertMemoryLimit。超过报 KindTooLarge。
	ConvertMemoryLimit int64

	// forceConvert 只给测试用：能转换的页一律转换（核对转换的像素）。
	forceConvert bool
}

func (o Options) withDefaults() Options {
	if o.TmpDir == "" {
		o.TmpDir = os.TempDir()
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.ConvertMemoryLimit <= 0 {
		o.ConvertMemoryLimit = DefaultConvertMemoryLimit
	}
	return o
}

// Progress 是进度，每处理完一页、每写完一件、最后各回调一次（节流由调用方做）。
type Progress struct {
	PagesDone, PagesTotal int
	ItemsDone, ItemsTotal int
	// BytesWritten 是已经写进 writer 的字节数。
	BytesWritten int64
}

// Summary 是生成的结果。
type Summary struct {
	// Size 是写进 writer 的字节数（整个 zip）。
	Size  int64
	Items int
	Pages int
	// EmbeddedPages、ConvertedPages：PDF 里原样嵌入、转换的页数（原图包都是 0）。
	EmbeddedPages, ConvertedPages int
	// Names 是每件在 zip 里的名字（PDF 是 `<名字>.pdf`，原图包是文件夹 `<名字>`），和 Job.Items 一一对应。
	Names []string
}

// zip 里另外两个文件的名字。
const (
	catalogName  = "目录.xlsx"
	manifestName = "清单.csv"
)

// Generate 生成导出物，写进 w。
//
// 成功时 zip 已经写完整（central directory 也写了），w 没有关闭，由调用方关闭（完成分片上传）；失败、取消时 w 里
// 是写了一半的 zip，调用方丢弃（中止上传）。返回的错误都是 *[Error]。
//
// 任何一页有问题整个导出失败，不跳过；之后可以用 [Inspect] 把剩下的页检查一遍，一次列出所有有问题的页。
func Generate(ctx context.Context, job *Job, src PageSource, w io.Writer, opts Options, progress func(Progress)) (Summary, error) {
	if err := validateJob(job); err != nil {
		return Summary{}, err
	}
	opts = opts.withDefaults()
	tmp, err := os.MkdirTemp(opts.TmpDir, "export-*")
	if err != nil {
		return Summary{}, classify(ctx, tempErr("建临时目录失败", err), KindTemp)
	}
	defer os.RemoveAll(tmp)

	g := &generator{
		ctx:      ctx,
		job:      job,
		src:      src,
		opts:     opts,
		tmp:      tmp,
		out:      &countWriter{w: w},
		progress: progress,
		buf:      make([]byte, copyBufSize),
	}
	g.zw = zip.NewWriter(g.out)
	g.names = ItemNames(job.Items, job.Format)
	for _, it := range job.Items {
		g.prog.PagesTotal += len(it.Pages)
	}
	g.prog.ItemsTotal = len(job.Items)

	if err := g.run(); err != nil {
		return Summary{}, err
	}
	g.sum.Size = g.out.n
	g.sum.Items = len(job.Items)
	g.sum.Pages = g.prog.PagesTotal
	g.sum.Names = make([]string, len(job.Items))
	for i, n := range g.names {
		if job.Format == FormatPDF {
			n += ".pdf"
		}
		g.sum.Names[i] = n
	}
	return g.sum, nil
}

// copyBufSize 是拷贝时一块的大小；每块之间检查一次取消。
const copyBufSize = 1 << 20

type generator struct {
	ctx      context.Context
	job      *Job
	src      PageSource
	opts     Options
	tmp      string
	out      *countWriter
	zw       *zip.Writer
	names    []string
	progress func(Progress)
	prog     Progress
	sum      Summary
	buf      []byte
}

func (g *generator) run() error {
	for i := range g.job.Items {
		if err := g.ctx.Err(); err != nil {
			return classify(g.ctx, err, KindTemp)
		}
		var err error
		if g.job.Format == FormatPDF {
			err = g.pdfItem(i)
		} else {
			err = g.imagesItem(i)
		}
		if err != nil {
			return err
		}
		g.prog.ItemsDone++
		g.report()
	}
	if err := g.writeManifest(); err != nil {
		return classify(g.ctx, err, KindTemp)
	}
	if err := g.writeCatalog(); err != nil {
		return classify(g.ctx, err, KindTemp)
	}
	if err := g.zw.Close(); err != nil {
		return classify(g.ctx, err, KindOutput)
	}
	g.report()
	return nil
}

func (g *generator) report() {
	if g.progress != nil {
		p := g.prog
		p.BytesWritten = g.out.n
		g.progress(p)
	}
}

func (g *generator) create(name string) (io.Writer, error) {
	return g.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: g.opts.Now})
}

// pageErr 把处理第 i 件第 j 页时的错误变成带页信息的 *Error。
func (g *generator) pageErr(err error, i, j int) *Error {
	item := &g.job.Items[i]
	return classify(g.ctx, err, KindCorrupt).atPage(i, item, j, &item.Pages[j])
}

// pdfItem 写一件的 PDF。
func (g *generator) pdfItem(i int) error {
	item := &g.job.Items[i]
	fw, err := g.create(g.names[i] + ".pdf")
	if err != nil {
		return classify(g.ctx, err, KindOutput).atPage(i, item, -1, nil)
	}
	pw := newPDFWriter(fw, item, g.opts.Now)
	if err := pw.begin(); err != nil {
		return classify(g.ctx, err, KindOutput).atPage(i, item, -1, nil)
	}
	for j := range item.Pages {
		page := &item.Pages[j]
		if err := g.ctx.Err(); err != nil {
			return g.pageErr(err, i, j)
		}
		converted, err := g.pdfPage(pw, page)
		if err != nil {
			return g.pageErr(err, i, j)
		}
		if converted {
			g.sum.ConvertedPages++
		} else {
			g.sum.EmbeddedPages++
		}
		g.prog.PagesDone++
		g.report()
	}
	if err := pw.finish(); err != nil {
		return classify(g.ctx, err, KindOutput).atPage(i, item, -1, nil)
	}
	return nil
}

// pdfPage 把一页读到临时文件、核对，再写进 PDF。返回这一页是不是转换的。
func (g *generator) pdfPage(pw *pdfWriter, page *Page) (converted bool, err error) {
	f, err := g.fetch(page)
	if err != nil {
		return false, err
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	defer func() {
		// 解析、解码时程序内部出错也算这一页的问题，不让整个进程崩掉。
		if r := recover(); r != nil {
			converted, err = false, corruptf("处理这一页时出错：%v", r)
		}
	}()
	ra := tempReaderAt{f}
	plan, err := analyze(ra, page, g.opts, false)
	if err != nil {
		return false, err
	}
	if err := pw.addPage(g.ctx, plan, ra, page, g.buf); err != nil {
		return false, err
	}
	return plan.convert != nil, nil
}

// fetch 把原图读到临时文件，边读边核对大小和 sha256。
func (g *generator) fetch(page *Page) (*os.File, error) {
	rc, err := g.src.Open(g.ctx, *page)
	if err != nil {
		return nil, sourceErr("读原图失败", err)
	}
	defer rc.Close()
	f, err := os.CreateTemp(g.tmp, "page-*")
	if err != nil {
		return nil, tempErr("建临时文件失败", err)
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err := g.copyVerified(f, rc, page, "写临时文件失败"); err != nil {
		return nil, err
	}
	ok = true
	return f, nil
}

// imagesItem 写一件的原图包：文件夹条目，再每页一个文件，字节原样从数据源拷进 zip（边拷边核对）。
func (g *generator) imagesItem(i int) error {
	item := &g.job.Items[i]
	dir := g.names[i] + "/"
	if _, err := g.create(dir); err != nil {
		return classify(g.ctx, err, KindOutput).atPage(i, item, -1, nil)
	}
	for j := range item.Pages {
		if err := g.imagesPage(dir, &item.Pages[j]); err != nil {
			return g.pageErr(err, i, j)
		}
		g.prog.PagesDone++
		g.report()
	}
	return nil
}

func (g *generator) imagesPage(dir string, page *Page) error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	rc, err := g.src.Open(g.ctx, *page)
	if err != nil {
		return sourceErr("读原图失败", err)
	}
	defer rc.Close()
	// 扩展名按文件头取：先看开头几个字节，和 mime 对不上算 Integrity。
	br := bufio.NewReaderSize(&sourceReader{r: rc}, 64<<10)
	head, err := br.Peek(8)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(head) < 8 && int64(len(head)) < page.Size {
		return sourceErr("读原图失败", fmt.Errorf("只读到 %d 字节，快照记的是 %d 字节", len(head), page.Size))
	}
	format, err := checkMagic(head, page.MIME)
	if err != nil {
		return err
	}
	fw, err := g.create(dir + pageFileName(page.PageNo, format.Ext()))
	if err != nil {
		return err
	}
	return g.copyVerified(fw, br, page, "")
}

// pageFileName 是原图包里一页的文件名：页号至少 4 位。
func pageFileName(pageNo int, ext string) string {
	return fmt.Sprintf("%04d.%s", pageNo, ext)
}

// copyVerified 从 r 读整个原图写进 w，边读边算 sha256；读多了、sha256 不对算 Integrity，读少了算 Source。
// w 的写入错误：writeOp 不为空时按临时文件算（KindTemp），为空时 w 是 zip 条目（countWriter 已经包好了）。
func (g *generator) copyVerified(w io.Writer, r io.Reader, page *Page, writeOp string) error {
	h := sha256.New()
	var n int64
	limit := page.Size + 1 // 多读一个字节，用来发现"比快照记的长"
	for n < limit {
		if err := g.ctx.Err(); err != nil {
			return err
		}
		chunk := g.buf[:min(int64(len(g.buf)), limit-n)]
		m, rerr := r.Read(chunk)
		if m > 0 {
			h.Write(chunk[:m])
			if n+int64(m) <= page.Size {
				if _, err := w.Write(chunk[:m]); err != nil {
					if writeOp != "" {
						return tempErr(writeOp, err)
					}
					return err
				}
			}
			n += int64(m)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			var ie *ioError
			if errors.As(rerr, &ie) {
				return rerr
			}
			return sourceErr("读原图失败", rerr)
		}
	}
	switch {
	case n < page.Size:
		return sourceErr("读原图失败", fmt.Errorf("只读到 %d 字节，快照记的是 %d 字节", n, page.Size))
	case n > page.Size:
		return integrityf("原图比快照记的 %d 字节长", page.Size)
	}
	if sum := h.Sum(nil); string(sum) != string(page.SHA256[:]) {
		return integrityf("原图的 sha256（%s）和快照记的（%s）不一致", hex.EncodeToString(sum), hex.EncodeToString(page.SHA256[:]))
	}
	return nil
}

// countWriter 记下写了多少字节，写失败时包成 KindOutput。
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		return n, outputErr(err)
	}
	return n, nil
}

// sourceReader 把数据源的读取错误包成 KindSource（io.EOF 原样返回）。
type sourceReader struct{ r io.Reader }

func (s *sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, sourceErr("读原图失败", err)
	}
	return n, err
}

// tempReaderAt 是临时文件，读取错误包成 KindTemp。
type tempReaderAt struct{ f *os.File }

func (t tempReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := t.f.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, tempErr("读临时文件失败", err)
	}
	return n, err
}
