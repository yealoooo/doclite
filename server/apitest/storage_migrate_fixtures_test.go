package apitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	"doclite/server/internal/app"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/storage"
)

// 存储迁移（M7-2）的测试支撑：在导出的测试环境（能上传、审核、归类、导出，执行器 e.start() 再启动）上，另外建存储配置
// （第二个桶，或者同一个桶的另一个前缀）、发起迁移、等它结束、核对对象。
//
// 第二个桶：TEST_S3_BUCKET2（默认 doclite-migrate-test），不存在时用 S3 的 CreateBucket 建（SeaweedFS 的 weed mini 用
// 管理员密钥，可以建）；也可以先 `echo 's3.bucket.create -name doclite-migrate-test' | weed shell`。每个测试在里面用自己
// 的前缀，结束时删掉。

// migration 是 `…/migrate` 的响应。
type migration struct {
	ID                uuid.UUID          `json:"id"`
	SourceProfileID   uuid.UUID          `json:"source_profile_id"`
	SourceProfileName string             `json:"source_profile_name"`
	TargetProfileID   uuid.UUID          `json:"target_profile_id"`
	TargetProfileName string             `json:"target_profile_name"`
	Status            string             `json:"status"`
	CancelRequested   bool               `json:"cancel_requested"`
	Total             int                `json:"total"`
	Migrated          int                `json:"migrated"`
	Failed            int                `json:"failed"`
	FailedObjects     int                `json:"failed_objects"`
	Attempts          int                `json:"attempts"`
	MaxAttempts       int                `json:"max_attempts"`
	ErrorMessage      *string            `json:"error_message"`
	CreatedByName     *string            `json:"created_by_name"`
	FinishedAt        *time.Time         `json:"finished_at"`
	Remaining         *int               `json:"remaining"`
	Failures          []migrationFailure `json:"failures"`
	FailuresTruncated bool               `json:"failures_truncated"`
}

type migrationFailure struct {
	StorageKey    string `json:"storage_key"`
	ReasonCode    string `json:"reason_code"`
	Reason        string `json:"reason"`
	SnapshotCount int    `json:"snapshot_count"`
	Snapshots     []struct {
		SnapshotID  uuid.UUID `json:"snapshot_id"`
		FileID      uuid.UUID `json:"file_id"`
		VersionNo   int       `json:"version_no"`
		ProjectCode *string   `json:"project_code"`
		FileName    *string   `json:"file_name"`
		FolderPath  *string   `json:"folder_path"`
	} `json:"snapshots"`
}

// profileWithMigration 是列表里一项的迁移摘要。
type profileWithMigration struct {
	storageProfile
	Migration         *migration `json:"migration"`
	IncomingMigration *migration `json:"incoming_migration"`
}

func withMigrateBatch(batch, workers int) func(*exportOpts) {
	return func(o *exportOpts) {
		o.app = func(a *app.Options) {
			a.StorageMigrate = storage.MigrateOptions{BatchSize: batch, Workers: workers, RetryBackoff: 50 * time.Millisecond}
		}
	}
}

func storagePath(id uuid.UUID, rest string) string {
	return "/api/system/storage-profiles/" + id.String() + rest
}

// addStorage 用管理员新建一个存储配置（endpoint 为空时用测试 S3），返回 id。
func (e *exportEnv) addStorage(name, endpoint, bucket, prefix string) uuid.UUID {
	e.t.Helper()
	body := storageBody(name, prefix)
	if endpoint != "" {
		body["endpoint"] = endpoint
	}
	body["bucket"] = bucket
	return e.admin.Post("/api/system/storage-profiles", body).Expect(http.StatusCreated).ID()
}

func (e *exportEnv) setDefault(id uuid.UUID) {
	e.t.Helper()
	e.admin.Post(storagePath(id, "/set-default"), nil).Expect(http.StatusOK)
}

func (e *exportEnv) startMigration(src, dst uuid.UUID) migration {
	e.t.Helper()
	return Decode[migration](e.admin.Post(storagePath(src, "/migrate"), map[string]any{"target_profile_id": dst}).
		Expect(http.StatusCreated))
}

func (e *exportEnv) migration(src uuid.UUID) migration {
	e.t.Helper()
	return Decode[migration](e.admin.Get(storagePath(src, "/migrate")).Expect(http.StatusOK))
}

