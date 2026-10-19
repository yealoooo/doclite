package shell

// M6-2：热文件夹的命令（web/src/lib/scan-queue.ts）：hotfolder_status、hotfolder_pick_dir、hotfolder_set、
// hotfolder_start、hotfolder_pause。监控目录只能用系统对话框选（页面不能随便指定本机路径）：hotfolder_set 只接受
// hotfolder_pick_dir 选过的目录或现有配置里的目录。托盘的"暂停 / 继续热文件夹"在 upload_tray.go。

import (
	"context"
	"errors"
	"path/filepath"

	"doclite/client/internal/proxy"
	"doclite/client/internal/upload"
)

func (s *Shell) registerHotfolderCommands() {
	u := s.uploads

	hot := func() (*upload.HotFolder, error) {
		if u.hot == nil {
			return nil, proxy.InternalDetail("热文件夹不可用（上传队列打不开）", errors.New(u.openErr))
		}
		return u.hot, nil
	}

	handle(s, "hotfolder_status", func(context.Context, noArgs) (any, error) {
		h, err := hot()
		if err != nil {
			return nil, err
		}
		return h.Status(), nil
	})

	handle(s, "hotfolder_pick_dir", func(context.Context, noArgs) (any, error) {
		if _, err := hot(); err != nil {
			return nil, err
		}
		dir, err := s.pickHotDir()
		if err != nil {
			return nil, proxy.InternalDetail("打不开系统对话框", err)
		}
		if dir == "" {
			return nil, nil
		}
		dir = filepath.Clean(dir)
		u.mu.Lock()
		u.picked[upload.PathKey(dir)] = true
		u.mu.Unlock()
		return dir, nil
	})

	handle(s, "hotfolder_set", func(_ context.Context, a struct {
		WatchDir       string `json:"watchDir"`
		TargetFolderID string `json:"targetFolderId"`
	}) (any, error) {
		h, err := hot()
		if err != nil {
			return nil, err
		}
		dir := filepath.Clean(a.WatchDir)
		u.mu.Lock()
		allowed := u.picked[upload.PathKey(dir)]
		u.mu.Unlock()
		if cur := h.Status().Config; cur != nil && upload.PathKey(cur.WatchDir) == upload.PathKey(dir) {
			allowed = true
		}
		if !allowed {
			return nil, proxy.InvalidRequest("监控目录只能用系统对话框选择")
		}
		st, err := h.Set(dir, a.TargetFolderID)
		if err != nil {
			return nil, shellError(err)
		}
		return st, nil
	})

	handle(s, "hotfolder_start", func(context.Context, noArgs) (any, error) {
		h, err := hot()
		if err != nil {
			return nil, err
		}
		st, err := h.Start()
		if err != nil {
			return nil, shellError(err)
		}
		return st, nil
	})

	handle(s, "hotfolder_pause", func(context.Context, noArgs) (any, error) {
		h, err := hot()
		if err != nil {
			return nil, err
		}
		st, err := h.Pause()
		if err != nil {
			return nil, shellError(err)
		}
		return st, nil
	})
}

// pickHotDirDialog 用系统对话框选监控目录。取消时返回空。
func (s *Shell) pickHotDirDialog() (string, error) {
	return s.openDialog().SetTitle("选择扫描软件保存文件的文件夹").CanChooseDirectories(true).CanChooseFiles(false).
		CanCreateDirectories(true).ShowHiddenFiles(false).ResolvesAliases(true).PromptForSingleSelection()
}
