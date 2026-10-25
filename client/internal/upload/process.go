package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 每个文件（requirements 第 6 节"每个文件"）：核对大小和修改时间 → 查编码方式（encoding.go，导出不了的不发请求）
// → 算 sha → 取或建文件夹 → 预检 `files:check` → 生成封面（生成不了就不带）→ 流式上传（file_name、content_sha256、
// cover?、file）。本地文件不改不删。
//
// 失败的分类：400、403、413、415 永久失败；网络错误、5xx、408、429、别的 409 临时失败（自动重试，1 秒起翻倍，封顶
// 5 分钟）；401、426 暂停整个队列（这一项放回排队，不算重试）；403 `license_expired`、`license_missing`（M7-1：许可证
// 到期或没有有效的许可证）和 401 一样暂停（重新登录后自动继续），不算文件失败；400 invalid_cover 去掉封面再传一次；
// 409 duplicate_content 记为"内容重复"；404：传到子文件夹的忘掉映射、按名字重建（最多三轮），传到目标文件夹本身的
// 记失败 target_not_found（不自动重建选定的目标）。

// progressEvery：上传进度事件的间隔。
const progressEvery = 250 * time.Millisecond

// requestTimeout：建文件夹、预检、GET /api/me 的时限。
const requestTimeout = 30 * time.Second

// netErrorCodes 是"连不上服务器"类的错误：server 恢复后（有文件入库了）把等它们重试的项提前。
var netErrorCodes = []string{ErrCodeNetwork, "busy", "http_502", "http_503", "http_504"}

type resultKind int

const (
	resDone resultKind = iota
	resDuplicate
	resFailed  // 永久失败
	resRetry   // 临时失败，退避后重试
	resRequeue // 放回排队，不算重试（服务器换了、暂停了）
	resWaiting // 热文件夹的文件又变了：回到等待写完
	resRemove  // 热文件夹的文件不见了：从队列删掉
	resPause   // 401、426：暂停整个队列，这一项放回排队
	resStale   // 404：子文件夹在 server 上不在了（调用方忘掉映射重建）
)

type result struct {
	kind    resultKind
	code    string
	message string
	pause   string
	details json.RawMessage
}

func failed(code, msg string) *result { return &result{kind: resFailed, code: code, message: msg} }
func retry(code, msg string) *result  { return &result{kind: resRetry, code: code, message: msg} }

func baseName(p string) string { return filepath.Base(p) }

// process 处理队首的一项（在工作协程里）。
func (e *Engine) process(it *Item) {
	it.Status = StatusUploading
	it.NextAttemptAt = nil
	e.save(it)
	r := e.run(e.ctx, it)
	if e.ctx.Err() != nil {
		// 壳在退出：这一项留给下次启动（库里"上传中"会回到"排队"）。
		return
	}
	e.finish(it, r)
}

func (e *Engine) finish(it *Item, r *result) {
	now := nowMS()
	e.logResult(it, r)
	switch r.kind {
	case resDone, resDuplicate:
		if r.kind == resDone {
			it.Status = StatusDone
		} else {
			it.Status = StatusDuplicate
		}
		it.NextAttemptAt, it.ErrorCode, it.ErrorMessage = nil, nil, nil
		it.FinishedAt = int64Ptr(now)
		e.save(it)
		e.afterSuccess()
		return
	case resFailed:
		it.Status = StatusFailed
		it.NextAttemptAt = nil
		it.ErrorCode, it.ErrorMessage = strPtr(r.code), strPtr(r.message)
		it.FinishedAt = int64Ptr(now)
	case resRetry:
		it.Status = StatusQueued
		it.Attempts++
		it.NextAttemptAt = int64Ptr(now + RetryDelay(it.Attempts).Milliseconds())
		it.ErrorCode, it.ErrorMessage = strPtr(r.code), strPtr(r.message)
		for _, c := range netErrorCodes {
			if c == r.code {
				e.mu.Lock()
				e.netTrouble = true
				e.mu.Unlock()
			}
		}
	case resRequeue:
		it.Status = StatusQueued
	case resPause:
		it.Status = StatusQueued
		it.ErrorCode, it.ErrorMessage = strPtr(r.pause), strPtr(r.message)
		e.save(it)
		e.pause(r.pause, r.message, r.details)
		return
	case resWaiting:
		resetResult(it, StatusWaiting)
	case resRemove:
		e.remove(it)
		return
	default:
		it.Status = StatusFailed
		it.ErrorCode, it.ErrorMessage = strPtr(ErrCodeInternal), strPtr(r.message)
	}
	e.save(it)
}

