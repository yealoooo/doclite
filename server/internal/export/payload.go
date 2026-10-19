package export

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// "当时的状态"（requirements 5.11"冻结"）记在 background_jobs.payload，建任务时和冻结的页在同一个快照里写下，
// 以后不再改写；job 行永久保留，重试复用同一行。执行时生成目录、PDF 用这里的条目信息，交付后变更拿它和现在的值比。
//
//	{
//	  "export_task_id": "…", "payload_version": 1, "resubmit_of": null,
//	  "only_ready": true, "require_locked_item": true,
//	  "item_count": 3, "page_count": 9, "skipped_item_count": 2,
//	  "scope_categories": [{"id": "…", "category_names": ["全宗", "文书"]}],
//	  "item_fields": [{"key": "responsible", "label": "责任者", "type": "text"}],
//	  "items": [{"id", "category_id", "category_names", "item_no", "archive_no", "title", "extra_fields", "status",
//	             "row_version", "page_count"}]
//	}
//
// items 按导出顺序。列表、详情只取 `payload - 'items'`（每个 job 读一次后缓存在进程里，见 store.go 的 fillHeaders）。
// 读 payload 先查 payload_version，不认识是内部错误。

// PayloadVersion 是 payload 的格式版本。
const PayloadVersion = 1

type payload struct {
	ExportTaskID      uuid.UUID       `json:"export_task_id"`
	PayloadVersion    int             `json:"payload_version"`
	ResubmitOf        *uuid.UUID      `json:"resubmit_of"`
	OnlyReady         bool            `json:"only_ready"`
	RequireLockedItem bool            `json:"require_locked_item"`
	ItemCount         int             `json:"item_count"`
	PageCount         int             `json:"page_count"`
	SkippedItemCount  int             `json:"skipped_item_count"`
	ScopeCategories   []ScopeCategory `json:"scope_categories"`
	ItemFields        []itemField     `json:"item_fields"`
	Items             []frozenItem    `json:"items,omitempty"`
}

// ScopeCategory 是范围里一个节点的名称链（根节点是空数组）。
type ScopeCategory struct {
	ID            uuid.UUID `json:"id"`
	CategoryNames []string  `json:"category_names"`
}

// itemField 是建任务时的著录项（目录.xlsx 的列、交付后变更的名称）。
type itemField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// frozenItem 是冻结时的一个条目。
type frozenItem struct {
	ID            uuid.UUID      `json:"id"`
	CategoryID    uuid.UUID      `json:"category_id"`
	CategoryNames []string       `json:"category_names"`
	ItemNo        int            `json:"item_no"`
	ArchiveNo     *string        `json:"archive_no"`
	Title         string         `json:"title"`
	ExtraFields   map[string]any `json:"extra_fields"`
	Status        string         `json:"status"`
	RowVersion    int            `json:"row_version"`
	PageCount     int            `json:"page_count"`
}

func errInternal(format string, args ...any) error {
	return fmt.Errorf("export: "+format, args...)
}

// parsePayload 解析整个 payload 并查版本。
func parsePayload(raw []byte) (*payload, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errInternal("导出任务的 payload 不对：%v", err)
	}
	if p.PayloadVersion != PayloadVersion {
		return nil, errInternal("导出任务的 payload 是第 %d 版格式，这个版本的 server 只认第 %d 版", p.PayloadVersion, PayloadVersion)
	}
	return &p, nil
}

// itemFields 是建任务时冻结的著录项。
func itemFields(defs []project.FieldDef) []itemField {
	out := make([]itemField, len(defs))
	for i, d := range defs {
		out[i] = itemField{Key: d.Key, Label: d.Label, Type: d.Type}
	}
	return out
}

// errNotReady 是建任务不满足条件：409 invalid_state，details 是预检结果。
func errNotReady(message string, p *Precheck) error {
	return httpx.InvalidState(message).WithDetails(p)
}
