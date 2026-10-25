package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/s3"
	"doclite/server/internal/storage"
)

// storageProfile 是 `/api/system/storage-profiles` 的一项。
type storageProfile struct {
	ID             uuid.UUID `json:"id"`
	ProfileName    string    `json:"profile_name"`
	StorageType    string    `json:"storage_type"`
	Endpoint       string    `json:"endpoint"`
	PublicEndpoint *string   `json:"public_endpoint"`
	Bucket         string    `json:"bucket"`
	Region         string    `json:"region"`
	PathPrefix     string    `json:"path_prefix"`
	AccessKey      string    `json:"access_key"`
	HasSecret      bool      `json:"has_secret"`
	SecretReadable bool      `json:"secret_readable"`
	ForcePathStyle bool      `json:"force_path_style"`
	URLExpireHours int       `json:"url_expire_hours"`
	Status         string    `json:"status"`
	IsDefault      bool      `json:"is_default"`
	Remark         *string   `json:"remark"`
	InUse          bool      `json:"in_use"`
	Deletable      bool      `json:"deletable"`
}

type testResult struct {
	OK                  bool    `json:"ok"`
	Result              string  `json:"result"`
	Message             string  `json:"message"`
	Detail              *string `json:"detail"`
	PublicEndpointCheck *struct {
		Result     string `json:"result"`
		Message    string `json:"message"`
		HTTPStatus *int   `json:"http_status"`
	} `json:"public_endpoint_check"`
}

// storageBody 是指向测试 S3 的新建请求体。
func storageBody(name, prefix string) map[string]any {
	c := S3Env()
	return map[string]any{
		"profile_name": name, "endpoint": c.Endpoint, "bucket": c.Bucket, "access_key": c.AccessKey,
		"secret_key": c.SecretKey, "path_prefix": prefix,
	}
}