// logResult 记下每一项的结论（客户现场查问题靠日志）。
func (e *Engine) logResult(it *Item, r *result) {
	attrs := []any{"id", it.ID, "source", it.Source, "rel_dir", it.RelDir, "file", it.FileName}
	switch r.kind {
	case resDone:
		e.log.Info("已入库", append(attrs, "server_file_name", deref(it.ServerFileName), "file_id", deref(it.FileID))...)
	case resDuplicate:
		e.log.Info("内容重复，已跳过", attrs...)
	case resFailed:
		e.log.Warn("上传失败（不自动重试）", append(attrs, "code", r.code, "message", r.message)...)
	case resRetry:
		e.log.Warn("上传暂时失败，稍后重试", append(attrs, "attempt", it.Attempts+1, "code", r.code, "message", r.message,
			"delay", RetryDelay(it.Attempts+1).String())...)
	case resWaiting:
		e.log.Info("文件又变了，回到等待写入完成", attrs...)
	case resRemove:
		e.log.Info("本地文件不见了，从队列删掉", attrs...)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// afterSuccess：出现过"连不上服务器"之后第一次有文件入库或判重，把等这类错误重试的项提前到现在（只这一次）。
func (e *Engine) afterSuccess() {
	e.mu.Lock()
	trouble := e.netTrouble
	e.netTrouble = false
	e.mu.Unlock()
	if !trouble {
		return
	}
	items, err := e.store.Expedite(context.Background(), e.api.Server(), netErrorCodes)
	if err != nil {
		e.log.Error("上传队列：提前重试失败", "err", err)
		return
	}
	for _, it := range items {
		e.ev.ItemChanged(it)
	}
	if len(items) > 0 {
		e.log.Info("服务器恢复了，等重试的项提前", "count", len(items))
		e.Wake()
	}
}

// run 做一项的全部步骤，返回结论（成功时结果已经写进 it）。
func (e *Engine) run(ctx context.Context, it *Item) *result {
	server := it.ServerURL
	if e.api.Server() != server {
		return &result{kind: resRequeue}
	}
	if r := e.verifyLocal(it); r != nil {
		return r
	}
	if r := checkEncoding(it); r != nil {
		return r
	}
	sha, err := hashFile(it.LocalPath, it.Size)
	if err != nil {
		return e.localFailure(it, err)
	}
	if r := e.verifyLocal(it); r != nil {
		return r
	}
	it.SHA256 = &sha

	var cover *Cover
	for round := 0; ; round++ {
		folderID, r := e.resolveFolder(ctx, it)
		if r == nil {
			it.FolderID = &folderID
			r = e.checkAndUpload(ctx, it, folderID, &cover)
		}
		if r.kind != resStale {
			return r
		}
		// 子文件夹在 server 上被删了：忘掉这条路径上的映射，按名字重建（最多三轮）。
		if err := e.store.ForgetFolders(ctx, server, it.TargetFolderID, RelKey(it.RelDir)); err != nil {
			return retry(ErrCodeInternal, "写本机队列库失败："+err.Error())
		}
		it.FolderID = nil
		if round >= 2 {
			return retry(r.code, r.message)
		}
		e.log.Info("子文件夹在服务器上不在了，按名字重建", "id", it.ID, "rel_dir", it.RelDir)
	}
}

// checkAndUpload 预检、生成封面、上传。cover 跨重建文件夹的几轮复用（只生成一次）。
func (e *Engine) checkAndUpload(ctx context.Context, it *Item, folderID string, cover **Cover) *result {
	server := it.ServerURL
	cctx, cancel := context.WithTimeout(ctx, requestTimeout)
	chk, err := e.api.Check(cctx, server, folderID, it.FileName, *it.SHA256, it.Size)
	cancel()
	if err != nil {
		return e.classify(ctx, it, err, false)
	}
	if chk.AlreadyUploaded {
		// 之前已经传上去了（断网重试）：记为已入库，这时没有 file_id。
		it.ServerFileName = strPtr(chk.FinalName)
		return &result{kind: resDone}
	}
	if chk.Duplicate != nil {
		it.DuplicateOf = chk.Duplicate
		return &result{kind: resDuplicate}
	}

	if *cover == nil {
		c := MakeCover(ctx, it.LocalPath)
		*cover = &c
		if c.Data != nil {
			it.CoverStatus, it.CoverReason = strPtr(CoverIncluded), nil
		} else {
			it.CoverStatus, it.CoverReason = strPtr(CoverNotGenerated), strPtr(c.Reason)
			e.log.Info("没有生成封面", "id", it.ID, "file", it.FileName, "reason", c.Reason)
		}
	}
	data := (*cover).Data
	if it.CoverStatus != nil && *it.CoverStatus == CoverRejected {
		data = nil
	}
	e.save(it) // 页面上显示"上传中"、有没有封面

	var last time.Time
	e.ev.Progress(it.ID, 0, it.Size)
	ing, err := e.api.uploadDroppingCover(ctx, server, "/api/folders/"+folderID+"/files", UploadInput{
		Path: it.LocalPath, FileName: it.FileName, SHA256: *it.SHA256, Size: it.Size, Cover: data,
	}, func(sent int64) {
		if now := time.Now(); sent >= it.Size || now.Sub(last) >= progressEvery {
			last = now
			e.ev.Progress(it.ID, sent, it.Size)
		}
	}, func(msg string) {
		e.log.Warn("服务器没收封面，去掉封面重传", "id", it.ID, "file", it.FileName, "message", msg)
		it.CoverStatus, it.CoverReason = strPtr(CoverRejected), strPtr(msg)
		e.save(it)
	})
	if err != nil {
		return e.classify(ctx, it, err, true)
	}
	it.FileID = strPtr(ing.FileID)
	it.ServerFileName = strPtr(ing.FileName)
	it.SortNo = int64Ptr(ing.SortNo)
	it.Warnings = ing.Warnings
	if it.Warnings == nil {
		it.Warnings = []string{}
	}
	return &result{kind: resDone}
}

// resolveFolder 取或建这一项要传到的文件夹：根目录里的文件传到目标文件夹本身；子文件夹逐层按名字建
// （if_exists: "return"），记住建出来的 folder_id（server 上改名、移动照传，删除后重建）。
func (e *Engine) resolveFolder(ctx context.Context, it *Item) (string, *result) {
	if it.RelDir == "" {
		return it.TargetFolderID, nil
	}
	server := it.ServerURL
	parts := strings.Split(it.RelDir, "/")
	parent := it.TargetFolderID
	for i, name := range parts {
		relKey := RelKey(strings.Join(parts[:i+1], "/"))
		id, err := e.store.FolderOf(ctx, server, it.TargetFolderID, relKey)
		if err != nil {
			return "", retry(ErrCodeInternal, "读本机队列库失败："+err.Error())
		}
		if id != "" {
			parent = id
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, requestTimeout)
		f, err := e.api.CreateFolder(cctx, server, parent, name)
		cancel()
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) {
				switch {
				case ae.Status == http.StatusNotFound && i == 0:
					return "", failed(ErrCodeTargetNotFound, "目标文件夹已被删除（或进不了它所在的项目），请重新选择目标文件夹")
				case ae.Status == http.StatusNotFound:
					return "", &result{kind: resStale, code: ErrCodeFolderRejected, message: "上一级子文件夹在服务器上不在了"}
				case ae.Status == http.StatusBadRequest || (ae.Status == http.StatusConflict && ae.Code == "tree_full"):
					return "", failed(ErrCodeFolderRejected, fmt.Sprintf("服务器拒绝建子文件夹「%s」：%s", name, ae.Message))
				}
			}
			return "", e.classify(ctx, it, err, false)
		}
		if err := e.store.SetFolder(ctx, server, it.TargetFolderID, relKey, f.ID); err != nil {
			return "", retry(ErrCodeInternal, "写本机队列库失败："+err.Error())
		}
		parent = f.ID
	}
	return parent, nil
}

