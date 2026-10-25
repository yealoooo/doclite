package upload

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Events 是队列发给页面的事件（internal/shell 用 Wails 的事件发出去，名字见 web/src/lib/scan-queue.ts）。
// 实现不能阻塞，也不能回调 Engine（可能在 Engine 的锁外、但在工作协程里调用）。
type Events interface {
	// ItemChanged：upload:item（新的或更新的项）。
	ItemChanged(it *Item)
	// ItemRemoved：upload:item-removed（热文件夹的文件在上传前不见了）。
	ItemRemoved(id int64)
	// Progress：upload:progress（约 250 毫秒一次）。
	Progress(id, sent, size int64)
	// StateChanged：upload:state（统计、暂停、正在传的组数变了）。
	StateChanged(st QueueStatus)
	// AuthRequired：upload:auth-required（401，队列暂停）。
	AuthRequired()
	// UpgradeRequired：upload:upgrade-required（426，队列暂停）。
	UpgradeRequired(details json.RawMessage)
	// HotfolderChanged：hotfolder:state。
	HotfolderChanged(st HotStatus)
}

// MaxGroups：最多同时上传几个子文件夹（requirements 第 6 节：≤ 2 个子文件夹并行，同一子文件夹串行）。
const MaxGroups = 2

// stateDebounce：统计变化后合并这么久再发 upload:state。
const stateDebounce = 250 * time.Millisecond

// Engine 是上传队列的调度：同一组（服务器 + 目标文件夹 + 子路径）按文件名自然顺序串行，最多 MaxGroups 组同时传，
// 组之间先发现的先传；401、426 时整个队列暂停（只在内存里，重新登录、upload_resume、重启壳后继续）。
type Engine struct {
	store *Store
	api   *Client
	log   *slog.Logger
	ev    Events

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}

	mu sync.Mutex
	// paused 是整个队列暂停的原因；nil 表示没暂停。
	paused *Pause
	// running：正在上传的组 → 项。
	running map[string]int64
	// inactive：被暂停的来源（热文件夹没在运行时有 hotfolder），它们的项不参与调度。
	inactive map[string]bool
	// netTrouble：出现过"连不上服务器"类的错误；之后第一次有文件入库时，把等这类错误重试的项提前到现在。
	netTrouble bool
	// last 是最近一次算出的整体状态（托盘用，不为每次刷新查库）。
	last       QueueStatus
	stateTimer *time.Timer
	started    bool
	// known 是热文件夹运行时记着的已入队文件（hotfolder.go）；删掉热文件夹的行时从里面拿掉，文件再出现时重新入队。
	known *knownPaths
}

// NewEngine 建调度器（还没开始，见 [Engine.Start]）。热文件夹这个来源一开始是暂停的（热文件夹开始运行时打开）。
func NewEngine(store *Store, api *Client, log *slog.Logger, ev Events) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		store: store, api: api, log: log, ev: ev,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		running:  map[string]int64{},
		inactive: map[string]bool{SourceHotfolder: true},
		last:     QueueStatus{PausedSources: []string{}},
	}
}

// Start 开始调度。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return
	}
	e.started = true
	e.mu.Unlock()
	e.wg.Add(2)
	go e.loop()
	go e.purgeLoop()
	e.markDirty()
}

// purgeAfter：已入库、内容重复的项结束这么久以后，本地文件也不在了，就从库里删掉（库只增不减）。
// purgeEvery：多久清理一次（壳常驻托盘，可能几周不退出）。
const (
	purgeAfter = 30 * 24 * time.Hour
	purgeEvery = 24 * time.Hour
)

