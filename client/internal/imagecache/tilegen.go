package imagecache

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"doclite/client/internal/imgdecode"
	"doclite/client/internal/tiles"
)

// 原图分块（requirements 第 6 节"大图切块"）：切块本身在 internal/tiles；这里管什么时候切、切好放哪：
//   - 只对来源记录里的快照切（和封面、原图一样）；原图先从缓存取，没有就用记录里的 URL 下载（和取原图共用一次）；
//   - 同一个快照同时只切一次（并发请求等同一个结果）；不同的图同时最多切 2 张，另有内存预算：两张加起来的估算
//     （tiles.Plan.Estimate，进程峰值的估算）不超过 tileMemoryBudget（1.5 GiB），单张超过它的不切（too_large）。
//     A3 600 DPI 的无压缩彩色 TIFF（200 MB，7016×9921）估算约 1 GB、实测峰值约 950 MB，能切；
//     更大的彩色图（例如 A2 600 DPI）超出预算，页面只显示封面；
//   - 一张图切了 tileTimeout 还没切完就中止（timeout）；写出的块合计超过 min(缓存上限的一半, 1 GiB) 也中止；
//   - 切好的整套块改名进缓存（<file_id>/<snapshot_id>.tiles/），索引里算一项，参与淘汰；缓存不可用时不切（503）；
//   - 切不了的（422：too_large、timeout、unsupported_format、unsupported、corrupt）本次运行记住，不再反复尝试；
//     只记在内存里、不写进索引（客户端升级了解码器重启后就会重新切，例如 M6-3b 以前切不了的 JPEG 压缩的 TIFF）；
//     本机读写出错（500）不记；
//   - 提前切块（prepare_tiles）：同时只有一个在跑，排队的只留最新的一张。

const (
	tileSlots        = 2
	tileMemoryBudget = 3 << 29 // 1.5 GiB
	tileTimeout      = 120 * time.Second
	tileOutputCap    = 1 << 30
)

type tiler struct {
	s *Service

	mu      sync.Mutex
	cond    *sync.Cond
	running int
	memory  int64
	flights map[string]*tileFlight
	failed  map[string]*tiles.Error

	prepRunning bool
	prepPending *Source
}

type tileFlight struct {
	done chan struct{}
	meta *tiles.Result
	dir  string
	err  *httpError
}

