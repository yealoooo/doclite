package apitest

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestPermsFolders 是文件目录接口（文件夹和文件的整理）的菜单权限：读、列、建文件夹 scan 或 files；整理只要 files。
func TestPermsFolders(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                          admin  manager  scanner  reviewer  cataloger  scan  files  review_assign  none
	GET    /api/folders/{id}                    Y      Y        Y        Y         Y         Y     Y        -           -
	GET    /api/folders/{id}/children           Y      Y        Y        Y         Y         Y     Y        -           -
	POST   /api/folders                         Y      Y        Y        Y         Y         Y     Y        -           -
	PATCH  /api/folders/{id}                    Y      Y        Y        Y         Y         -     Y        -           -
	DELETE /api/folders/{id}                    Y      Y        Y        Y         Y         -     Y        -           -
	PATCH  /api/folder-files/{id}               Y      Y        Y        Y         Y         -     Y        -           -
	POST   /api/folder-files/{id}/copy          Y      Y        Y        Y         Y         -     Y        -           -
	DELETE /api/folder-files/{id}               Y      Y        Y        Y         Y         -     Y        -           -
	POST   /api/folder-files:move               Y      Y        Y        Y         Y         -     Y        -           -
	POST   /api/folder-files:delete             Y      Y        Y        Y         Y         -     Y        -           -
	`)
}

// TestFolderTree：新建（重名、if_exists、名称规则）、读和祖先链、children、改名、备注、移动（子树换前缀、成环）、
// 根文件夹、tree_full、别的项目、项目授权、审计和事件。
func TestFolderTree(t *testing.T) {
	t.Parallel()
	env := NewEnv(t, fastStream)
	admin := env.Admin()
	p := env.CreateProject("FT")
	root := p.RootFolderID
	stream := openStream(t, admin, p.ID)

	a := mkdir(admin, root, "2025 文书")
	if a.ProjectID != p.ID || a.ParentID == nil || *a.ParentID != root || a.PathCode != "00001.00001." ||
		len(a.Ancestors) != 1 || a.Ancestors[0].ID != root {
		t.Fatalf("新建：%+v", a)
	}
	if ev := stream.next(t); ev["type"] != "folders.changed" || !hasIDs(ev["folder_ids"], a.ID, root) {
		t.Fatalf("事件：%v", ev)
	}
	admin.Post("/api/folders", map[string]any{"parent_id": root, "folder_name": "2025 文书"}).ExpectError(409, "conflict")
	same := Decode[folderItem](admin.Post("/api/folders", map[string]any{"parent_id": root, "folder_name": "2025 文书",
		"if_exists": "return"}).Expect(200))
	if same.ID != a.ID {
		t.Fatal("if_exists 没有返回已有的")
	}
	for _, bad := range []string{"con.txt", "a/b", " 前面有空格", "以点结尾.", ""} {
		admin.Post("/api/folders", map[string]any{"parent_id": root, "folder_name": bad}).ExpectError(400, "invalid_input")
	}
	admin.Post("/api/folders", map[string]any{"parent_id": uuid.New(), "folder_name": "x"}).ExpectError(404, "not_found")

	// 多层；读文件夹带祖先链；children 带计数。
	b := mkdir(admin, a.ID, "盒 001")
	c := mkdir(admin, b.ID, "补扫")
	got := Decode[folderItem](admin.Get("/api/folders/" + c.ID.String()).Expect(200))
	if got.PathCode != "00001.00001.00001.00001." || len(got.Ancestors) != 3 || got.Ancestors[2].ID != b.ID {
		t.Fatalf("祖先链：%+v", got)
	}
	ch := listChildren(admin, a.ID)
	if len(ch.Folders) != 1 || ch.Folders[0].ID != b.ID || ch.Folders[0].FolderCount != 1 || ch.Folders[0].FileCount != 0 ||
		ch.Files == nil {
		t.Fatalf("children：%+v", ch)
	}

	// 改名（重名 409，大小写不同的同名也算）、备注（"" 清空）、没有变化不写审计。
	mkdir(admin, a.ID, "盒 002")
	admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"folder_name": "盒 002"}).ExpectError(409, "conflict")
	admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"folder_name": "con"}).ExpectError(400, "invalid_input")
	upd := Decode[folderItem](admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"folder_name": "盒 003", "remark": " 3 号库 A 架 "}).Expect(200))
	if upd.FolderName != "盒 003" || upd.Remark == nil || *upd.Remark != "3 号库 A 架" {
		t.Fatalf("改名、备注：%+v", upd)
	}
	audits := QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'folder' AND target_id = $1`, b.ID)
	admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"folder_name": "盒 003"}).Expect(200)
	if QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'folder' AND target_id = $1`, b.ID) != audits {
		t.Fatal("没有变化也写了审计")
	}
	upd = Decode[folderItem](admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"remark": ""}).Expect(200))
	if upd.Remark != nil {
		t.Fatal("备注没有清空")
	}

	// 移动：子树一起换前缀（parent_id、path_code），成环 409，目标在别的项目 404。
	d := mkdir(admin, root, "待整理")
	admin.Patch("/api/folders/"+a.ID.String(), map[string]any{"parent_id": c.ID}).ExpectError(409, "tree_cycle")
	admin.Patch("/api/folders/"+a.ID.String(), map[string]any{"parent_id": a.ID}).ExpectError(409, "tree_cycle")
	other := env.CreateProject("FT2")
	admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"parent_id": other.RootFolderID}).ExpectError(404, "not_found")
	moved := Decode[folderItem](admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"parent_id": d.ID}).Expect(200))
	if *moved.ParentID != d.ID || !strings.HasPrefix(moved.PathCode, d.PathCode) || len(moved.PathCode) != len(d.PathCode)+6 {
		t.Fatalf("移动：%+v（目标 %s）", moved, d.PathCode)
	}
	got = Decode[folderItem](admin.Get("/api/folders/" + c.ID.String()).Expect(200))
	if got.PathCode != moved.PathCode+"00001." || got.Ancestors[1].ID != d.ID {
		t.Fatalf("子树没有换前缀：%+v", got)
	}
	if QueryValue[string](env, `SELECT action FROM audit_logs WHERE target_id = $1 ORDER BY id DESC LIMIT 1`, b.ID) != "move" {
		t.Fatal("移动没有审计")
	}
	// 移动的目标里有同名的 409。
	mkdir(admin, root, "盒 003")
	admin.Patch("/api/folders/"+b.ID.String(), map[string]any{"parent_id": root}).ExpectError(409, "conflict")

	// 根文件夹：不能改名、移动、删除，可以改备注。
	admin.Patch("/api/folders/"+root.String(), map[string]any{"folder_name": "新名字"}).ExpectError(409, "root_folder_locked")
	admin.Patch("/api/folders/"+root.String(), map[string]any{"parent_id": d.ID}).ExpectError(409, "root_folder_locked")
	admin.Delete("/api/folders/"+root.String()).ExpectError(409, "root_folder_locked")
	admin.Patch("/api/folders/"+root.String(), map[string]any{"remark": "档案室"}).Expect(200)

	// 每层最多 99999 个。
	env.Exec(`UPDATE folders SET last_child_no = 99999 WHERE id = $1`, d.ID)
	admin.Post("/api/folders", map[string]any{"parent_id": d.ID, "folder_name": "满了"}).ExpectError(409, "tree_full")

	// 项目授权：没授权 403，授权后可以；只有 scan 的能建不能改。
	scanner := env.As("scanner")
	scanner.Get("/api/folders/"+a.ID.String()).ExpectError(403, "forbidden")
	scanner.Post("/api/folders", map[string]any{"parent_id": a.ID, "folder_name": "x"}).ExpectError(403, "forbidden")
	env.GrantProject(p.ID, env.PersonaID("scanner"), env.PersonaID("scan"))
	scanner.Get("/api/folders/" + a.ID.String()).Expect(200)
	scan := env.As("scan")
	mkdir(scan, a.ID, "扫描员建的")
	scan.Patch("/api/folders/"+a.ID.String(), map[string]any{"remark": "x"}).ExpectError(403, "forbidden")

	// 子树里的文件超过 1 万个不能一次删 400（直接造数据：子文件夹里插 10001 个文件），什么都不改。
	big := mkdir(admin, root, "大")
	bigSub := mkdir(admin, big.ID, "里面")
	env.Exec(`WITH f AS (SELECT gen_random_uuid() AS id, g FROM generate_series(1, 10001) g),
