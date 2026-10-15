package upload

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

// 热文件夹（requirements 第 6 节，用户决定 2026-09-27）：只监控一个目录，对应 server 上的一个目标文件夹（可换）；
// 根目录里的文件传到目标文件夹本身，子文件夹逐层按名字建（见 process.go 的 resolveFolder）。
//
//   - 发现文件：fsnotify 的事件只作提示（去抖 300 毫秒后重新扫描整个目录），另外定时全量扫描兜底（默认 15 秒）。
//     内存里记着已入队文件的大小和修改时间（每次定时扫描从库里重读），没变的不碰库。忽略规则见 ignore.go；
//     不跟随符号链接，最多 32 层；只收 jpg、jpeg、tif、tiff、png。
//   - 写完判定：每 500 毫秒看一次"等待写入完成"的项（settle.go 的 Judge）：写完了进排队；60 秒还不完整记失败；
//     写完之前文件不见了从队列删掉。
//   - 换目标只影响之后发现的文件；暂停 = 停监控 + 队列不再开始热文件夹的项（正在传的传完），配置里记 running，
//     壳重启后按它恢复；服务器换了热文件夹停下（目标属于原来的服务器）。

// DefaultScanInterval 是定时全量扫描的间隔。
const DefaultScanInterval = 15

const (
	settleEvery = 500 * time.Millisecond
	eventDelay  = 300 * time.Millisecond
)

// HotFolder 是热文件夹。
type HotFolder struct {
	e   *Engine
	log *slog.Logger

	mu       sync.Mutex
	cfg      *HotConfig
	running  bool
	watching bool
	errMsg   string
	lastScan int64
	scans    int
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewHotFolder 读出配置（不开始运行，见 [HotFolder.Restore]）。
func NewHotFolder(e *Engine, log *slog.Logger) (*HotFolder, error) {
	cfg, err := e.store.HotConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return &HotFolder{e: e, log: log, cfg: cfg}, nil
}

// HotError 是热文件夹的操作不能做（给页面看的原因）。
type HotError struct{ Msg string }

func (e *HotError) Error() string { return e.Msg }

// Status 是 hotfolder_status。
func (h *HotFolder) Status() HotStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statusLocked()
}

func (h *HotFolder) statusLocked() HotStatus {
	st := HotStatus{Running: h.running, Watching: h.running && h.watching, Scans: h.scans}
	if h.cfg != nil {
		c := *h.cfg
		st.Config = &c
	}
	if h.errMsg != "" {
		st.Error = strPtr(h.errMsg)
	}
	if h.lastScan > 0 {
		st.LastScanAt = int64Ptr(h.lastScan)
	}
	return st
}

// Configured：设置过热文件夹。
func (h *HotFolder) Configured() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg != nil
}

func (h *HotFolder) emit() {
	h.e.ev.HotfolderChanged(h.Status())
}

// Restore 在壳启动时按上次的状态恢复：配置里记着在运行、目标属于当前服务器就开始。
func (h *HotFolder) Restore() {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil || !cfg.Running {
		return
	}
	if _, err := h.Start(); err != nil {
		h.log.Warn("热文件夹没能按上次的状态开始", "err", err)
	}
}