// TestStorageCRUD：新建、读、改、删；密钥不出现在任何输出里，密文绑定行 id；校验规则。
func TestStorageCRUD(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	prefix := S3Prefix(t)
	c := S3Env()

	res := admin.Post("/api/system/storage-profiles", map[string]any{
		"profile_name": "  测试存储  ", "endpoint": c.Endpoint + "/", "public_endpoint": "", "bucket": c.Bucket,
		"access_key": c.AccessKey, "secret_key": " " + c.SecretKey + " ", "path_prefix": prefix, "remark": "  ",
	}).Expect(201)
	if bytes.Contains(res.Body, []byte(c.SecretKey)) {
		t.Fatal("响应里有 secret_key")
	}
	var p storageProfile
	res.JSON(&p)
	if p.ProfileName != "测试存储" || p.Endpoint != c.Endpoint || p.PublicEndpoint != nil || p.Region != "us-east-1" ||
		p.PathPrefix != prefix || !p.HasSecret || !p.SecretReadable || !p.ForcePathStyle || p.URLExpireHours != 24 ||
		p.Status != "active" || p.IsDefault || p.Remark != nil || p.InUse || !p.Deletable || p.StorageType != "s3" {
		t.Fatalf("新建：%+v", p)
	}
	enc := QueryValue[[]byte](env, "SELECT secret_key_enc FROM storage_profiles WHERE id = $1", p.ID)
	if bytes.Contains(enc, []byte(c.SecretKey)) || enc[0] != 1 {
		t.Fatal("secret_key 没有加密")
	}
	if n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1 AND detail::text LIKE '%'||$2||'%'",
		p.ID, c.SecretKey); n != 0 {
		t.Fatal("审计里有 secret_key")
	}

	// 默认 path_prefix 是 doclite/；空串表示不用前缀。
	var p2 storageProfile
	body := storageBody("第二个", "")
	delete(body, "path_prefix")
	admin.Post("/api/system/storage-profiles", body).Expect(201).JSON(&p2)
	if p2.PathPrefix != "doclite/" {
		t.Fatalf("默认前缀：%q", p2.PathPrefix)
	}
	admin.Patch("/api/system/storage-profiles/"+p2.ID.String(), map[string]any{"path_prefix": ""}).Expect(200).JSON(&p2)
	if p2.PathPrefix != "" {
		t.Fatalf("清空前缀：%q", p2.PathPrefix)
	}

	// 校验。
	for _, bad := range []map[string]any{
		{"endpoint": "http://127.0.0.1:8333/s3"},
		{"endpoint": "127.0.0.1:8333"},
		{"public_endpoint": "ftp://x"},
		{"bucket": "Doclite"},
		{"bucket": "ab"},
		{"path_prefix": "doclite"},
		{"path_prefix": "/doclite/"},
		{"path_prefix": "a//b/"},
		{"path_prefix": "a b/"},
		{"url_expire_hours": 169},
		{"url_expire_hours": 0},
		{"region": "us east"},
		{"secret_key": ""},
		{"profile_name": " "},
	} {
		body := storageBody("坏的", prefix)
		for k, v := range bad {
			body[k] = v
		}
		admin.Post("/api/system/storage-profiles", body).ExpectError(400, "invalid_input")
	}

	// PATCH：secret_key 不传或空串不改；改了只在审计里记 secret_key_changed。
	before := QueryValue[[]byte](env, "SELECT secret_key_enc FROM storage_profiles WHERE id = $1", p.ID)
	admin.Patch("/api/system/storage-profiles/"+p.ID.String(), map[string]any{"secret_key": "", "remark": "备注"}).Expect(200).JSON(&p)
	if p.Remark == nil || *p.Remark != "备注" || !bytes.Equal(before, QueryValue[[]byte](env, "SELECT secret_key_enc FROM storage_profiles WHERE id = $1", p.ID)) {
		t.Fatalf("空 secret_key 不该改密钥：%+v", p)
	}
	admin.Patch("/api/system/storage-profiles/"+p.ID.String(), map[string]any{"secret_key": "new-secret"}).Expect(200)
	if d := QueryValue[string](env, "SELECT detail::text FROM audit_logs WHERE target_id = $1 ORDER BY id DESC LIMIT 1", p.ID); !strings.Contains(d, "secret_key_changed") || strings.Contains(d, "new-secret") {
		t.Fatalf("审计：%s", d)
	}
	// 没有实际变化：不写审计。
	n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", p.ID)
	admin.Patch("/api/system/storage-profiles/"+p.ID.String(), map[string]any{"secret_key": "new-secret", "remark": " 备注 "}).Expect(200)
	if QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_id = $1", p.ID) != n {
		t.Fatal("没有变化也写了审计")
	}

	// 密文绑定行 id：挪到别的行解不开。
	env.Exec("UPDATE storage_profiles SET secret_key_enc = $1 WHERE id = $2", before, p2.ID)
	admin.Get("/api/system/storage-profiles/" + p2.ID.String()).Expect(200).JSON(&p2)
	if p2.SecretReadable {
		t.Fatal("挪到别的行的密文不该能解开")
	}
	var tr testResult
	admin.Post("/api/system/storage-profiles/"+p2.ID.String()+"/test", nil).Expect(200).JSON(&tr)
	if tr.OK || tr.Result != "secret_unreadable" || tr.Message == "" {
		t.Fatalf("解不开的密钥测试连接：%+v", tr)
	}
	// 重填密钥后恢复。
	admin.Patch("/api/system/storage-profiles/"+p2.ID.String(), map[string]any{"secret_key": c.SecretKey}).Expect(200).JSON(&p2)
	if !p2.SecretReadable {
		t.Fatal("重填后应能解开")
	}

	// 列表：默认的在最前，其余按创建时间。
	var list []storageProfile
	admin.Get("/api/system/storage-profiles").Expect(200).JSON(&list)
	if len(list) != 2 || list[0].ID != p.ID || list[1].ID != p2.ID {
		t.Fatalf("列表：%+v", list)
	}
	admin.Post("/api/system/storage-profiles/"+p2.ID.String()+"/set-default", nil).Expect(200)
	admin.Get("/api/system/storage-profiles").Expect(200).JSON(&list)
	if list[0].ID != p2.ID || !list[0].IsDefault || list[1].IsDefault {
		t.Fatalf("设默认后的列表：%+v", list)
	}

	// 删除：非默认、没被引用的可以删。
	admin.Delete("/api/system/storage-profiles/" + p.ID.String()).Expect(204)
	admin.Get("/api/system/storage-profiles/"+p.ID.String()).ExpectError(404, "not_found")
	admin.Delete("/api/system/storage-profiles/"+uuid.NewString()).ExpectError(404, "not_found")
}

