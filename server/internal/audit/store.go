package audit

// SQL。跨模块的只读查询：列表 LEFT JOIN users、projects 取操作人显示名和项目名（一页一条语句，不逐条查）；筛选栏的
// 选项读 users、projects 全表（用户、项目都不多）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"doclite/server/internal/platform/db"
)

// timeSlack：created_at 是写审计的事务开始的时间，id 是插入时取的，两者只是基本同序——一个事务开始得早、插入得晚，
// 它的 created_at 会比前面几条 id 小的还早。把时间范围换成 id 范围时两头各放宽这么多（远大于任何写审计的事务的时长），
// 再在范围里按 created_at 精确过滤。
const timeSlack = time.Hour

// idRange 是时间范围换成的 id 范围 [lo, hi)；0 表示那一头不限。empty：范围里一条都没有。
type idRange struct {
	lo, hi int64
	empty  bool
}

// timeToIDs 把 [from, to) 换成 id 的范围（两头各放宽 timeSlack）。
func timeToIDs(ctx context.Context, q db.Querier, from, to *time.Time) (idRange, error) {
	var lo, hi int64
	if err := q.QueryRow(ctx, `SELECT coalesce(min(id), 0), coalesce(max(id), 0) FROM audit_logs`).Scan(&lo, &hi); err != nil {
		return idRange{}, err
	}
	if hi == 0 {
		return idRange{empty: true}, nil
	}
	var r idRange
	var err error
	if from != nil {
		if r.lo, err = idBound(ctx, q, lo, hi+1, from.Add(-timeSlack)); err != nil {
			return idRange{}, err
		}
	}
	if to != nil {
		if r.hi, err = idBound(ctx, q, lo, hi+1, to.Add(timeSlack)); err != nil {
			return idRange{}, err
		}
	}
	r.empty = r.lo > hi || (to != nil && r.hi <= lo) || (r.hi != 0 && r.lo >= r.hi)
	return r, nil
}

// idBound 在 [lo, hi) 里二分查找第一条 created_at >= t 的记录的 id（按 id 和 created_at 同序找；id 有空洞时可能返回
// 空洞里的一个值，当边界用效果一样）；都早于 t 时返回 hi。每一步是主键上的一次点查。
func idBound(ctx context.Context, q db.Querier, lo, hi int64, t time.Time) (int64, error) {
	for lo < hi {
		mid := lo + (hi-lo)/2
		var id int64
		var at time.Time
		err := q.QueryRow(ctx, `SELECT id, created_at FROM audit_logs WHERE id >= $1 ORDER BY id LIMIT 1`, mid).Scan(&id, &at)
		switch {
		case db.IsNoRows(err) || (err == nil && id >= hi):
			hi = mid // [mid, hi) 里没有记录
		case err != nil:
			return 0, err
		case !at.Before(t):
			hi = id
		default:
			lo = id + 1
		}
	}
	return lo, nil
}

// listLogs 是一页记录（多取一条，判断还有没有下一页），新的在前。
func listLogs(ctx context.Context, q db.Querier, f Filter, b idRange) ([]Log, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Cursor > 0 {
		add("id < $%d", f.Cursor)
	}
	if b.lo > 0 {
		add("id >= $%d", b.lo)
	}
	if b.hi > 0 {
		add("id < $%d", b.hi)
	}
	if f.From != nil {
		add("created_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("created_at < $%d", *f.To)
	}
	if f.OperatorID != nil {
		add("operator_id = $%d", *f.OperatorID)
	}
	if f.ProjectID != nil {
		add("project_id = $%d", *f.ProjectID)
	}
	if f.TargetType != "" {
		add("target_type = $%d", f.TargetType)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, fmt.Sprintf(`SELECT a.id, a.created_at, a.operator_id, u.display_name, u.username,
  a.project_id, p.project_code, p.project_name, a.target_type, a.target_id, a.action, a.detail
FROM (SELECT * FROM audit_logs %s ORDER BY id DESC LIMIT $%d) a
LEFT JOIN users u ON u.id = a.operator_id
LEFT JOIN projects p ON p.id = a.project_id
ORDER BY a.id DESC`, cond, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Log
	for rows.Next() {
		var l Log
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.OperatorID, &l.OperatorName, &l.OperatorUsername, &l.ProjectID,
			&l.ProjectCode, &l.ProjectName, &l.TargetType, &l.TargetID, &l.Action, &l.Detail); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func filterOptions(ctx context.Context, q db.Querier) (*FilterOptions, error) {
	out := &FilterOptions{Users: []OptionUser{}, Projects: []OptionProject{}}
	rows, err := q.Query(ctx, `SELECT id, username, display_name, status FROM users ORDER BY lower(username), id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u OptionUser
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status); err != nil {
			rows.Close()
			return nil, err
		}
		out.Users = append(out.Users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT id, project_code, project_name, deleted_at IS NOT NULL FROM projects
ORDER BY deleted_at IS NOT NULL, project_code, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p OptionProject
		if err := rows.Scan(&p.ID, &p.ProjectCode, &p.ProjectName, &p.Deleted); err != nil {
			return nil, err
		}
		out.Projects = append(out.Projects, p)
	}
	return out, rows.Err()
}
