package upload

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// 重扫件上传（requirements 第 6 节"重扫件上传"，用户决定 2026-10-07 Q6）：不进队列。选一个文件 → 查文件尾完整 →
// 算 sha → 生成封面 → 上传到 `POST /api/files/{fid}/rescan`；400 invalid_cover 去掉封面重传一次，409 conflict 再发
// 一次，其余错误不重试（交给页面写成提示，web/src/lib/rescan.ts）。同时只做一个（由调用方保证）。
//
// 选的文件不合格时不发请求（重扫会把"已驳回"消耗掉，不能撤回），返回 [UserError]：扩展名不对、文件尾不完整、
// 编码方式导出不了（encoding.go，提示和 server 一样）、读不了、算 sha 前后大小或修改时间变了。提示文字和页面的一样
// （lib/rescan.ts 的 MSG_INCOMPLETE、MSG_EXTENSION）。

// 给页面的提示（和 web/src/lib/rescan.ts 一样）。
const (
	MsgIncomplete = "选的文件不完整或还在写入，请等扫描软件写完再选"
	MsgExtension  = "只能选 jpg、jpeg、tif、tiff、png 的图片"
	msgChanged    = "选的文件在上传时被改动了，请等扫描软件写完再选"
	msgUnreadable = "读不了选的文件"
)

// rescanProgressEvery：重扫上传进度事件的间隔（最多每 200 毫秒一次）。
const rescanProgressEvery = 200 * time.Millisecond

// UserError 是命令本身不能做（页面收到 invalid_request），Msg 是给人看的原因（不写本机路径）。
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// RescanCover 是封面的情况。
type RescanCover struct {
	Status string  `json:"status"`
	Reason *string `json:"reason"`
	MS     *int64  `json:"ms"`
}

// RescanFailure 是 server 返回的错误（status 0 表示连不上服务器）。
type RescanFailure struct {
	Status  int             `json:"status"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

// RescanOutcome 是 pick_and_rescan 的结果（web/src/lib/rescan.ts 的 ShellRescanOutcome）。
type RescanOutcome struct {
	Cancelled bool            `json:"cancelled"`
	LocalName *string         `json:"local_name"`
	Size      *int64          `json:"size"`
	SHA256    *string         `json:"sha256"`
	Cover     *RescanCover    `json:"cover"`
	Attempts  int             `json:"attempts"`
	Result    json.RawMessage `json:"result"`
	Error     *RescanFailure  `json:"error"`
	ElapsedMS int64           `json:"elapsed_ms"`
}

// 重扫进度的阶段。
const (
	PhaseHashing   = "hashing"
	PhaseCover     = "cover"
	PhaseUploading = "uploading"
)

// RescanProgress 收进度（rescan:progress 事件）。
type RescanProgress func(phase string, sent, size int64)

// Rescan 把本机文件 path 作为 fileID 的重扫件上传（server 是要传到的服务器）。
func (c *Client) Rescan(ctx context.Context, server, fileID, path string, progress RescanProgress) (*RescanOutcome, error) {
	started := time.Now()
	name := filepath.Base(path)
	if !SupportedImage(name) {
		return nil, &UserError{MsgExtension}
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, &UserError{msgUnreadable}
	}
	size, mtime := st.Size(), st.ModTime().UnixNano()
	out := &RescanOutcome{LocalName: &name, Size: &size}
	progress(PhaseHashing, 0, size)
	complete, err := TailCompleteFile(path)
	if err != nil {
		return nil, &UserError{msgUnreadable}
	}
	if !complete {
		return nil, &UserError{MsgIncomplete}
	}
	if msg := UnsupportedEncoding(path, size); msg != "" {
		return nil, &UserError{msg}
	}
	sha, err := hashFile(path, size)
	if err != nil {
		if errors.Is(err, ErrFileChanged) {
			return nil, &UserError{msgChanged}
		}
		return nil, &UserError{msgUnreadable}
	}
	if changed(path, size, mtime) {
		return nil, &UserError{msgChanged}
	}
	out.SHA256 = &sha

	progress(PhaseCover, 0, size)
	cv := MakeCover(ctx, path)
	ms := cv.MS
	out.Cover = &RescanCover{Status: CoverIncluded, MS: &ms}
	if cv.Data == nil {
		out.Cover = &RescanCover{Status: CoverNotGenerated, Reason: strPtr(cv.Reason)}
	}

	data := cv.Data
	conflictRetried := false
	for {
		out.Attempts++
		var last time.Time
		progress(PhaseUploading, 0, size)
		ing, err := c.uploadDroppingCover(ctx, server, "/api/files/"+fileID+"/rescan", UploadInput{
			Path: path, FileName: name, SHA256: sha, Size: size, Cover: data,
		}, func(sent int64) {
			if now := time.Now(); now.Sub(last) >= rescanProgressEvery && sent < size {
				last = now
				progress(PhaseUploading, sent, size)
			}
		}, func(msg string) {
			data = nil
			out.Cover = &RescanCover{Status: CoverRejected, Reason: strPtr(msg), MS: &ms}
		})
		if err == nil {
			progress(PhaseUploading, size, size)
			out.Result = ing.Raw
			break
		}
		var ae *APIError
		if errors.As(err, &ae) {
			if ae.Code == "conflict" && !conflictRetried {
				conflictRetried = true
				continue
			}
			out.Error = &RescanFailure{Status: ae.Status, Code: ae.Code, Message: ae.Message, Details: ae.Details}
			break
		}
		if errors.Is(err, ErrFileChanged) || changed(path, size, mtime) {
			return nil, &UserError{msgChanged}
		}
		var le *localError
		if errors.As(err, &le) {
			return nil, &UserError{msgUnreadable}
		}
		if errors.Is(err, errServerChanged) || errors.Is(err, errNoServer) {
			return nil, &UserError{"服务器地址换了，没有上传"}
		}
		// 连不上：先问一次 GET /api/me，会话失效（401）、要升级（426）或许可证挡住（403，M7-1）就报那个错误。
		msg := err.Error()
		var ne *NetError
		if errors.As(err, &ne) {
			if me := c.probeSession(ctx, server); me != nil {
				out.Error = &RescanFailure{Status: me.Status, Code: me.Code, Message: me.Message, Details: me.Details}
				break
			}
			msg = ne.Msg
		}
		out.Error = &RescanFailure{Status: 0, Code: ErrCodeNetwork, Message: msg}
		break
	}
	out.ElapsedMS = time.Since(started).Milliseconds()
	return out, nil
}

// changed：文件的大小或修改时间和之前不一样了（或者读不了了）。
func changed(path string, size, mtime int64) bool {
	st, err := os.Stat(path)
	return err != nil || st.Size() != size || st.ModTime().UnixNano() != mtime
}
