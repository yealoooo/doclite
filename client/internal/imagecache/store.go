package imagecache

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"doclite/client/internal/appdata"
	"doclite/client/internal/tiles"
)

// 本机缓存的存放（requirements 第 6 节"图片缓存"）：
//
//	image-cache/
//	├── index.json                      索引：每项的 file_id、snapshot_id、project_id、种类、大小、最近访问；上限
//	├── tmp/                            下载、切块的临时文件（启动时清空）
//	└── <file_id>/
//	    ├── <snapshot_id>.cover.jpg     封面
//	    ├── <snapshot_id>.original.<ext> 原图（jpg、png、tif，认不出的 bin）
//	    └── <snapshot_id>.tiles/        原图分块：<级>/<x>_<y>.<jpg|png>，索引里算一项
//
// 按 snapshot_id 存（快照不会变，不需要失效）。索引是 JSON 加内存：改动后 2 秒写盘，只改了访问时间的 30 秒写盘，
// 退出时写盘。不做索引损坏重建和启动对账（requirements 的简化）：索引读不出来时改名为 index.json.bad、从空的开始；
// 索引外的文件（例如崩溃前没来得及记进索引的）不计入占用，同一张图再下载时会覆盖它，"全部清空"会删掉它们。

// 种类。
const (
	KindCover    = "cover"
	KindOriginal = "original"
	KindTiles    = "tiles"
)

// 上限（字节）：默认 2 GB，可在 100 MB – 1 TB 之间改（web/src/lib/image-cache.ts）。
const (
	DefaultLimit = 2 << 30
	MinLimit     = 100 << 20
	MaxLimit     = 1 << 40
)

const (
	flushDelay      = 2 * time.Second
	touchFlushDelay = 30 * time.Second
	indexName       = "index.json"
	indexVersion    = 1
)

// entry 是索引的一项（JSON 用短键名，几万项时索引文件小一些）。
type entry struct {
	FileID     string        `json:"f"`
	SnapshotID string        `json:"s"`
	ProjectID  string        `json:"p"`
	Kind       string        `json:"k"`
	Ext        string        `json:"e,omitempty"`
	Size       int64         `json:"n"`
	Access     int64         `json:"a"` // 最近访问，Unix 毫秒
	Tiles      *tiles.Result `json:"t,omitempty"`
}

func entryKey(snapshotID, kind string) string { return snapshotID + "." + kind }

func (e *entry) key() string { return entryKey(e.SnapshotID, e.Kind) }

// relPath 是缓存目录下的相对路径。
func (e *entry) relPath() string {
	name := e.SnapshotID
	switch e.Kind {
	case KindCover:
		name += ".cover.jpg"
	case KindOriginal:
		name += ".original." + e.Ext
	default:
		name += ".tiles"
	}
	return filepath.Join(e.FileID, name)
}

type indexFile struct {
	Version    int      `json:"version"`
	LimitBytes int64    `json:"limit_bytes"`
	Entries    []*entry `json:"entries"`
}

// token 是存入时核对的"清空代数"：开始下载、切块时取一个，存入时代数变了（这段时间里清空过）就不存。
type token struct{ all, project uint64 }

// store 是索引和文件。所有改动（存入、淘汰、清空、改上限）都在 mu 里做。
type store struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	entries  map[string]*entry
	total    int64
	limit    int64
	clearAll uint64
	clears   map[string]uint64 // project_id → 清空次数

	dirty, touched bool
	timer          *time.Timer
	timerAt        time.Time
	closed         bool
}

func openStore(dir string, log *slog.Logger) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("建不了缓存目录：%w", err)
	}
	tmp := filepath.Join(dir, "tmp")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, fmt.Errorf("建不了缓存的临时目录：%w", err)
	}
	s := &store{dir: dir, log: log, entries: map[string]*entry{}, limit: DefaultLimit, clears: map[string]uint64{}}
	path := filepath.Join(dir, indexName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("读不了缓存索引：%w", err)
	default:
		var idx indexFile
		if err := json.Unmarshal(data, &idx); err != nil || idx.Version != indexVersion {
			log.Warn("缓存索引读不出来，改名后从空的开始", "err", err, "version", idx.Version)
			_ = os.Rename(path, path+".bad")
			break
		}
		if idx.LimitBytes >= MinLimit && idx.LimitBytes <= MaxLimit {
			s.limit = idx.LimitBytes
		}
		for _, e := range idx.Entries {
			if e == nil || e.Size < 0 || (e.Kind != KindCover && e.Kind != KindOriginal && e.Kind != KindTiles) {
				continue
			}
			if _, dup := s.entries[e.key()]; dup {
				continue
			}
			s.entries[e.key()] = e
			s.total += e.Size
		}
	}
	// 能不能写：先写一次索引（新建的目录、只读的目录在这里就能发现）。
	if err := s.writeIndexLocked(); err != nil {
		return nil, fmt.Errorf("写不了缓存索引：%w", err)
	}
	return s, nil
}

