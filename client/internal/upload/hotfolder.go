package upload

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

// 热文件夹（requirements 第 6 节，用户决定 2026-09-27）：只监控一个目录，对应 server 上的一个目标文件夹（可换）；
// 根目录里的文件传到目标文件夹本身，子文件夹逐层按名字建（见 process.go 的 resolveFolder）。
//
//   - 发现文件：fsnotify 的事件只作提示（去抖 300 毫秒后只重新列出有事件的目录，新出现的子目录整个列出），另外
//     定时全量扫描兜底（有目录事件时 60 秒，没有时 15 秒）。内存里记着已入队文件的大小和修改时间（开始运行时从库里读
//     一次，之后随入队、删除更新），没变的不碰库。列目录的规则见 walk.go（和上传按钮选的文件夹一样）；只收 jpg、jpeg、
//     tif、tiff、png。
//   - 写完判定：每 500 毫秒看一次"等待写入完成"的项（settle.go 的 Judge）：写完了进排队；60 秒还不完整记失败；
//     写完之前文件不见了从队列删掉。
//   - 换目标只影响之后发现的文件；暂停 = 停监控 + 队列不再开始热文件夹的项（正在传的传完），配置里记 running，
//     壳重启后按它恢复；服务器换了热文件夹停下（目标属于原来的服务器）。

// PollIntervalSecs 是没有目录事件时定时全量扫描的间隔（秒，hotfolder 状态里的 scan_interval_secs）。
const PollIntervalSecs = 15

const (
	// fullScanEvery 是有目录事件时兜底的全量扫描间隔。
	fullScanEvery = 60 * time.Second
	settleEvery   = 500 * time.Millisecond
	eventDelay    = 300 * time.Millisecond
	// enqueueBatch：扫描发现的文件每这么多个一个事务入队。
	enqueueBatch = 500
)

// HotFolder 是热文件夹。
type HotFolder struct {
	e   *Engine
	log *slog.Logger

	// op 让开始、暂停、设置、停下、服务器换了一个一个来（托盘、页面、壳启动时的恢复可能同时调用）。
	op sync.Mutex

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

func (h *HotFolder) emit() {
	h.e.ev.HotfolderChanged(h.Status())
}

// Restore 在壳启动时按上次的状态恢复：配置里记着在运行、目标属于当前服务器就开始。
func (h *HotFolder) Restore() {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil || !cfg.AutoStart {
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
	h.op.Lock()
	defer h.op.Unlock()
	h.mu.Lock()
	running := h.running
	cfg := HotConfig{WatchDir: filepath.Clean(watchDir), TargetFolderID: targetFolderID, ServerURL: server,
		ScanIntervalSecs: PollIntervalSecs, AutoStart: running}
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
	h.op.Lock()
	defer h.op.Unlock()
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
	cfg.AutoStart = true
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
	h.op.Lock()
	defer h.op.Unlock()
	h.stopRun()
	h.mu.Lock()
	if h.cfg != nil && h.cfg.AutoStart {
		cfg := *h.cfg
		cfg.AutoStart = false
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

// Shutdown：壳退出时停下（不改配置里的 AutoStart，下次启动照样恢复）。
func (h *HotFolder) Shutdown() {
	h.op.Lock()
	defer h.op.Unlock()
	h.stopRun()
}

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
		h.run(ctx, cfg, done)
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

// run 是监控的主循环（done 是这一次运行的标识，见 serverChanged）。
func (h *HotFolder) run(ctx context.Context, cfg HotConfig, done chan struct{}) {
	server := cfg.ServerURL
	sc := &hotScan{h: h, cfg: cfg, watched: map[string]bool{}}
	settle := map[int64]*Settle{}

	known, err := h.e.store.KnownPaths(ctx, server, SourceHotfolder)
	if err != nil {
		// 读不出来就从空的开始：每个文件都去库里查一次（慢一些，结果一样）。
		h.log.Error("热文件夹：读已入队的文件失败", "err", err)
		known = map[string]fileSig{}
	}
	sc.known = &knownPaths{m: known}
	h.e.setKnown(sc.known)
	defer h.e.setKnown(nil)

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
	interval := time.Duration(PollIntervalSecs) * time.Second
	if sc.watcher != nil {
		events, errs = sc.watcher.Events, sc.watcher.Errors
		interval = fullScanEvery
	}
	scanTick := time.NewTicker(interval)
	defer scanTick.Stop()
	serverTick := time.NewTicker(time.Duration(PollIntervalSecs) * time.Second)
	defer serverTick.Stop()
	settleTick := time.NewTicker(settleEvery)
	defer settleTick.Stop()
	var delay <-chan time.Time
	// dirty 是有事件、等着重新列出的目录。
	dirty := map[string]bool{}

	sc.scan(ctx, nil)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				sc.unwatch(ev.Name)
			}
			dirty[filepath.Dir(ev.Name)] = true
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
			dirs := dirty
			dirty = map[string]bool{}
			sc.scan(ctx, dirs)
		case <-serverTick.C:
			if cur := h.e.api.Server(); cur != server {
				h.log.Warn("服务器地址换了，热文件夹停下", "from", server, "to", cur)
				go h.serverChanged(done)
				return
			}
		case <-scanTick.C:
			sc.scan(ctx, nil)
		case <-settleTick.C:
			h.settle(ctx, server, settle, sc)
		}
	}
}

// serverChanged：服务器地址换了（目标文件夹属于原来的服务器），停下并提示。配置里的 AutoStart 不改：换回原来的
// 服务器后壳重启时照样恢复。done 是发现服务器换了的那一次运行：轮到这里之前已经暂停、或者按新配置重新开始了
// （h.done 不是它了），就什么也不做。
func (h *HotFolder) serverChanged(done chan struct{}) {
	h.op.Lock()
	defer h.op.Unlock()
	h.mu.Lock()
	current := h.done == done
	h.mu.Unlock()
	if !current {
		return
	}
	h.stopRun()
	h.setError("服务器地址换了，热文件夹已停止：目标文件夹属于原来的服务器，请重新选择目标文件夹")
}

// knownPaths 是已入队文件（path_key）的大小和修改时间。扫描、写完判定在监控协程里改它，上传队列删掉热文件夹的行时
// 从别的协程拿掉（[Engine.forgetKnown]），所以带锁。
type knownPaths struct {
	mu sync.Mutex
	m  map[string]fileSig
}

func (k *knownPaths) same(key string, sig fileSig) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	old, ok := k.m[key]
	return ok && old == sig
}