// Set 保存监控目录和目标文件夹（目标属于当前服务器）。正在运行时按新配置继续。
func (h *HotFolder) Set(watchDir, targetFolderID string) (HotStatus, error) {
	server := h.e.api.Server()
	if server == "" {
		return HotStatus{}, &HotError{"还没有配置服务器地址"}
	}
	if _, err := uuid.Parse(targetFolderID); err != nil {
		return HotStatus{}, &HotError{"目标文件夹的 id 不对"}
	}
	if err := checkWatchDir(watchDir); err != nil {
		return HotStatus{}, err
	}
	h.mu.Lock()
	running := h.running
	cfg := HotConfig{WatchDir: filepath.Clean(watchDir), TargetFolderID: targetFolderID, ServerURL: server,
		ScanIntervalSecs: DefaultScanInterval, Running: running}
	if h.cfg != nil && h.cfg.ScanIntervalSecs > 0 {
		cfg.ScanIntervalSecs = h.cfg.ScanIntervalSecs
	}
	if err := h.e.store.SetHotConfig(context.Background(), cfg); err != nil {
		h.mu.Unlock()
		return HotStatus{}, err
	}
	h.cfg = &cfg
	h.errMsg = ""
	h.mu.Unlock()
	h.log.Info("热文件夹的配置已保存", "watch_dir", cfg.WatchDir, "target", cfg.TargetFolderID)
	if running {
		h.stopRun()
		h.startRun(cfg)
	}
	h.emit()
	return h.Status(), nil
}

// checkWatchDir：监控目录要存在、是目录，不能是盘符根目录或用户主目录本身（错误信息不写路径）。
func checkWatchDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return &HotError{"监控目录要是完整的路径"}
	}
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return &HotError{"不能监控整个磁盘的根目录，请选择扫描软件保存文件的文件夹"}
	}
	if home, err := os.UserHomeDir(); err == nil && PathKey(home) == PathKey(clean) {
		return &HotError{"不能监控用户主目录本身，请选择扫描软件保存文件的文件夹"}
	}
	st, err := os.Stat(clean)
	if err != nil || !st.IsDir() {
		return &HotError{"监控目录不存在或打不开"}
	}
	return nil
}

// Start 开始监控（hotfolder_start、托盘的"继续热文件夹"）。
func (h *HotFolder) Start() (HotStatus, error) {
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		return HotStatus{}, &HotError{"还没有设置热文件夹：先选择监控目录和目标文件夹"}
	}
	cfg := *h.cfg
	running := h.running
	h.mu.Unlock()
	if running {
		return h.Status(), nil
	}
	if server := h.e.api.Server(); server == "" || server != cfg.ServerURL {
		return HotStatus{}, &HotError{"热文件夹的目标文件夹属于另一个服务器（" + cfg.ServerURL + "），请重新选择目标文件夹"}
	}
	if err := checkWatchDir(cfg.WatchDir); err != nil {
		h.setError(err.Error())
		return HotStatus{}, err
	}
	cfg.Running = true
	if err := h.e.store.SetHotConfig(context.Background(), cfg); err != nil {
		return HotStatus{}, err
	}
	h.mu.Lock()
	h.cfg = &cfg
	h.errMsg = ""
	h.mu.Unlock()
	h.startRun(cfg)
	h.log.Info("热文件夹开始监控", "watch_dir", cfg.WatchDir)
	h.emit()
	return h.Status(), nil
}

// Pause 暂停（停监控，队列不再开始热文件夹的项，正在传的传完）。壳重启后保持暂停。
func (h *HotFolder) Pause() (HotStatus, error) {
	h.stopRun()
	h.mu.Lock()
	if h.cfg != nil && h.cfg.Running {
		cfg := *h.cfg
		cfg.Running = false
		if err := h.e.store.SetHotConfig(context.Background(), cfg); err != nil {
			h.mu.Unlock()
			return HotStatus{}, err
		}
		h.cfg = &cfg
	}
	h.mu.Unlock()
	h.log.Info("热文件夹已暂停")
	h.emit()
	return h.Status(), nil
}

// Shutdown：壳退出时停下（不改配置里的 running，下次启动照样恢复）。
func (h *HotFolder) Shutdown() { h.stopRun() }

func (h *HotFolder) setError(msg string) {
	h.mu.Lock()
	h.errMsg = msg
	h.mu.Unlock()
	h.emit()
}

func (h *HotFolder) startRun(cfg HotConfig) {
	ctx, cancel := context.WithCancel(h.e.ctx)
	done := make(chan struct{})
	h.mu.Lock()
	h.running = true
	h.cancel = cancel
	h.done = done
	h.mu.Unlock()
	h.e.SetSourceActive(SourceHotfolder, true)
	go func() {
		defer close(done)
		h.run(ctx, cfg)
	}()
}

