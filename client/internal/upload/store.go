package upload

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // 纯 Go 的 SQLite（不用 cgo）
)

// 本机库（requirements 第 6 节"上传队列"）：数据目录下的 upload-queue.db（SQLite，WAL），一行一个本地文件。
// 选 SQLite（modernc.org/sqlite，纯 Go）而不是 bbolt：队列要按状态计数、按 id 倒序取最近的、按"服务器 + 目标 +
// 子路径"分组取自然顺序的队首，这些用 SQL 和索引写起来最直接，积压几万项也不用整个读进内存；出问题时也能用任何
// sqlite 工具打开查看。一个数据目录只一个进程打开：旁边的 upload-queue.lock 用系统的排他锁锁住。
//
// 表：
//   - upload_items：队列项。(server_url, path_key) 唯一：同一个服务器下一个本地路径只有一行；没变不重传，变了当新内容。
//     rel_key 是子路径的小写（server 的同级文件夹名不分大小写），sort_key 是文件名的自然顺序键（[NaturalKey]）；
//   - folder_map：服务器 + 目标文件夹 + 子路径（小写）→ server 上建好的 folder_id；
//   - hotfolder：热文件夹的配置（一行）。

const (
	queueDBName   = "upload-queue.db"
	queueLockName = "upload-queue.lock"
	schemaVersion = 1
)

// ErrQueueLocked：同一个数据目录的队列已经被另一个进程打开了。
var ErrQueueLocked = errors.New("上传队列已被另一个 Doclite 进程打开")

const schema = `
CREATE TABLE IF NOT EXISTS upload_items (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	source           TEXT    NOT NULL,
	server_url       TEXT    NOT NULL,
	local_path       TEXT    NOT NULL,
	path_key         TEXT    NOT NULL,
	file_name        TEXT    NOT NULL,
	rel_dir          TEXT    NOT NULL,
	rel_key          TEXT    NOT NULL,
	sort_key         BLOB    NOT NULL,
	target_folder_id TEXT    NOT NULL,
	folder_id        TEXT,
	size             INTEGER NOT NULL,
	mtime_ns         INTEGER NOT NULL,
	sha256           TEXT,
	status           TEXT    NOT NULL,
	attempts         INTEGER NOT NULL DEFAULT 0,
	next_attempt_at  INTEGER,
	file_id          TEXT,
	server_file_name TEXT,
	sort_no          INTEGER,
	cover_status     TEXT,
	cover_reason     TEXT,
	error_code       TEXT,
	error_message    TEXT,
	warnings         TEXT    NOT NULL DEFAULT '[]',
	duplicate_of     TEXT,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	finished_at      INTEGER,
	UNIQUE (server_url, path_key)
);
CREATE INDEX IF NOT EXISTS upload_items_active ON upload_items (server_url, target_folder_id, rel_key, sort_key, id)
	WHERE status IN ('waiting', 'queued', 'uploading');
CREATE INDEX IF NOT EXISTS upload_items_status ON upload_items (server_url, status);
CREATE TABLE IF NOT EXISTS folder_map (
	server_url       TEXT NOT NULL,
	target_folder_id TEXT NOT NULL,
	rel_key          TEXT NOT NULL,
	folder_id        TEXT NOT NULL,
	PRIMARY KEY (server_url, target_folder_id, rel_key)
);
CREATE TABLE IF NOT EXISTS hotfolder (
	id                 INTEGER PRIMARY KEY CHECK (id = 1),
	watch_dir          TEXT    NOT NULL,
	target_folder_id   TEXT    NOT NULL,
	server_url         TEXT    NOT NULL,
	scan_interval_secs INTEGER NOT NULL,
	running            INTEGER NOT NULL
);
`

// Store 是打开的队列库。
type Store struct {
	db   *sql.DB
	lock *os.File
}

// OpenStore 打开（没有就建）dir 下的队列库，并把上次没传完的"上传中"放回"排队"。
// 别的进程已经打开时返回 [ErrQueueLocked]。
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockFile(filepath.Join(dir, queueLockName))
	if err != nil {
		return nil, err
	}
	// 普通路径（不用 file: 的 URI 写法，路径里的空格、中文不用转义）；? 后面是驱动自己处理的参数。
	dsn := filepath.Join(dir, queueDBName) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		lock.Close()
		return nil, err
	}
	// 一个连接：写入天然串行，不会撞 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	if err := s.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	if _, err := db.Exec(`UPDATE upload_items SET status = 'queued', updated_at = ? WHERE status = 'uploading'`, nowMS()); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > schemaVersion {
		return fmt.Errorf("上传队列库是更新版本的客户端建的（版本 %d）", v)
	}
	if v == schemaVersion {
		return nil
	}
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// Close 关闭库并释放锁。
func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
	return err
}

