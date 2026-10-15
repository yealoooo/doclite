package apitest

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/config"
	"doclite/server/internal/platform/s3"
)

// S3 的测试支撑：
//   - 连接参数从环境变量读（CLAUDE.md"测试"）；SeaweedFS 没启动时 S3 测试明确失败，不悄悄跳过；
//   - 每个测试一个独立的 key 前缀（S3Prefix），不同 worktree、不同测试同时跑也不会碰到别人的对象；测试结束时
//     删掉这个前缀下的全部对象；测试里建的存储配置把 path_prefix 设成它；
//   - env.UseS3() 按 S3Env 登记默认存储（和首次启动的登记一样），path_prefix 是这个测试的前缀。

// S3Config 是测试用的 S3 连接参数。
type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
}

// S3Env 读 TEST_S3_*，没设置的用本机 SeaweedFS 的默认值。
func S3Env() S3Config {
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	return S3Config{
		Endpoint:  get("TEST_S3_ENDPOINT", "http://127.0.0.1:8333"),
		AccessKey: get("TEST_S3_ACCESS_KEY", "doclite-dev"),
		SecretKey: get("TEST_S3_SECRET_KEY", "doclite-dev-secret"),
		Bucket:    get("TEST_S3_BUCKET", "doclite"),
	}
}

var (
	s3Once   sync.Once
	s3Shared *s3.Client
	s3Err    error
)

// S3Client 是连 S3Env 的客户端（测试里直接读写对象用）。SeaweedFS 连不上时测试失败。
func S3Client(t *testing.T) *s3.Client {
	t.Helper()
	s3Once.Do(func() {
		c := S3Env()
		s3Shared, s3Err = s3.New(s3.Config{Endpoint: c.Endpoint, Bucket: c.Bucket, AccessKey: c.AccessKey,
			SecretKey: c.SecretKey, ForcePathStyle: true})
		if s3Err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, s3Err = s3Shared.List(ctx, "apitest/", 1)
	})
	if s3Err != nil {
		t.Fatalf("连不上测试用的 S3（%s，桶 %s）：%v\nS3 测试需要本机的 SeaweedFS：见 CLAUDE.md 的启动命令，或设置 TEST_S3_*",
			S3Env().Endpoint, S3Env().Bucket, s3Err)
	}
	return s3Shared
}

// S3Prefix 返回这个测试专用的对象 key 前缀：`apitest/<日期>/<测试名>-<随机>/`（以 / 结尾，符合存储配置
// path_prefix 的规则：每段只有字母、数字和 - _ .）。测试结束时删掉这个前缀下的全部对象。
func S3Prefix(t *testing.T) string {
	t.Helper()
	client := S3Client(t)
	name := sanitizeName(t.Name())
	if len(name) > 48 {
		name = name[:48]
	}
	prefix := "apitest/" + time.Now().Format("20060102") + "/" + name + "-" + randomHex(4) + "/"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := client.DeletePrefix(ctx, prefix); err != nil {
			t.Logf("清理 S3 前缀 %s 失败：%v", prefix, err)
		}
	})
	return prefix
}

// UseS3 按 S3Env 登记默认存储（和首次启动按 S3_* 登记一样，名称"SeaweedFS（随系统部署）"），path_prefix 是这个
// 测试的前缀。返回存储配置的 id 和前缀。库里已经有存储配置时测试失败。
func (e *Env) UseS3() (uuid.UUID, string) {
	e.t.Helper()
	prefix := S3Prefix(e.t)
	c := S3Env()
	if err := e.App.EnsureInitialStorage(context.Background(), &config.InitialStorage{
		Endpoint: c.Endpoint, Bucket: c.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey, PathPrefix: prefix,
	}); err != nil {
		e.t.Fatal(err)
	}
	id := QueryValue[uuid.UUID](e, "SELECT id FROM storage_profiles WHERE is_default")
	if p := QueryValue[string](e, "SELECT path_prefix FROM storage_profiles WHERE id = $1", id); p != prefix {
		e.t.Fatalf("库里已经有存储配置（默认存储的前缀 %s）", p)
	}
	return id, prefix
}

// PutObject 往测试 S3 里放一个对象。
func PutObject(t *testing.T, key, contentType string, data []byte) {
	t.Helper()
	if err := S3Client(t).Put(context.Background(), key, strings.NewReader(string(data)), s3.PutOptions{ContentType: contentType}); err != nil {
		t.Fatal(err)
	}
}

// httpGet 直接 GET 一个地址（预签名 URL），不带 Cookie。
func httpGet(t *testing.T, url string) (int, []byte) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}
