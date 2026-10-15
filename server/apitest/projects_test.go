package apitest

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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

// projectDetail 是 `/api/system/projects/{id}` 的响应。
type projectDetail struct {
	ID             uuid.UUID      `json:"id"`
	ProjectCode    string         `json:"project_code"`
	ProjectName    string         `json:"project_name"`
	Description    *string        `json:"description"`
	Status         string         `json:"status"`
	Settings       map[string]any `json:"settings"`
	RootFolderID   uuid.UUID      `json:"root_folder_id"`
	CategoryRootID uuid.UUID      `json:"category_root_id"`
	CreatedBy      uuid.UUID      `json:"created_by"`
	UpdatedAt      string         `json:"updated_at"`
	Users          []struct {
		ID       uuid.UUID `json:"id"`
		Username string    `json:"username"`
		Status   string    `json:"status"`
	} `json:"users"`
	Roles []struct {
		ID       uuid.UUID `json:"id"`
		RoleCode string    `json:"role_code"`
	} `json:"roles"`
}

// CreateProject 用管理员建一个项目，返回详情。
func (e *Env) CreateProject(code string) projectDetail {
	e.t.Helper()
	var d projectDetail
	e.Admin().Post("/api/system/projects", map[string]any{"project_code": code, "project_name": code + " 项目"}).
		Expect(http.StatusCreated).JSON(&d)
	return d
}

// GrantProject 个人授权给这些用户（整体替换为这些人，不授权角色）。
func (e *Env) GrantProject(pid uuid.UUID, userIDs ...uuid.UUID) {
	e.t.Helper()
	if userIDs == nil {
		userIDs = []uuid.UUID{}
	}
	e.Admin().Put("/api/projects/"+pid.String()+"/access", map[string]any{"user_ids": userIDs, "role_ids": []uuid.UUID{}}).
		Expect(http.StatusOK)
}

