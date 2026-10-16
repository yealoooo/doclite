package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"doclite/server/internal/platform/jobs"
	"doclite/server/internal/platform/s3"
)

// targetKey 是对象在目标存储里的 key：目标存储的 path_prefix + 硬性规则 7 的后三段（`<项目编码>/<file_id>/v<n>.<ext>`、
// `v<n>.cover.jpg`）。共用对象的快照指向同一个 key，换算后也还是同一个。不是这个形状的 key（理论上没有）：去掉源存储现在
// 的前缀（有的话）再加目标的前缀。同一个桶里换前缀也不会和源对象撞 key（建任务时拒绝了地址、桶、前缀都相同的目标）。
func targetKey(key, srcPrefix, dstPrefix string) string {
	parts := strings.Split(key, "/")
	if n := len(parts); n >= 3 {
		if _, err := uuid.Parse(parts[n-2]); err == nil && parts[n-3] != "" {
			return dstPrefix + strings.Join(parts[n-3:], "/")
		}
	}
	return dstPrefix + strings.TrimPrefix(key, srcPrefix)
}

// copyObject 把源存储的 key 复制到目标存储的 newKey 并核对：
//   - 源对象读到临时文件（TMP_DIR/migrate/），同时算 sha256；want 不为 nil 时（原图）和快照记的 sha256、大小核对；
//   - 写到目标存储（Content-Type 用 contentType，元数据原样带上，Cache-Control 按目标存储的预签名有效期）；
//   - 从目标存储读回来再算一次 sha256，和源对象的比。
//
// 核对不一致返回失败代码和说明（这个对象的快照不改）；源对象不存在是 source_missing。读写存储、临时盘出错返回可以重试的
// 错误（整次执行停下，按执行器的退避重试，从断点接着做）。不删源对象。
func (r *migrateRunner) copyObject(ctx context.Context, ends *endpoints, key, newKey string, want *groupRow,
	contentType string) (code, why string, err error) {
	body, info, err := ends.srcC.GetWithInfo(ctx, key)
	if errors.Is(err, s3.ErrNotFound) {
		return "source_missing", "源存储里没有这个对象（" + key + "），没有迁", nil
	}
	if err != nil {
		return "", "", retryS3("读源存储", err)
	}
	dir := MigrateTmpDir(r.m.opts.TmpDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		body.Close()
		return "", "", jobs.Retry(fmt.Errorf("建临时目录失败：%w", err))
	}
	f, err := os.CreateTemp(dir, "obj-*.part")
	if err != nil {
		body.Close()
		return "", "", jobs.Retry(fmt.Errorf("建临时文件失败：%w", err))
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), body)
	body.Close()
	if err != nil {
		return "", "", retryS3("读源存储（或写临时文件）", err)
	}
	sum := h.Sum(nil)
	if want != nil {
		if n != want.Size {
			return "size_mismatch", fmt.Sprintf("源存储里的原图有 %d 字节，快照记的是 %d 字节，没有迁", n, want.Size), nil
		}
		if !bytes.Equal(sum, want.SHA256) {
			return "sha256_mismatch", "源存储里的原图 sha256 和快照记的不一样，没有迁", nil
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", jobs.Retry(fmt.Errorf("读临时文件失败：%w", err))
	}
	if contentType == "" {
		contentType = info.ContentType
	}
	if err := ends.dstC.Put(ctx, newKey, f, s3.PutOptions{ContentType: contentType, CacheControl: ends.cacheControl,
		Metadata: info.Metadata}); err != nil {
		return "", "", retryS3("写目标存储", err)
	}
	rc, err := ends.dstC.Get(ctx, newKey)
	if errors.Is(err, s3.ErrNotFound) {
		return "verify_failed", "复制到目标存储后读不到（" + newKey + "），没有迁", nil
	}
	if err != nil {
		return "", "", retryS3("读目标存储核对", err)
	}
	h2 := sha256.New()
	m, err := io.Copy(h2, rc)
	rc.Close()
	if err != nil {
		return "", "", retryS3("读目标存储核对", err)
	}
	if m != n || !bytes.Equal(h2.Sum(nil), sum) {
		return "verify_failed", "复制到目标存储后读回来和源对象不一致（" + newKey + "），没有迁", nil
	}
	return "", "", nil
}

func retryS3(what string, err error) error {
	return jobs.Retry(fmt.Errorf("%s失败：%w", what, err))
}