// TestStorageStatusRules：状态 active ⇄ readonly → disabled 和默认、引用的规则。
func TestStorageStatusRules(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	defID, prefix := env.UseS3()
	var other storageProfile
	admin.Post("/api/system/storage-profiles", storageBody("备用", prefix)).Expect(201).JSON(&other)
	path := func(id uuid.UUID) string { return "/api/system/storage-profiles/" + id.String() }

	// 首次启动登记的默认存储。
	var def storageProfile
	admin.Get(path(defID)).Expect(200).JSON(&def)
	if !def.IsDefault || def.ProfileName != storage.InitialProfileName || def.Deletable || def.Status != "active" {
		t.Fatalf("默认存储：%+v", def)
	}
	// 默认的不能改只读、停用、删除。
	admin.Patch(path(defID), map[string]any{"status": "readonly"}).ExpectError(409, "storage_is_default")
	admin.Patch(path(defID), map[string]any{"status": "disabled"}).ExpectError(409, "storage_is_default")
	admin.Delete(path(defID)).ExpectError(409, "storage_is_default")
	admin.Patch(path(defID), map[string]any{"status": "paused"}).ExpectError(400, "invalid_input")

	// 只读的不能设默认；启用后可以。
	admin.Patch(path(other.ID), map[string]any{"status": "readonly"}).Expect(200)
	admin.Post(path(other.ID)+"/set-default", nil).ExpectError(409, "storage_not_active")
	admin.Patch(path(other.ID), map[string]any{"status": "disabled"}).Expect(200)
	admin.Post(path(other.ID)+"/set-default", nil).ExpectError(409, "storage_not_active")
	admin.Patch(path(other.ID), map[string]any{"status": "active"}).Expect(200)

	// 有快照引用：不能停用（可以只读），不能删。
	adminID := env.UserID("admin")
	pid, fid := uuid.New(), uuid.New()
	env.Exec(`INSERT INTO projects (id, project_code, project_name, created_by) VALUES ($1, 'S1', 'S1', $2)`, pid, adminID)
	env.Exec(`INSERT INTO files (id) VALUES ($1)`, fid)
	env.Exec(`INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id, storage_key, content_sha256,
  file_size, mime_type) VALUES ($1, $2, 1, 'original', $3, 'k', '\x00', 1, 'image/jpeg')`, uuid.New(), fid, other.ID)
	admin.Get(path(other.ID)).Expect(200).JSON(&other)
	if !other.InUse || other.Deletable {
		t.Fatalf("被引用的存储：%+v", other)
	}
	admin.Patch(path(other.ID), map[string]any{"status": "disabled"}).ExpectError(409, "storage_in_use")
	admin.Delete(path(other.ID)).ExpectError(409, "storage_in_use")
	admin.Patch(path(other.ID), map[string]any{"status": "readonly"}).Expect(200).JSON(&other)
	if other.Status != "readonly" {
		t.Fatalf("只读：%+v", other)
	}

	// 设默认：旧默认取消，只有一个默认；已经是默认时什么都不做。
	admin.Patch(path(other.ID), map[string]any{"status": "active"}).Expect(200)
	admin.Post(path(other.ID)+"/set-default", nil).Expect(200).JSON(&other)
	admin.Get(path(defID)).Expect(200).JSON(&def)
	if !other.IsDefault || def.IsDefault || !def.Deletable {
		t.Fatalf("设默认：%+v %+v", other, def)
	}
	n := QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_type = 'storage_profile'")
	admin.Post(path(other.ID)+"/set-default", nil).Expect(200)
	if QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE target_type = 'storage_profile'") != n {
		t.Fatal("重复设默认不该写审计")
	}
	// 原来的默认现在可以停用、再启用、删除。
	admin.Patch(path(defID), map[string]any{"status": "disabled"}).Expect(200)
	admin.Patch(path(defID), map[string]any{"status": "active"}).Expect(200)
	admin.Delete(path(defID)).Expect(204)

	// 首次启动的登记只在一条都没有时做。
	if err := env.App.EnsureInitialStorage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if QueryValue[int](env, "SELECT count(*) FROM storage_profiles") != 1 {
		t.Fatal("已有存储配置时不该再登记")
	}
}