func (h *HotFolder) stopRun() {
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	h.cancel, h.done = nil, nil
	h.running = false
	h.watching = false
	h.mu.Unlock()
	h.e.SetSourceActive(SourceHotfolder, false)
	if cancel != nil {
		cancel()
		<-done
	}
}

// run 是监控的主循环。
func (h *HotFolder) run(ctx context.Context, cfg HotConfig) {
	server := cfg.ServerURL
	interval := time.Duration(max(cfg.ScanIntervalSecs, 1)) * time.Second
	sc := &hotScan{h: h, cfg: cfg, watched: map[string]bool{}}
	settle := map[int64]*Settle{}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		h.log.Warn("建不了目录事件监控，只靠定时扫描", "err", err)
	} else {
		sc.watcher = w
		defer w.Close()
	}
	h.mu.Lock()
	h.watching = sc.watcher != nil
	h.mu.Unlock()

	var events <-chan fsnotify.Event
	var errs <-chan error
	if sc.watcher != nil {
		events, errs = sc.watcher.Events, sc.watcher.Errors
	}
	scanTick := time.NewTicker(interval)
	defer scanTick.Stop()
	settleTick := time.NewTicker(settleEvery)
	defer settleTick.Stop()
	var delay <-chan time.Time

	sc.full(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if delay == nil {
				delay = time.After(eventDelay)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			h.log.Warn("目录事件监控出错", "err", err)
		case <-delay:
			delay = nil
			sc.scan(ctx, false)
		case <-scanTick.C:
			if cur := h.e.api.Server(); cur != server {
				h.log.Warn("服务器地址换了，热文件夹停下", "from", server, "to", cur)
				go h.serverChanged()
				return
			}
			sc.full(ctx)
		case <-settleTick.C:
			h.settle(ctx, server, settle, sc)
		}
	}
}

// serverChanged：服务器地址换了（目标文件夹属于原来的服务器），停下并提示。配置里的 running 不改：换回原来的
// 服务器后壳重启时照样恢复。
func (h *HotFolder) serverChanged() {
	h.stopRun()
	h.setError("服务器地址换了，热文件夹已停止：目标文件夹属于原来的服务器，请重新选择目标文件夹")
}

// hotScan 是一次运行里扫描用的状态。
type hotScan struct {
	h       *HotFolder
	cfg     HotConfig
	watcher *fsnotify.Watcher
	watched map[string]bool
	// known：已入队文件（path_key）的大小和修改时间。
	known map[string]fileSig
}

// full 是定时的全量扫描：先从库里重读已入队的文件（文件不见了被删掉的行、别处改过的行都会对上）。
func (sc *hotScan) full(ctx context.Context) {
	sc.scan(ctx, true)
}

func (sc *hotScan) scan(ctx context.Context, reload bool) {
	h := sc.h
	if reload || sc.known == nil {
		known, err := h.e.store.KnownPaths(ctx, sc.cfg.ServerURL, SourceHotfolder)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Error("热文件夹：读队列库失败", "err", err)
			}
			return
		}
		sc.known = known
	}
	st, err := os.Stat(sc.cfg.WatchDir)
	if err != nil || !st.IsDir() {
		msg := "监控目录不存在或读不了，等它恢复后自动继续"
		h.mu.Lock()
		changed := h.errMsg != msg
		h.errMsg = msg
		h.mu.Unlock()
		if changed {
			h.log.Warn("热文件夹的监控目录读不了", "err", err)
			h.emit()
		}
		return
	}
	found := 0
	sc.walk(ctx, sc.cfg.WatchDir, "", 0, &found)
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	h.errMsg = ""
	if reload {
		h.lastScan = nowMS()
		h.scans++
	}
	h.mu.Unlock()
	if reload || found > 0 {
		h.emit()
	}
}