func (s *store) tmpDir() string { return filepath.Join(s.dir, "tmp") }

func (s *store) path(e *entry) string { return filepath.Join(s.dir, e.relPath()) }

// lookup 取一项并记下访问时间（返回副本）。
func (s *store) lookup(key string) (entry, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return entry{}, "", false
	}
	e.Access = time.Now().UnixMilli()
	s.touched = true
	s.scheduleLocked()
	return *e, s.path(e), true
}

// has 判断有没有这一项（不记访问）。
func (s *store) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	return ok
}

func (s *store) currentLimit() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

func (s *store) token(projectID string) token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return token{all: s.clearAll, project: s.clears[projectID]}
}

// put 把临时文件（或目录）src 存成 e：改名到位、记进索引，超出上限时按最近访问淘汰到 90%（刚存的这项不删）。
// 拿到 tok 之后清空过这个项目（或全部）时不存，返回 false（src 留给调用方处理）。
func (s *store) put(e *entry, src string, tok token) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.all != s.clearAll || tok.project != s.clears[e.ProjectID] {
		return false, nil
	}
	dst := s.path(e)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	key := e.key()
	if old, ok := s.entries[key]; ok {
		s.total -= old.Size
		delete(s.entries, key)
	}
	_ = os.RemoveAll(dst) // 索引外的旧文件（见文件开头）
	if err := os.Rename(src, dst); err != nil {
		s.dirty = true
		s.scheduleLocked()
		return false, err
	}
	e.Access = time.Now().UnixMilli()
	s.entries[key] = e
	s.total += e.Size
	if s.total > s.limit {
		s.evictLocked(s.limit*9/10, key)
	}
	s.dirty = true
	s.scheduleLocked()
	return true, nil
}

// remove 删掉一项（文件不见了、索引对不上时）。
func (s *store) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return
	}
	_ = os.RemoveAll(s.path(e))
	delete(s.entries, key)
	s.total -= e.Size
	s.dirty = true
	s.scheduleLocked()
}

// pickEvictions 按最近访问从旧到新挑要删的项，直到占用不超过 target；keep 这一项不删。
func pickEvictions(entries []*entry, total, target int64, keep string) []*entry {
	if total <= target {
		return nil
	}
	sorted := append([]*entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Access != sorted[j].Access {
			return sorted[i].Access < sorted[j].Access
		}
		return sorted[i].key() < sorted[j].key()
	})
	var out []*entry
	for _, e := range sorted {
		if total <= target {
			break
		}
		if e.key() == keep {
			continue
		}
		out = append(out, e)
		total -= e.Size
	}
	return out
}

func (s *store) evictLocked(target int64, keep string) {
	list := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	victims := pickEvictions(list, s.total, target, keep)
	for _, e := range victims {
		if err := os.RemoveAll(s.path(e)); err != nil {
			// 删不掉（Windows 上正被读着）：照样从索引里拿掉，文件留在索引外（见文件开头）。
			s.log.Warn("淘汰缓存时删不掉文件", "path", s.path(e), "err", err)
		}
		delete(s.entries, e.key())
		s.total -= e.Size
	}
	if len(victims) > 0 {
		s.log.Info("缓存超出上限，按最近访问淘汰", "removed", len(victims), "total", s.total, "limit", s.limit)
	}
}

// ClearReport 是 cache_clear 的结果。
type ClearReport struct {
	RemovedFiles int   `json:"removed_files"`
	RemovedBytes int64 `json:"removed_bytes"`
	// FailedFiles 是删不掉的项数（例如被别的程序占着）；它们留在索引里，下次再试。
	FailedFiles int `json:"failed_files"`
}