// TestStorageConnection：在真实的 SeaweedFS 上测试连接的各种结论。
func TestStorageConnection(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	prefix := S3Prefix(t)
	run := func(body map[string]any) testResult {
		t.Helper()
		var r testResult
		admin.Post("/api/system/storage-profiles/test", body).Expect(200).JSON(&r)
		return r
	}
	with := func(kv ...any) map[string]any {
		b := storageBody("x", prefix)
		for i := 0; i < len(kv); i += 2 {
			b[kv[i].(string)] = kv[i+1]
		}
		return b
	}

	if r := run(with()); !r.OK || r.Result != "ok" || r.Detail != nil || r.PublicEndpointCheck != nil {
		t.Fatalf("ok：%+v", r)
	}
	if r := run(with("bucket", "doclite-no-such-bucket")); r.OK || r.Result != "bucket_not_found" || r.Detail == nil {
		t.Fatalf("桶不存在：%+v", r)
	}
	if r := run(with("secret_key", "wrong-secret")); r.Result != "auth_failed" || strings.Contains(*r.Detail, "wrong-secret") {
		t.Fatalf("密钥错：%+v", r)
	}
	if r := run(with("access_key", "no-such-access-key")); r.Result != "auth_failed" {
		t.Fatalf("access_key 不存在：%+v", r)
	}
	if r := run(with("endpoint", "http://127.0.0.1:1")); r.Result != "unreachable" {
		t.Fatalf("连不上：%+v", r)
	}
	// 不是 S3：指向 doclite server 自己。
	if r := run(with("endpoint", env.URL)); r.Result != "other" {
		t.Fatalf("不是 S3：%+v", r)
	}
	// public_endpoint 和 endpoint 不同（同一个存储的另一种写法）：用它签名访问不存在的 key，应得 404 NoSuchKey。
	pub := altEndpoint(t, S3Env().Endpoint)
	if r := run(with("public_endpoint", pub)); !r.OK || r.PublicEndpointCheck == nil || r.PublicEndpointCheck.Result != "ok" ||
		r.PublicEndpointCheck.HTTPStatus == nil || *r.PublicEndpointCheck.HTTPStatus != 404 {
		t.Fatalf("public_endpoint ok：%+v %+v", r, r.PublicEndpointCheck)
	}
	if r := run(with("public_endpoint", "http://127.0.0.1:1")); !r.OK || r.PublicEndpointCheck == nil ||
		r.PublicEndpointCheck.Result != "unreachable" {
		t.Fatalf("public_endpoint 连不上：%+v", r)
	}
	if r := run(with("public_endpoint", env.URL)); !r.OK || r.PublicEndpointCheck == nil || r.PublicEndpointCheck.Result != "rejected" {
		t.Fatalf("public_endpoint 不是 S3：%+v", r)
	}
	// 输入不合格是 400。
	admin.Post("/api/system/storage-profiles/test", with("endpoint", "http://h/x")).ExpectError(400, "invalid_input")

	// 已保存的配置：不带请求体用保存的；带了未保存的修改（不带 secret_key 用保存的）。
	var p storageProfile
	admin.Post("/api/system/storage-profiles", storageBody("已保存", prefix)).Expect(201).JSON(&p)
	var r testResult
	admin.Post("/api/system/storage-profiles/"+p.ID.String()+"/test", nil).Expect(200).JSON(&r)
	if !r.OK {
		t.Fatalf("已保存：%+v", r)
	}
	admin.Post("/api/system/storage-profiles/"+p.ID.String()+"/test", map[string]any{"bucket": "doclite-typo"}).Expect(200).JSON(&r)
	if r.Result != "bucket_not_found" {
		t.Fatalf("已保存 + 改桶名：%+v", r)
	}
	admin.Post("/api/system/storage-profiles/"+p.ID.String()+"/test", map[string]any{"secret_key": "wrong"}).Expect(200).JSON(&r)
	if r.Result != "auth_failed" {
		t.Fatalf("已保存 + 改密钥：%+v", r)
	}
	admin.Post("/api/system/storage-profiles/"+uuid.NewString()+"/test", nil).ExpectError(404, "not_found")
}

// altEndpoint 是同一个 S3 地址的另一种写法（规整后和原来不同）：127.0.0.1 ↔ localhost，别的 IPv4 地址写成
// IPv4 映射的 IPv6 地址（[::ffff:a.b.c.d]），域名后面加一个点。
func altEndpoint(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	switch ip := net.ParseIP(host); {
	case host == "localhost":
		host = "127.0.0.1"
	case ip != nil && ip.IsLoopback():
		host = "localhost"
	case ip != nil && ip.To4() != nil:
		host = "[::ffff:" + host + "]"
	case ip == nil:
		host += "."
	}
	if p := u.Port(); p != "" {
		u.Host = host + ":" + p
	} else {
		u.Host = host
	}
	return u.String()
}

