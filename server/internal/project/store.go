package project

import (
	"context"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
)

// project 的 SQL：projects、project_members、project_roles。
// 跨模块的只读查询：folders（项目的根文件夹）、user_roles、roles（是不是 admin、角色授权）。

// MyProject 是 `GET /api/projects` 的一项。
type MyProject struct {
	ID          uuid.UUID `json:"id"`
	ProjectCode string    `json:"project_code"`
	ProjectName string    `json:"project_name"`
	Status      string    `json:"status"`
	// RootFolderID 是根文件夹：文件目录、热文件夹从这里开始。
	RootFolderID uuid.UUID `json:"root_folder_id"`
}

// listMyProjects：用户能进的未删除项目（含已归档的），按编码排序。
// 能进 = admin，或在 project_members 里，或拥有 project_roles 里的某个角色（requirements 第 4 节"项目授权"）。
func listMyProjects(ctx context.Context, q db.Querier, userID uuid.UUID) ([]MyProject, error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.project_code, p.project_name, p.status, f.id
FROM projects p
JOIN folders f ON f.project_id = p.id AND f.parent_id IS NULL
WHERE p.deleted_at IS NULL
  AND (EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
               WHERE ur.user_id = $1 AND r.role_code = $2)
    OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
    OR EXISTS (SELECT 1 FROM project_roles pr JOIN user_roles ur ON ur.role_id = pr.role_id
               WHERE pr.project_id = p.id AND ur.user_id = $1))
ORDER BY p.project_code, p.id`, userID, auth.AdminRoleCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MyProject{}
	for rows.Next() {
		var p MyProject
		if err := rows.Scan(&p.ID, &p.ProjectCode, &p.ProjectName, &p.Status, &p.RootFolderID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