// purgeLoop 启动时清理一次，之后每 purgeEvery 一次。
func (e *Engine) purgeLoop() {
	defer e.wg.Done()
	t := time.NewTicker(purgeEvery)
	defer t.Stop()
	for {
		e.purge()
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// purge 删掉早就结束、本地文件已经不在了的项。
func (e *Engine) purge() {
	purged, err := e.store.PurgeFinished(e.ctx, nowMS()-purgeAfter.Milliseconds())
	if err != nil {
		if e.ctx.Err() == nil {
			e.log.Warn("上传队列：清理早已结束的项失败", "err", err)
		}
		return
	}
	if len(purged) == 0 {
		return
	}
	for _, p := range purged {
		e.forgetKnown(p.Source, p.PathKey)
	}
	e.log.Info("上传队列：清理了早已结束、本地文件也不在了的项", "count", len(purged))
	e.markDirty()
}

// setKnown 记下热文件夹运行时的已入队文件（停下时传 nil）。
func (e *Engine) setKnown(k *knownPaths) {
	e.mu.Lock()
	e.known = k
	e.mu.Unlock()
}

// forgetKnown：热文件夹的一行被删掉了，下次扫描时这个文件（如果又出现）重新入队。
func (e *Engine) forgetKnown(source, pathKey string) {
	if source != SourceHotfolder {
		return
	}
	e.mu.Lock()
	k := e.known
	e.mu.Unlock()
	if k != nil {
		k.forget(pathKey)
	}
}

// Stop 停止调度，最多等 timeout 让正在传的停下来（没传完的下次启动时从头再来：库里"上传中"回到"排队"）。
func (e *Engine) Stop(timeout time.Duration) {
	e.cancel()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		e.log.Warn("上传队列没能在时限内停下")
	}
}

// Wake 让调度器马上看一次有没有可以开始的项。
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) loop() {
	defer e.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		e.schedule()
		select {
		case <-e.ctx.Done():
			return
		case <-e.wake:
		case <-t.C:
		}
	}
}

func groupKey(it *Item) string {
	return it.ServerURL + "\x00" + it.TargetFolderID + "\x00" + RelKey(it.RelDir)
}

func (e *Engine) activeSourcesLocked() []string {
	var out []string
	for _, src := range []string{SourceHotfolder, SourceManual} {
		if !e.inactive[src] {
			out = append(out, src)
		}
	}
	return out
}

// schedule 取各组的队首，能开始的（排队、到了重试时间、这一组没在传）开始传，直到占满 MaxGroups。
func (e *Engine) schedule() {
	e.mu.Lock()
	if e.paused != nil || len(e.running) >= MaxGroups || e.ctx.Err() != nil {
		e.mu.Unlock()
		return
	}
	sources := e.activeSourcesLocked()
	e.mu.Unlock()
	server := e.api.Server()
	if server == "" {
		return
	}
	heads, err := e.store.Heads(e.ctx, server, sources)
	if err != nil {
		if e.ctx.Err() == nil {
			e.log.Error("上传队列：取队首失败", "err", err)
		}
		return
	}
	now := nowMS()
	for _, h := range heads {
		if h.Status != StatusQueued {
			continue
		}
		if h.NextAttemptAt != nil && *h.NextAttemptAt > now {
			// 系统时间往回调过：下次重试时间比"现在 + 5 分钟"还晚的按 5 分钟算。
			if limit := now + MaxRetryDelay.Milliseconds(); *h.NextAttemptAt > limit {
				h.NextAttemptAt = int64Ptr(limit)
				e.save(h)
			}
			continue
		}
		key := groupKey(h)
		e.mu.Lock()
		if e.paused != nil || len(e.running) >= MaxGroups {
			e.mu.Unlock()
			return
		}
		if _, busy := e.running[key]; busy {
			e.mu.Unlock()
			continue
		}
		if !e.api.HasSession() {
			e.mu.Unlock()
			// 没有会话 Cookie（没登录、退出了、到期了）：不发请求，直接按 401 暂停。
			e.pause(PauseAuth, "登录已失效，上传已暂停。重新登录后自动继续", nil)
			return
		}
		e.running[key] = h.ID
		e.mu.Unlock()
		e.wg.Add(1)
		go e.work(key, h)
	}
}

func (e *Engine) work(key string, it *Item) {
	defer func() {
		e.mu.Lock()
		delete(e.running, key)
		e.mu.Unlock()
		e.wg.Done()
		e.markDirty()
		e.Wake()
	}()
	e.process(it)
}

// pause 暂停整个队列（401 会话失效、426 版本太低）。已经暂停时不重复发事件。
func (e *Engine) pause(reason, message string, details json.RawMessage) {
	e.mu.Lock()
	if e.paused != nil && e.paused.Reason == reason {
		e.mu.Unlock()
		return
	}
	e.paused = &Pause{Reason: reason, Message: message, Details: details, Since: nowMS()}
	e.mu.Unlock()
	e.log.Warn("上传队列暂停", "reason", reason)
	if reason == PauseAuth {
		e.ev.AuthRequired()
	} else {
		e.ev.UpgradeRequired(details)
	}
	e.markDirty()
}