// TestPresign：给快照生成图片地址（files/batch 用的辅助函数），在真实的 SeaweedFS 上取图。
func TestPresign(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	defID, prefix := env.UseS3()
	key, cover := prefix+"P1/"+uuid.NewString()+"/v1.jpg", prefix+"P1/x/v1.cover.jpg"
	PutObject(t, key, "image/jpeg", []byte("original-bytes"))
	PutObject(t, cover, "image/jpeg", []byte("cover-bytes"))

	ctx := context.Background()
	urls, err := env.App.Storage.ImageURLs(ctx, []storage.ObjectRef{
		{StorageProfileID: defID, StorageKey: key, CoverKey: &cover},
		{StorageProfileID: defID, StorageKey: key},
		{StorageProfileID: uuid.New(), StorageKey: key},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := urls[0]
	if u.OriginalURL == nil || u.CoverURL == nil || u.URLExpiresAt == nil || u.URLError != nil {
		t.Fatalf("地址：%+v", u)
	}
	if st, body := httpGet(t, *u.OriginalURL); st != 200 || string(body) != "original-bytes" {
		t.Fatalf("取原图：%d %s", st, body)
	}
	if st, body := httpGet(t, *u.CoverURL); st != 200 || string(body) != "cover-bytes" {
		t.Fatalf("取封面：%d %s", st, body)
	}
	// 签名时间是 UTC+8 当天 0 点，有效期 24×3600+86400 秒。
	q := mustQuery(t, *u.OriginalURL)
	sign := s3.SignTime(time.Now())
	if q.Get("X-Amz-Date") != sign.Format("20060102T150405Z") || q.Get("X-Amz-Expires") != "172800" ||
		!u.URLExpiresAt.Equal(sign.Add(48*time.Hour)) {
		t.Fatalf("签名时间、有效期：%s %s", *u.OriginalURL, u.URLExpiresAt)
	}
	// 同一天同一张图的地址不变；没有封面时 cover_url 为 null；存储不存在时 url_error。
	if urls[1].OriginalURL == nil || *urls[1].OriginalURL != *u.OriginalURL || urls[1].CoverURL != nil {
		t.Fatalf("同一张图：%+v", urls[1])
	}
	if urls[2].URLError == nil || urls[2].URLError.Code != "storage_unavailable" || urls[2].OriginalURL != nil {
		t.Fatalf("存储不存在：%+v", urls[2])
	}
	data, _ := json.Marshal(urls[0])
	if !strings.Contains(string(data), `"cover_url"`) || strings.Contains(string(data), "url_error") {
		t.Fatalf("JSON：%s", data)
	}

	// 用 public_endpoint 签；url_expire_hours 封顶 7 天。
	pub := strings.Replace(S3Env().Endpoint, "127.0.0.1", "localhost", 1)
	env.Admin().Patch("/api/system/storage-profiles/"+defID.String(), map[string]any{"public_endpoint": pub, "url_expire_hours": 168}).Expect(200)
	urls, _ = env.App.Storage.ImageURLs(ctx, []storage.ObjectRef{{StorageProfileID: defID, StorageKey: key}})
	if !strings.HasPrefix(*urls[0].OriginalURL, pub+"/") || mustQuery(t, *urls[0].OriginalURL).Get("X-Amz-Expires") != "604800" {
		t.Fatalf("public_endpoint：%s", *urls[0].OriginalURL)
	}
	if st, body := httpGet(t, *urls[0].OriginalURL); st != 200 || string(body) != "original-bytes" {
		t.Fatalf("按 public_endpoint 取原图：%d %s", st, body)
	}

	// 密钥解不开：url_error secret_unreadable；客户端 500 secret_unreadable。
	env.Exec("UPDATE storage_profiles SET secret_key_enc = '\\x01' WHERE id = $1", defID)
	env.App.Storage.ForgetCache() // 绕过存储模块直接改了库
	urls, _ = env.App.Storage.ImageURLs(ctx, []storage.ObjectRef{{StorageProfileID: defID, StorageKey: key}})
	if urls[0].URLError == nil || urls[0].URLError.Code != "secret_unreadable" {
		t.Fatalf("密钥解不开：%+v", urls[0])
	}
	if _, err := env.App.Storage.DefaultTarget(ctx); err == nil || !strings.Contains(err.Error(), "secret_unreadable") {
		t.Fatalf("DefaultTarget：%v", err)
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// TestPermsStorage 是存储配置接口的菜单权限。
func TestPermsStorage(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                          admin  system.storage  system.projects  manager  none
	GET    /api/system/storage-profiles                         Y          Y              -            -       -
	POST   /api/system/storage-profiles                         Y          Y              -            -       -
	POST   /api/system/storage-profiles/test                    Y          Y              -            -       -
	GET    /api/system/storage-profiles/{id}                    Y          Y              -            -       -
	PATCH  /api/system/storage-profiles/{id}                    Y          Y              -            -       -
	DELETE /api/system/storage-profiles/{id}                    Y          Y              -            -       -
	POST   /api/system/storage-profiles/{id}/test               Y          Y              -            -       -
	POST   /api/system/storage-profiles/{id}/set-default        Y          Y              -            -       -
	`)
}
