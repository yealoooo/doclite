package audit

// SQL。跨模块的只读查询：列表 LEFT JOIN users、projects 取操作人显示名和项目名（一页一条语句，不逐条查）；筛选栏的
// 选项读 users、projects 全表（用户、项目都不多）。

import (
	"context"
	"fmt"
	"strings"

	"doclite/server/internal/platform/db"
)

// listLogs 是一页记录（多取一条，判断还有没有下一页），新的在前。按时间筛选走 created_at 的 BRIN 索引（0007）。
func listLogs(ctx context.Context, q db.Querier, f Filter) ([]Log, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Cursor > 0 {
		add("id < $%d", f.Cursor)
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
