package apitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPermsAudit 是审计日志接口的菜单权限：只要 system.audit。
func TestPermsAudit(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                              admin  system.audit  system.users  system.projects  manager  none
	GET    /api/system/audit-logs                   Y         Y              -              -            -       -
	GET    /api/system/audit-logs/filter-options    Y         Y              -              -            -       -
	`)
}

type auditLog struct {
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

type auditList struct {
	Items      []auditLog `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// auditQuery 查一页；q 是查询参数（不含问号）。
func auditQuery(c *Client, q string) auditList {
	c.t.Helper()
	return Decode[auditList](c.Get("/api/system/audit-logs?" + q).Expect(http.StatusOK))
}

func auditIDs(items []auditLog) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// TestAuditLogs：列表（新的在前，带操作人显示名、项目编码和名称、detail）、按操作人 / target_type / action / 项目 /
// 时间筛选、游标翻页走完和整页一致、不认识的动作原样返回、参数不对 400、筛选栏的选项。
func TestAuditLogs(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	adminID := env.UserID(AdminUsername)

	// 另一个操作人：能建项目的自建角色。
	env.CreateRole("aud_proj", "system.projects", "system.audit")
	opID := env.CreateUser("aud_op", "aud_proj")
	op := env.Login("aud_op", DefaultPassword)
	var p2 projectDetail
	op.Post("/api/system/projects", map[string]any{"project_code": "AUD2", "project_name": "审计二"}).Expect(201).JSON(&p2)
	p1 := env.CreateProject("AUD1")
	mkdir(admin, p1.RootFolderID, "盒 1")
	admin.Delete("/api/system/projects/" + p2.ID.String()).Expect(http.StatusNoContent)

	total := QueryValue[int](env, `SELECT count(*) FROM audit_logs`)
	all := auditQuery(admin, "limit=200")
	if len(all.Items) != total || all.NextCursor != nil || total < 7 {
		t.Fatalf("全部：%d 条（库里 %d），next_cursor %v", len(all.Items), total, all.NextCursor)
	}
	ids := auditIDs(all.Items)
	if !slices.IsSortedFunc(ids, func(a, b int64) int { return int(b - a) }) {
		t.Fatalf("应该按 id 倒序：%v", ids)
	}
	// 最新一条：aud_op 建的项目被 admin 删了。
	if it := all.Items[0]; it.TargetType != "project" || it.Action != "delete" || *it.OperatorID != adminID ||
		*it.OperatorName != "系统管理员" || *it.OperatorUsername != AdminUsername || *it.ProjectID != p2.ID ||
		*it.ProjectCode != "AUD2" || *it.ProjectName != "审计二" || !strings.Contains(string(it.Detail), `"before"`) {
		t.Fatalf("最新一条：%+v %s", it, it.Detail)
	}
	// 系统写的（初始管理员）：操作人为空。
	if it := all.Items[len(all.Items)-1]; it.OperatorID != nil || it.OperatorName != nil || it.TargetType != "user" || it.Action != "create" {
		t.Fatalf("最早一条：%+v", it)
	}

	// 按条件筛选：结果和库里一致。
	for _, c := range []struct {
		q, where string
		args     []any
	}{
		{"operator_id=" + opID.String(), "operator_id = $1", []any{opID}},
		{"project_id=" + p1.ID.String(), "project_id = $1", []any{p1.ID}},
		{"target_type=project", "target_type = 'project'", nil},
		{"target_type=project&action=create", "target_type = 'project' AND action = 'create'", nil},
		{"action=create&operator_id=" + adminID.String(), "action = 'create' AND operator_id = $1", []any{adminID}},
		{"action=no_such_action", "false", nil},
	} {
		got := auditQuery(admin, c.q+"&limit=200")
		want := QueryValue[[]int64](env, `SELECT coalesce(array_agg(id ORDER BY id DESC), '{}') FROM audit_logs WHERE `+c.where, c.args...)
		if !slices.Equal(auditIDs(got.Items), want) {
			t.Errorf("%s：%v，应为 %v", c.q, auditIDs(got.Items), want)
		}
	}
	if got := auditQuery(admin, "project_id="+p1.ID.String()); len(got.Items) < 2 || *got.Items[0].ProjectCode != "AUD1" ||
		got.Items[0].TargetType != "folder" {
		t.Fatalf("项目 AUD1：%+v", got.Items)
	}

	// 游标翻页：每页 3 条走完，和一次取全部一致。
	var walked []int64
	cursor := ""
	for range 100 {
		page := auditQuery(admin, "limit=3"+cursor)
		walked = append(walked, auditIDs(page.Items)...)
		if page.NextCursor == nil {
			break
		}
		if len(page.Items) != 3 {
			t.Fatalf("不是最后一页却只有 %d 条", len(page.Items))
		}
		cursor = "&cursor=" + *page.NextCursor
	}
	if !slices.Equal(walked, ids) {
		t.Fatalf("翻页：%v，应为 %v", walked, ids)
	}

	// 时间范围：在现有记录之后造几条（created_at 和 id 同序），其中一条比前一条早 10 分钟（写审计的事务开始得早、
	// 插入得晚），时间范围换成 id 范围时也不能漏掉它；另有一条不认识的动作（存储迁移的）。
	t0 := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Hour)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	for _, c := range []struct {
		d                  time.Duration
		targetType, action string
	}{
		{0, "user", "t0"}, {time.Hour, "user", "t1"}, {2 * time.Hour, "user", "t2"}, {2*time.Hour - 10*time.Minute, "user", "t2_late"},
		{3 * time.Hour, "user", "t3"}, {4 * time.Hour, "file_snapshot", "storage_migrate"},
	} {
		env.Exec(`INSERT INTO audit_logs (target_type, target_id, action, detail, created_at) VALUES ($1, $2, $3, '{"x": 1}', $4)`,
			c.targetType, uuid.New(), c.action, at(c.d))
	}
	rfc := func(t time.Time) string { return url.QueryEscape(t.Format(time.RFC3339)) }
	actions := func(l auditList) string {
		var s []string
		for _, it := range l.Items {
			s = append(s, it.Action)
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct{ q, want string }{
		{"from=" + rfc(at(time.Hour)) + "&to=" + rfc(at(3*time.Hour)), "t2_late,t2,t1"},
		{"from=" + rfc(at(110*time.Minute)) + "&to=" + rfc(at(2*time.Hour)), "t2_late"},
		{"from=" + rfc(at(3*time.Hour)), "storage_migrate,t3"},
		{"from=" + rfc(at(time.Hour)) + "&to=" + rfc(at(110*time.Minute)), "t1"},
		{"from=" + rfc(at(time.Hour)) + "&target_type=file_snapshot", "storage_migrate"},
		{"to=" + rfc(at(time.Hour)) + "&from=" + rfc(at(-time.Hour)), "t0"},
		{"from=" + rfc(at(5*time.Hour)), ""},
		{"from=" + rfc(at(2*time.Hour)) + "&to=" + rfc(at(time.Hour)), ""},
		{"from=" + url.QueryEscape(at(time.Hour).In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339Nano)) + "&to=" + rfc(at(90*time.Minute)), "t1"},
	} {
		if got := actions(auditQuery(admin, c.q)); got != c.want {
			t.Errorf("%s：%s，应为 %s", c.q, got, c.want)
		}
	}
	// 只给 to：现有的记录都在，造的都不在。
	if got := auditQuery(admin, "limit=200&to="+rfc(at(0))); !slices.Equal(auditIDs(got.Items), ids) {
		t.Errorf("to=t0：%d 条，应为 %d 条", len(got.Items), len(ids))
	}
	// 时间范围也能翻页。
	page := auditQuery(admin, "limit=2&from="+rfc(at(0)))
	if actions(page) != "storage_migrate,t3" || page.NextCursor == nil {
		t.Fatalf("时间范围第 1 页：%s", actions(page))
	}
	if page = auditQuery(admin, "limit=2&from="+rfc(at(0))+"&cursor="+*page.NextCursor); actions(page) != "t2_late,t2" {
		t.Fatalf("时间范围第 2 页：%s", actions(page))
	}

	// 参数不对。
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "cursor=0", "cursor=abc", "from=2026-10-16",
		"to=yesterday", "operator_id=1", "project_id=x", "target_type=" + strings.Repeat("a", 65)} {
		admin.Get("/api/system/audit-logs?"+q).ExpectError(http.StatusBadRequest, "invalid_input")
	}

	// 筛选栏的选项：全部用户、全部项目（已删除的标出来）。
	var opts struct {
		Users []struct {
			ID          uuid.UUID `json:"id"`
			Username    string    `json:"username"`
			DisplayName string    `json:"display_name"`
			Status      string    `json:"status"`
		} `json:"users"`
		Projects []struct {
			ID          uuid.UUID `json:"id"`
			ProjectCode string    `json:"project_code"`
			ProjectName string    `json:"project_name"`
			Deleted     bool      `json:"deleted"`
		} `json:"projects"`
	}
	op.Get("/api/system/audit-logs/filter-options").Expect(http.StatusOK).JSON(&opts)
	var users, projects []string
	for _, u := range opts.Users {
		users = append(users, fmt.Sprintf("%s/%s/%s", u.Username, u.DisplayName, u.Status))
	}
	for _, p := range opts.Projects {
		projects = append(projects, fmt.Sprintf("%s/%s/%v", p.ProjectCode, p.ProjectName, p.Deleted))
	}
	if strings.Join(users, ",") != AdminUsername+"/系统管理员/active,aud_op/aud_op/active" ||
		strings.Join(projects, ",") != "AUD1/AUD1 项目/false,AUD2/审计二/true" {
		t.Fatalf("选项：%v %v", users, projects)
	}
}
