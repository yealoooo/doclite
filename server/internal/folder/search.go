package folder

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 按文件名搜（requirements 5.5，用户 2026-10-25）：在文件夹和它的全部子文件夹里找名字含关键字的文件（不分大小写，
// % _ \ 按字面），按文件夹 path_code、sort_no 排，最多 limit 个。

// 搜索的条数：默认 100，最多 200；关键字 1–100 字。
const (
	DefaultSearchLimit = 100
	MaxSearchLimit     = 200
	maxSearchChars     = 100
)

// SearchHit 是搜到的一个文件。
type SearchHit struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileID       uuid.UUID `json:"file_id"`
	FileName     string    `json:"file_name"`
	FolderID     uuid.UUID `json:"folder_id"`
	// FolderPath 是所在文件夹的路径（"项目根/盒 1/件 3"）。
	FolderPath string `json:"folder_path"`
	SortNo     int    `json:"sort_no"`
}

// SearchResult 是 `GET /api/folders/{id}/search` 的响应；Truncated：不止 limit 个，只返回了前 limit 个。
type SearchResult struct {
	Items     []SearchHit `json:"items"`
	Truncated bool        `json:"truncated"`
}

// Search 在文件夹 id（项目 projectID）的子树里按文件名搜。
func (s *Service) Search(ctx context.Context, projectID, id uuid.UUID, q string, limit int) (*SearchResult, error) {
	q = strings.TrimSpace(q)
	if n := utf8.RuneCountInString(q); n == 0 || n > maxSearchChars {
		return nil, httpx.Invalid("搜索的关键字要 1–%d 个字", maxSearchChars)
	}
	out := &SearchResult{}
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if f == nil || f.Deleted || f.ProjectID != projectID {
			return errFolderNotFound
		}
		out.Items, err = searchFiles(ctx, tx, projectID, f.PathCode, db.ContainsPattern(q), limit+1)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items, out.Truncated = out.Items[:limit], true
	}
	return out, nil
}
