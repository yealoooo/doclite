package export

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/catalog"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/project"
)

// 预检（requirements 5.11"预检"，用户决定 Q7）：就绪 = 不在修改中、（require_locked_item 时）已锁定、档号不空、
// ≥ 1 页、（勾了"只导出审核通过的"时）全部页 approved。原因码 source_modifying（来源文件夹修改中，2026-10-19，不管
// require_locked_item）、not_locked、no_archive_no、no_pages、pages_not_approved（带不是通过的页数和前 5 页）。不就绪的
// 条目最多列 500 件；件数、页数、各原因件数是全部的。单任务 ≤ 20 万页（按就绪的页数）。

// 审核状态的叫法（"第 3 页：待审"）。
var reviewLabels = map[string]string{"unassigned": "待分配", "pending": "待审", "approved": "通过", "rejected": "驳回"}

func reviewLabel(s string) string {
	if l, ok := reviewLabels[s]; ok {
		return l
	}
	return s
}

// 条目状态的叫法。
var itemStatusLabels = map[string]string{"draft": "未锁定", "locked": "已锁定"}

// PageRef 是不就绪原因里举例的一页。
type PageRef struct {
	PageNo       int       `json:"page_no"`
	FileID       uuid.UUID `json:"file_id"`
	FileName     string    `json:"file_name"`
	ReviewStatus string    `json:"review_status"`
}

// NotReadyReason 是一个条目不能导出的一种原因。
type NotReadyReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// ItemStatus：not_locked 时条目现在的状态。
	ItemStatus *string `json:"item_status,omitempty"`
	// Count、Pages：pages_not_approved 时不是通过的页数和前 5 页。
	Count *int      `json:"count,omitempty"`
	Pages []PageRef `json:"pages,omitempty"`
}

// NotReadyItem 是预检里不能导出的条目（按导出顺序）。
type NotReadyItem struct {
	ItemID        uuid.UUID        `json:"item_id"`
	CategoryID    uuid.UUID        `json:"category_id"`
	CategoryNames []string         `json:"category_names"`
	ItemNo        int              `json:"item_no"`
	ArchiveNo     *string          `json:"archive_no"`
	Title         string           `json:"title"`
	Status        string           `json:"status"`
	PageCount     int              `json:"page_count"`
	Reasons       []NotReadyReason `json:"reasons"`
}

// ReasonCounts 是各原因涉及的件数（一件可以有几种原因）。
type ReasonCounts struct {
	SourceModifying  int `json:"source_modifying"`
	NotLocked        int `json:"not_locked"`
	NoArchiveNo      int `json:"no_archive_no"`
	NoPages          int `json:"no_pages"`
	PagesNotApproved int `json:"pages_not_approved"`
}

// summary："1 件修改中，3 件没有锁定，1 件没有档号"。
func (c ReasonCounts) summary() string {
	var parts []string
	for _, x := range []struct {
		n    int
		what string
	}{{c.SourceModifying, "修改中（来源文件夹解除了封存）"}, {c.NotLocked, "没有锁定"}, {c.NoArchiveNo, "没有档号"}, {c.NoPages, "没有页"}, {c.PagesNotApproved, "有页不是审核通过"}} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf("%d 件%s", x.n, x.what))
		}
	}
	return strings.Join(parts, "，")
}

// Precheck 是预检的结果（`POST …/export-tasks:precheck` 的响应；建任务 409 时也放在 details 里）。
type Precheck struct {
	ScopeType string      `json:"scope_type"`
	ScopeIDs  []uuid.UUID `json:"scope_ids"`
	// ResubmitOf：补交的哪个任务；这时 scope_ids 是算出来的条目。
	ResubmitOf        *uuid.UUID     `json:"resubmit_of"`
	ExportFormat      string         `json:"export_format"`
	OnlyApproved      bool           `json:"only_approved"`
	RequireLockedItem bool           `json:"require_locked_item"`
	ItemCount         int            `json:"item_count"`
	PageCount         int            `json:"page_count"`
	ReadyItemCount    int            `json:"ready_item_count"`
	ReadyPageCount    int            `json:"ready_page_count"`
	NotReadyCount     int            `json:"not_ready_count"`
	NotReadyPageCount int            `json:"not_ready_page_count"`
	ReasonCounts      ReasonCounts   `json:"reason_counts"`
	NotReady          []NotReadyItem `json:"not_ready"`
	NotReadyTruncated bool           `json:"not_ready_truncated"`
	MaxPageCount      int            `json:"max_page_count"`
	PageLimitExceeded bool           `json:"page_limit_exceeded"`
	PageLimitMessage  *string        `json:"page_limit_message"`
}

