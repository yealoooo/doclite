package export

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 下载（requirements 5.11"下载和保留"，用户决定 Q5）：返回 JSON（不用 302：壳经代理会把 zip 转一遍），url 是按天取整、
// 带 response-content-disposition 的预签名地址，浏览器直连 S3。只对 done、没过期、导出物还在的任务；过期 409
// invalid_state，存储停用 409 storage_unavailable。每次成功写一条审计 export_task / download，不发事件。
//
// 文件名 `<项目编码>_<范围名>_<日期>.zip`：范围名是一个节点的名称（根节点"全部"）、几个节点"<第一个>等 n 个分类"、
// 条目是第一件的档号（几件"<档号>等 n 件"）、补交"补交 n 件"；Windows 不允许的字符换成 `_`，范围名最多 40 个字；
// 日期是导出完成那天（UTC+8）。Content-Disposition 另带 ASCII 的兜底名 `<项目编码>_export_<日期>.zip`，中文按
// RFC 5987 编码。

// maxScopeLabelChars 是文件名里范围名最多几个字。
const maxScopeLabelChars = 40

// exportZone 是导出时间用的时区：UTC+8（和预签名按天取整一样，不看容器的 TZ）。
var exportZone = time.FixedZone("UTC+8", 8*3600)

// Download 是 `GET /api/export-tasks/{id}/download` 的响应。
type Download struct {
	URL          string     `json:"url"`
	FileName     string     `json:"file_name"`
	Size         int64      `json:"size"`
	ExpiresAt    *time.Time `json:"expires_at"`
	URLExpiresAt time.Time  `json:"url_expires_at"`
}

// Download 给任务的导出物生成下载地址。
func (s *Service) Download(ctx context.Context, userID, id uuid.UUID) (*Download, error) {
	t, err := getTask(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errTaskNotFound
	}
	if t.Status != StatusDone {
		return nil, httpx.InvalidState(fmt.Sprintf("任务%s，没有可以下载的导出物", statusLabel(t.Status)))
	}
	if t.Expired || t.OutputKey == nil || t.StorageProfileID == nil {
		when := ""
		if t.ExpiresAt != nil {
			when = fmt.Sprintf("（%s 到期）", t.ExpiresAt.In(exportZone).Format("2006-01-02 15:04"))
		}
		return nil, httpx.InvalidState(fmt.Sprintf("导出物已过期删除%s，任务和明细还在，可以按原来的范围重新导出", when))
	}
	code, err := file.ProjectCode(ctx, s.pool, t.ProjectID)
	if err != nil {
		return nil, err
	}
	firstArchiveNo := ""
	if t.ScopeType == scopeTypeItem && t.Header.ResubmitOf == nil && t.JobID != nil {
		if firstArchiveNo, err = firstFrozenArchiveNo(ctx, s.pool, *t.JobID); err != nil {
			return nil, err
		}
	}
	finished := s.now()
	if t.FinishedAt != nil {
		finished = *t.FinishedAt
	}
	name := DownloadFileName(code, scopeLabel(t, firstArchiveNo), finished)
	disp := ContentDisposition(asciiFileName(code, finished), name)
	signed, err := s.storage.PresignDownload(ctx, *t.StorageProfileID, *t.OutputKey, disp)
	if err != nil {
		return nil, err
	}
	var size int64
	if t.OutputSize != nil {
		size = *t.OutputSize
	}
	pid := t.ProjectID
	if err := audit.Write(ctx, s.pool, audit.Entry{ProjectID: &pid, OperatorID: audit.Operator(userID),
		TargetType: audit.TargetExportTask, TargetID: id, Action: "download",
		Detail: map[string]any{"file_name": name, "size": size, "storage_profile_id": *t.StorageProfileID,
			"output_key": *t.OutputKey, "url_expires_at": signed.ExpiresAt}}); err != nil {
		return nil, err
	}
	return &Download{URL: signed.URL, FileName: name, Size: size, ExpiresAt: t.ExpiresAt, URLExpiresAt: signed.ExpiresAt}, nil
}

// firstFrozenArchiveNo 是冻结的第一件的档号（范围是条目时文件名用它）。
func firstFrozenArchiveNo(ctx context.Context, q db.Querier, jobID uuid.UUID) (string, error) {
	var no *string
	err := q.QueryRow(ctx, `SELECT payload->'items'->0->>'archive_no' FROM background_jobs WHERE id = $1`, jobID).Scan(&no)
	if err != nil || no == nil {
		return "", err
	}
	return *no, nil
}

// scopeLabel 是文件名里的范围名。
func scopeLabel(t *taskRow, firstArchiveNo string) string {
	h := &t.Header
	if h.ResubmitOf != nil {
		return fmt.Sprintf("补交%d件", h.ItemCount)
	}
	if t.ScopeType == scopeTypeItem {
		if h.ItemCount > 1 {
			return fmt.Sprintf("%s等%d件", firstArchiveNo, h.ItemCount)
		}
		return firstArchiveNo
	}
	first := "全部"
	if len(h.ScopeCategories) > 0 {
		if names := h.ScopeCategories[0].CategoryNames; len(names) > 0 {
			first = names[len(names)-1]
		}
	}
	if n := len(h.ScopeCategories); n > 1 {
		return fmt.Sprintf("%s等%d个分类", first, n)
	}
	return first
}

// sanitizeNamePart：Windows 不允许的字符和控制字符换成 `_`，去掉首尾空白和结尾的点、空格，最多 40 个字。
func sanitizeNamePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune(`\/:*?"<>|`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	out := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if utf8.RuneCountInString(out) > maxScopeLabelChars {
		out = string([]rune(out)[:maxScopeLabelChars])
	}
	return out
}

// DownloadFileName 是下载的文件名：`<项目编码>_<范围名>_<日期>.zip`（日期是 finished 那天，UTC+8，YYYYMMDD）。
func DownloadFileName(projectCode, label string, finished time.Time) string {
	label = sanitizeNamePart(label)
	if label == "" {
		label = "导出"
	}
	return fmt.Sprintf("%s_%s_%s.zip", sanitizeNamePart(projectCode), label, finished.In(exportZone).Format("20060102"))
}

// asciiFileName 是只认 ASCII 的客户端用的文件名：`<项目编码>_export_<日期>.zip`（非 ASCII 字符换成 `_`）。
func asciiFileName(projectCode string, finished time.Time) string {
	code := strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return r
		}
		return '_'
	}, projectCode)
	return fmt.Sprintf("%s_export_%s.zip", code, finished.In(exportZone).Format("20060102"))
}

// ContentDisposition 是响应头 Content-Disposition 的值：attachment，filename 是 ASCII 的兜底名，filename* 是按 RFC 5987
// 编码的 UTF-8 文件名。
func ContentDisposition(asciiName, name string) string {
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiName, rfc5987(name))
}

// rfc5987 是 RFC 5987 / 8187 的 ext-value 编码：attr-char 以外的字节都按 %XX 编码。
func rfc5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