// PathKey 是比较本地路径用的键：Windows、macOS 的文件系统默认不分大小写，转成小写。
func PathKey(path string) string {
	p := filepath.Clean(path)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// RelKey 是子路径的比较键（server 的同级文件夹名不分大小写）。
func RelKey(relDir string) string { return strings.ToLower(relDir) }

const itemCols = `id, source, server_url, local_path, file_name, rel_dir, target_folder_id, folder_id, size, mtime_ns,
	sha256, status, attempts, next_attempt_at, file_id, server_file_name, sort_no, cover_status, cover_reason,
	error_code, error_message, warnings, duplicate_of, created_at, updated_at, finished_at`

type scanner interface{ Scan(dest ...any) error }

func scanItem(r scanner) (*Item, error) {
	var it Item
	var warnings string
	var dup sql.NullString
	err := r.Scan(&it.ID, &it.Source, &it.ServerURL, &it.LocalPath, &it.FileName, &it.RelDir, &it.TargetFolderID,
		&it.FolderID, &it.Size, &it.MtimeNS, &it.SHA256, &it.Status, &it.Attempts, &it.NextAttemptAt, &it.FileID,
		&it.ServerFileName, &it.SortNo, &it.CoverStatus, &it.CoverReason, &it.ErrorCode, &it.ErrorMessage, &warnings,
		&dup, &it.CreatedAt, &it.UpdatedAt, &it.FinishedAt)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(warnings), &it.Warnings) != nil || it.Warnings == nil {
		it.Warnings = []string{}
	}
	if dup.Valid && dup.String != "" {
		it.DuplicateOf = json.RawMessage(dup.String)
	}
	return &it, nil
}