func (k *knownPaths) set(key string, sig fileSig) {
	k.mu.Lock()
	k.m[key] = sig
	k.mu.Unlock()
}

func (k *knownPaths) forget(key string) {
	k.mu.Lock()
	delete(k.m, key)
	k.mu.Unlock()
}

// hotScan 是一次运行里扫描用的状态。
type hotScan struct {
	h       *HotFolder
	cfg     HotConfig
	watcher *fsnotify.Watcher
	// watched 是加了目录事件监控的目录。
	watched map[string]bool
	known   *knownPaths
}

// unwatch：目录被删、改名了，它和它下面的目录不再算在监控里（fsnotify 自己会撤掉）。
func (sc *hotScan) unwatch(dir string) {
	prefix := dir + string(filepath.Separator)
	for d := range sc.watched {
		if d == dir || strings.HasPrefix(d, prefix) {
			delete(sc.watched, d)
		}
	}
}

// scan 列出目录、把新的和变了的文件加入队列。dirs 为 nil 时是全量扫描；否则只重新列出这几个目录（有事件的），
// 其中没加监控的子目录（新出现的）整个列出。
func (sc *hotScan) scan(ctx context.Context, dirs map[string]bool) {
	h := sc.h
	root := sc.cfg.WatchDir
	st, err := os.Stat(root)
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
	var pending []LocalFile
	found := 0
	collect := func(f LocalFile) {
		if !SupportedImage(f.Path) || sc.known.same(PathKey(f.Path), fileSig{size: f.Size, mtime: f.MtimeNS}) {
			return
		}
		pending = append(pending, f)
		if len(pending) >= enqueueBatch {
			found += sc.enqueue(ctx, pending)
			pending = pending[:0]
		}
	}
	if dirs == nil {
		WalkImages(root, "", 0, sc.enter(ctx, ""), collect)
	} else {
		for dir := range dirs {
			rel, err := filepath.Rel(root, dir)
			if err != nil || (dir != root && !sc.watched[dir]) {
				continue // 不在监控里（已经删掉了，或者是被忽略的目录）
			}
			depth := 0
			if rel == "." {
				rel = ""
			} else {
				rel = filepath.ToSlash(rel)
				depth = strings.Count(rel, "/") + 1
			}
			WalkImages(dir, rel, depth, sc.enter(ctx, dir), collect)
		}
	}
	found += sc.enqueue(ctx, pending)
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	h.errMsg = ""
	if dirs == nil {
		h.lastScan = nowMS()
		h.scans++
	}
	h.mu.Unlock()
	if dirs == nil || found > 0 {
		h.emit()
	}
}

// enter 是 WalkImages 进每个目录之前的检查：停下了就不再往下；给没监控的目录加上监控。只列出有事件的目录时
// （start 不为空），已经在监控里的子目录不再列出（它们自己有事件）。
func (sc *hotScan) enter(ctx context.Context, start string) func(dir string) bool {
	return func(dir string) bool {
		if ctx.Err() != nil {
			return false
		}
		if sc.watched[dir] {
			return start == "" || dir == start
		}
		if sc.watcher != nil && sc.watcher.Add(dir) == nil {
			sc.watched[dir] = true
		}
		return true
	}
}

// enqueue 把扫描发现的一批文件加入队列（一个事务），记进 known。返回加入的个数。正在上传、没加入的不记：
// 上传中被覆盖的文件，传完以后下次扫描还要再看一次（大小和修改时间变了就重新入队）。
func (sc *hotScan) enqueue(ctx context.Context, files []LocalFile) int {
	if len(files) == 0 || ctx.Err() != nil {
		return 0
	}
	added, skipped, err := sc.h.e.enqueue(ctx, sc.cfg.ServerURL, SourceHotfolder, files, sc.cfg.TargetFolderID)
	if err != nil {
		if ctx.Err() == nil {
			sc.h.log.Error("热文件夹：加入队列失败", "err", err)
		}
		return 0
	}
	uploading := map[string]bool{}
	for _, s := range skipped {
		if s.Reason == reasonUploading {
			uploading[s.Path] = true
		}
	}
	for _, f := range files {
		if !uploading[f.Path] {
			sc.known.set(PathKey(f.Path), fileSig{size: f.Size, mtime: f.MtimeNS})
		}
	}
	return len(added)
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
				h.e.remove(it) // 也从 known 里拿掉
				delete(state, it.ID)
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
			sc.known.set(PathKey(it.LocalPath), fileSig{size: size, mtime: mtime})
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
