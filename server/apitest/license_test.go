package apitest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/app"
	"doclite/server/internal/license"
)

// 许可证（M7-1，requirements 第 11 节）。开发构建（没有公钥）不检查；注入测试公钥后按规则检查。
// 测试自己生成密钥对、用服务端的 license.Sign 造许可证（编码和签名对象与签发工具相同，见 internal/license 的
// 单元测试）；另外用签发工具签的夹具（internal/license/testdata）走一遍导入。

// licClock 是可以拨动的时钟。
type licClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *licClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *licClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// licenseEnv 建一个检查许可证的环境：测试公钥（返回私钥）、可拨动的时钟（从现在开始）。
func licenseEnv(t *testing.T, opts ...func(*app.Options)) (*Env, ed25519.PrivateKey, *licClock) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clock := &licClock{t: time.Now()}
	env := NewEnv(t, append([]func(*app.Options){func(o *app.Options) {
		o.License = license.Options{PublicKey: pub, Now: clock.now}
	}}, opts...)...)
	return env, priv, clock
}

// signLicense 给这个环境的 install_id 签一份许可证：席位 seats，从时钟的现在起 valid 内有效。
func signLicense(env *Env, priv ed25519.PrivateKey, clock *licClock, seats uint32, valid time.Duration, mutate ...func(*license.Payload)) string {
	now := clock.now()
	p := license.Payload{
		LicenseID: uuid.NewString(), Company: "测试公司", Edition: "enterprise", MaxUsers: seats,
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(valid), HardwareID: env.App.License.InstallID(),
	}
	for _, m := range mutate {
		m(&p)
	}
	return license.Sign(priv, p)
}

