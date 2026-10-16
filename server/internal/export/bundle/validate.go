package bundle

import (
	"strings"

	"doclite/server/internal/platform/imagehdr"
)

// 状态的中文（目录.xlsx、清单.csv 里写中文）。
var (
	itemStatusLabels = map[string]string{"draft": "草稿", "done": "已完成", "locked": "已定稿"}
	reviewLabels     = map[string]string{"unassigned": "待分配", "pending": "待审", "approved": "通过", "rejected": "驳回"}
)

// maxXLSXColumns 是 xlsx 的列数上限。
const maxXLSXColumns = 16384

// validateJob 检查调用方给的 Job（都是调用方的问题，KindInvalidInput）。
func validateJob(job *Job) *Error {
	if job == nil {
		return invalidInput("没有导出内容")
	}
	if job.Format != FormatPDF && job.Format != FormatImages {
		return invalidInput("导出格式 %q 不认识", job.Format)
	}
	if len(job.Items) == 0 {
		return invalidInput("没有要导出的条目")
	}
	if cols := 6 + len(job.FieldNames); cols > maxXLSXColumns {
		return invalidInput("著录项有 %d 个，目录.xlsx 放不下", len(job.FieldNames))
	}
	for i := range job.Items {
		item := &job.Items[i]
		bad := func(format string, args ...any) *Error {
			return invalidInput(format, args...).atPage(i, item, -1, nil)
		}
		if strings.TrimSpace(item.ArchiveNo) == "" {
			return bad("第 %d 件没有档号", i+1)
		}
		if len(item.FieldValues) != len(job.FieldNames) {
			return bad("著录项的值有 %d 个，列名有 %d 个", len(item.FieldValues), len(job.FieldNames))
		}
		if _, ok := itemStatusLabels[item.Status]; !ok {
			return bad("条目状态 %q 不认识", item.Status)
		}
		if len(item.Pages) == 0 {
			return bad("没有页")
		}
		prev := 0
		for j := range item.Pages {
			p := &item.Pages[j]
			bad := func(format string, args ...any) *Error {
				return invalidInput(format, args...).atPage(i, item, j, p)
			}
			if p.PageNo <= prev {
				return bad("页号不是从 1 起严格递增")
			}
			prev = p.PageNo
			if formatOfMIME(p.MIME) == "" {
				return bad("mime %q 不认识", p.MIME)
			}
			if _, ok := reviewLabels[p.ReviewStatus]; !ok {
				return bad("审核状态 %q 不认识", p.ReviewStatus)
			}
			if p.Size <= 0 {
				return bad("文件大小 %d 不对", p.Size)
			}
		}
	}
	return nil
}

func formatOfMIME(mime string) imagehdr.Format {
	for _, f := range []imagehdr.Format{imagehdr.JPEG, imagehdr.PNG, imagehdr.TIFF} {
		if f.MIME() == mime {
			return f
		}
	}
	return ""
}

// 魔数（和 imagehdr 的识别一致）。
var magics = []struct {
	magic  string
	format imagehdr.Format
}{
	{"\xFF\xD8\xFF", imagehdr.JPEG},
	{"\x89PNG\r\n\x1a\n", imagehdr.PNG},
	{"II*\x00", imagehdr.TIFF},
	{"MM\x00*", imagehdr.TIFF},
}

// checkMagic 按文件开头的字节认格式，和快照的 mime 对不上算 Integrity（不是同一个文件了）。
func checkMagic(head []byte, mime string) (imagehdr.Format, error) {
	want := formatOfMIME(mime)
	for _, m := range magics {
		if strings.HasPrefix(string(head), m.magic) {
			if m.format != want {
				return "", integrityf("文件开头是 %s，快照记的是 %s", strings.ToUpper(string(m.format)), mime)
			}
			return m.format, nil
		}
	}
	return "", integrityf("文件开头不是 JPEG、PNG、TIFF，快照记的是 %s", mime)
}
