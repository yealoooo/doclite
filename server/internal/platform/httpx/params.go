package httpx

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// 分页：page 从 1 开始；page_size 默认 20、最大 100（requirements 5.2）。
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// PageRequest 是列表接口的分页参数。
type PageRequest struct {
	Page     int
	PageSize int
}

// Limit 是 SQL 的 LIMIT。
func (p PageRequest) Limit() int { return p.PageSize }

// Offset 是 SQL 的 OFFSET。
func (p PageRequest) Offset() int { return (p.Page - 1) * p.PageSize }

// Page 是一页数据，`{items, total, page, page_size}`。
type Page[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// NewPage 组装一页；items 为 nil 时输出 `[]`。
func NewPage[T any](items []T, total int, req PageRequest) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Page: req.Page, PageSize: req.PageSize}
}

// ParsePage 读查询参数 page、page_size。不是整数、page < 1、page_size 不在 1–100 之间时 400 `invalid_input`。
func ParsePage(r *http.Request) (PageRequest, error) {
	q := r.URL.Query()
	p := PageRequest{Page: 1, PageSize: DefaultPageSize}
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, Invalid("page 要是从 1 开始的整数")
		}
		p.Page = n
	}
	if v := strings.TrimSpace(q.Get("page_size")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxPageSize {
			return p, Invalid("page_size 要在 1 到 %d 之间", MaxPageSize)
		}
		p.PageSize = n
	}
	// 页号太大时 OFFSET 会溢出，按参数不对处理。
	if p.Page > 1_000_000 {
		return p, Invalid("page 太大")
	}
	return p, nil
}

// PathUUID 读路径参数 name（例如 `{id}`），不是合法的 UUID 时 400 `invalid_input`。
func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, Invalid("路径参数 %s 不是合法的 UUID", name)
	}
	return id, nil
}