type licenseView struct {
	Status    string `json:"status"`
	InstallID string `json:"install_id"`
	License   *struct {
		LicenseID string    `json:"license_id"`
		Company   string    `json:"company"`
		Edition   string    `json:"edition"`
		MaxUsers  int       `json:"max_users"`
		IssuedAt  time.Time `json:"issued_at"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"license"`
	DaysLeft       *int    `json:"days_left"`
	OnlineSessions int     `json:"online_sessions"`
	InvalidReason  *string `json:"invalid_reason"`
	ImportedBy     *string `json:"imported_by"`
}

type meNotice struct {
	LicenseNotice *struct {
		Status   string `json:"status"`
		DaysLeft *int   `json:"days_left"`
	} `json:"license_notice"`
}

// getNotice 是 GET /api/me 里的 license_notice（每次新解析，没有这个字段时为空）。
func getNotice(c *Client) meNotice {
	var me meNotice
	c.Get("/api/me").Expect(200).JSON(&me)
	return me
}

func getLicense(c *Client) licenseView {
	var v licenseView
	c.Get("/api/system/license").Expect(200).JSON(&v)
	return v
}

func importLicense(c *Client, text string) *Response {
	return c.Put("/api/system/license", map[string]any{"license": text})
}

func loginStatus(env *Env, username, password string) *Response {
	return env.Anon().Post("/api/auth/login", map[string]any{"username": username, "password": password})
}

var installIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// TestLicenseDev：开发构建不检查——谁都能登录，状态 dev，不能导入；install_id 首次启动生成、以后不变。
func TestLicenseDev(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	env.As("scanner").Get("/api/me").Expect(200)
	v := getLicense(admin)
	if v.Status != "dev" || v.License != nil || !installIDPattern.MatchString(v.InstallID) || v.OnlineSessions != 2 {
		t.Fatalf("开发构建：%+v", v)
	}
	if got := QueryValue[string](env, `SELECT setting_value #>> '{}' FROM system_settings WHERE setting_key = 'install_id'`); got != v.InstallID {
		t.Fatalf("库里的 install_id：%s", got)
	}
	if err := env.App.License.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.App.License.InstallID() != v.InstallID {
		t.Fatal("重新启动后 install_id 变了")
	}
	importLicense(admin, "anything").ExpectError(409, "invalid_state")
	me := getNotice(admin)
	if me.LicenseNotice != nil {
		t.Fatalf("开发构建不该有提醒：%+v", me.LicenseNotice)
	}
}

// TestPermsLicense：许可证接口要 system.license。
func TestPermsLicense(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                          admin  system.license  system.users  manager  none
	GET  /api/system/license    Y          Y               -          -       -
	PUT  /api/system/license    Y          Y               -          -       -
	`)
}

// TestLicenseImport：没导入时只有 admin 能登录；导入的各种错误；导入后别人能登录；同一份再导入不写库。
func TestLicenseImport(t *testing.T) {
	t.Parallel()
	env, priv, clock := licenseEnv(t)
	admin := env.Admin()
	env.CreateUser("u1", "scanner")

	// 没导入：admin 能登录（有提醒），别人不能。
	v := getLicense(admin)
	if v.Status != "missing" || v.License != nil || !installIDPattern.MatchString(v.InstallID) {
		t.Fatalf("没导入：%+v", v)
	}
	me := getNotice(admin)
	if me.LicenseNotice == nil || me.LicenseNotice.Status != "missing" {
		t.Fatalf("admin 的提醒：%+v", me.LicenseNotice)
	}
	loginStatus(env, "u1", DefaultPassword).ExpectError(403, "license_missing")
	// 密码错时先报密码错（不透露许可证的状态）。
	loginStatus(env, "u1", "wrong-password").ExpectError(401, "invalid_credentials")

	// 导入的错误。
	importLicense(admin, "  ").ExpectError(400, "invalid_input")
	importLicense(admin, "-----BEGIN DOCLITE LICENSE-----\nnot base64!\n-----END DOCLITE LICENSE-----").
		ExpectError(400, "invalid_license")
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	res := importLicense(admin, signLicense(env, otherKey, clock, 5, 365*24*time.Hour)).ExpectError(400, "invalid_license")
	if !strings.Contains(string(res.Body), `"reason":"signature"`) {
		t.Fatalf("签名错的 details：%s", res.Body)
	}
	// 用对的私钥签了之后再改内容（席位 5 → 500）。
	importLicense(admin, tamper(t, signLicense(env, priv, clock, 5, 365*24*time.Hour), "max_users", 500)).
		ExpectError(400, "invalid_license")
	res = importLicense(admin, signLicense(env, priv, clock, 5, 365*24*time.Hour, func(p *license.Payload) {
		p.HardwareID = "sha256:" + strings.Repeat("0", 64)
	})).ExpectError(400, "license_mismatch")
	if !strings.Contains(string(res.Body), v.InstallID) {
		t.Fatalf("install_id 不符的 details：%s", res.Body)
	}
	importLicense(admin, signLicense(env, priv, clock, 5, -time.Minute)).ExpectError(400, "license_file_expired")
	loginStatus(env, "u1", DefaultPassword).ExpectError(403, "license_missing")
	if n := QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'license'`); n != 0 {
		t.Fatalf("失败的导入写了审计：%d", n)
	}

	// 正常导入：立即生效。
	text := signLicense(env, priv, clock, 5, 365*24*time.Hour)
	var got licenseView
	importLicense(admin, text).Expect(200).JSON(&got)
	if got.Status != "valid" || got.License == nil || got.License.MaxUsers != 5 || got.License.Company != "测试公司" ||
		got.License.Edition != "enterprise" || got.DaysLeft == nil || *got.DaysLeft != 365 ||
		got.ImportedBy == nil || *got.ImportedBy != "系统管理员" {
		t.Fatalf("导入后：%+v", got)
	}
	env.Login("u1", DefaultPassword).Get("/api/me").Expect(200)
	me = getNotice(admin)
	if me.LicenseNotice != nil {
		t.Fatalf("有效期还长，不该提醒：%+v", me.LicenseNotice)
	}
	if n := QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'license' AND action = 'import'
AND target_id = $1`, uuid.MustParse(got.License.LicenseID)); n != 1 {
		t.Fatalf("导入的审计：%d", n)
	}
	// 同一份再导入：不写库、不审计。
	importLicense(admin, "\n"+text+"\n\n").Expect(200)
	if n := QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'license'`); n != 1 {
		t.Fatalf("重复导入写了审计：%d", n)
	}
	// 重启（重新读库验签）后照样有效。
	if err := env.App.License.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := getLicense(admin); v.Status != "valid" || v.License == nil || v.License.LicenseID != got.License.LicenseID {
		t.Fatalf("重新读库：%+v", v)
	}
}

// tamper 改 .lic 里 payload 的一个字段（不重新签名）。
func tamper(t *testing.T, text, field string, value any) string {
	t.Helper()
	var b64 strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "-----") {
			b64.WriteString(line)
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["payload"].(map[string]any)[field] = value
	data, _ = json.Marshal(raw)
	return "-----BEGIN DOCLITE LICENSE-----\n" + base64.StdEncoding.EncodeToString(data) + "\n-----END DOCLITE LICENSE-----\n"
}