func queryItems(ctx context.Context, db *sql.DB, query string, args ...any) ([]*Item, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Get 取一项；不存在时返回 nil, nil。
func (s *Store) Get(ctx context.Context, id int64) (*Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM upload_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

// FindByPath 按服务器和本地路径找一项；没有时返回 nil, nil。
func (s *Store) FindByPath(ctx context.Context, server, localPath string) (*Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM upload_items WHERE server_url = ? AND path_key = ?`,
		server, PathKey(localPath)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

func warningsJSON(w []string) string {
	if w == nil {
		w = []string{}
	}
	b, _ := json.Marshal(w)
	return string(b)
}

// Insert 新加一项（ID、CreatedAt、UpdatedAt 由这里填）。
func (s *Store) Insert(ctx context.Context, it *Item) error {
	now := nowMS()
	it.CreatedAt, it.UpdatedAt = now, now
	if it.Warnings == nil {
		it.Warnings = []string{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO upload_items (source, server_url, local_path, path_key, file_name, rel_dir,
		rel_key, sort_key, target_folder_id, folder_id, size, mtime_ns, sha256, status, attempts, next_attempt_at, file_id,
		server_file_name, sort_no, cover_status, cover_reason, error_code, error_message, warnings, duplicate_of, created_at,
		updated_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.Source, it.ServerURL, it.LocalPath, PathKey(it.LocalPath), it.FileName, it.RelDir, RelKey(it.RelDir),
		NaturalKey(it.FileName), it.TargetFolderID, it.FolderID, it.Size, it.MtimeNS, it.SHA256, it.Status, it.Attempts,
		it.NextAttemptAt, it.FileID, it.ServerFileName, it.SortNo, it.CoverStatus, it.CoverReason, it.ErrorCode,
		it.ErrorMessage, warningsJSON(it.Warnings), nullJSON(it.DuplicateOf), it.CreatedAt, it.UpdatedAt, it.FinishedAt)
	if err != nil {
		return err
	}
	it.ID, err = res.LastInsertId()
	return err
}

// Update 写回一项的全部可变字段（UpdatedAt 由这里填）。
func (s *Store) Update(ctx context.Context, it *Item) error {
	it.UpdatedAt = max(nowMS(), it.UpdatedAt+1)
	if it.Warnings == nil {
		it.Warnings = []string{}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE upload_items SET source = ?, file_name = ?, rel_dir = ?, rel_key = ?, sort_key = ?,
		target_folder_id = ?, folder_id = ?, size = ?, mtime_ns = ?, sha256 = ?, status = ?, attempts = ?, next_attempt_at = ?,
		file_id = ?, server_file_name = ?, sort_no = ?, cover_status = ?, cover_reason = ?, error_code = ?, error_message = ?,
		warnings = ?, duplicate_of = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
		it.Source, it.FileName, it.RelDir, RelKey(it.RelDir), NaturalKey(it.FileName), it.TargetFolderID, it.FolderID,
		it.Size, it.MtimeNS, it.SHA256, it.Status, it.Attempts, it.NextAttemptAt, it.FileID, it.ServerFileName, it.SortNo,
		it.CoverStatus, it.CoverReason, it.ErrorCode, it.ErrorMessage, warningsJSON(it.Warnings), nullJSON(it.DuplicateOf),
		it.UpdatedAt, it.FinishedAt, it.ID)
	return err
}

// Delete 删掉一项。
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM upload_items WHERE id = ?`, id)
	return err
}

// Heads 取各组的队首（组 = 服务器 + 目标文件夹 + 子路径；组内按文件名自然顺序，还没结束的——等待写完、排队、
// 上传中——里最前面的一项），只看 sources 这几个来源（被暂停的来源不参与，不会卡住同一个文件夹里别的来源的项）。
// 按队首的 id 排序（先发现的组先传）。
func (s *Store) Heads(ctx context.Context, server string, sources []string) ([]*Item, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(sources)), ", ")
	args := []any{server}
	for _, src := range sources {
		args = append(args, src)
	}
	return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY target_folder_id, rel_key ORDER BY sort_key, id) AS rn
		FROM upload_items
		WHERE server_url = ? AND status IN ('waiting', 'queued', 'uploading') AND source IN (`+ph+`)
	) WHERE rn = 1 ORDER BY id`, args...)
}

// WaitingOf 取某个来源等待写完的项。
func (s *Store) WaitingOf(ctx context.Context, server, source string) ([]*Item, error) {
	return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items
		WHERE server_url = ? AND status = 'waiting' AND source = ? ORDER BY id`, server, source)
}

// Stats 统计当前服务器各状态的数量。
func (s *Store) Stats(ctx context.Context, server string) (Stats, error) {
	var st Stats
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*),
		SUM(CASE WHEN status = 'queued' AND attempts > 0 AND next_attempt_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM upload_items WHERE server_url = ? GROUP BY status`, server)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var status Status
		var n, retrying int
		if err := rows.Scan(&status, &n, &retrying); err != nil {
			return st, err
		}
		switch status {
		case StatusWaiting:
			st.Waiting = n
		case StatusQueued:
			st.Queued = n
			st.Retrying = retrying
		case StatusUploading:
			st.Uploading = n
		case StatusDone:
			st.Done = n
		case StatusDuplicate:
			st.Duplicate = n
		case StatusFailed:
			st.Failed = n
		}
		st.Total += n
	}
	return st, rows.Err()
}

// maxKeptRows：最近文件之外另外返回的"上传中""失败"最多几项。
const maxKeptRows = 2000

// Recent 取当前服务器最近发现的 limit 项（id 倒序）；statuses 不为空时只要这几种状态。没有筛选时另外带上全部
// "上传中""失败"的项（页面上它们不受行数限制）。
func (s *Store) Recent(ctx context.Context, server string, limit int, statuses []Status) ([]*Item, error) {
	if len(statuses) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(statuses)), ", ")
		args := []any{server}
		for _, st := range statuses {
			args = append(args, string(st))
		}
		args = append(args, limit)
		return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items WHERE server_url = ? AND status IN (`+ph+`)
			ORDER BY id DESC LIMIT ?`, args...)
	}
	return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items WHERE id IN (
			SELECT id FROM (SELECT id FROM upload_items WHERE server_url = ? ORDER BY id DESC LIMIT ?)
			UNION SELECT id FROM (SELECT id FROM upload_items WHERE server_url = ? AND status IN ('uploading', 'failed')
				ORDER BY id DESC LIMIT ?)
		) ORDER BY id DESC`, server, limit, server, maxKeptRows)
}

// Failed 取当前服务器失败的项；ids 不为空时只要其中的。
func (s *Store) Failed(ctx context.Context, server string, ids []int64) ([]*Item, error) {
	if len(ids) == 0 {
		return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items WHERE server_url = ? AND status = 'failed' ORDER BY id`, server)
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := []any{server}
	for _, id := range ids {
		args = append(args, id)
	}
	return queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items WHERE server_url = ? AND status = 'failed'
		AND id IN (`+ph+`) ORDER BY id`, args...)
}

// Expedite 把等"连不上服务器"类错误重试的项提前到现在（server 恢复后不用等满退避时间）。返回改了的项。
func (s *Store) Expedite(ctx context.Context, server string, codes []string) ([]*Item, error) {
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(codes)), ", ")
	now := nowMS()
	args := []any{server, now}
	for _, c := range codes {
		args = append(args, c)
	}
	items, err := queryItems(ctx, s.db, `SELECT `+itemCols+` FROM upload_items WHERE server_url = ? AND status = 'queued'
		AND next_attempt_at > ? AND error_code IN (`+ph+`)`, args...)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	for _, it := range items {
		it.NextAttemptAt = int64Ptr(now)
		if err := s.Update(ctx, it); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// KnownPaths 是某个来源已入队的文件（path_key → 大小、修改时间），热文件夹扫描时没变的不再查库。
func (s *Store) KnownPaths(ctx context.Context, server, source string) (map[string]fileSig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path_key, size, mtime_ns FROM upload_items WHERE server_url = ? AND source = ?`,
		server, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fileSig{}
	for rows.Next() {
		var key string
		var sig fileSig
		if err := rows.Scan(&key, &sig.size, &sig.mtime); err != nil {
			return nil, err
		}
		out[key] = sig
	}
	return out, rows.Err()
}

