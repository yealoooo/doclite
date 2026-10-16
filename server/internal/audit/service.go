package audit

// 审计日志的查询（M7-3，"系统 → 审计日志"页，菜单 system.audit；用户 2026-10-16：做简单版）：只读，按时间、操作人、
// target_type、action、项目筛选，按 id 倒序游标分页（不用 OFFSET），带操作人显示名、项目名。
//
// 时间范围靠"id 和 created_at 基本同序"先换成 id 的范围（在主键上二分查找，见 store.go 的 idBound），再在这个范围里
// 按 created_at 精确过滤；表上没有 created_at 的索引，不改表结构。

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/httpx"
)

// 每页条数。
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// maxNameLen：target_type、action 筛选值的长度上限。
const maxNameLen = 64

// Service 是审计日志的查询（写在 audit.go 的 [Write]，由各模块在自己的事务里调用）。
type Service struct {
	pool *pgxpool.Pool
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Filter 是查询条件；零值表示不限。
type Filter struct {
	// From、To：created_at 在 [From, To) 里。
	From, To   *time.Time
	OperatorID *uuid.UUID
	ProjectID  *uuid.UUID
	TargetType string
	Action     string
	// Cursor 是上一页最后一条的 id（只取比它小的）；0 是第一页。
	Cursor int64
	Limit  int
}

// Log 是一条审计记录。操作人、项目是当前的名字（不是记录时的）；操作人为空表示系统（初始管理员、初始存储）。
type Log struct {
	ID               int64           `json:"id"`
	CreatedAt        time.Time       `json:"created_at"`
	OperatorID       *uuid.UUID      `json:"operator_id"`
	OperatorName     *string         `json:"operator_name"`
	OperatorUsername *string         `json:"operator_username"`
	ProjectID        *uuid.UUID      `json:"project_id"`
	ProjectCode      *string         `json:"project_code"`
	ProjectName      *string         `json:"project_name"`
	TargetType       string          `json:"target_type"`
	TargetID         uuid.UUID       `json:"target_id"`
	Action           string          `json:"action"`
	Detail           json.RawMessage `json:"detail"`
}

// LogList 是 `GET /api/system/audit-logs` 的响应。
type LogList struct {
	Items []Log `json:"items"`
	// NextCursor 是下一页的 cursor（原样传回）；没有下一页时为 null。
	NextCursor *string `json:"next_cursor"`
}

// ParseFilter 读查询参数：from、to（RFC 3339）、operator_id、project_id（UUID）、target_type、action、cursor、limit
// （默认 50，1–200）。不合法 400 invalid_input。
func ParseFilter(get func(string) string) (Filter, error) {
	f := Filter{Limit: DefaultLimit}
	parseTime := func(name string) (*time.Time, error) {
		v := strings.TrimSpace(get(name))
		if v == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, httpx.Invalid("%s 要是 RFC 3339 格式的时间（例如 2026-10-16T00:00:00+08:00）", name)
		}
		return &t, nil
	}
	parseID := func(name string) (*uuid.UUID, error) {
		v := strings.TrimSpace(get(name))
		if v == "" {
			return nil, nil
		}
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, httpx.Invalid("%s 不是合法的 UUID", name)
		}
		return &id, nil
	}
	var err error
	if f.From, err = parseTime("from"); err != nil {
		return f, err
	}
	if f.To, err = parseTime("to"); err != nil {
		return f, err
	}
	if f.OperatorID, err = parseID("operator_id"); err != nil {
		return f, err
	}
	if f.ProjectID, err = parseID("project_id"); err != nil {
		return f, err
	}
	for _, p := range []struct {
		name string
		dst  *string
	}{{"target_type", &f.TargetType}, {"action", &f.Action}} {
		v := strings.TrimSpace(get(p.name))
		if len(v) > maxNameLen {
			return f, httpx.Invalid("%s 太长", p.name)
		}
		*p.dst = v
	}
	if v := strings.TrimSpace(get("cursor")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return f, httpx.Invalid("cursor 不对：要原样传回上一页的 next_cursor")
		}
		f.Cursor = n
	}
	if v := strings.TrimSpace(get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			return f, httpx.Invalid("limit 要在 1 到 %d 之间", MaxLimit)
		}
		f.Limit = n
	}
	return f, nil
}

// List 查一页审计记录，新的在前。
func (s *Service) List(ctx context.Context, f Filter) (*LogList, error) {
	out := &LogList{Items: []Log{}}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return out, nil
	}
	var b idRange
	if f.From != nil || f.To != nil {
		var err error
		if b, err = timeToIDs(ctx, s.pool, f.From, f.To); err != nil {
			return nil, err
		}
		if b.empty {
			return out, nil
		}
	}
	rows, err := listLogs(ctx, s.pool, f, b)
	if err != nil {
		return nil, err
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		c := strconv.FormatInt(rows[len(rows)-1].ID, 10)
		out.NextCursor = &c
	}
	out.Items = append(out.Items, rows...)
	return out, nil
}

// FilterOptions 是筛选栏的选项：全部用户（含停用的）和全部项目（含已删除的）。看审计的人不一定有用户、项目菜单，
// 所以单给一个接口（同 projects 的 access-candidates）。
type FilterOptions struct {
	Users    []OptionUser    `json:"users"`
	Projects []OptionProject `json:"projects"`
}

// OptionUser 是筛选栏里的一个操作人。
type OptionUser struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
}

// OptionProject 是筛选栏里的一个项目。
type OptionProject struct {
	ID          uuid.UUID `json:"id"`
	ProjectCode string    `json:"project_code"`
	ProjectName string    `json:"project_name"`
	Deleted     bool      `json:"deleted"`
}

// Options 是筛选栏的选项。
func (s *Service) Options(ctx context.Context) (*FilterOptions, error) {
	return filterOptions(ctx, s.pool)
}
