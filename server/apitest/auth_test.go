package apitest

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"
	"time"

	"doclite/server/internal/app"
	"doclite/server/internal/config"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/semver"
	"doclite/server/migrations"
)

type me struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Roles       []struct {
		RoleCode string `json:"role_code"`
		RoleName string `json:"role_name"`
	} `json:"roles"`
}

type myMenu struct {
	MenuCode   string  `json:"menu_code"`
	ParentCode *string `json:"parent_code"`
	MenuName   string  `json:"menu_name"`
	RoutePath  *string `json:"route_path"`
	SortNo     int     `json:"sort_no"`
}

func sessionCookie(t *testing.T, r *Response) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "doclite_session" {
			return c
		}
	}
	t.Fatalf("响应里没有 doclite_session：%v", r.Header.Values("Set-Cookie"))
	return nil
}

func TestHealthAndUnknownRoutes(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	anon := env.Anon()

	var h struct{ Status, Version string }
	anon.Get("/api/health").Expect(200).JSON(&h)
	if h.Status != "ok" || h.Version == "" {
		t.Fatalf("health：%+v", h)
	}
	anon.Get("/api/nope").ExpectError(404, "not_found")
	anon.Get("/api").ExpectError(404, "not_found")
	anon.Post("/api/health", nil).ExpectError(405, "method_not_allowed")
	// server 只提供接口（M7-5）：/api 以外的路径一律 JSON 404，不托管网页、不回退到 index.html。
	for _, p := range []string{"/", "/login", "/files", "/index.html", "/assets/app.js"} {
		anon.Get(p).ExpectError(404, "not_found")
	}
	anon.Post("/login", nil).ExpectError(404, "not_found")
	// 路径参数不是 UUID：先过菜单检查，再 400。
	env.Admin().Get("/api/system/users/not-a-uuid").ExpectError(400, "invalid_input")
	// 请求体不是 JSON、Content-Type 不对、太大。
	env.Admin().Post("/api/system/roles", "{").ExpectError(400, "invalid_input")
	env.Admin().Post("/api/system/roles", strings.Repeat(" ", 1<<20+1)).ExpectError(413, "payload_too_large")
	c := env.Admin()
	c.Header.Set("Content-Type", "text/plain")
	c.Post("/api/system/roles", nil).ExpectError(400, "invalid_input")
	c.Header.Del("Content-Type")
}

func TestLoginLogout(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	anon := env.Anon()

	anon.Post("/api/auth/login", map[string]any{"username": "admin", "password": "wrong-pass"}).ExpectError(401, "invalid_credentials")
	anon.Post("/api/auth/login", map[string]any{"username": "nobody", "password": "whatever"}).ExpectError(401, "invalid_credentials")
	anon.Post("/api/auth/login", map[string]any{"username": " ", "password": "x"}).ExpectError(400, "invalid_input")
	anon.Post("/api/auth/login", "not json").ExpectError(400, "invalid_input")
	bad := env.Anon()
	bad.Header.Set("X-Client-Type", "mobile")
	bad.Post("/api/auth/login", map[string]any{"username": "admin", "password": AdminPassword}).ExpectError(400, "invalid_input")
	anon.Get("/api/me").ExpectError(401, "unauthorized")

	// 登录名不区分大小写，前后空白忽略。
	c := env.Anon()
	r := c.Post("/api/auth/login", map[string]any{"username": " ADMIN ", "password": AdminPassword}).Expect(200)
	var m me
	r.JSON(&m)
	if m.Username != "admin" || m.Status != "active" || len(m.Roles) != 1 || m.Roles[0].RoleCode != "admin" || m.Roles[0].RoleName != "系统管理员" {
		t.Fatalf("登录响应：%+v", m)
	}
	ck := sessionCookie(t, r)
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || ck.MaxAge != 12*3600 || ck.Secure || len(ck.Value) != 43 {
		t.Fatalf("Cookie 属性不对：%+v", ck)
	}

	// 库里只存令牌的 sha256；记下客户端类型、IP、到期时间；last_login_at 写了。
	sum := sha256.Sum256([]byte(ck.Value))
	var clientType, ip string
	var ttl time.Duration
	err := env.Pool.QueryRow(context.Background(), `SELECT client_type, host(ip_address), expires_at - created_at
FROM user_sessions WHERE token_hash = $1`, sum[:]).Scan(&clientType, &ip, &ttl)
	must(t, err)
	if clientType != "browser" || ip != "127.0.0.1" || ttl != 12*time.Hour {
		t.Fatalf("会话行：%s %s %s", clientType, ip, ttl)
	}
	if QueryValue[int](env, "SELECT count(*) FROM user_sessions WHERE token_hash = convert_to($1, 'UTF8')", ck.Value) != 0 {
		t.Fatal("库里不应存令牌原文")
	}
	if !QueryValue[bool](env, "SELECT last_login_at IS NOT NULL FROM users WHERE username = 'admin'") {
		t.Fatal("last_login_at 没写")
	}

	c.Get("/api/me").Expect(200).JSON(&m)
	if m.Username != "admin" {
		t.Fatalf("me：%+v", m)
	}
	var projects []any
	c.Get("/api/projects").Expect(200).JSON(&projects)
	if projects == nil || len(projects) != 0 {
		t.Fatalf("项目列表应为 []：%s", c.Get("/api/projects").Body)
	}

	// 退出：204、删 Cookie、会话撤销；再退出一次也是 204。
	r = c.Post("/api/auth/logout", nil).Expect(204)
	if ck := sessionCookie(t, r); ck.MaxAge >= 0 || ck.Value != "" {
		t.Fatalf("退出时应删 Cookie：%+v", ck)
	}
	if !QueryValue[bool](env, "SELECT revoked_at IS NOT NULL FROM user_sessions WHERE token_hash = $1", sum[:]) {
		t.Fatal("退出后会话没有撤销")
	}
	c.Get("/api/me").ExpectError(401, "unauthorized")
	c.Post("/api/auth/logout", nil).Expect(204)

	// 撤销之后拿旧令牌也不行（Cookie 被浏览器删了，手工带上）。
	old := env.Anon()
	old.Header.Set("Cookie", "doclite_session="+ck.Value)
	old.Get("/api/me").ExpectError(401, "unauthorized")

	// 桌面客户端。
	d := env.Anon()
	d.Header.Set("X-Client-Type", "desktop")
	r = d.Post("/api/auth/login", map[string]any{"username": "admin", "password": AdminPassword}).Expect(200)
	sum = sha256.Sum256([]byte(sessionCookie(t, r).Value))
	if QueryValue[string](env, "SELECT client_type FROM user_sessions WHERE token_hash = $1", sum[:]) != "desktop" {
		t.Fatal("client_type 应为 desktop")
	}
}

