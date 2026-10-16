package license

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// license 的 SQL：system_settings 里的两行（这个模块自己的）。
//   - `install_id`：值是 JSON 字符串 `"sha256:<64 位十六进制>"`，首次启动生成，以后不变；
//   - `license`：值是 JSON 字符串，导入的 .lic 原文（启动时重新验签）；updated_by、updated_at 是谁、什么时候导入的。
//
// install_id 用签发工具要求的 hardware_id 格式（`sha256:` + 64 位十六进制，工具签发时会校验），内容是 32 字节随机数，
// 这样签发时直接把它填进 --hardware-id。

const (
	keyInstallID = "install_id"
	keyLicense   = "license"
)

// newInstallID 生成 install_id：`sha256:` + 32 字节随机数的十六进制。
func newInstallID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(b), nil
}

// ensureInstallID 读 install_id，没有时生成并写入（两个进程同时首次启动时以先写入的为准）。
func ensureInstallID(ctx context.Context, q db.Querier) (string, error) {
	id, err := newInstallID()
	if err != nil {
		return "", err
	}
	if _, err := q.Exec(ctx, `INSERT INTO system_settings (setting_key, setting_value) VALUES ($1, to_jsonb($2::text))
ON CONFLICT (setting_key) DO NOTHING`, keyInstallID, id); err != nil {
		return "", err
	}
	var out string
	err = q.QueryRow(ctx, `SELECT setting_value #>> '{}' FROM system_settings WHERE setting_key = $1`, keyInstallID).Scan(&out)
	return out, err
}

// storedLicense 是库里导入的许可证。
type storedLicense struct {
	Text       string
	ImportedAt time.Time
	ImportedBy *string // 导入人的显示名（用户不在了、系统写入时为空）
}

// loadLicense 读导入的许可证；没有时 nil。lock 为 true 时锁住这一行（导入时，和另一个导入串行）。
func loadLicense(ctx context.Context, q db.Querier, lock bool) (*storedLicense, error) {
	sql := `SELECT s.setting_value #>> '{}', s.updated_at, u.display_name
FROM system_settings s LEFT JOIN users u ON u.id = s.updated_by
WHERE s.setting_key = $1`
	if lock {
		sql += ` FOR NO KEY UPDATE OF s`
	}
	var st storedLicense
	err := q.QueryRow(ctx, sql, keyLicense).Scan(&st.Text, &st.ImportedAt, &st.ImportedBy)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// saveLicense 写入（或替换）导入的许可证，返回写入时间。
func saveLicense(ctx context.Context, q db.Querier, text string, by uuid.UUID) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `INSERT INTO system_settings (setting_key, setting_value, updated_by, updated_at)
VALUES ($1, to_jsonb($2::text), $3, now())
ON CONFLICT (setting_key) DO UPDATE SET setting_value = EXCLUDED.setting_value, updated_by = EXCLUDED.updated_by,
  updated_at = EXCLUDED.updated_at
RETURNING updated_at`, keyLicense, text, by).Scan(&at)
	return at, err
}

// lockInstallID 锁住 install_id 这一行（导入时先锁它：第一次导入时 license 行还不存在，锁不住）。
func lockInstallID(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `SELECT 1 FROM system_settings WHERE setting_key = $1 FOR NO KEY UPDATE`, keyInstallID)
	return err
}