// notReadyReasons 是一个条目不能导出的原因（顺序：修改中、锁定、档号、页、审核）；就绪时为空。
func notReadyReasons(it *scopeItem, requireLocked, onlyApproved bool) []NotReadyReason {
	var out []NotReadyReason
	if it.SourceStatus == "modifying" {
		out = append(out, NotReadyReason{Code: "source_modifying",
			Message: "来源文件夹解除了封存、正在修改，要先重新归档"})
	}
	if requireLocked && it.Status != "locked" {
		st := it.Status
		out = append(out, NotReadyReason{Code: "not_locked", ItemStatus: &st,
			Message: fmt.Sprintf("没有锁定（现在是“%s”），项目设置要求导出的条目已锁定", itemStatusLabels[st])})
	}
	if it.ArchiveNo == nil || *it.ArchiveNo == "" {
		out = append(out, NotReadyReason{Code: "no_archive_no", Message: "没有档号（条目详情里有原因）"})
	}
	if it.PageCount == 0 {
		out = append(out, NotReadyReason{Code: "no_pages", Message: "没有页"})
	}
	if onlyApproved && it.Unapproved > 0 {
		n := it.Unapproved
		pages := it.Examples
		if len(pages) > MaxExamplePages {
			pages = pages[:MaxExamplePages]
		}
		shown := make([]string, 0, messageExamples)
		for _, p := range pages {
			if len(shown) == messageExamples {
				break
			}
			shown = append(shown, fmt.Sprintf("第 %d 页：%s", p.PageNo, reviewLabel(p.ReviewStatus)))
		}
		more := ""
		if n > len(shown) {
			more = "……"
		}
		out = append(out, NotReadyReason{Code: "pages_not_approved", Count: &n, Pages: pages,
			Message: fmt.Sprintf("有 %d 页不是审核通过（%s%s）", n, strings.Join(shown, "；"), more)})
	}
	return out
}

// evaluate 按就绪规则评估范围里的条目（items 已按导出顺序），返回预检结果和每个条目是否就绪。
func evaluate(req *request, requireLocked bool, items []*scopeItem) (*Precheck, []bool) {
	p := &Precheck{
		ScopeType: req.ScopeType, ScopeIDs: req.ScopeIDs, ResubmitOf: req.ResubmitOf, ExportFormat: string(req.Format),
		OnlyApproved: req.OnlyApproved, RequireLockedItem: requireLocked, NotReady: []NotReadyItem{}, MaxPageCount: MaxTaskPages,
	}
	if p.ScopeIDs == nil {
		p.ScopeIDs = []uuid.UUID{}
	}
	ready := make([]bool, len(items))
	for i, it := range items {
		p.ItemCount++
		p.PageCount += it.PageCount
		reasons := notReadyReasons(it, requireLocked, req.OnlyApproved)
		if len(reasons) == 0 {
			ready[i] = true
			p.ReadyItemCount++
			p.ReadyPageCount += it.PageCount
			continue
		}
		p.NotReadyCount++
		p.NotReadyPageCount += it.PageCount
		for _, r := range reasons {
			switch r.Code {
			case "source_modifying":
				p.ReasonCounts.SourceModifying++
			case "not_locked":
				p.ReasonCounts.NotLocked++
			case "no_archive_no":
				p.ReasonCounts.NoArchiveNo++
			case "no_pages":
				p.ReasonCounts.NoPages++
			case "pages_not_approved":
				p.ReasonCounts.PagesNotApproved++
			}
		}
		if len(p.NotReady) >= MaxListed {
			p.NotReadyTruncated = true
			continue
		}
		p.NotReady = append(p.NotReady, NotReadyItem{
			ItemID: it.ID, CategoryID: it.CategoryID, CategoryNames: it.CategoryNames, ItemNo: it.ItemNo,
			ArchiveNo: it.ArchiveNo, Title: it.Title, Status: it.Status, PageCount: it.PageCount, Reasons: reasons,
		})
	}
	if p.ReadyPageCount > MaxTaskPages {
		p.PageLimitExceeded = true
		m := fmt.Sprintf("这次要导出 %d 页，超过单个导出任务的上限 %d 页，请按分类分批导出", p.ReadyPageCount, MaxTaskPages)
		p.PageLimitMessage = &m
	}
	return p, ready
}