// waitMigration 等迁移满足 cond（每 30 毫秒看一次）。
func (e *exportEnv) waitMigration(src uuid.UUID, timeout time.Duration, what string, cond func(migration) bool) migration {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m := e.migration(src)
		if cond(m) {
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("迁移在 %s 内没有%s：%+v（error_message %s）", timeout, what, m, str(m.ErrorMessage))
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (e *exportEnv) waitMigrationDone(src uuid.UUID) migration {
	e.t.Helper()
	return e.waitMigration(src, 60*time.Second, "完成", func(m migration) bool {
		if m.Status == "failed" || m.Status == "canceled" {
			e.t.Fatalf("迁移没有完成：%+v（%s）", m, str(m.ErrorMessage))
		}
		return m.Status == "done"
	})
}

// uploadCovered 由扫描员上传一个内容唯一的 G4 TIFF（2480×3508）和合规的封面。
func (e *exportEnv) uploadCovered(folderID uuid.UUID, name string) ingestResponse {
	e.t.Helper()
	e.counter++
	data := unique(sampleBytes(e.t, "tiff-g4-1bit.tif"), e.p.ProjectCode+"-"+name+"-"+uuid.NewString())
	return Decode[ingestResponse](e.scan.Upload(folderID, name, data, coverFor(e.t, 2480, 3508)).Expect(http.StatusCreated))
}

// snapRow 是一个快照现在的位置。
type snapRow struct {
	ID        uuid.UUID
	FileID    uuid.UUID
	VersionNo int
	Profile   uuid.UUID
	Key       string
	Cover     *string
	SHA       []byte
}

// snapshots 是项目里全部快照（按 file_id、版本号）。
func (e *exportEnv) snapshots() []snapRow {
	e.t.Helper()
	rows, err := e.Pool.Query(context.Background(), `SELECT s.id, s.file_id, s.version_no, s.storage_profile_id, s.storage_key,
  s.cover_key, s.content_sha256
FROM file_snapshots s JOIN folder_files ff ON ff.file_id = s.file_id
WHERE ff.project_id = $1 ORDER BY s.file_id, s.version_no`, e.p.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []snapRow
	for rows.Next() {
		var r snapRow
		if err := rows.Scan(&r.ID, &r.FileID, &r.VersionNo, &r.Profile, &r.Key, &r.Cover, &r.SHA); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *exportEnv) snapshotsIn(profile uuid.UUID) int {
	e.t.Helper()
	return QueryValue[int](e.Env, `SELECT count(*) FROM file_snapshots WHERE storage_profile_id = $1`, profile)
}

// bucket2Name 是第二个测试桶（TEST_S3_BUCKET2，默认 doclite-migrate-test）。
func bucket2Name() string {
	if name := os.Getenv("TEST_S3_BUCKET2"); name != "" {
		return name
	}
	return "doclite-migrate-test"
}

var (
	bucket2Once sync.Once
	bucket2Err  error
)

// ensureBucket2 确认第二个测试桶存在，不存在时建（每个测试进程一次）。
func ensureBucket2() error {
	bucket2Once.Do(func() {
		c, name := S3Env(), bucket2Name()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		api := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String(c.Endpoint), UsePathStyle: true,
			Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: testHTTP})
		if _, err := api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(name)}); err != nil {
			_, err := api.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
			var owned *types.BucketAlreadyOwnedByYou
			var exists *types.BucketAlreadyExists
			if err != nil && !errors.As(err, &owned) && !errors.As(err, &exists) {
				bucket2Err = fmt.Errorf("第二个测试桶 %s 不存在也建不了：%w", name, err)
			}
		}
	})
	return bucket2Err
}

// bucket2 是第二个测试桶的名字和连它的客户端（不存在时建），另给一个这个测试专用的前缀（结束时删掉）。
func bucket2(t *testing.T) (string, *s3.Client, string) {
	t.Helper()
	S3Client(t)
	c := S3Env()
	name := bucket2Name()
	if err := ensureBucket2(); err != nil {
		t.Fatal(err)
	}
	client, err := s3.New(s3.Config{Endpoint: c.Endpoint, Bucket: name, AccessKey: c.AccessKey, SecretKey: c.SecretKey,
		ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "apitest/" + time.Now().Format("20060102") + "/" + sanitizeName(t.Name()) + "-" + randomHex(4) + "/"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := deletePrefix(ctx, client, prefix); err != nil {
			t.Logf("清理第二个桶的前缀 %s 失败：%v", prefix, err)
		}
	})
	return name, client, prefix
}

// objectSHA 读一个对象，返回 sha256（不存在时 nil）。
func objectSHA(t *testing.T, c *s3.Client, key string) []byte {
	t.Helper()
	rc, err := c.Get(context.Background(), key)
	if errors.Is(err, s3.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	h := sha256.New()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	h.Write(buf.Bytes())
	return h.Sum(nil)
}

// listAll 是 prefix 下全部对象（最多 1000 个）。
func listAll(t *testing.T, c *s3.Client, prefix string) []string {
	t.Helper()
	keys, err := c.List(context.Background(), prefix, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// tail3 是 key 的后三段（<项目编码>/<file_id>/v<n>.<ext>）。
func tail3(key string) string {
	parts := strings.Split(key, "/")
	return strings.Join(parts[len(parts)-3:], "/")
}

// auditCount 数审计。
func (e *exportEnv) auditCount(targetType, action string) int {
	e.t.Helper()
	return QueryValue[int](e.Env, `SELECT count(*) FROM audit_logs WHERE target_type = $1 AND action = $2`, targetType, action)
}