// TestLicenseSeats：席位按登录会话数算（同一人两处算 2，admin 的也算）；满了别人登不上，admin 总能登录；
// 30 分钟没有活动的不算；退出的不算。
func TestLicenseSeats(t *testing.T) {
	t.Parallel()
	env, priv, clock := licenseEnv(t)
	admin := env.Admin() // 会话 1
	importLicense(admin, signLicense(env, priv, clock, 3, 365*24*time.Hour)).Expect(200)
	env.CreateUser("u1", "scanner")
	env.CreateUser("u2", "reviewer")

	u1a := env.Login("u1", DefaultPassword) // 2
	env.Login("u1", DefaultPassword)        // 3：同一个人第二处
	res := loginStatus(env, "u2", DefaultPassword).ExpectError(403, "license_seats_full")
	if !strings.Contains(string(res.Body), `"max_users":3`) || !strings.Contains(string(res.Body), `"online_sessions":3`) {
		t.Fatalf("席位满的 details：%s", res.Body)
	}
	env.Login(AdminUsername, AdminPassword) // admin 总能登录：4
	if v := getLicense(admin); v.OnlineSessions != 4 {
		t.Fatalf("在线会话数：%d", v.OnlineSessions)
	}
	loginStatus(env, "u2", DefaultPassword).ExpectError(403, "license_seats_full")

	// u1 的两个会话 30 分钟没有活动：不算在线（admin 的两个还算），u2 能登录了。
	env.Exec(`UPDATE user_sessions SET last_seen_at = now() - interval '31 minutes'
WHERE user_id = (SELECT id FROM users WHERE username = 'u1')`)
	if v := getLicense(admin); v.OnlineSessions != 2 {
		t.Fatalf("30 分钟无活动后：%d", v.OnlineSessions)
	}
	u2 := env.Login("u2", DefaultPassword) // 3
	// u1 的会话没失效，再发请求就又算在线了（4 个，超过席位也不踢人，只挡新登录）。
	u1a.Get("/api/me").Expect(200)
	if v := getLicense(admin); v.OnlineSessions != 4 {
		t.Fatalf("u1 又活动后：%d", v.OnlineSessions)
	}
	// 退出的不算。
	u2.Post("/api/auth/logout", nil).Expect(204)
	u1a.Post("/api/auth/logout", nil).Expect(204)
	if v := getLicense(admin); v.OnlineSessions != 2 {
		t.Fatalf("退出后：%d", v.OnlineSessions)
	}
	env.Login("u2", DefaultPassword)
}

// TestLicenseExpiry：快到期时提醒 admin；到期后非 admin 登不上、已登录的请求被拒（实时通道也断开），admin 照常；
// 导入新的立即恢复（已登录的不用重新登录）。
func TestLicenseExpiry(t *testing.T) {
	t.Parallel()
	env, priv, clock := licenseEnv(t, fastStream)
	admin := env.Admin()
	importLicense(admin, signLicense(env, priv, clock, 10, 40*24*time.Hour)).Expect(200)
	scanner := env.As("scanner")
	d := env.CreateProject("LIC")
	env.GrantProject(d.ID, env.PersonaID("scanner"))
	userAdmin := env.As("system.license") // 有许可证菜单的非 admin：看得到提醒，但同样受到期的限制

	// 还有 40 天：不提醒。过 15 天（还剩 25 天）：提醒有 system.license 菜单的人，别人看不到。
	me := getNotice(admin)
	if me.LicenseNotice != nil {
		t.Fatalf("40 天：%+v", me.LicenseNotice)
	}
	clock.advance(15 * 24 * time.Hour)
	if v := getLicense(admin); v.Status != "expiring" || v.DaysLeft == nil || *v.DaysLeft != 25 {
		t.Fatalf("25 天：%+v", v)
	}
	me = getNotice(admin)
	if me.LicenseNotice == nil || me.LicenseNotice.Status != "expiring" || *me.LicenseNotice.DaysLeft != 25 {
		t.Fatalf("admin 的提醒：%+v", me.LicenseNotice)
	}
	me = getNotice(userAdmin)
	if me.LicenseNotice == nil || me.LicenseNotice.Status != "expiring" {
		t.Fatalf("system.license 的提醒：%+v", me.LicenseNotice)
	}
	me = getNotice(scanner)
	if me.LicenseNotice != nil {
		t.Fatalf("扫描员不该看到提醒：%+v", me.LicenseNotice)
	}
	stream := openStream(t, scanner, d.ID)
	adminStream := openStream(t, admin, d.ID)

	// 到期。
	clock.advance(26 * 24 * time.Hour)
	scanner.Get("/api/me").ExpectError(403, "license_expired")
	scanner.Get("/api/projects").ExpectError(403, "license_expired")
	userAdmin.Get("/api/system/license").ExpectError(403, "license_expired")
	loginStatus(env, personaCode("scanner"), DefaultPassword).ExpectError(403, "license_expired")
	stream.closedWithin(t, 2*time.Second, "许可证到期后非 admin 的实时通道")
	admin.Get("/api/projects").Expect(200)
	env.Login(AdminUsername, AdminPassword).Get("/api/me").Expect(200)
	if v := getLicense(admin); v.Status != "expired" || v.DaysLeft != nil {
		t.Fatalf("到期：%+v", v)
	}
	me = getNotice(admin)
	if me.LicenseNotice == nil || me.LicenseNotice.Status != "expired" {
		t.Fatalf("到期的提醒：%+v", me.LicenseNotice)
	}
	select {
	case <-adminStream.done:
		t.Fatal("admin 的实时通道不该断开")
	case <-time.After(400 * time.Millisecond):
	}

	// 导入新的：立即恢复，已登录的会话接着用。
	importLicense(admin, signLicense(env, priv, clock, 10, 365*24*time.Hour)).Expect(200)
	scanner.Get("/api/me").Expect(200)
	userAdmin.Get("/api/system/license").Expect(200)
	if n := QueryValue[int](env, `SELECT count(*) FROM audit_logs WHERE target_type = 'license' AND action = 'replace'
AND detail ? 'before' AND detail ? 'after'`); n != 1 {
		t.Fatalf("替换的审计：%d", n)
	}
}