// itemName 是条目的称呼：有档号用档号，没有用"第 n 件"。
func itemName(archiveNo *string, itemNo int) string {
	if archiveNo != nil && *archiveNo != "" {
		return *archiveNo
	}
	return fmt.Sprintf("第 %d 件", itemNo)
}

// checkCreatable：建任务的前提（范围里有条目、至少一件就绪、only_ready 为 false 时全部就绪、页数不超过上限），
// 不满足 409 invalid_state，details 是预检结果。
func checkCreatable(p *Precheck, onlyReady bool) error {
	if p.ItemCount == 0 {
		return errNotReady("导出范围里没有条目（未删除的），没有可以导出的", p)
	}
	example := ""
	if len(p.NotReady) > 0 && len(p.NotReady[0].Reasons) > 0 {
		i := p.NotReady[0]
		example = fmt.Sprintf("，例如 %s：%s", itemName(i.ArchiveNo, i.ItemNo), i.Reasons[0].Message)
	}
	if p.ReadyItemCount == 0 {
		return errNotReady(fmt.Sprintf("导出范围里的 %d 件都不满足导出条件（%s）%s", p.ItemCount, p.ReasonCounts.summary(), example), p)
	}
	if !onlyReady && p.NotReadyCount > 0 {
		return errNotReady(fmt.Sprintf("有 %d 件不满足导出条件（%s）%s；可以只导出就绪的 %d 件，或者先处理这些条目",
			p.NotReadyCount, p.ReasonCounts.summary(), example, p.ReadyItemCount), p)
	}
	if p.PageLimitMessage != nil {
		return errNotReady(*p.PageLimitMessage, p)
	}
	return nil
}

// scoped 是在一个快照里读出的范围和预检。
type scoped struct {
	settings *project.Settings
	scope    *resolved
	items    []*scopeItem
	precheck *Precheck
	ready    []bool
}

// readScope 在快照 tx 里读设置（第一条语句）、范围、条目和页，做预检。
func (s *Service) readScope(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, req *request) (*scoped, error) {
	st, err := project.ReadSettings(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	out := &scoped{settings: st}
	if out.scope, err = s.resolveScope(ctx, tx, projectID, req); err != nil {
		return nil, err
	}
	if len(out.scope.itemIDs) > 0 {
		if out.items, err = scopeItems(ctx, tx, projectID, out.scope.itemIDs); err != nil {
			return nil, err
		}
	}
	catIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, it := range out.items {
		if !seen[it.CategoryID] {
			seen[it.CategoryID] = true
			catIDs = append(catIDs, it.CategoryID)
		}
	}
	names, err := catalog.CategoryNames(ctx, tx, catIDs)
	if err != nil {
		return nil, err
	}
	for _, it := range out.items {
		it.CategoryNames = names[it.CategoryID]
	}
	out.precheck, out.ready = evaluate(req, out.settings.RequireLockedItem, out.items)
	return out, nil
}

// Precheck 是预检（不写库）。
func (s *Service) Precheck(ctx context.Context, projectID uuid.UUID, body *RequestBody) (*Precheck, error) {
	req, err := parseRequest(body)
	if err != nil {
		return nil, err
	}
	var out *Precheck
	err = db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		sc, err := s.readScope(ctx, tx, projectID, req)
		if err != nil {
			return err
		}
		out = sc.precheck
		return nil
	})
	return out, err
}
