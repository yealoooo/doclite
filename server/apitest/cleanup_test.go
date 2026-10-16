package apitest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/s3"
)

// 清理被中断的测试留下的东西（强杀、超时、卡住被杀时 t.Cleanup 来不及跑）。每个测试进程第一次用到数据库、S3 时在后台
// 各做一次，不耽误测试：
//   - 测试库 `doclite_test_<16 位十六进制>`：建了超过 6 小时、而且没有任何连接的。建库时间看库名的前 8 位（Unix 秒，
//     见 newTestDBName）；以前的随机库名看库目录的修改时间（pg_stat_file，要超级用户或 pg_read_server_files，
//     没权限就不动它）。删库不带 FORCE：这时有人连上了就删不掉。模板库（doclite_test_tpl_…）不动；
//   - 两个测试桶里 `apitest/<日期>/` 下日期早于今天的对象；零点到 6 点之间只删前天及更早的，跨零点还在跑的测试不受影响。
//
// 几个 worktree 同时跑测试时各清各的，不会删到别人正在用的：正在用的库有连接，刚建的库不到 6 小时，今天的前缀不删。

// staleAge 是残留多久才清理。
const staleAge = 6 * time.Hour

var testDBPattern = regexp.MustCompile(`^doclite_test_[0-9a-f]{16}$`)

// nameTimeSince 是库名开始带建库时间的日子：前 8 位换算出来早于它的是以前的随机库名。
var nameTimeSince = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

// newTestDBName 是测试库的名字：`doclite_test_` + 建库时间（Unix 秒，8 位十六进制）+ 8 位随机十六进制。
func newTestDBName(now time.Time) string {
	return fmt.Sprintf("doclite_test_%08x%s", uint32(now.Unix()), randomHex(4))
}

// testDBCreatedAt 从库名读建库时间；不是 newTestDBName 的格式（以前的随机库名）时 ok 为 false。
func testDBCreatedAt(name string, now time.Time) (time.Time, bool) {
	if !testDBPattern.MatchString(name) {
		return time.Time{}, false
	}
	sec, err := strconv.ParseUint(strings.TrimPrefix(name, "doclite_test_")[:8], 16, 32)
	if err != nil {
		return time.Time{}, false
	}
	t := time.Unix(int64(sec), 0)
	if t.Before(nameTimeSince) || t.After(now.Add(time.Hour)) {
		return time.Time{}, false
	}
	return t, true
}

// dropStaleDBs 删掉残留的测试库，返回删了哪些。
func dropStaleDBs(ctx context.Context, mp *pgxpool.Pool, now time.Time) ([]string, error) {
	rows, err := mp.Query(ctx, `SELECT d.datname, d.oid::text FROM pg_database d
WHERE d.datname ~ '^doclite_test_[0-9a-f]{16}$'
  AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datid = d.oid)`)
	if err != nil {
		return nil, err
	}
	type cand struct{ Name, OID string }
	cands, err := pgx.CollectRows(rows, pgx.RowToStructByPos[cand])
	if err != nil {
		return nil, err
	}
	var dropped []string
	for _, c := range cands {
		if !testDBPattern.MatchString(c.Name) {
			continue
		}
		if created, ok := testDBCreatedAt(c.Name, now); ok {
			if now.Sub(created) < staleAge {
				continue
			}
		} else {
			// 以前的随机库名：库目录最后一次改动（建库、第一次连上时都会改）在 6 小时前。没权限读就不动它。
			var old bool
			if err := mp.QueryRow(ctx, `SELECT now() - (pg_stat_file('base/' || $1)).modification > $2::interval`,
				c.OID, fmt.Sprintf("%d seconds", int(staleAge.Seconds()))).Scan(&old); err != nil || !old {
				continue
			}
		}
		if _, err := mp.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{c.Name}.Sanitize()); err != nil {
			continue // 这时有人连上了
		}
		dropped = append(dropped, c.Name)
	}
	return dropped, nil
}

// cleanupStaleDBs 在后台清理残留的测试库（maintenancePool 第一次连上时调用）。
func cleanupStaleDBs(mp *pgxpool.Pool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		dropped, err := dropStaleDBs(ctx, mp, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "apitest：清理残留的测试库失败：%v\n", err)
		}
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "apitest：删掉了 %d 个残留的测试库\n", len(dropped))
		}
	}()
}

// staleDateCutoff：`apitest/<日期>/` 的日期小于它的可以删（6 小时前那一刻的日期，本机时区，和 S3Prefix 一样）。
func staleDateCutoff(now time.Time) string { return now.Add(-staleAge).Format("20060102") }