// TestLicenseStreamActivity：打开的实时通道算活动——复核会话时会更新 last_seen_at（距上次不到 1 分钟不写）。
func TestLicenseStreamActivity(t *testing.T) {
	t.Parallel()
	env := NewEnv(t, fastStream)
	scanner := env.As("scanner")
	d := env.CreateProject("SSE")
	env.GrantProject(d.ID, env.PersonaID("scanner"))
	uid := env.PersonaID("scanner")
	openStream(t, scanner, d.ID)
	env.Exec(`UPDATE user_sessions SET last_seen_at = now() - interval '40 minutes' WHERE user_id = $1`, uid)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if QueryValue[bool](env, `SELECT bool_and(last_seen_at > now() - interval '1 minute') FROM user_sessions WHERE user_id = $1`, uid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("实时通道打开期间 last_seen_at 没有更新")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 刚写过：不到 1 分钟不再写。
	before := QueryValue[time.Time](env, `SELECT last_seen_at FROM user_sessions WHERE user_id = $1`, uid)
	time.Sleep(400 * time.Millisecond)
	if after := QueryValue[time.Time](env, `SELECT last_seen_at FROM user_sessions WHERE user_id = $1`, uid); !after.Equal(before) {
		t.Fatalf("1 分钟内又写了 last_seen_at：%v → %v", before, after)
	}
}

// TestLicenseToolFixture：签发工具（../bak/doclite-license）签的 .lic 能导入；改过内容的验签失败。
func TestLicenseToolFixture(t *testing.T) {
	t.Parallel()
	read := func(name string) string {
		b, err := os.ReadFile("../internal/license/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	pub, err := license.ParsePublicKey(read("test-public.pem"))
	if err != nil {
		t.Fatal(err)
	}
	// 夹具 fixed-seconds 的有效期是 2026-01-02 03:04:05Z 到 2027-01-02 03:04:05Z，时钟拨到中间。
	clock := &licClock{t: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	env := NewEnv(t, func(o *app.Options) { o.License = license.Options{PublicKey: pub, Now: clock.now} })
	// 夹具绑定的 install_id。
	const fixtureInstallID = "sha256:b81b02b95e391caf5068cfce64fdfa521b1aaae2d1a50d5c3578115f456896ee"
	env.Exec(`UPDATE system_settings SET setting_value = to_jsonb($1::text) WHERE setting_key = 'install_id'`, fixtureInstallID)
	if err := env.App.License.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := env.Admin()

	text := read("fixed-seconds.lic")
	importLicense(admin, tamper(t, text, "max_users", 500)).ExpectError(400, "invalid_license")
	importLicense(admin, tamper(t, text, "expires_at", "2099-01-02T03:04:05Z")).ExpectError(400, "invalid_license")
	var v licenseView
	importLicense(admin, text).Expect(200).JSON(&v)
	if v.Status != "valid" || v.License == nil || v.License.Company != "Acme 测试公司" || v.License.MaxUsers != 50 ||
		v.License.LicenseID != "11111111-2222-3333-4444-555555555555" ||
		!v.License.ExpiresAt.Equal(time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("工具签的许可证：%+v", v)
	}
	// 命令行 issue 签的（有效期到 2036）、公司名带各种转义字符的：替换。
	importLicense(admin, read("cli-issue.lic")).Expect(200).JSON(&v)
	if v.License == nil || v.License.Company != "CLI 签发公司" || v.License.Edition != "standard" || v.License.MaxUsers != 3 {
		t.Fatalf("cli-issue：%+v", v)
	}
	importLicense(admin, read("issue-now.lic")).Expect(200).JSON(&v)
	if v.License == nil || !strings.Contains(v.License.Company, "\"引号\" \\反斜杠 <a&b> tab[\t]") {
		t.Fatalf("issue-now：%+v", v)
	}
}
