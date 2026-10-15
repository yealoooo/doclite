package apitest

import (
	"os"
	"testing"
	"time"
)

// S3 的测试支撑（M1-3 起用到）：
//   - 连接参数从环境变量读（CLAUDE.md"测试"）；SeaweedFS 没启动时 S3 测试要明确报错，不悄悄跳过；
//   - 每个测试一个独立的 key 前缀（S3Prefix），不同 worktree、不同测试同时跑也不会碰到别人的对象；
//     测试里建的存储配置把 path_prefix 设成它。
//
// M1-3 加 S3 客户端（internal/platform/s3）时，在 S3Prefix 里注册 t.Cleanup：删掉这个前缀下的全部对象。

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

// S3Prefix 返回这个测试专用的对象 key 前缀：`apitest/<日期>/<测试名>-<随机>/`（以 / 结尾，符合存储配置
// path_prefix 的规则：每段只有字母、数字和 - _ .）。
func S3Prefix(t *testing.T) string {
	t.Helper()
	name := sanitizeName(t.Name())
	if len(name) > 48 {
		name = name[:48]
	}
	return "apitest/" + time.Now().Format("20060102") + "/" + name + "-" + randomHex(4) + "/"
}
