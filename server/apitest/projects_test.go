package apitest

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestMyProjects 是 GET /api/projects：admin 看到全部未删除项目；其他人看到个人授权和角色授权的；按编码排序。
// 项目的增删改和授权接口在 M1-3，这里直接在库里造数据。
func TestMyProjects(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	adminID := env.UserID("admin")
	mk := func(code string, deleted bool) uuid.UUID {
		id, root := uuid.New(), uuid.New()
		env.Exec(`INSERT INTO projects (id, project_code, project_name, created_by, deleted_at)
VALUES ($1, $2, $2 || ' 名称', $3, CASE WHEN $4 THEN now() END)`, id, code, adminID, deleted)
		env.Exec(`INSERT INTO folders (id, project_id, folder_name, path_code, created_by) VALUES ($1, $2, $3, '00001.', $4)`,
			root, id, code, adminID)
		return id
	}
	pB := mk("P-B", false)
	pA := mk("P-A", false)
	mk("P-C", false)
	pD := mk("P-D", true) // 已删除

	scannerID := env.PersonaID("scanner")
	env.Exec(`INSERT INTO project_members (project_id, user_id) VALUES ($1, $2), ($3, $2)`, pB, scannerID, pD)
	env.Exec(`INSERT INTO project_roles (project_id, role_id) VALUES ($1, $2)`, pA, env.RoleID("reviewer"))

	codes := func(c *Client) string {
		var list []struct {
			ID           uuid.UUID `json:"id"`
			ProjectCode  string    `json:"project_code"`
			ProjectName  string    `json:"project_name"`
			Status       string    `json:"status"`
			RootFolderID uuid.UUID `json:"root_folder_id"`
		}
		c.Get("/api/projects").Expect(200).JSON(&list)
		var out []string
		for _, p := range list {
			if p.RootFolderID == uuid.Nil || p.Status != "active" || p.ProjectName != p.ProjectCode+" 名称" {
				t.Fatalf("项目：%+v", p)
			}
			out = append(out, p.ProjectCode)
		}
		return strings.Join(out, ",")
	}
	if got := codes(env.Admin()); got != "P-A,P-B,P-C" {
		t.Fatalf("admin：%s", got)
	}
	if got := codes(env.As("scanner")); got != "P-B" {
		t.Fatalf("个人授权：%s", got)
	}
	if got := codes(env.As("reviewer")); got != "P-A" {
		t.Fatalf("角色授权：%s", got)
	}
	if got := codes(env.As("none")); got != "" {
		t.Fatalf("没有授权：%s", got)
	}
}
