package apitest

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"

	"doclite/server/internal/config"
)

// TestMain 在跑测试之前：
//   - 读 server/.env（不存在就跳过；已经有的环境变量不覆盖），测试的连接参数（TEST_DATABASE_URL、TEST_S3_*）和 server
//     写在同一个文件里，每个 worktree 拷一份 .env 就能跑；
//   - 没在命令行给 -parallel 时，同时跑的测试最多 TEST_PARALLEL 个（默认 4；给了 -parallel 以命令行为准）。开发用的
//     PostgreSQL 在远程、和别的业务共用连接上限：每个测试有自己的连接池（见 testPoolMaxConns），并发多了会把连接占满。
//     4 是实测定的（2026-10-16，往返 43 ms）：全部 API 测试 8–11 分钟；同时跑 2 个时同一批测试慢 2 倍多。
func TestMain(m *testing.M) {
	if err := config.LoadDotEnv("../.env"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	flag.Parse()
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "test.parallel" {
			explicit = true
		}
	})
	if !explicit {
		n := 4
		if v, err := strconv.Atoi(os.Getenv("TEST_PARALLEL")); err == nil && v > 0 {
			n = v
		}
		if err := flag.Set("test.parallel", strconv.Itoa(n)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