// walk 列出 dir（相对根目录 rel，第 depth 层）里的文件和子目录。
func (sc *hotScan) walk(ctx context.Context, dir, rel string, depth int, found *int) {
	if ctx.Err() != nil {
		return
	}
	if sc.watcher != nil && !sc.watched[dir] {
		if err := sc.watcher.Add(dir); err == nil {
			sc.watched[dir] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		name := ent.Name()
		if ent.Type()&fs.ModeSymlink != 0 || IgnoredName(name, ent.IsDir()) {
			continue
		}
		info, err := ent.Info()
		if err != nil || hiddenOrSystem(info) {
			continue
		}
		path := filepath.Join(dir, name)
		if ent.IsDir() {
			if depth < MaxDepth {
				sub := name
				if rel != "" {
					sub = rel + "/" + name
				}
				sc.walk(ctx, path, sub, depth+1, found)
			}
			continue
		}
		if !info.Mode().IsRegular() || !SupportedImage(name) {
			continue
		}
		key := PathKey(path)
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		if old, ok := sc.known[key]; ok && old == sig {
			continue
		}
		it, _, err := sc.h.e.upsert(ctx, sc.cfg.ServerURL, SourceHotfolder,
			LocalFile{Path: path, RelDir: rel, Size: sig.size, MtimeNS: sig.mtime}, sc.cfg.TargetFolderID)
		if err != nil {
			if ctx.Err() == nil {
				sc.h.log.Error("热文件夹：加入队列失败", "err", err)
			}
			continue
		}
		sc.known[key] = sig
		if it != nil {
			*found++
		}
	}
}

// settle 推进"等待写入完成"的项。
func (h *HotFolder) settle(ctx context.Context, server string, state map[int64]*Settle, sc *hotScan) {
	items, err := h.e.store.WaitingOf(ctx, server, SourceHotfolder)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Error("热文件夹：读等待写完的项失败", "err", err)
		}
		return
	}
	now := time.Now()
	seen := make(map[int64]bool, len(items))
	ready := false
	for _, it := range items {
		seen[it.ID] = true
		st, err := os.Stat(it.LocalPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// 写完之前不见了（扫描软件的临时文件改名、用户删了）：从队列删掉。
				h.e.remove(it)
				delete(state, it.ID)
				if sc.known != nil {
					delete(sc.known, PathKey(it.LocalPath))
				}
			}
			continue
		}
		size, mtime := st.Size(), st.ModTime().UnixNano()
		s, v := Judge(state[it.ID], size, mtime, now, fileProbe(it.LocalPath))
		state[it.ID] = s
		dirty := false
		if size != it.Size || mtime != it.MtimeNS {
			it.Size, it.MtimeNS = size, mtime
			dirty = true
			if sc.known != nil {
				sc.known[PathKey(it.LocalPath)] = fileSig{size: size, mtime: mtime}
			}
		}
		switch v {
		case Ready:
			it.Status = StatusQueued
			it.ErrorCode, it.ErrorMessage = nil, nil
			delete(state, it.ID)
			ready = true
			dirty = true
		case Incomplete:
			it.Status = StatusFailed
			it.ErrorCode = strPtr(ErrCodeIncomplete)
			it.ErrorMessage = strPtr(fmt.Sprintf("文件不完整（%d 秒没有变化，文件尾仍不完整，扫描软件可能没写完）", int(SettleGiveUp/time.Second)))
			it.FinishedAt = int64Ptr(nowMS())
			delete(state, it.ID)
			dirty = true
		case Locked:
			if it.ErrorCode == nil || *it.ErrorCode != ErrCodeFileLocked {
				it.ErrorCode, it.ErrorMessage = strPtr(ErrCodeFileLocked), strPtr("文件被其他程序占用")
				dirty = true
			}
		}
		if dirty {
			h.e.save(it)
		}
	}
	for id := range state {
		if !seen[id] {
			delete(state, id)
		}
	}
	if ready {
		h.e.Wake()
	}
}