// Resume 清除暂停（upload_resume）。
func (e *Engine) Resume(ctx context.Context) QueueStatus {
	e.mu.Lock()
	was := e.paused
	e.paused = nil
	e.mu.Unlock()
	if was != nil {
		e.log.Info("上传队列继续", "was", was.Reason)
		e.Wake()
	}
	st := e.Status(ctx)
	e.ev.StateChanged(st)
	return st
}

// LoginSucceeded：页面登录成功了（会话代理看到 POST /api/auth/login 成功），401 引起的暂停自动继续。
func (e *Engine) LoginSucceeded() {
	e.mu.Lock()
	resumed := e.paused != nil && e.paused.Reason == PauseAuth
	if resumed {
		e.paused = nil
	}
	e.mu.Unlock()
	if resumed {
		e.log.Info("重新登录了，上传队列继续")
		e.markDirty()
		e.Wake()
	}
}

// SetSourceActive 打开或暂停一个来源（热文件夹开始、暂停）。
func (e *Engine) SetSourceActive(source string, active bool) {
	e.mu.Lock()
	e.inactive[source] = !active
	e.mu.Unlock()
	e.markDirty()
	e.Wake()
}

// Status 算出整体状态（查一次统计）。
func (e *Engine) Status(ctx context.Context) QueueStatus {
	server := e.api.Server()
	var stats Stats
	if server != "" {
		var err error
		if stats, err = e.store.Stats(ctx, server); err != nil {
			e.log.Error("上传队列：统计失败", "err", err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st := QueueStatus{Stats: stats, Running: len(e.running), PausedSources: []string{}}
	if server != "" {
		st.ServerURL = strPtr(server)
	}
	if e.paused != nil {
		p := *e.paused
		st.Paused = &p
	}
	for _, src := range []string{SourceHotfolder, SourceManual} {
		if e.inactive[src] {
			st.PausedSources = append(st.PausedSources, src)
		}
	}
	e.last = st
	return st
}

// LastStatus 是最近一次算出的整体状态（不查库，托盘用）。
func (e *Engine) LastStatus() QueueStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// markDirty：统计可能变了，合并 250 毫秒内的变化再发一次 upload:state。
func (e *Engine) markDirty() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateTimer != nil {
		return
	}
	e.stateTimer = time.AfterFunc(stateDebounce, func() {
		e.mu.Lock()
		e.stateTimer = nil
		e.mu.Unlock()
		if e.ctx.Err() != nil {
			return
		}
		e.ev.StateChanged(e.Status(e.ctx))
	})
}

// save 写回一项并发 upload:item。写库失败只记日志（下次调度、扫描会再处理）。
func (e *Engine) save(it *Item) {
	if err := e.store.Update(context.Background(), it); err != nil {
		e.log.Error("上传队列：写回失败", "id", it.ID, "err", err)
		return
	}
	e.ev.ItemChanged(it)
	e.markDirty()
}

func (e *Engine) remove(it *Item) {
	if err := e.store.Delete(context.Background(), it.ID); err != nil {
		e.log.Error("上传队列：删除失败", "id", it.ID, "err", err)
		return
	}
	e.forgetKnown(it.Source, PathKey(it.LocalPath))
	e.ev.ItemRemoved(it.ID)
	e.markDirty()
}

// Recent 是 upload_recent：当前服务器最近发现的项。
func (e *Engine) Recent(ctx context.Context, limit int, statuses []Status) ([]*Item, error) {
	server := e.api.Server()
	if server == "" {
		return []*Item{}, nil
	}
	limit = min(max(limit, 1), 1000)
	items, err := e.store.Recent(ctx, server, limit, statuses)
	if items == nil {
		items = []*Item{}
	}
	return items, err
}

// RetryFailed 让失败的项重新开始（ids 为空时是全部）：热文件夹的回到"等待写入完成"重新判定，上传按钮的回到排队。
// 返回重试的个数。
func (e *Engine) RetryFailed(ctx context.Context, ids []int64) (int, error) {
	server := e.api.Server()
	if server == "" {
		return 0, nil
	}
	var items []*Item
	err := e.store.InTx(ctx, func(tx *Store) error {
		var err error
		if items, err = tx.Failed(ctx, server, ids); err != nil {
			return err
		}
		for _, it := range items {
			status := StatusQueued
			if it.Source == SourceHotfolder {
				status = StatusWaiting
			}
			resetResult(it, status)
			if err := tx.Update(ctx, it); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		e.ev.ItemChanged(it)
	}
	if len(items) > 0 {
		e.markDirty()
		e.Wake()
	}
	return len(items), nil
}

// resetResult 清掉上一次的结果和错误，重新开始（sha、文件夹、封面都重新来）。
func resetResult(it *Item, status Status) {
	it.Status = status
	it.Attempts = 0
	it.NextAttemptAt = nil
	it.SHA256 = nil
	it.FolderID = nil
	it.FileID = nil
	it.ServerFileName = nil
	it.SortNo = nil
	it.CoverStatus = nil
	it.CoverReason = nil
	it.ErrorCode = nil
	it.ErrorMessage = nil
	it.Warnings = []string{}
	it.DuplicateOf = nil
	it.FinishedAt = nil
}

// LocalFile 是找到的一个本机文件。
type LocalFile struct {
	Path    string
	RelDir  string
	Size    int64
	MtimeNS int64
}

// Skipped 是 pick_and_enqueue_upload 没加入队列的文件。
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// reasonUploading 是 upsert 因为正在上传没加入的原因（热文件夹不把这时的大小和修改时间记成已入队，见 hotScan.enqueue）。
const reasonUploading = "正在上传"

// upsert 把一个本机文件放进队列（在 tx 里）：同一个服务器下一个本地路径只有一行。没有就新加；大小和修改时间没变
// 不重传；变了当新内容（重置这一行，server 上按同名自动改名）。返回加入（或重新开始）的项；没加入时 reason 是原因。
func upsert(ctx context.Context, tx *Store, server, source string, f LocalFile, target string) (*Item, string, error) {
	status := StatusQueued
	if source == SourceHotfolder {
		status = StatusWaiting
	}
	old, err := tx.FindByPath(ctx, server, f.Path)
	if err != nil {
		return nil, "", err
	}
	if old == nil {
		it := &Item{Source: source, ServerURL: server, LocalPath: f.Path, FileName: baseName(f.Path), RelDir: f.RelDir,
			TargetFolderID: target, Size: f.Size, MtimeNS: f.MtimeNS, Status: status, Warnings: []string{}}
		return it, "", tx.Insert(ctx, it)
	}
	unchanged := old.Size == f.Size && old.MtimeNS == f.MtimeNS
	switch {
	case old.Status == StatusUploading:
		// 正在传：传的时候会核对大小和修改时间，变了自己回到等待。
		return nil, reasonUploading, nil
	case unchanged && source == SourceHotfolder:
		return nil, "没有变化", nil
	case unchanged && (old.Status == StatusWaiting || old.Status == StatusQueued):
		return nil, "已经在上传队列里", nil
	case unchanged && (old.Status == StatusDone || old.Status == StatusDuplicate):
		return nil, "已经上传过（文件没有改动）", nil
	}
	// 变了（新内容），或者上传按钮重新选了失败的文件：按这次的目标从头再来。
	old.Source, old.FileName, old.RelDir, old.TargetFolderID = source, baseName(f.Path), f.RelDir, target
	old.Size, old.MtimeNS = f.Size, f.MtimeNS
	resetResult(old, status)
	return old, "", tx.Update(ctx, old)
}

// enqueue 把一批本机文件放进队列（一个事务；出错时整批不加），之后逐项发 upload:item（页面的列表靠它更新）。
func (e *Engine) enqueue(ctx context.Context, server, source string, files []LocalFile, target string) (added []*Item, skipped []Skipped, err error) {
	err = e.store.InTx(ctx, func(tx *Store) error {
		for _, f := range files {
			it, reason, err := upsert(ctx, tx, server, source, f, target)
			if err != nil {
				return err
			}
			if it == nil {
				skipped = append(skipped, Skipped{Path: f.Path, Reason: reason})
				continue
			}
			added = append(added, it)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, it := range added {
		e.ev.ItemChanged(it)
	}
	if len(added) > 0 {
		e.markDirty()
		e.Wake()
	}
	return added, skipped, nil
}

// Enqueue 把上传按钮选的文件加入队列（直接排队），传到 target（选文件夹时 RelDir 是相对路径，逐层建子文件夹）。
func (e *Engine) Enqueue(ctx context.Context, files []LocalFile, target string) (added []int64, skipped []Skipped, err error) {
	items, skipped, err := e.enqueue(ctx, e.api.Server(), SourceManual, files, target)
	if err != nil {
		return nil, nil, err
	}
	added = make([]int64, 0, len(items))
	for _, it := range items {
		added = append(added, it.ID)
	}
	if skipped == nil {
		skipped = []Skipped{}
	}
	return added, skipped, nil
}