// classify 把请求的错误归类。upload 为 true 时是上传请求本身：网络出错先问一次 GET /api/me，是 401、426 就暂停。
func (e *Engine) classify(ctx context.Context, it *Item, err error, upload bool) *result {
	if errors.Is(err, errServerChanged) || errors.Is(err, errNoServer) {
		return &result{kind: resRequeue}
	}
	if errors.Is(err, ErrFileChanged) {
		return e.changedDuringUpload(it)
	}
	var le *localError
	if errors.As(err, &le) {
		return e.localFailure(it, le.err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return e.classifyAPI(it, ae)
	}
	var ne *NetError
	if errors.As(err, &ne) {
		if upload && ctx.Err() == nil {
			if r := e.changedDuringUpload(it); r.kind != resFailed {
				return r
			}
			if me := e.api.probeSession(ctx, it.ServerURL); me != nil {
				return e.classifyAPI(it, me)
			}
		}
		return retry(ErrCodeNetwork, ne.Msg)
	}
	return retry(ErrCodeInternal, err.Error())
}

func (e *Engine) classifyAPI(it *Item, ae *APIError) *result {
	switch {
	case ae.Status == http.StatusUnauthorized:
		return &result{kind: resPause, pause: PauseAuth, message: "登录已失效，上传已暂停。重新登录后自动继续"}
	case licenseBlocked(ae):
		return &result{kind: resPause, pause: PauseAuth, message: licensePauseMessage(ae)}
	case ae.Status == http.StatusUpgradeRequired:
		return &result{kind: resPause, pause: PauseUpgrade, message: "客户端版本过低，上传已暂停：" + ae.Message, details: ae.Details}
	case ae.Status == http.StatusNotFound && it.RelDir == "":
		return failed(ErrCodeTargetNotFound, "目标文件夹已被删除（或进不了它所在的项目），请重新选择目标文件夹")
	case ae.Status == http.StatusNotFound:
		return &result{kind: resStale, code: ae.Code, message: ae.Message}
	case ae.Status == http.StatusConflict && ae.Code == "duplicate_content":
		it.DuplicateOf = ae.Details
		return &result{kind: resDuplicate}
	case ae.Code == "checksum_mismatch":
		// 本地文件变了：热文件夹的回到等待；没变说明传输出了问题，按永久失败（手动重试）。
		if r := e.changedDuringUpload(it); r.kind != resFailed {
			return r
		}
		return failed(ae.Code, ae.Message)
	case ae.Status == http.StatusConflict, ae.Status == http.StatusRequestTimeout, ae.Status == http.StatusTooManyRequests,
		ae.Status >= 500:
		return retry(ae.Code, ae.Message)
	}
	// 400、403、413、415 等：永久失败。
	return failed(ae.Code, ae.Message)
}

// verifyLocal 核对本地文件还是入队时的大小和修改时间。
func (e *Engine) verifyLocal(it *Item) *result {
	st, err := os.Stat(it.LocalPath)
	if err != nil {
		return e.localFailure(it, err)
	}
	size, mtime := st.Size(), st.ModTime().UnixNano()
	if size == it.Size && mtime == it.MtimeNS {
		return nil
	}
	it.Size, it.MtimeNS = size, mtime
	if it.Source == SourceHotfolder {
		return &result{kind: resWaiting}
	}
	return retry(ErrCodeFileChanged, "文件在上传前又被改动了")
}

// changedDuringUpload：上传过程中出了问题，看本地文件是不是变了。变了：热文件夹的回到等待、上传按钮的临时失败；
// 没变返回 resFailed（调用方接着按原来的错误处理）。
func (e *Engine) changedDuringUpload(it *Item) *result {
	if r := e.verifyLocal(it); r != nil {
		if r.kind == resRetry && r.code == ErrCodeFileChanged {
			r.message = "文件在上传过程中被改动了"
		}
		return r
	}
	return failed(ErrCodeFileChanged, "文件在上传过程中被改动了")
}

// localFailure：读本地文件出错。不见了：热文件夹的从队列删掉，上传按钮的记失败；别的错误按临时失败。
func (e *Engine) localFailure(it *Item, err error) *result {
	if errors.Is(err, fs.ErrNotExist) {
		if it.Source == SourceHotfolder {
			return &result{kind: resRemove}
		}
		return failed(ErrCodeFileMissing, "本地文件不见了")
	}
	if errors.Is(err, ErrFileChanged) {
		return e.changedDuringUpload(it)
	}
	return retry(ErrCodeFileUnreadable, "读不了本地文件："+err.Error())
}

// hashFile 算文件前 size 个字节的 sha256（小写 hex）。文件比 size 短返回 [ErrFileChanged]。
func hashFile(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", ErrFileChanged
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// licenseBlocked：403 `license_expired`、`license_missing`（M7-1：服务器的许可证到期或没有有效的许可证，非管理员的
// 请求被拒）。和会话失效一样暂停整个队列，不是这个文件的问题。
func licenseBlocked(ae *APIError) bool {
	return ae.Status == http.StatusForbidden && (ae.Code == "license_expired" || ae.Code == "license_missing")
}

func licensePauseMessage(ae *APIError) string {
	if ae.Code == "license_expired" {
		return "服务器的许可证已到期，上传已暂停。管理员导入新的许可证后重新登录，自动继续"
	}
	return "服务器还没有有效的许可证，上传已暂停。管理员导入许可证后重新登录，自动继续"
}