// TestProjectCRUD：新建（同一事务建根文件夹和归类根）、列表、详情、改名（根一起改）、删除。
func TestProjectCRUD(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	adminID := env.UserID("admin")

	var d projectDetail
	admin.Post("/api/system/projects", map[string]any{
		"project_code": " WS2025 ", "project_name": "  市档案馆 2025 文书 ", "description": " 第一批 ",
	}).Expect(201).JSON(&d)
	if d.ProjectCode != "WS2025" || d.ProjectName != "市档案馆 2025 文书" || d.Description == nil || *d.Description != "第一批" ||
		d.Status != "active" || d.CreatedBy != adminID || len(d.Users) != 0 || len(d.Roles) != 0 {
		t.Fatalf("新建：%+v", d)
	}
	// 根文件夹和归类根。
	if QueryValue[string](env, `SELECT folder_name || '|' || path_code FROM folders WHERE id = $1 AND project_id = $2 AND parent_id IS NULL`,
		d.RootFolderID, d.ID) != "市档案馆 2025 文书|00001." {
		t.Fatal("根文件夹不对")
	}
	if QueryValue[string](env, `SELECT category_type || '|' || category_code || '|' || category_name || '|' || path_code
FROM archive_categories WHERE id = $1 AND project_id = $2 AND parent_id IS NULL`, d.CategoryRootID, d.ID) != "root|WS2025|市档案馆 2025 文书|00001." {
		t.Fatal("归类根不对")
	}
	// settings 补上默认值，库里是 {}。
	if d.Settings["archive_no"].(map[string]any)["template"] != "{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}" ||
		len(d.Settings["review"].(map[string]any)["reject_reasons"].([]any)) != 8 ||
		d.Settings["export"].(map[string]any)["retention_days"] != float64(7) {
		t.Fatalf("默认 settings：%v", d.Settings)
	}
	if QueryValue[string](env, "SELECT settings::text FROM projects WHERE id = $1", d.ID) != "{}" {
		t.Fatal("库里的 settings 应是 {}")
	}
	// 建项目的人不自动成为成员：admin 本来就能进。
	if QueryValue[int](env, "SELECT count(*) FROM project_members WHERE project_id = $1", d.ID) != 0 {
		t.Fatal("不该自动成为成员")
	}

	// 编码：不合法 400，重复 409（区分大小写），删除后可再用。
	for _, bad := range []string{"", ".", "..", "-a", "a/b", "文书", strings.Repeat("a", 33)} {
		admin.Post("/api/system/projects", map[string]any{"project_code": bad, "project_name": "x"}).ExpectError(400, "invalid_input")
	}
	for _, bad := range []string{"", "a/b", "结尾是点.", "CON", strings.Repeat("名", 101)} {
		admin.Post("/api/system/projects", map[string]any{"project_code": "OK1", "project_name": bad}).ExpectError(400, "invalid_input")
	}
	admin.Post("/api/system/projects", map[string]any{"project_code": "OK1", "project_name": "x", "description": strings.Repeat("字", 501)}).
		ExpectError(400, "invalid_input")
	admin.Post("/api/system/projects", map[string]any{"project_code": "WS2025", "project_name": "重复"}).ExpectError(409, "conflict")
	env.CreateProject("ws2025")

	// 列表：新建的在前；keyword、status 筛选；人数、角色数。
	type page struct {
		Items []struct {
			ProjectCode  string    `json:"project_code"`
			RootFolderID uuid.UUID `json:"root_folder_id"`
			MemberCount  int       `json:"member_count"`
			RoleCount    int       `json:"role_count"`
			UpdatedAt    string    `json:"updated_at"`
		} `json:"items"`
		Total int `json:"total"`
	}
	var pg page
	admin.Get("/api/system/projects").Expect(200).JSON(&pg)
	if pg.Total != 2 || pg.Items[0].ProjectCode != "ws2025" || pg.Items[1].ProjectCode != "WS2025" || pg.Items[1].RootFolderID != d.RootFolderID {
		t.Fatalf("列表：%+v", pg)
	}
	admin.Get("/api/system/projects?keyword=" + url.QueryEscape("文书")).Expect(200).JSON(&pg)
	if pg.Total != 1 || pg.Items[0].ProjectCode != "WS2025" {
		t.Fatalf("keyword：%+v", pg)
	}
	admin.Get("/api/system/projects?status=archived").Expect(200).JSON(&pg)
	if pg.Total != 0 {
		t.Fatalf("status：%+v", pg)
	}
	admin.Get("/api/system/projects?status=deleted").ExpectError(400, "invalid_input")

	// 改名：根文件夹、归类根一起改；版本严格递增。
	var d2 projectDetail
	admin.Patch("/api/system/projects/"+d.ID.String(), map[string]any{"project_name": "新名字", "status": "archived", "description": ""}).
		Expect(200).JSON(&d2)
	if d2.ProjectName != "新名字" || d2.Status != "archived" || d2.Description != nil || d2.UpdatedAt == d.UpdatedAt {
		t.Fatalf("修改：%+v", d2)
	}
	if QueryValue[string](env, "SELECT folder_name FROM folders WHERE id = $1", d.RootFolderID) != "新名字" ||
		QueryValue[string](env, "SELECT category_name FROM archive_categories WHERE id = $1", d.CategoryRootID) != "新名字" {
		t.Fatal("根没有一起改名")
	}
	// 项目编码不能改（PATCH 里没有这个字段，传了也不理）。
	admin.Patch("/api/system/projects/"+d.ID.String(), map[string]any{"project_code": "X"}).Expect(200).JSON(&d2)
	if d2.ProjectCode != "WS2025" {
		t.Fatal("编码被改了")
	}
	admin.Patch("/api/system/projects/"+d.ID.String(), map[string]any{"status": "closed"}).ExpectError(400, "invalid_input")
	admin.Patch("/api/system/projects/"+uuid.NewString(), map[string]any{"status": "active"}).ExpectError(404, "not_found")

	// 删除：有未删除的文件 409；删掉文件后可以删；删后 404，编码可以再用。
	fid := uuid.New()
	env.Exec(`INSERT INTO files (id) VALUES ($1)`, fid)
	env.Exec(`INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, created_by) VALUES ($1, $2, $3, $4, 'a.jpg', $5)`,
		uuid.New(), d.ID, d.RootFolderID, fid, adminID)
	admin.Delete("/api/system/projects/"+d.ID.String()).ExpectError(409, "project_not_empty")
	env.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, fid)
	admin.Delete("/api/system/projects/" + d.ID.String()).Expect(204)
	admin.Get("/api/system/projects/"+d.ID.String()).ExpectError(404, "not_found")
	admin.Delete("/api/system/projects/"+d.ID.String()).ExpectError(404, "not_found")
	admin.Get("/api/projects/"+d.ID.String()+"/access").ExpectError(404, "not_found")
	env.CreateProject("WS2025")
	if got := len(Decode[[]MyProjectJSON](admin.Get("/api/projects").Expect(200))); got != 2 {
		t.Fatalf("我的项目：%d", got)
	}
	// 审计：create、update（一次 PATCH 一条）、delete；没有变化的 PATCH、被拒绝的删除不写。
	if n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_type = 'project' AND target_id = $1", d.ID); n != 3 {
		t.Fatalf("审计条数 %d", n)
	}
}