// clear 清空一个项目（projectID 为空表示全部）。在途的下载、切块在存入时发现代数变了，不再存。
// 全部清空时连索引外的文件一起删（每个 <file_id> 目录整个删掉）。
func (s *store) clear(projectID string) ClearReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rep ClearReport
	if projectID == "" {
		s.clearAll++
	} else {
		s.clears[projectID]++
	}
	dirs := map[string]bool{}
	for key, e := range s.entries {
		if projectID != "" && e.ProjectID != projectID {
			continue
		}
		dirs[e.FileID] = true
		if err := os.RemoveAll(s.path(e)); err != nil {
			rep.FailedFiles++
			continue
		}
		rep.RemovedFiles++
		rep.RemovedBytes += e.Size
		s.total -= e.Size
		delete(s.entries, key)
	}
	if projectID == "" {
		if list, err := os.ReadDir(s.dir); err == nil {
			for _, d := range list {
				if d.IsDir() && IsUUID(d.Name()) {
					dirs[d.Name()] = true
				}
			}
		}
	}
	// 删掉空了的 <file_id> 目录；全部清空时删掉没有索引项的整个目录。
	inUse := map[string]bool{}
	for _, e := range s.entries {
		inUse[e.FileID] = true
	}
	for d := range dirs {
		p := filepath.Join(s.dir, d)
		if inUse[d] {
			continue
		}
		if projectID == "" {
			_ = os.RemoveAll(p)
		} else {
			_ = os.Remove(p)
		}
	}
	s.dirty = true
	_ = s.flushLocked()
	return rep
}

// setLimit 改上限，超出时立即淘汰到 90%。
func (s *store) setLimit(limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = limit
	if s.total > limit {
		s.evictLocked(limit*9/10, "")
	}
	s.dirty = true
	_ = s.flushLocked()
}

// usage 是占用的统计。
func (s *store) usage() (total int64, count int, limit int64, projects []ProjectUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]*ProjectUsage{}
	for _, e := range s.entries {
		u := by[e.ProjectID]
		if u == nil {
			u = &ProjectUsage{ProjectID: e.ProjectID}
			by[e.ProjectID] = u
		}
		u.TotalBytes += e.Size
		u.FileCount++
		switch e.Kind {
		case KindCover:
			u.CoverCount++
		case KindOriginal:
			u.OriginalCount++
		case KindTiles:
			u.TilesCount++
		}
	}
	projects = make([]ProjectUsage, 0, len(by))
	for _, u := range by {
		projects = append(projects, *u)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].TotalBytes != projects[j].TotalBytes {
			return projects[i].TotalBytes > projects[j].TotalBytes
		}
		return projects[i].ProjectID < projects[j].ProjectID
	})
	return s.total, len(s.entries), s.limit, projects
}

// scheduleLocked：有改动 2 秒后写盘，只改了访问时间 30 秒后写盘。
func (s *store) scheduleLocked() {
	if s.closed {
		return
	}
	delay := touchFlushDelay
	if s.dirty {
		delay = flushDelay
	}
	at := time.Now().Add(delay)
	if s.timer != nil {
		if !s.timerAt.After(at) {
			return
		}
		s.timer.Stop()
	}
	s.timerAt = at
	s.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.timer = nil
		if err := s.flushLocked(); err != nil {
			s.log.Warn("写缓存索引失败", "err", err)
		}
	})
}

func (s *store) flushLocked() error {
	if !s.dirty && !s.touched {
		return nil
	}
	if err := s.writeIndexLocked(); err != nil {
		return err
	}
	s.dirty, s.touched = false, false
	return nil
}

func (s *store) writeIndexLocked() error {
	idx := indexFile{Version: indexVersion, LimitBytes: s.limit, Entries: make([]*entry, 0, len(s.entries))}
	for _, e := range s.entries {
		idx.Entries = append(idx.Entries, e)
	}
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return appdata.WriteFileAtomic(filepath.Join(s.dir, indexName), data, 0o600)
}

// close 退出时写盘。
func (s *store) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if err := s.flushLocked(); err != nil {
		s.log.Warn("退出时写缓存索引失败", "err", err)
	}
}