// deleteStalePrefixes 删掉桶里 `apitest/<日期>/` 下日期小于 cutoff 的对象，返回删了几个。key 按字典序排、日期小的
// 在前：每次看最前面的 key，日期够早就把这一天的前缀整个删掉，再看下一个。
func deleteStalePrefixes(ctx context.Context, c *s3.Client, cutoff string) (int, error) {
	total, last := 0, ""
	for {
		keys, err := c.List(ctx, "apitest/", 1)
		if err != nil || len(keys) == 0 {
			return total, err
		}
		date, _, ok := strings.Cut(strings.TrimPrefix(keys[0], "apitest/"), "/")
		if !ok || !isDate(date) || date >= cutoff || keys[0] == last {
			return total, nil
		}
		last = keys[0]
		n, err := deletePrefix(ctx, c, "apitest/"+date+"/")
		total += n
		if err != nil {
			return total, err
		}
	}
}

func isDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	_, err := time.Parse("20060102", s)
	return err == nil
}

// cleanupStalePrefixes 在后台清理两个测试桶里残留的前缀（S3Client 第一次连上时调用）。
func cleanupStalePrefixes() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		cutoff := staleDateCutoff(time.Now())
		c := S3Env()
		for _, bucket := range []string{c.Bucket, bucket2Name()} {
			client, err := s3.New(s3.Config{Endpoint: c.Endpoint, Bucket: bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey,
				ForcePathStyle: true})
			if err != nil {
				continue
			}
			n, err := deleteStalePrefixes(ctx, client, cutoff)
			if err != nil && !strings.Contains(err.Error(), "NoSuchBucket") {
				fmt.Fprintf(os.Stderr, "apitest：清理桶 %s 里残留的 apitest/ 前缀失败：%v\n", bucket, err)
			}
			if n > 0 {
				fmt.Fprintf(os.Stderr, "apitest：删掉了桶 %s 里 %d 个残留的对象（apitest/ 下 %s 以前的日期）\n", bucket, n, cutoff)
			}
		}
	}()
}

// deletePrefix 删掉 prefix 下的全部对象（同时删 8 个：远程存储一次往返几十毫秒，一个一个删太慢），返回删了几个。
func deletePrefix(ctx context.Context, c *s3.Client, prefix string) (int, error) {
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return 0, fmt.Errorf("deletePrefix：前缀 %q 不对", prefix)
	}
	total := 0
	for {
		keys, err := c.List(ctx, prefix, 1000)
		if err != nil || len(keys) == 0 {
			return total, err
		}
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			errs []error
			next = make(chan string)
		)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := range next {
					if err := c.Delete(ctx, k); err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
					}
				}
			}()
		}
		for _, k := range keys {
			next <- k
		}
		close(next)
		wg.Wait()
		if len(errs) > 0 {
			return total + len(keys) - len(errs), errors.Join(errs...)
		}
		total += len(keys)
	}
}

// TestCleanupRules：清理残留时怎么认库名和日期（不连数据库和 S3）。
func TestCleanupRules(t *testing.T) {
	now := time.Date(2026, 10, 17, 5, 0, 0, 0, time.Local)
	name := newTestDBName(now.Add(-time.Hour))
	if !testDBPattern.MatchString(name) {
		t.Fatalf("库名 %s 不是 doclite_test_<16 位十六进制>", name)
	}
	if at, ok := testDBCreatedAt(name, now); !ok || !at.Equal(now.Add(-time.Hour)) {
		t.Fatalf("从 %s 读出的建库时间：%v %v", name, at, ok)
	}
	// 以前的随机库名（前 8 位换算出来不在合理范围里）、模板库、别的库都不认。
	for _, n := range []string{"doclite_test_0000000012345678", "doclite_test_ffffffff12345678", "doclite_test_tpl_3e3d5dcd7812679e",
		"doclite_test", "doclite_dev", "castoon", "doclite_test_" + fmt.Sprintf("%08x", uint32(now.Add(2*time.Hour).Unix())) + "00000000"} {
		if _, ok := testDBCreatedAt(n, now); ok {
			t.Errorf("%s 不该按库名算建库时间", n)
		}
	}
	// 5 点：昨天（17 日的前一天）的前缀还不删，前天的删；7 点以后昨天的也删。
	if got := staleDateCutoff(now); got != "20261016" {
		t.Errorf("5 点的分界 %s", got)
	}
	if got := staleDateCutoff(now.Add(2 * time.Hour)); got != "20261017" {
		t.Errorf("7 点的分界 %s", got)
	}
	if !isDate("20261016") || isDate("2026101") || isDate("TestUplo") || isDate("20261341") {
		t.Error("isDate")
	}
}