func TestSessionExpiryAndLastSeen(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	c := env.Admin()
	sid := QueryValue[string](env, "SELECT id::text FROM user_sessions ORDER BY created_at DESC LIMIT 1")
	expires := QueryValue[time.Time](env, "SELECT expires_at FROM user_sessions WHERE id = $1", sid)

	// 有操作也不续期。
	c.Get("/api/me").Expect(200)
	env.Exec("UPDATE user_sessions SET last_seen_at = now() - interval '2 minutes' WHERE id = $1", sid)
	c.Get("/api/me").Expect(200)
	if got := QueryValue[time.Time](env, "SELECT expires_at FROM user_sessions WHERE id = $1", sid); !got.Equal(expires) {
		t.Fatalf("到期时间变了：%s → %s", expires, got)
	}
	// last_seen_at 超过 1 分钟才写。
	if QueryValue[bool](env, "SELECT last_seen_at < now() - interval '1 minute' FROM user_sessions WHERE id = $1", sid) {
		t.Fatal("超过 1 分钟应更新 last_seen_at")
	}
	env.Exec("UPDATE user_sessions SET last_seen_at = now() - interval '30 seconds' WHERE id = $1", sid)
	before := QueryValue[time.Time](env, "SELECT last_seen_at FROM user_sessions WHERE id = $1", sid)
	c.Get("/api/me").Expect(200)
	if got := QueryValue[time.Time](env, "SELECT last_seen_at FROM user_sessions WHERE id = $1", sid); !got.Equal(before) {
		t.Fatal("不到 1 分钟不应更新 last_seen_at")
	}

	// 到期就失效。
	env.Exec("UPDATE user_sessions SET expires_at = now() - interval '1 second' WHERE id = $1", sid)
	c.Get("/api/me").ExpectError(401, "unauthorized")
}

func TestDisabledUserCannotLogin(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	id := env.CreateUser("rev1", "reviewer")
	c := env.Login("rev1", DefaultPassword)
	c.Get("/api/me").Expect(200)

	env.Admin().Post("/api/system/users/"+id.String()+"/disable", nil).Expect(200)
	// 停用即撤销会话。
	c.Get("/api/me").ExpectError(401, "unauthorized")
	env.Anon().Post("/api/auth/login", map[string]any{"username": "rev1", "password": DefaultPassword}).ExpectError(403, "user_disabled")
	// 密码不对时不透露"已停用"。
	env.Anon().Post("/api/auth/login", map[string]any{"username": "rev1", "password": "wrong-pass"}).ExpectError(401, "invalid_credentials")

	env.Admin().Post("/api/system/users/"+id.String()+"/enable", nil).Expect(200)
	env.Login("rev1", DefaultPassword).Get("/api/me").Expect(200)
}