ins AS (INSERT INTO files (id) SELECT id FROM f)
INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
SELECT gen_random_uuid(), $1, $2, f.id, 'x' || f.g || '.jpg', f.g, $3 FROM f`, p.ID, bigSub.ID, env.UserID("admin"))
	admin.Delete("/api/folders/"+big.ID.String()).ExpectError(400, "invalid_input")
	admin.Get("/api/folders/" + bigSub.ID.String()).Expect(200)

	// 删除空文件夹：子树一起删（软删除），之后 404，编码不回收。
	admin.Delete("/api/folders/" + d.ID.String()).Expect(204)
	admin.Get("/api/folders/"+c.ID.String()).ExpectError(404, "not_found")
	if QueryValue[int](env, `SELECT count(*) FROM folders WHERE path_code LIKE $1 || '%' AND project_id = $2 AND deleted_at IS NULL`,
		d.PathCode, p.ID) != 0 {
		t.Fatal("子树没有一起删")
	}
	e := mkdir(admin, root, "待整理")
	if e.PathCode == d.PathCode {
		t.Fatal("编码被回收了")
	}
}

// hasIDs：事件里的 id 列表包含这些 id。
func hasIDs(v any, ids ...uuid.UUID) bool {
	list, _ := v.([]any)
	var got []string
	for _, x := range list {
		s, _ := x.(string)
		got = append(got, s)
	}
	for _, id := range ids {
		if !slices.Contains(got, id.String()) {
			return false
		}
	}
	return true
}

// TestFolderFiles：文件的改名、移动（自动改名）、复制（不复制字节）、删除（已归类 409、取消分配）、批量移动和删除、
// 删除有文件的文件夹、文件日志。
func TestFolderFiles(t *testing.T) {
	t.Parallel()
	env, _ := newUploadEnv(t)
	admin := env.Admin()
	p := env.CreateProject("FF")
	x := mkdir(admin, p.RootFolderID, "盒 X")
	y := mkdir(admin, p.RootFolderID, "盒 Y")
	a := uploadOK(t, admin, x.ID, "a.jpg", "a")
	b := uploadOK(t, admin, x.ID, "b.jpg", "b")
	c := uploadOK(t, admin, x.ID, "c.jpg", "c")
	yb := uploadOK(t, admin, y.ID, "B.jpg", "yb")
	ff := func(id uuid.UUID) string { return "/api/folder-files/" + id.String() }

	// 改名：撞名（不分大小写）409；名字不合法 400；成功后只改 folder_files。
	admin.Patch(ff(a.FolderFileID), map[string]any{"file_name": "B.JPG"}).ExpectError(409, "conflict")
	admin.Patch(ff(a.FolderFileID), map[string]any{"file_name": "nul.jpg"}).ExpectError(400, "invalid_input")
	r := Decode[struct {
		FileName string `json:"file_name"`
		Renamed  bool   `json:"renamed"`
	}](admin.Patch(ff(a.FolderFileID), map[string]any{"file_name": "A1.jpg"}).Expect(200))
	if r.FileName != "A1.jpg" || r.Renamed {
		t.Fatalf("改名：%+v", r)
	}

	// 移动：目标里有同名的自动改名，排到末尾；同时改名和移动、新名字撞名 409。
	admin.Patch(ff(b.FolderFileID), map[string]any{"file_name": "B.jpg", "folder_id": y.ID}).ExpectError(409, "conflict")
	keyBefore := QueryValue[string](env, `SELECT storage_key FROM file_snapshots WHERE file_id = $1`, b.FileID)
	m := Decode[struct {
		FolderID uuid.UUID `json:"folder_id"`
		FileName string    `json:"file_name"`
		SortNo   int       `json:"sort_no"`
		Renamed  bool      `json:"renamed"`
	}](admin.Patch(ff(b.FolderFileID), map[string]any{"folder_id": y.ID}).Expect(200))
	if m.FolderID != y.ID || m.FileName != "b (1).jpg" || !m.Renamed || m.SortNo != 2 {
		t.Fatalf("移动：%+v", m)
	}
	if QueryValue[string](env, `SELECT storage_key FROM file_snapshots WHERE file_id = $1`, b.FileID) != keyBefore {
		t.Fatal("移动改了对象 key")
	}
	admin.Patch(ff(b.FolderFileID), map[string]any{"folder_id": uuid.New()}).ExpectError(404, "not_found")

	// 复制：新 file_id，v1 copy，复用源快照的 key，不复制字节；不给 folder_id 时复制到原文件夹、自动改名。
	objects := len(s3Keys(t, env))
	cp := Decode[struct {
		FileID     uuid.UUID `json:"file_id"`
		SnapshotID uuid.UUID `json:"snapshot_id"`
		FolderID   uuid.UUID `json:"folder_id"`
		FileName   string    `json:"file_name"`
		Renamed    bool      `json:"renamed"`
	}](admin.Post(ff(c.FolderFileID)+"/copy", map[string]any{}).Expect(201))
	if cp.FileID == c.FileID || cp.FolderID != x.ID || cp.FileName != "c (1).jpg" || !cp.Renamed {
		t.Fatalf("复制：%+v", cp)
	}
	if QueryValue[string](env, `SELECT snapshot_type || ' ' || storage_key || ' ' || (parent_snapshot_id = $2)::text
