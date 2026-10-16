// Package apitest 是 API 测试：每个测试起一个真实的 server（httptest）连自己的数据库，用 HTTP 调接口。
//
// 写法：
//
//	func TestXxx(t *testing.T) {
//		t.Parallel()
//		env := NewEnv(t)                          // 新库（从模板库克隆）+ 新 server + 初始管理员
//		admin := env.Admin()                      // 已登录的管理员
//		u := admin.Post("/api/system/users", map[string]any{…}).Expect(201)
//		var item struct{ ID string `json:"id"` }
//		u.JSON(&item)
//		admin.Get("/api/system/users/x").ExpectError(400, "invalid_input")
//	}
//
// 数据库：第一次用到时建模板库 `doclite_test_tpl_<迁移的哈希>` 并执行全部迁移（多个进程、多个 worktree 同时跑时
// 用咨询锁排队，只建一次）；之后每个测试 `CREATE DATABASE … TEMPLATE …` 克隆一份，测试结束时删掉。
// 迁移文件变了，哈希就变，自动建新的模板库。连接参数见 TEST_DATABASE_URL（默认 doclite_test 库，只用来执行建库删库）。
//
// 日志：默认丢弃；设 TEST_LOG=1 时打到 stderr。
package apitest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/app"
	"doclite/server/internal/auth"
	"doclite/server/internal/config"
	"doclite/server/internal/platform/db"
	"doclite/server/migrations"
)

// 初始管理员（每个 Env 都有）。
const (
	AdminUsername = "admin"
	AdminPassword = "admin-pass-123"
	// DefaultPassword 是 fixtures 建的用户的密码。
	DefaultPassword = "pass-123456"
)

// fastPassword 是测试用的 argon2id 参数（64 KiB、1 轮），比生产参数快几百倍；校验按哈希里的参数算，规则不变。
var fastPassword = auth.PasswordParams{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

func testDatabaseURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://doclite@127.0.0.1:5432/doclite_test"
}

// withDatabase 把连接串里的库名换成 name。
func withDatabase(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		panic(fmt.Sprintf("TEST_DATABASE_URL 格式不对：%v", err))
	}
	u.Path = "/" + name
	return u.String()
}

var (
	adminOnce sync.Once
	adminPool *pgxpool.Pool // 连 TEST_DATABASE_URL，只用来建库、删库
	adminErr  error

	tplOnce sync.Once
	tplName string
	tplErr  error
)

func maintenancePool() (*pgxpool.Pool, error) {
	adminOnce.Do(func() {
		adminPool, adminErr = pgxpool.New(context.Background(), testDatabaseURL())
		if adminErr == nil {
			adminErr = adminPool.Ping(context.Background())
		}
		if adminErr != nil {
			adminErr = fmt.Errorf("连不上测试库 %s（设置 TEST_DATABASE_URL）：%w", testDatabaseURL(), adminErr)
		}
	})
	return adminPool, adminErr
}

// templateDB 返回模板库的名字；不存在时建好（执行全部迁移）。
func templateDB() (string, error) {
	tplOnce.Do(func() { tplName, tplErr = buildTemplate() })
	return tplName, tplErr
}

func buildTemplate() (string, error) {
	ctx := context.Background()
	ms, err := db.LoadMigrations(migrations.FS)
	if err != nil {
		return "", err
	}
	name := "doclite_test_tpl_" + db.MigrationsHash(ms)[:16]

	// 咨询锁要在同一个连接上加和解，单独开一个连接。
	conn, err := pgx.Connect(ctx, testDatabaseURL())
	if err != nil {
		return "", fmt.Errorf("连不上测试库 %s（设置 TEST_DATABASE_URL）：%w", testDatabaseURL(), err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(6000, 2)"); err != nil {
		return "", err
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock(6000, 2)") //nolint:errcheck

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}
	// 先建在临时名字上，迁移完再改名：模板库存在就一定是完整的。
	build := name + "_build"
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+build+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+build); err != nil {
		return "", err
	}
	pool, err := db.Open(ctx, withDatabase(testDatabaseURL(), build))
	if err != nil {
		return "", err
	}
	_, err = db.Migrate(ctx, pool, migrations.FS, nil)
	pool.Close()
	if err != nil {
		return "", fmt.Errorf("模板库执行迁移失败：%w", err)
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+build+" RENAME TO "+name); err != nil {
		return "", err
	}
	return name, nil
}

// cloneDB 从模板库克隆一个库给这个测试用，测试结束时删掉。返回连接串。
func cloneDB(t *testing.T) string {
	t.Helper()
	tpl, err := templateDB()
	if err != nil {
		t.Fatal(err)
	}
	mp, err := maintenancePool()
	if err != nil {
		t.Fatal(err)
	}
	name := "doclite_test_" + randomHex(8)
	ctx := context.Background()
	if _, err := mp.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+tpl); err != nil {
		t.Fatalf("克隆测试库失败：%v", err)
	}
	t.Cleanup(func() {
		if _, err := mp.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("删除测试库 %s 失败：%v", name, err)
		}
	})
	return withDatabase(testDatabaseURL(), name)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func testLogger() *slog.Logger {
	if os.Getenv("TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Env 是一个测试的环境：自己的数据库、自己的 server、初始管理员。
type Env struct {
	t    *testing.T
	URL  string
	Pool *pgxpool.Pool
	App  *app.App

	mu       sync.Mutex
	admin    *Client
	personas map[string]*persona
}

// NewEnv 建测试环境。opts 可以改 server 的配置（客户端版本、Cookie Secure、网页目录……）。
func NewEnv(t *testing.T, opts ...func(*app.Options)) *Env {
	t.Helper()
	return NewEnvWithLog(t, testLogger(), opts...)
}

// NewEnvWithLog 同 [NewEnv]，server 的日志写到 log（要检查日志的测试用，见 logCapture）。
func NewEnvWithLog(t *testing.T, log *slog.Logger, opts ...func(*app.Options)) *Env {
	t.Helper()
	dsn := cloneDB(t)
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// 模板库已经迁移过，这里再走一遍和启动时一样的流程（只核对校验和，不执行）。
	if _, err := db.Migrate(ctx, pool, migrations.FS, nil); err != nil {
		t.Fatal(err)
	}
	options := app.Options{SessionTTL: 12 * time.Hour, Password: fastPassword, MasterKey: testMasterKey()}
	for _, o := range opts {
		o(&options)
	}
	a, err := app.New(pool, options, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureInitialAdmin(ctx, &config.InitAdmin{
		Username: AdminUsername, Password: AdminPassword, DisplayName: "系统管理员",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.InitLicense(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &Env{t: t, URL: srv.URL, Pool: pool, App: a, personas: map[string]*persona{}}
}

// testMasterKey 是每个测试自己的随机主密钥。
func testMasterKey() config.MasterKey {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	mk, err := config.ParseMasterKey(base64.StdEncoding.EncodeToString(b))
	if err != nil {
		panic(err)
	}
	return mk
}

// Exec 直接在测试库上执行 SQL（造数据、改时间）。
func (e *Env) Exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("执行 SQL 失败：%v\n%s", err, sql)
	}
}

// QueryValue 查一个值。
func QueryValue[T any](e *Env, sql string, args ...any) T {
	e.t.Helper()
	var v T
	if err := e.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("查询失败：%v\n%s", err, sql)
	}
	return v
}

// sanitizeName 把测试名变成能放进 S3 key 的样子。
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