func newTiler(s *Service) *tiler {
	t := &tiler{s: s, flights: map[string]*tileFlight{}, failed: map[string]*tiles.Error{}}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func unavailable(msg string) *httpError {
	return &httpError{status: http.StatusServiceUnavailable, code: "unavailable", message: msg}
}

func unprocessable(e *tiles.Error) *httpError {
	return &httpError{status: http.StatusUnprocessableEntity, code: string(e.Code), message: e.Message}
}

// ensure 拿到切好的分块和它的目录：已经切好就直接给（hit），没有就切（或等正在切的那一次）。
// ctx 先取消时返回值都是零值。
func (t *tiler) ensure(ctx context.Context, src Source) (*tiles.Result, string, bool, *httpError) {
	st := t.s.store
	if st == nil {
		return nil, "", false, unavailable("本机图片缓存不可用，切不了块")
	}
	t.mu.Lock()
	if e := t.failed[src.SnapshotID]; e != nil {
		t.mu.Unlock()
		return nil, "", false, unprocessable(e)
	}
	t.mu.Unlock()
	if e, dir, ok := st.lookup(entryKey(src.SnapshotID, KindTiles)); ok && e.Tiles != nil {
		return e.Tiles, dir, true, nil
	}
	t.mu.Lock()
	f := t.flights[src.SnapshotID]
	if f == nil {
		f = &tileFlight{done: make(chan struct{})}
		t.flights[src.SnapshotID] = f
		go t.run(src, f)
	}
	t.mu.Unlock()
	select {
	case <-f.done:
		return f.meta, f.dir, false, f.err
	case <-ctx.Done():
		return nil, "", false, nil
	}
}

// state 是提前切块看到的状态（不切）。
func (t *tiler) state(sid string) (status string, failed *tiles.Error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.failed[sid]; e != nil {
		return "failed", e
	}
	if t.s.store != nil && t.s.store.has(entryKey(sid, KindTiles)) {
		return "ready", nil
	}
	if t.flights[sid] != nil {
		return "generating", nil
	}
	return "", nil
}

// PrepareResult 是 prepare_tiles 的结果（web/src/lib/image-cache.ts 的 PrepareTilesResult）。
type PrepareResult struct {
	Status  string  `json:"status"`
	Code    *string `json:"code"`
	Message *string `json:"message"`
}

// prepare 提前切一张（马上返回）：已切好 ready、正在切 generating、排上了 queued（只留最新的一张）、切不了 failed。
func (t *tiler) prepare(src Source) PrepareResult {
	if t.s.store == nil {
		code, msg := "unavailable", "本机图片缓存不可用，切不了块"
		return PrepareResult{Status: "failed", Code: &code, Message: &msg}
	}
	switch status, e := t.state(src.SnapshotID); status {
	case "failed":
		code := string(e.Code)
		return PrepareResult{Status: status, Code: &code, Message: &e.Message}
	case "ready", "generating":
		return PrepareResult{Status: status}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.prepRunning {
		s := src
		t.prepPending = &s
		return PrepareResult{Status: "queued"}
	}
	t.prepRunning = true
	go t.prepLoop(src)
	return PrepareResult{Status: "generating"}
}

func (t *tiler) prepLoop(src Source) {
	for {
		_, _, _, _ = t.ensure(context.Background(), src)
		t.mu.Lock()
		next := t.prepPending
		t.prepPending = nil
		if next == nil {
			t.prepRunning = false
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
		src = *next
	}
}

// run 切一张（在自己的协程里，请求方走了也会切完）。
func (t *tiler) run(src Source, f *tileFlight) {
	defer func() {
		if r := recover(); r != nil {
			t.s.log.Error("切块时出错", "snapshot_id", src.SnapshotID, "panic", r)
			f.meta, f.err = nil, &httpError{status: http.StatusInternalServerError, code: "internal", message: "切块时出错"}
		}
		t.mu.Lock()
		delete(t.flights, src.SnapshotID)
		t.mu.Unlock()
		close(f.done)
	}()
	f.meta, f.dir, f.err = t.generate(src)
	if f.err != nil {
		t.s.log.Warn("切块失败", "file_id", src.FileID, "snapshot_id", src.SnapshotID, "err", f.err)
	}
}

func (t *tiler) remember(sid string, e *tiles.Error) *httpError {
	t.mu.Lock()
	t.failed[sid] = e
	t.mu.Unlock()
	t.s.c.tilingFailures.Add(1)
	return unprocessable(e)
}

func (t *tiler) generate(src Source) (*tiles.Result, string, *httpError) {
	st := t.s.store
	tok := st.token(src.ProjectID)
	path, release, _, herr := t.s.original(context.Background(), src)
	if herr != nil {
		if herr.status == http.StatusUnprocessableEntity {
			return nil, "", t.remember(src.SnapshotID, &tiles.Error{Code: imgdecode.Kind(herr.code), Message: herr.message})
		}
		return nil, "", herr
	}
	defer release()

	plan, err := tiles.NewPlan(path)
	if err != nil {
		return nil, "", t.tileError(src.SnapshotID, err)
	}
	if plan.Estimate > tileMemoryBudget {
		return nil, "", t.remember(src.SnapshotID, &tiles.Error{Code: imgdecode.KindTooLarge,
			Message: "原图太大，整张解码约要 " + mb(plan.Estimate) + "，超过客户端切块的内存上限 " + mb(tileMemoryBudget)})
	}
	t.acquire(plan.Estimate)
	defer t.releaseSlot(plan.Estimate)

	out, err := os.MkdirTemp(t.s.tmpDir, src.SnapshotID+".tiles.*.part")
	if err != nil {
		return nil, "", ioError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), tileTimeout)
	defer cancel()
	res, err := plan.Generate(ctx, out, tiles.Options{MaxOutputBytes: min(st.currentLimit()/2, tileOutputCap)})
	if plan.Estimate > 64<<20 {
		// 整张解码占的内存尽快还给系统（壳是常驻的）。
		debug.FreeOSMemory()
	}
	if err != nil {
		_ = os.RemoveAll(out)
		return nil, "", t.tileError(src.SnapshotID, err)
	}
	e := &entry{FileID: src.FileID, SnapshotID: src.SnapshotID, ProjectID: src.ProjectID, Kind: KindTiles,
		Size: res.Bytes, Tiles: res}
	stored, err := st.put(e, out, tok)
	if !stored {
		_ = os.RemoveAll(out)
		if err != nil {
			return nil, "", ioError(err)
		}
		return nil, "", unavailable("切块期间缓存被清空了，请重试")
	}
	t.s.c.tilings.Add(1)
	t.s.log.Info("切块", "file_id", src.FileID, "snapshot_id", src.SnapshotID, "result", res.String(),
		"estimate_mb", plan.Estimate>>20)
	return res, st.path(e), nil
}

func (t *tiler) tileError(sid string, err error) *httpError {
	var te *tiles.Error
	if errors.As(err, &te) {
		if te.Permanent() {
			return t.remember(sid, te)
		}
		return &httpError{status: http.StatusInternalServerError, code: string(te.Code), message: te.Message}
	}
	return ioError(err)
}

// acquire 等一个切块名额和内存预算。
func (t *tiler) acquire(bytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.running >= tileSlots || t.memory+bytes > tileMemoryBudget {
		t.cond.Wait()
	}
	t.running++
	t.memory += bytes
}

func (t *tiler) releaseSlot(bytes int64) {
	t.mu.Lock()
	t.running--
	t.memory -= bytes
	t.mu.Unlock()
	t.cond.Broadcast()
}

// tileFile 是一块的文件（分块目录里的相对路径）。
func tileFile(dir string, level, x, y int, ext string) string {
	return filepath.Join(dir, tiles.TilePath(level, x, y, ext))
}

func mb(n int64) string { return strconv.FormatInt(n>>20, 10) + " MB" }

// PrepareTiles 提前给一张图切块（prepare_tiles，马上返回）。不在来源记录里（或没有原图地址）时返回 false。
func (s *Service) PrepareTiles(fileID, snapshotID string) (PrepareResult, bool) {
	fid, ok1 := normUUID(fileID)
	sid, ok2 := normUUID(snapshotID)
	if !ok1 || !ok2 {
		return PrepareResult{}, false
	}
	src, ok := s.lookup(fid, sid)
	if !ok || src.OriginalURL == "" {
		return PrepareResult{}, false
	}
	return s.tiler.prepare(src), true
}