FROM file_snapshots WHERE file_id = $1 AND version_no = 1`, cp.FileID, c.SnapshotID) !=
		"copy "+QueryValue[string](env, `SELECT storage_key FROM file_snapshots WHERE file_id = $1`, c.FileID)+" true" {
		t.Fatal("复制的快照不对")
	}
	if len(s3Keys(t, env)) != objects {
		t.Fatal("复制写了新的对象")
	}
	admin.Post(ff(c.FolderFileID)+"/copy", map[string]any{"folder_id": y.ID}).Expect(201)

	// 删除：已归类 409；取消进行中的分配；再删 404。
	env.catalog(p.ID, mkdir(admin, p.RootFolderID, "来源").ID, c.FileID)
	admin.Delete(ff(c.FolderFileID)).ExpectError(409, "file_cataloged")
	rev := env.openReview(p.ID, a.FileID)
	admin.Delete(ff(a.FolderFileID)).Expect(204)
	if QueryValue[string](env, `SELECT status FROM file_reviews WHERE id = $1`, rev) != "cancelled" {
		t.Fatal("没有取消分配")
	}
	admin.Delete(ff(a.FolderFileID)).ExpectError(404, "not_found")

	// 文件日志：新的在前，detail 存当时的名字和路径。
	logs := Decode[struct {
		Logs []struct {
			Action       string         `json:"action"`
			OperatorName string         `json:"operator_name"`
			Detail       map[string]any `json:"detail"`
		} `json:"logs"`
	}](admin.Get("/api/files/" + b.FileID.String() + "/logs").Expect(200))
	if len(logs.Logs) != 2 || logs.Logs[0].Action != "move" || logs.Logs[1].Action != "upload" ||
		logs.Logs[0].Detail["from_path"] != "FF 项目/盒 X" || logs.Logs[0].Detail["renamed_from"] != "b.jpg" ||
		logs.Logs[1].Detail["folder_path"] != "FF 项目/盒 X" || logs.Logs[0].OperatorName != "系统管理员" {
		t.Fatalf("日志：%+v", logs.Logs)
	}
	admin.Get("/api/files/"+a.FileID.String()+"/logs").ExpectError(404, "not_found")

	// 批量移动：按原文件夹、sort_no 排到目标末尾；已在目标里的 moved=false；参数和 404。
	d1 := uploadOK(t, admin, x.ID, "d1.jpg", "d1")
	d2 := uploadOK(t, admin, x.ID, "d2.jpg", "d2")
	z := mkdir(admin, p.RootFolderID, "盒 Z")
	mv := Decode[struct {
		Items []struct {
			FolderFileID uuid.UUID `json:"folder_file_id"`
			SortNo       int       `json:"sort_no"`
			Moved        bool      `json:"moved"`
		} `json:"items"`
	}](admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{d2.FolderFileID, d1.FolderFileID, yb.FolderFileID},
		"folder_id": y.ID}).Expect(200))
	if len(mv.Items) != 3 || mv.Items[0].FolderFileID != d1.FolderFileID || !mv.Items[0].Moved ||
		mv.Items[1].FolderFileID != d2.FolderFileID || mv.Items[1].SortNo != mv.Items[0].SortNo+1 ||
		mv.Items[2].FolderFileID != yb.FolderFileID || mv.Items[2].Moved {
		t.Fatalf("批量移动：%+v", mv.Items)
	}
	many := make([]uuid.UUID, 501)
	for i := range many {
		many[i] = uuid.New()
	}
	admin.Post("/api/folder-files:move", map[string]any{"ids": many, "folder_id": z.ID}).ExpectError(400, "invalid_input")
	admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{}, "folder_id": z.ID}).ExpectError(400, "invalid_input")
	ghost := uuid.New()
	res := admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{d1.FolderFileID, ghost}, "folder_id": z.ID}).
		ExpectError(404, "not_found")
	if det := Decode[struct {
		Details struct {
			IDs []uuid.UUID `json:"ids"`
		} `json:"details"`
	}](res); len(det.Details.IDs) != 1 || det.Details.IDs[0] != ghost {
		t.Fatalf("details.ids：%s", res.Body)
	}
	q := env.CreateProject("FF2")
	qf := uploadOK(t, admin, q.RootFolderID, "q.jpg", "q")
	admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{d1.FolderFileID, qf.FolderFileID}, "folder_id": z.ID}).
		ExpectError(400, "invalid_input")

	// 批量删除：有已归类的整体不做（details.ids 是 folder_files 的 id），去掉之后成功。
	res = admin.Post("/api/folder-files:delete", map[string]any{"ids": []uuid.UUID{d1.FolderFileID, c.FolderFileID}}).
		ExpectError(409, "file_cataloged")
	if !strings.Contains(string(res.Body), c.FolderFileID.String()) || strings.Contains(string(res.Body), d1.FolderFileID.String()) {
		t.Fatalf("details.ids：%s", res.Body)
	}
	if QueryValue[bool](env, `SELECT deleted_at IS NULL FROM folder_files WHERE id = $1`, d1.FolderFileID) != true {
		t.Fatal("整体不做却删了")
	}
	admin.Post("/api/folder-files:delete", map[string]any{"ids": []uuid.UUID{d1.FolderFileID, d2.FolderFileID}}).Expect(204)
	if names := listChildren(admin, y.ID).fileNames(); !slices.Equal(names, []string{"B.jpg", "b (1).jpg", "c.jpg"}) {
		t.Fatalf("Y 里的文件：%v", names)
	}

	// 删除有文件的文件夹：子树里有已归类的 409、什么都不改；否则文件夹和文件一起删、每个文件记 delete、取消分配。
	admin.Delete("/api/folders/"+x.ID.String()).ExpectError(409, "file_cataloged")
	env.Exec(`DELETE FROM archive_item_files WHERE file_id = $1`, c.FileID)
	sub := mkdir(admin, x.ID, "子")
	s1 := uploadOK(t, admin, sub.ID, "s1.jpg", "s1")
	rev = env.openReview(p.ID, s1.FileID)
	admin.Delete("/api/folders/" + x.ID.String()).Expect(204)
	if QueryValue[int](env, `SELECT count(*) FROM folder_files WHERE folder_id = ANY($1) AND deleted_at IS NULL`,
		[]uuid.UUID{x.ID, sub.ID}) != 0 {
		t.Fatal("文件没有一起删")
	}
	if QueryValue[int](env, `SELECT count(*) FROM file_logs WHERE action = 'delete' AND file_id = ANY($1)`,
		[]uuid.UUID{c.FileID, cp.FileID, s1.FileID}) != 3 {
		t.Fatal("每个文件要记一条 delete")
	}
	if QueryValue[string](env, `SELECT status FROM file_reviews WHERE id = $1`, rev) != "cancelled" {
		t.Fatal("没有取消分配")
	}
	if QueryValue[string](env, `SELECT detail->'before'->>'folder_path' FROM audit_logs WHERE target_id = $1 AND action = 'delete'`,
		x.ID) != "FF 项目/盒 X" {
		t.Fatal("删除的审计不对")
	}
}

// s3Keys 是这个测试的 S3 前缀下的对象。
func s3Keys(t *testing.T, env *Env) []string {
	t.Helper()
	prefix := QueryValue[string](env, `SELECT path_prefix FROM storage_profiles WHERE is_default`)
	keys, err := S3Client(t).List(t.Context(), prefix, 10000)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