// MyProjectJSON 是 GET /api/projects 的一项。
type MyProjectJSON struct {
	ID          uuid.UUID `json:"id"`
	ProjectCode string    `json:"project_code"`
}

// TestProjectVersionCheck：expected_updated_at 版本检查。
func TestProjectVersionCheck(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	d := env.CreateProject("V1")
	path := "/api/system/projects/" + d.ID.String()

	// 原样传回：通过；版本严格递增（RFC 3339，微秒）。
	var d2 projectDetail
	admin.Patch(path, map[string]any{"description": "一", "expected_updated_at": d.UpdatedAt}).Expect(200).JSON(&d2)
	t1, _ := time.Parse(time.RFC3339Nano, d.UpdatedAt)
	t2, _ := time.Parse(time.RFC3339Nano, d2.UpdatedAt)
	if !t2.After(t1) {
		t.Fatalf("版本没有递增：%s → %s", d.UpdatedAt, d2.UpdatedAt)
	}
	// 旧版本：409，什么都不改（没有实际变化的修改也检查）。
	admin.Patch(path, map[string]any{"description": "二", "expected_updated_at": d.UpdatedAt}).ExpectError(409, "conflict")
	admin.Patch(path, map[string]any{"expected_updated_at": d.UpdatedAt}).ExpectError(409, "conflict")
	if QueryValue[string](env, "SELECT description FROM projects WHERE id = $1", d.ID) != "一" {
		t.Fatal("409 时不该改")
	}
	// 截到毫秒不相等；同一时刻的其他写法（别的时区）相等。
	ms := t2.Truncate(time.Millisecond)
	if !ms.Equal(t2) {
		admin.Patch(path, map[string]any{"description": "三", "expected_updated_at": ms.Format(time.RFC3339Nano)}).ExpectError(409, "conflict")
	}
	other := t2.In(time.FixedZone("x", 8*3600)).Format("2006-01-02T15:04:05.000000000Z07:00")
	admin.Patch(path, map[string]any{"description": "三", "expected_updated_at": other}).Expect(200).JSON(&d2)
	admin.Patch(path, map[string]any{"description": "四", "expected_updated_at": "昨天"}).ExpectError(400, "invalid_input")
	// 不带不查；改授权也换版本。
	var d3 projectDetail
	admin.Patch(path, map[string]any{"description": "四"}).Expect(200).JSON(&d3)
	env.GrantProject(d.ID, env.PersonaID("scanner"))
	admin.Patch(path, map[string]any{"description": "五", "expected_updated_at": d3.UpdatedAt}).ExpectError(409, "conflict")
	// 没有实际变化：不换版本、不写审计。
	admin.Get(path).Expect(200).JSON(&d3)
	n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", d.ID)
	var d4 projectDetail
	admin.Patch(path, map[string]any{"description": " 四 ", "expected_updated_at": d3.UpdatedAt}).Expect(200).JSON(&d4)
	if d4.UpdatedAt != d3.UpdatedAt || QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", d.ID) != n {
		t.Fatal("没有变化也换了版本或写了审计")
	}
}

