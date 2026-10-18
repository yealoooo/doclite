package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// zip、清单.csv、目录.xlsx 的结构（原图包），再用 unzip -t 验证 zip。
func TestImagesBundle(t *testing.T) {
	src := newMemSource()
	p1 := src.addSample(t, 1, "jpeg-gray.jpg")
	p1.UploadName = "=cmd|' /C calc'!A0.jpg" // 原文件名防公式注入
	p2 := src.addSample(t, 2, "png-1bit.png")
	p2.UploadName = "-扫描, \"第二页\".png"
	p3 := src.addSample(t, 5, "tiff-g4-1bit.tif") // 页号不连续也照用
	p4 := src.addSample(t, 1, "tiff-none-gray8.tif")
	p4.ReviewStatus = "pending"
	p5 := src.addSample(t, 1, "jpeg-progressive.jpg")

	item1 := testItem("A-2025-0001", p1, p2, p3)
	item1.FieldValues = []string{"2025", "0012"}
	item1.CategoryNames = []string{"文书", "2025"}
	item2 := testItem("a-2025-0001", p4) // 不区分大小写和第一件重名
	item2.Status, item2.FieldValues = "draft", []string{"2024", ""}
	item3 := testItem("CON.1", p5) // 保留名
	item3.Status, item3.FieldValues = "draft", []string{"", "=1+1"}
	job := &Job{Format: FormatImages, FieldNames: []string{"年度", "件号"}, Items: []Item{item1, item2, item3}}

	var progress []Progress
	tmp := t.TempDir()
	now := time.Date(2026, 10, 16, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	var buf bytes.Buffer
	sum, err := Generate(context.Background(), job, src, &buf, Options{TmpDir: tmp, Now: now},
		func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyDir(t, tmp)
	z := buf.Bytes()

	wantNames := []string{"A-2025-0001", "a-2025-0001 (2)", "CON_.1"}
	if !slices.Equal(sum.Names, wantNames) || sum.Items != 3 || sum.Pages != 5 || sum.Size != int64(len(z)) {
		t.Fatalf("Summary 不对：%+v", sum)
	}
	last := progress[len(progress)-1]
	if last != (Progress{PagesDone: 5, PagesTotal: 5, ItemsDone: 3, ItemsTotal: 3, BytesWritten: int64(len(z))}) {
		t.Fatalf("最后的进度不对：%+v", last)
	}

	// zip 的条目：顺序、不压缩、UTF-8 名、时间、内容原样。
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []string{
		"A-2025-0001/", "A-2025-0001/0001.jpg", "A-2025-0001/0002.png", "A-2025-0001/0005.tif",
		"a-2025-0001 (2)/", "a-2025-0001 (2)/0001.tif",
		"CON_.1/", "CON_.1/0001.jpg",
		"清单.csv", "目录.xlsx",
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Method != zip.Store {
			t.Errorf("%s 压缩了（Method %d）", f.Name, f.Method)
		}
		if !f.Modified.Equal(now) {
			t.Errorf("%s 的时间是 %v", f.Name, f.Modified)
		}
	}
	if !slices.Equal(names, wantEntries) {
		t.Fatalf("zip 的条目是 %q，应该是 %q", names, wantEntries)
	}
	if f := zr.File[8]; f.Flags&0x800 == 0 {
		t.Error("中文文件名没有置 UTF-8 标志")
	}
	for name, want := range map[string]string{
		"A-2025-0001/0002.png": "png-1bit.png", "a-2025-0001 (2)/0001.tif": "tiff-none-gray8.tif", "CON_.1/0001.jpg": "jpeg-progressive.jpg",
	} {
		if !bytes.Equal(zipFile(t, z, name), sample(t, want)) {
			t.Errorf("%s 的内容和原图不一样", name)
		}
	}

	// 清单.csv：BOM、CRLF、每页一行、只有原文件名防公式注入。
	raw := zipFile(t, z, "清单.csv")
	if !bytes.HasPrefix(raw, []byte("\xEF\xBB\xBF")) {
		t.Fatal("清单.csv 没有 BOM")
	}
	if n, crlf := bytes.Count(raw, []byte("\n")), bytes.Count(raw, []byte("\r\n")); n != crlf || n != 6 {
		t.Fatalf("清单.csv 有 %d 个换行、%d 个 CRLF，应该都是 6", n, crlf)
	}
	rows, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rows[0], manifestHeader) {
		t.Fatalf("表头是 %q", rows[0])
	}
	want1 := []string{"A-2025-0001", "1", "A-2025-0001/0001.jpg", p1.FileID.String(), p1.SnapshotID.String(), "1",
		hex.EncodeToString(p1.SHA256[:]), "'=cmd|' /C calc'!A0.jpg", "通过"}
	if !slices.Equal(rows[1], want1) {
		t.Fatalf("第 1 行是 %q，应该是 %q", rows[1], want1)
	}
	if rows[2][7] != "'-扫描, \"第二页\".png" {
		t.Fatalf("原文件名是 %q", rows[2][7])
	}
	if rows[3][1] != "5" || rows[3][2] != "A-2025-0001/0005.tif" {
		t.Fatalf("第 3 行是 %q", rows[3])
	}
	if rows[4][0] != "a-2025-0001" || rows[4][2] != "a-2025-0001 (2)/0001.tif" || rows[4][8] != "待审" {
		t.Fatalf("第 4 行是 %q（档号原样写，zip 内文件名是改过的）", rows[4])
	}

	// 目录.xlsx：用 excelize 读回来。
	x, err := excelize.OpenReader(bytes.NewReader(zipFile(t, z, "目录.xlsx")))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if sheets := x.GetSheetList(); !slices.Equal(sheets, []string{"目录"}) {
		t.Fatalf("工作表是 %q", sheets)
	}
	xrows, err := x.GetRows("目录")
	if err != nil {
		t.Fatal(err)
	}
	wantRows := [][]string{
		{"序号", "档号", "题名", "年度", "件号", "页数", "状态", "分类路径"},
		{"1", "A-2025-0001", "题名 A-2025-0001", "2025", "0012", "3", "已锁定", "文书 / 2025"},
		{"2", "a-2025-0001", "题名 a-2025-0001", "2024", "", "1", "未锁定"},
		{"3", "CON.1", "题名 CON.1", "", "=1+1", "1", "未锁定"},
	}
	if len(xrows) != len(wantRows) {
		t.Fatalf("目录.xlsx 有 %d 行", len(xrows))
	}
	for i := range wantRows {
		// GetRows 去掉行尾的空单元格。
		if !slices.Equal(xrows[i], wantRows[i]) && !slices.Equal(xrows[i], append(slices.Clone(wantRows[i]), "")) {
			t.Fatalf("第 %d 行是 %q，应该是 %q", i+1, xrows[i], wantRows[i])
		}
	}
	for _, cell := range []string{"A2", "E2", "E4", "F3"} {
		typ, err := x.GetCellType("目录", cell)
		if err != nil {
			t.Fatal(err)
		}
		if typ != excelize.CellTypeInlineString && typ != excelize.CellTypeSharedString {
			t.Errorf("%s 不是文本（类型 %v）", cell, typ)
		}
		if f, _ := x.GetCellFormula("目录", cell); f != "" {
			t.Errorf("%s 成了公式 %q", cell, f)
		}
	}
	styleID, err := x.GetCellStyle("目录", "B1")
	if err != nil {
		t.Fatal(err)
	}
	style, err := x.GetStyle(styleID)
	if err != nil || style.Font == nil || !style.Font.Bold {
		t.Fatalf("首行没有加粗：%+v %v", style, err)
	}
	panes, err := x.GetPanes("目录")
	if err != nil || !panes.Freeze || panes.YSplit != 1 {
		t.Fatalf("首行没有冻结：%+v %v", panes, err)
	}

	// unzip -t（系统自带）。
	unzip, err := exec.LookPath("unzip")
	if err != nil {
		t.Skip("没有 unzip")
	}
	path := filepath.Join(t.TempDir(), "out.zip")
	if err := os.WriteFile(path, z, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(unzip, "-t", path).CombinedOutput(); err != nil || !strings.Contains(string(out), "No errors detected") {
		t.Fatalf("unzip -t 不通过：%v\n%s", err, out)
	}
}

// PDF 格式：每件一个 PDF 平铺，清单里的 zip 内文件名是所在的 PDF，文档信息是题名和档号。
func TestPDFBundle(t *testing.T) {
	src := newMemSource()
	item1 := testItem("A-2025-0001", src.addSample(t, 1, "jpeg-gray.jpg"), src.addSample(t, 2, "tiff-g4-1bit.tif"))
	item1.Title = "关于（2025）年度的通知"
	item2 := testItem("目录.xlsx", src.addSample(t, 1, "png-alpha.png"))
	job := &Job{Format: FormatPDF, Items: []Item{item1, item2}}
	z, sum, err := generate(t, job, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sum.Names, []string{"A-2025-0001.pdf", "目录.xlsx.pdf"}) || sum.EmbeddedPages != 2 || sum.ConvertedPages != 1 {
		t.Fatalf("Summary 不对：%+v", sum)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if want := []string{"A-2025-0001.pdf", "目录.xlsx.pdf", "清单.csv", "目录.xlsx"}; !slices.Equal(names, want) {
		t.Fatalf("zip 的条目是 %q", names)
	}
	d := parsePDF(t, zipFile(t, z, "A-2025-0001.pdf"))
	if len(d.pages()) != 2 {
		t.Fatal("页数不对")
	}
	info := d.info()
	if info["Title"] != "hex:"+strings.Trim(pdfText(item1.Title), "<>") || info["Subject"] != "hex:"+strings.Trim(pdfText("A-2025-0001"), "<>") {
		t.Fatalf("Title、Subject 不对：%v", info)
	}
	if info["CreationDate"] != "D:20261016093000+08'00'" {
		t.Fatalf("CreationDate 是 %v", info["CreationDate"])
	}
	rows, err := csv.NewReader(bytes.NewReader(zipFile(t, z, "清单.csv")[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][2] != "A-2025-0001.pdf" || rows[2][2] != "A-2025-0001.pdf" || rows[3][2] != "目录.xlsx.pdf" {
		t.Fatalf("zip 内文件名不对：%q", rows)
	}
}
