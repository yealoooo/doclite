package bundle

import (
	"bufio"
	"encoding/csv"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// 清单.csv：每页一行，是每页能反查的依据。UTF-8 带 BOM、CRLF 换行。
var manifestHeader = []string{"档号", "页号", "zip 内文件名", "file_id", "snapshot_id", "版本号", "sha256", "原文件名", "审核状态"}

func (g *generator) writeManifest() error {
	fw, err := g.create(manifestName)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(fw, 64<<10)
	if _, err := bw.WriteString("\uFEFF"); err != nil {
		return err
	}
	cw := csv.NewWriter(bw)
	cw.UseCRLF = true
	if err := cw.Write(manifestHeader); err != nil {
		return err
	}
	for i := range g.job.Items {
		item := &g.job.Items[i]
		for j := range item.Pages {
			p := &item.Pages[j]
			if err := cw.Write([]string{
				item.ArchiveNo,
				strconv.Itoa(p.PageNo),
				g.zipNameOfPage(i, p),
				p.FileID.String(),
				p.SnapshotID.String(),
				strconv.Itoa(p.VersionNo),
				hex.EncodeToString(p.SHA256[:]),
				defuseFormula(p.UploadName),
				reviewLabels[p.ReviewStatus],
			}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	return bw.Flush()
}

// zipNameOfPage 是这一页在 zip 里的文件名：PDF 是所在的 `<名字>.pdf`，原图包是 `<名字>/0001.jpg`。
func (g *generator) zipNameOfPage(i int, p *Page) string {
	if g.job.Format == FormatPDF {
		return g.names[i] + ".pdf"
	}
	return g.names[i] + "/" + pageFileName(p.PageNo, formatOfMIME(p.MIME).Ext())
}

// defuseFormula：以 `= + - @`、制表符、回车开头的前面加 `'`，免得 Excel 当公式执行。只用在"原文件名"一列；
// 档号、zip 内文件名原样写（它们要和库里、zip 里逐字对得上）。
func defuseFormula(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// 目录.xlsx：一个工作表"目录"，每件一行；首行加粗并冻结；所有单元格都按文本写（数字格式 @）。
func (g *generator) writeCatalog() error {
	path, err := g.buildCatalog()
	if err != nil {
		return err
	}
	defer os.Remove(path)
	f, err := os.Open(path)
	if err != nil {
		return tempErr("读目录.xlsx 失败", err)
	}
	defer f.Close()
	fw, err := g.create(catalogName)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(fw, &tempReader{f}, g.buf)
	return err
}

const catalogSheet = "目录"

// buildCatalog 用 excelize 的流式写入把目录.xlsx 写到临时文件（写进 zip 时写入错误才能和临时文件的错误分开）。
func (g *generator) buildCatalog() (string, error) {
	fail := func(err error) (string, error) { return "", tempErr("生成目录.xlsx 失败", err) }
	x := excelize.NewFile(excelize.Options{TmpDir: g.tmp})
	defer x.Close()
	if err := x.SetSheetName("Sheet1", catalogSheet); err != nil {
		return fail(err)
	}
	text, err := x.NewStyle(&excelize.Style{NumFmt: 49})
	if err != nil {
		return fail(err)
	}
	bold, err := x.NewStyle(&excelize.Style{NumFmt: 49, Font: &excelize.Font{Bold: true}})
	if err != nil {
		return fail(err)
	}
	sw, err := x.NewStreamWriter(catalogSheet)
	if err != nil {
		return fail(err)
	}
	if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return fail(err)
	}
	nf := len(g.job.FieldNames)
	widths := map[int]float64{1: 6, 2: 24, 3: 40, 4 + nf: 6, 5 + nf: 8, 6 + nf: 40}
	for col, wd := range widths {
		if err := sw.SetColWidth(col, col, wd); err != nil {
			return fail(err)
		}
	}
	header := append([]string{"序号", "档号", "题名"}, g.job.FieldNames...)
	header = append(header, "页数", "状态", "分类路径")
	if err := sw.SetRow("A1", cells(header, bold)); err != nil {
		return fail(err)
	}
	row := make([]string, 0, len(header))
	for i := range g.job.Items {
		item := &g.job.Items[i]
		row = append(row[:0], strconv.Itoa(i+1), item.ArchiveNo, item.Title)
		row = append(row, item.FieldValues...)
		row = append(row, strconv.Itoa(len(item.Pages)), itemStatusLabels[item.Status], strings.Join(item.CategoryNames, " / "))
		cell, err := excelize.CoordinatesToCellName(1, i+2)
		if err != nil {
			return fail(err)
		}
		if err := sw.SetRow(cell, cells(row, text)); err != nil {
			return fail(err)
		}
	}
	if err := sw.Flush(); err != nil {
		return fail(err)
	}
	out, err := os.CreateTemp(g.tmp, "catalog-*.xlsx")
	if err != nil {
		return fail(err)
	}
	path := out.Name()
	if _, err := x.WriteTo(out); err != nil {
		out.Close()
		os.Remove(path)
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(path)
		return fail(err)
	}
	return path, nil
}

func cells(values []string, style int) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = excelize.Cell{StyleID: style, Value: v}
	}
	return out
}

// tempReader 把临时文件的读取错误包成 KindTemp。
type tempReader struct{ f *os.File }

func (t *tempReader) Read(p []byte) (int, error) {
	n, err := t.f.Read(p)
	if err != nil && err != io.EOF {
		return n, tempErr("读临时文件失败", err)
	}
	return n, err
}