// TestProjectSettings：settings 整体替换，只校验已知键，保留未知键。
func TestProjectSettings(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	d := env.CreateProject("S1")
	path := "/api/system/projects/" + d.ID.String()

	good := map[string]any{
		"review":     map[string]any{"reject_reasons": []string{"blur", "自定义原因"}, "rescan_reassign": "same_reviewer"},
		"export":     map[string]any{"require_locked_item": false, "retention_days": 30},
		"archive_no": map[string]any{"template": "{fonds}-{year}-{item_no:03}"},
		"catalog":    map[string]any{"lock_requires_approved": false},
		"item_fields": []any{
			map[string]any{"key": "author", "label": "责任者", "type": "text", "required": true, "options": []any{}, "max_length": 50, "default": "某单位"},
			map[string]any{"key": "date", "label": "日期", "type": "date", "required": false, "options": []any{}, "max_length": nil, "default": "2025-02-28"},
			map[string]any{"key": "level", "label": "密级", "type": "enum", "options": []any{"公开", "内部"}, "default": "公开"},
		},
		"auto_process": map[string]any{"未知": []any{1, 2}},
	}
	var d2 projectDetail
	admin.Patch(path, map[string]any{"settings": good}).Expect(200).JSON(&d2)
	if d2.Settings["auto_process"] == nil || d2.Settings["export"].(map[string]any)["retention_days"] != float64(30) ||
		len(d2.Settings["item_fields"].([]any)) != 3 {
		t.Fatalf("保存后：%v", d2.Settings)
	}
	// 整体替换：只给一部分时，其余读时补默认值。
	admin.Patch(path, map[string]any{"settings": map[string]any{"x": 1}}).Expect(200).JSON(&d2)
	if d2.Settings["x"] != float64(1) || d2.Settings["export"].(map[string]any)["retention_days"] != float64(7) {
		t.Fatalf("整体替换：%v", d2.Settings)
	}

	bad := []struct {
		settings any
		msg      string
	}{
		{[]any{1}, "必须是 JSON 对象"},
		{map[string]any{"review": map[string]any{"reject_reasons": []any{}}}, "review.reject_reasons"},
		{map[string]any{"review": map[string]any{"rescan_reassign": "any"}}, "rescan_reassign"},
		{map[string]any{"export": map[string]any{"retention_days": 0}}, "retention_days"},
		{map[string]any{"export": map[string]any{"retention_days": 7.5}}, "retention_days"},
		{map[string]any{"export": map[string]any{"require_locked_item": "yes"}}, "require_locked_item"},
		{map[string]any{"catalog": map[string]any{"lock_requires_approved": 1}}, "lock_requires_approved"},
		{map[string]any{"archive_no": map[string]any{"template": "{fonds}-{year}"}}, "档号模板"},
		{map[string]any{"archive_no": map[string]any{"template": "{fonds}{item_no}"}}, "档号模板"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "title", "label": "题名", "type": "text"}}}, "item_fields[0].key"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "text"}, map[string]any{"key": "a", "label": "B", "type": "text"}}}, "item_fields[1].key"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "number"}}}, "item_fields[0].type"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "enum"}}}, "item_fields[0]"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "enum", "options": []any{" 公开"}}}}, "item_fields[0].options[0]"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "date", "max_length": 10}}}, "item_fields[0].max_length"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "text", "max_length": 10001}}}, "item_fields[0].max_length"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "text", "max_length": 3, "default": "超过三个字"}}}, "item_fields[0].default"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "date", "default": "2025-02-30"}}}, "item_fields[0].default"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "a", "label": "A", "type": "enum", "options": []any{"x"}, "default": "y"}}}, "item_fields[0].default"},
		{map[string]any{"item_fields": []any{map[string]any{"key": "", "label": "A", "type": "text"}}}, "item_fields[0].key"},
		{map[string]any{"big": strings.Repeat("x", 64<<10)}, "KiB"},
	}
	for _, b := range bad {
		res := admin.Patch(path, map[string]any{"settings": b.settings})
		if res.Status == 413 {
			continue // 超过 64 KiB 的请求体可能先被 JSON 上限挡住
		}
		res.ExpectError(400, "invalid_input")
		var e struct{ Message string }
		res.JSON(&e)
		if !strings.HasPrefix(e.Message, "settings 不对：") || !strings.Contains(e.Message, b.msg) {
			t.Errorf("settings %v：%s", b.settings, e.Message)
		}
	}
	// settings 不变、没有实际变化：不写审计。
	n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", d.ID)
	admin.Patch(path, map[string]any{"settings": map[string]any{"x": 1}}).Expect(200)
	if QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", d.ID) != n {
		t.Fatal("settings 没变也写了审计")
	}
}

