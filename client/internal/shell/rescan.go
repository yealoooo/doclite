package shell

// M6-2：重扫件上传 `pick_and_rescan {fileId}`（requirements 第 6 节"重扫件上传"，web/src/lib/rescan.ts）：
// 系统对话框选一个文件 → 查文件尾 → 算 sha → 封面 → 上传到 POST /api/files/{fid}/rescan，同步返回结果；不进上传队列，
// 同时只做一个。期间发 `rescan:progress {file_id, phase, sent, size}`（上传中最多每 200 毫秒一次）。
// `--smoke` 时用环境变量 DOCLITE_SMOKE_RESCAN_PATH 的文件代替对话框（smokeRun.install），自检多一步 rescan。

import (
	"context"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"doclite/client/internal/proxy"
	"doclite/client/internal/upload"
)

// SmokeRescan 是 smoke_config 的 rescan（web/src/lib/shell.ts 的 SmokeConfig['rescan']），从环境变量读：
//
//	DOCLITE_SMOKE_RESCAN_PATH     重扫件（本机文件；设置了才跑 rescan 一步）
//	DOCLITE_SMOKE_RESCAN_FILE_ID  要重扫的文件；不设时取项目待重扫列表的第一个
//	DOCLITE_SMOKE_RESCAN_PROJECT  项目编码；不设时取第一个能进的项目
//	DOCLITE_SMOKE_RESCAN_EXPECT   done（默认）或 incomplete（给的是写了一半的文件，要求壳不发请求）
type SmokeRescan struct {
	Enabled  bool    `json:"enabled"`
	FileName *string `json:"file_name"`
	FileID   *string `json:"file_id"`
	Project  *string `json:"project"`
	Expect   string  `json:"expect"`
}

const smokeRescanPathEnv = "DOCLITE_SMOKE_RESCAN_PATH"

func envPtr(name string) *string {
	if v := os.Getenv(name); v != "" {
		return &v
	}
	return nil
}

func smokeRescanConfig() SmokeRescan {
	c := SmokeRescan{Expect: "done"}
	path := os.Getenv(smokeRescanPathEnv)
	if path == "" {
		return c
	}
	name := filepath.Base(path)
	c.Enabled = true
	c.FileName = &name
	c.FileID = envPtr("DOCLITE_SMOKE_RESCAN_FILE_ID")
	c.Project = envPtr("DOCLITE_SMOKE_RESCAN_PROJECT")
	if os.Getenv("DOCLITE_SMOKE_RESCAN_EXPECT") == "incomplete" {
		c.Expect = "incomplete"
	}
	return c
}

// pickRescanDialog 用系统对话框选一个重扫件。取消时返回空。
func (s *Shell) pickRescanDialog() (string, error) {
	return s.openDialog().SetTitle("选择重扫件（一张）").CanChooseFiles(true).CanChooseDirectories(false).
		ShowHiddenFiles(false).ResolvesAliases(true).AddFilter("图片（JPEG、PNG、TIFF）", imageFilter).
		PromptForSingleSelection()
}

func (s *Shell) registerRescanCommands() {
	u := s.uploads

	handle(s, "pick_and_rescan", func(_ context.Context, a struct {
		FileID string `json:"fileId"`
	}) (any, error) {
		server := s.proxy.Server()
		if server == "" {
			return nil, proxy.NotConfigured()
		}
		if _, err := uuid.Parse(a.FileID); err != nil {
			return nil, proxy.InvalidRequest("文件 id 不对")
		}
		if !u.rescanBusy.CompareAndSwap(false, true) {
			return nil, proxy.InvalidRequest("正在上传另一张重扫件，请等它传完")
		}
		defer u.rescanBusy.Store(false)

		path, err := s.pickRescan()
		if err != nil {
			return nil, proxy.InternalDetail("打不开系统对话框", err)
		}
		if path == "" {
			return upload.RescanOutcome{Cancelled: true}, nil
		}
		progress := func(phase string, sent, size int64) {
			if app := s.app; app != nil {
				app.Event.Emit("rescan:progress", map[string]any{"file_id": a.FileID, "phase": phase, "sent": sent, "size": size})
			}
		}
		// 不跟页面的调用一起取消：请求发出去以后 server 照样会处理，页面重载了也让它传完。
		out, err := u.api.Rescan(context.Background(), server, a.FileID, path, progress)
		if err != nil {
			s.log.Info("重扫件没有上传", "file_id", a.FileID, "reason", err)
			return nil, shellError(err)
		}
		if out.Error != nil {
			s.log.Info("重扫件上传失败", "file_id", a.FileID, "status", out.Error.Status, "code", out.Error.Code)
		} else {
			s.log.Info("重扫件已上传", "file_id", a.FileID, "attempts", out.Attempts, "ms", out.ElapsedMS)
		}
		return out, nil
	})
}