// fileSig 是判断"文件变没变"的大小和修改时间。
type fileSig struct {
	size, mtime int64
}

// FolderOf 查子路径映射到的 folder_id；没有时返回空串。
func (s *Store) FolderOf(ctx context.Context, server, target, relKey string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT folder_id FROM folder_map WHERE server_url = ? AND target_folder_id = ? AND rel_key = ?`,
		server, target, relKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SetFolder 记下子路径映射到的 folder_id。
func (s *Store) SetFolder(ctx context.Context, server, target, relKey, folderID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO folder_map (server_url, target_folder_id, rel_key, folder_id) VALUES (?, ?, ?, ?)
		ON CONFLICT (server_url, target_folder_id, rel_key) DO UPDATE SET folder_id = excluded.folder_id`,
		server, target, relKey, folderID)
	return err
}

// ForgetFolders 忘掉 relKey 这条路径上各层的映射（server 上被删了，下次按名字重建）。
func (s *Store) ForgetFolders(ctx context.Context, server, target, relKey string) error {
	parts := strings.Split(relKey, "/")
	for i := range parts {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM folder_map WHERE server_url = ? AND target_folder_id = ? AND rel_key = ?`,
			server, target, strings.Join(parts[:i+1], "/")); err != nil {
			return err
		}
	}
	return nil
}

// HotConfig 读热文件夹的配置；没有设置过时返回 nil。
func (s *Store) HotConfig(ctx context.Context) (*HotConfig, error) {
	var c HotConfig
	var running int
	err := s.db.QueryRowContext(ctx, `SELECT watch_dir, target_folder_id, server_url, scan_interval_secs, running FROM hotfolder WHERE id = 1`).
		Scan(&c.WatchDir, &c.TargetFolderID, &c.ServerURL, &c.ScanIntervalSecs, &running)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Running = running != 0
	return &c, nil
}

// SetHotConfig 保存热文件夹的配置。
func (s *Store) SetHotConfig(ctx context.Context, c HotConfig) error {
	running := 0
	if c.Running {
		running = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO hotfolder (id, watch_dir, target_folder_id, server_url, scan_interval_secs, running)
		VALUES (1, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET watch_dir = excluded.watch_dir,
		target_folder_id = excluded.target_folder_id, server_url = excluded.server_url,
		scan_interval_secs = excluded.scan_interval_secs, running = excluded.running`,
		c.WatchDir, c.TargetFolderID, c.ServerURL, c.ScanIntervalSecs, running)
	return err
}