// TestProjectAccess：授权整体替换、选人候选、能进的项目、项目授权检查（404/403）。
func TestProjectAccess(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	d := env.CreateProject("A1")
	other := env.CreateProject("A2")
	scannerID, reviewerID := env.PersonaID("scanner"), env.PersonaID("reviewer")
	disabledID := env.CreateUser("zz_disabled", "scanner")
	admin.Post("/api/system/users/"+disabledID.String()+"/disable", nil).Expect(200)
	accessPath := "/api/projects/" + d.ID.String() + "/access"

	type access struct {
		Users []struct {
			ID       uuid.UUID `json:"id"`
			Username string    `json:"username"`
			Status   string    `json:"status"`
		} `json:"users"`
		Roles []struct {
			ID       uuid.UUID `json:"id"`
			RoleCode string    `json:"role_code"`
		} `json:"roles"`
	}
	var a access
	admin.Put(accessPath, map[string]any{
		"user_ids": []uuid.UUID{disabledID, scannerID, scannerID}, "role_ids": []uuid.UUID{env.RoleID("cataloger"), env.RoleID("manager")},
	}).Expect(200).JSON(&a)
	if len(a.Users) != 2 || a.Users[0].Username != "p_scanner" || a.Users[1].Status != "disabled" ||
		len(a.Roles) != 2 || a.Roles[0].RoleCode != "manager" || a.Roles[1].RoleCode != "cataloger" {
		t.Fatalf("授权：%+v", a)
	}
	admin.Get(accessPath).Expect(200).JSON(&a)
	if len(a.Users) != 2 {
		t.Fatalf("读授权：%+v", a)
	}
	// 整体替换。
	admin.Put(accessPath, map[string]any{"user_ids": []uuid.UUID{reviewerID}, "role_ids": []uuid.UUID{}}).Expect(200).JSON(&a)
	if len(a.Users) != 1 || a.Users[0].ID != reviewerID || len(a.Roles) != 0 {
		t.Fatalf("整体替换：%+v", a)
	}
	admin.Put(accessPath, map[string]any{"user_ids": []uuid.UUID{uuid.New()}, "role_ids": []uuid.UUID{}}).ExpectError(400, "invalid_input")
	admin.Put(accessPath, map[string]any{"user_ids": []uuid.UUID{}, "role_ids": []uuid.UUID{uuid.New()}}).ExpectError(400, "invalid_input")
	admin.Put(accessPath, map[string]any{"user_ids": []uuid.UUID{}}).ExpectError(400, "invalid_input")
	admin.Put("/api/projects/"+uuid.NewString()+"/access", map[string]any{"user_ids": []uuid.UUID{}, "role_ids": []uuid.UUID{}}).ExpectError(404, "not_found")

	// 选人候选：全部用户（含停用的，带 role_ids）和全部角色。
	var c struct {
		Users []struct {
			ID      uuid.UUID   `json:"id"`
			Status  string      `json:"status"`
			RoleIDs []uuid.UUID `json:"role_ids"`
		} `json:"users"`
		Roles []struct {
			RoleCode string `json:"role_code"`
		} `json:"roles"`
	}
	admin.Get("/api/system/projects/access-candidates").Expect(200).JSON(&c)
	if len(c.Users) != 4 || len(c.Roles) != 5 || c.Roles[0].RoleCode != "admin" {
		t.Fatalf("候选：%+v", c)
	}
	for _, u := range c.Users {
		if u.ID == disabledID && (u.Status != "disabled" || len(u.RoleIDs) != 1 || u.RoleIDs[0] != env.RoleID("scanner")) {
			t.Fatalf("停用的候选：%+v", u)
		}
	}

	// 项目授权检查：实时通道是"能进项目即可"的接口。个人授权、角色授权、admin 能进；进不了 403；不存在、已删除 404。
	streamStatus := func(c *Client, pid uuid.UUID) (int, string) {
		t.Helper()
		st, code := openStreamStatus(t, c, pid)
		return st, code
	}
	if st, _ := streamStatus(env.As("reviewer"), d.ID); st != 200 {
		t.Fatalf("个人授权：%d", st)
	}
	if st, code := streamStatus(env.As("scanner"), d.ID); st != 403 || code != "forbidden" {
		t.Fatalf("撤销授权后：%d %s", st, code)
	}
	admin.Put("/api/projects/"+other.ID.String()+"/access", map[string]any{"user_ids": []uuid.UUID{}, "role_ids": []uuid.UUID{env.RoleID("scanner")}}).Expect(200)
	if st, _ := streamStatus(env.As("scanner"), other.ID); st != 200 {
		t.Fatalf("角色授权：%d", st)
	}
	if st, _ := streamStatus(admin, d.ID); st != 200 {
		t.Fatalf("admin：%d", st)
	}
	if st, code := streamStatus(admin, uuid.New()); st != 404 || code != "not_found" {
		t.Fatalf("不存在：%d %s", st, code)
	}
	if st, code := streamStatus(env.As("none"), uuid.New()); st != 404 || code != "not_found" {
		t.Fatalf("不存在（没授权的人）：%d %s", st, code)
	}
	admin.Delete("/api/system/projects/" + other.ID.String()).Expect(204)
	if st, code := streamStatus(env.As("scanner"), other.ID); st != 404 || code != "not_found" {
		t.Fatalf("已删除：%d %s", st, code)
	}
	// 能进的项目列表跟着授权变。
	if got := len(Decode[[]MyProjectJSON](env.As("reviewer").Get("/api/projects").Expect(200))); got != 1 {
		t.Fatalf("reviewer 能进的项目：%d", got)
	}
	if got := len(Decode[[]MyProjectJSON](env.As("scanner").Get("/api/projects").Expect(200))); got != 0 {
		t.Fatalf("scanner 能进的项目：%d", got)
	}
	// AccessibleProjects（批量接口用）。
	ok, err := env.App.Auth.AccessibleProjects(context.Background(), reviewerID, []uuid.UUID{d.ID, other.ID, uuid.New()})
	if err != nil || len(ok) != 1 || !ok[d.ID] {
		t.Fatalf("AccessibleProjects：%v %v", ok, err)
	}
}

// TestPermsProjects 是项目接口的菜单权限。
func TestPermsProjects(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                   admin  system.projects  system.users  manager  scanner  none
	GET    /api/system/projects                          Y          Y              -           -        -       -
	POST   /api/system/projects                          Y          Y              -           -        -       -
	GET    /api/system/projects/access-candidates        Y          Y              -           -        -       -
	GET    /api/system/projects/{id}                     Y          Y              -           -        -       -
	PATCH  /api/system/projects/{id}                     Y          Y              -           -        -       -
	DELETE /api/system/projects/{id}                     Y          Y              -           -        -       -
	GET    /api/projects/{pid}/access                    Y          Y              -           -        -       -
	PUT    /api/projects/{pid}/access                    Y          Y              -           -        -       -
	# 能进项目即可（不要求菜单）：随机的项目 id 是 404，不是 403。
	GET    /api/projects/{pid}/stream                    Y          Y              Y           Y        Y       Y
	`)
}