func TestCookieSecure(t *testing.T) {
	t.Parallel()
	env := NewEnv(t, func(o *app.Options) { o.CookieSecure = true; o.SessionTTL = 2 * time.Hour })
	r := env.Anon().Post("/api/auth/login", map[string]any{"username": "admin", "password": AdminPassword}).Expect(200)
	if ck := sessionCookie(t, r); !ck.Secure || ck.MaxAge != 7200 {
		t.Fatalf("Cookie：%+v", ck)
	}
}

func TestClientVersion(t *testing.T) {
	t.Parallel()
	minV, _ := semver.Parse("1.2.0")
	latest, _ := semver.Parse("1.3.0")
	env := NewEnv(t, func(o *app.Options) {
		o.ClientVersions = config.ClientVersions{MinVersion: &minV, LatestVersion: &latest, DownloadURL: "http://srv/dl"}
	})
	withVersion := func(v string) *Client {
		c := env.Anon()
		c.Header.Set("X-Client-Version", v)
		return c
	}
	// 浏览器不带头：放行。
	env.Anon().Get("/api/me").ExpectError(401, "unauthorized")
	// 低于最低版本：426，先于鉴权和参数解析。
	var body struct {
		Details struct {
			ClientVersion string  `json:"client_version"`
			MinVersion    string  `json:"min_version"`
			LatestVersion *string `json:"latest_version"`
			DownloadURL   *string `json:"download_url"`
		} `json:"details"`
	}
	withVersion("1.1.9").Get("/api/me").ExpectError(426, "client_upgrade_required").JSON(&body)
	d := body.Details
	if d.ClientVersion != "1.1.9" || d.MinVersion != "1.2.0" || d.LatestVersion == nil || *d.LatestVersion != "1.3.0" ||
		d.DownloadURL == nil || *d.DownloadURL != "http://srv/dl" {
		t.Fatalf("426 的 details：%+v", d)
	}
	withVersion("1.2.0-beta.1").Post("/api/auth/login", "bad json").ExpectError(426, "client_upgrade_required")
	withVersion("abc").Get("/api/me").ExpectError(400, "invalid_client_version")
	withVersion("1.2.0").Get("/api/me").ExpectError(401, "unauthorized")
	withVersion("2.0.0").Post("/api/auth/login", map[string]any{"username": "admin", "password": AdminPassword}).Expect(200)
	// /api/health、/api/shell/* 不检查；网页不检查。
	withVersion("0.1.0").Get("/api/health").Expect(200)
	withVersion("0.1.0").Get("/api/shell/latest").ExpectError(404, "not_found")
	withVersion("0.1.0").Get("/").Expect(404)
}

func TestInitialAdmin(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	if n := QueryValue[int](env, "SELECT count(*) FROM users"); n != 1 {
		t.Fatalf("应只有初始管理员，实际 %d 个用户", n)
	}
	if QueryValue[string](env, `SELECT detail->>'source' FROM audit_logs
WHERE target_type = 'user' AND action = 'create' AND operator_id IS NULL`) != "init_admin" {
		t.Fatal("初始管理员应写审计（操作人为系统）")
	}
	// 已经有用户时不再建。
	must(t, env.App.EnsureInitialAdmin(context.Background(), &config.InitAdmin{Username: "other", Password: "other-pass", DisplayName: "x"}))
	if n := QueryValue[int](env, "SELECT count(*) FROM users"); n != 1 {
		t.Fatalf("不应再建初始管理员，实际 %d 个用户", n)
	}
}

func TestMigrations(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	ctx := context.Background()
	versions := QueryValue[[]string](env, "SELECT array_agg(version ORDER BY version) FROM schema_migrations")
	if strings.Join(versions, ",") != "0001_init,0002_review_indexes,0003_item_no_index,0004_folder_archive" {
		t.Fatalf("schema_migrations：%v", versions)
	}
	ran, err := db.Migrate(ctx, env.Pool, migrations.FS, nil)
	if err != nil || len(ran) != 0 {
		t.Fatalf("再执行一次应什么都不做：%v %v", ran, err)
	}
	// 执行过的文件被改过：拒绝启动。
	env.Exec("UPDATE schema_migrations SET checksum = 'x' WHERE version = '0002_review_indexes'")
	if _, err := db.Migrate(ctx, env.Pool, migrations.FS, nil); err == nil || !strings.Contains(err.Error(), "被改过") {
		t.Fatalf("校验和不一致应报错：%v", err)
	}
	// 库里有程序不认识的迁移：拒绝启动。
	env.Exec("DELETE FROM schema_migrations WHERE version = '0002_review_indexes'")
	env.Exec("INSERT INTO schema_migrations (version, checksum) VALUES ('9999_future', 'x')")
	if _, err := db.Migrate(ctx, env.Pool, migrations.FS, nil); err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("不认识的迁移应报错：%v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
