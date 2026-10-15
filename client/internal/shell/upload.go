package shell

// M6-2：上传队列（requirements 第 6 节"上传队列""每个文件"），给页面的命令和事件。核心在 internal/upload；
// 这里把它接到 Wails 上：命令（upload_status、upload_recent、upload_retry_failed、upload_resume、
// pick_and_enqueue_upload）、事件（upload:*，名字见 web/src/lib/scan-queue.ts）、壳启动和退出时开始、停止，
// 页面登录成功时让 401 引起的暂停自动继续。热文件夹的命令在 hotfolder.go，重扫件在 rescan.go，托盘在 upload_tray.go。

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"

	"doclite/client/internal/proxy"
	"doclite/client/internal/upload"
)

// uploadState 是壳里的上传队列、热文件夹和重扫件。队列库打不开时（另一个进程占着、库坏了）engine 为 nil，
// openErr 是原因：命令返回 internal，托盘写"上传队列不可用"。
type uploadState struct {
	api     *upload.Client
	store   *upload.Store
	engine  *upload.Engine
	hot     *upload.HotFolder
	openErr string

	mu sync.Mutex
	// picked：用系统对话框选过的监控目录（hotfolder_set 只接受这些或现有配置里的目录）。
	picked map[string]bool
	// rescanBusy：正在上传重扫件（同时只做一个）。
	rescanBusy atomic.Bool
}

// initUploads 打开队列库、登记命令（registerCommands 调用；还没有开始调度，见 startUploads）。
func (s *Shell) initUploads() {
	u := &uploadState{api: upload.NewClient(s.proxy), picked: map[string]bool{}}
	s.uploads = u
	if store, err := upload.OpenStore(s.cfg.DataDir); err != nil {
		u.openErr = err.Error()
		s.log.Error("上传队列打不开", "err", err)
	} else {
		u.store = store
		u.engine = upload.NewEngine(store, u.api, s.log, &uploadEvents{s: s})
		if u.hot, err = upload.NewHotFolder(u.engine, s.log); err != nil {
			u.openErr = "读不了热文件夹的配置：" + err.Error()
			s.log.Error("热文件夹的配置读不了", "err", err)
			u.engine, u.store = nil, nil
			store.Close()
		}
	}
	s.proxy.Observe(s.observeUploads)
	s.registerUploadCommands()
	s.registerHotfolderCommands()
	s.registerRescanCommands()
}

// startUploads：应用起来以后开始调度，热文件夹按上次的状态恢复。
func (s *Shell) startUploads() {
	u := s.uploads
	if u == nil || u.engine == nil {
		return
	}
	u.engine.Start()
	u.hot.Restore()
}

// stopUploads：壳退出时停下（正在传的下次启动从头再来）、关库释放锁。
func (s *Shell) stopUploads() {
	u := s.uploads
	if u == nil || u.engine == nil {
		return
	}
	u.hot.Shutdown()
	u.engine.Stop(3 * time.Second)
	if err := u.store.Close(); err != nil {
		s.log.Warn("关上传队列库出错", "err", err)
	}
}

// observeUploads：页面登录成功（会话代理看到 POST /api/auth/login 成功）时，401 引起的暂停自动继续。
func (s *Shell) observeUploads(req *proxy.Request, status int, _ []byte) {
	u := s.uploads
	if u == nil || u.engine == nil {
		return
	}
	if req.Path == proxy.LoginPath && req.Method == http.MethodPost && status >= 200 && status < 300 {
		u.engine.LoginSucceeded()
	}
}

// queue 返回调度器；队列打不开时返回给页面的错误。
func (u *uploadState) queue() (*upload.Engine, error) {
	if u.engine == nil {
		return nil, proxy.Internal("上传队列不可用：" + u.openErr)
	}
	return u.engine, nil
}

// shellError 把 internal/upload 的错误换成给页面的 {kind, message}。
func shellError(err error) error {
	var ue *upload.UserError
	var he *upload.HotError
	var pe *proxy.Error
	switch {
	case errors.As(err, &ue):
		return proxy.InvalidRequest(ue.Msg)
	case errors.As(err, &he):
		return proxy.InvalidRequest(he.Msg)
	case errors.As(err, &pe):
		return pe
	}
	return proxy.Internal(err.Error())
}

func (s *Shell) registerUploadCommands() {
	u := s.uploads

	handle(s, "upload_status", func(ctx context.Context, _ noArgs) (any, error) {
		e, err := u.queue()
		if err != nil {
			return nil, err
		}
		return e.Status(ctx), nil
	})

	handle(s, "upload_recent", func(ctx context.Context, a struct {
		Limit  int             `json:"limit"`
		Status []upload.Status `json:"status"`
	}) (any, error) {
		e, err := u.queue()
		if err != nil {
			return nil, err
		}
		items, err := e.Recent(ctx, a.Limit, a.Status)
		if err != nil {
			return nil, shellError(err)
		}
		return items, nil
	})

	handle(s, "upload_retry_failed", func(ctx context.Context, a struct {
		IDs []int64 `json:"ids"`
	}) (any, error) {
		e, err := u.queue()
		if err != nil {
			return nil, err
		}
		n, err := e.RetryFailed(ctx, a.IDs)
		if err != nil {
			return nil, shellError(err)
		}
		return n, nil
	})

	handle(s, "upload_resume", func(ctx context.Context, _ noArgs) (any, error) {
		e, err := u.queue()
		if err != nil {
			return nil, err
		}
		return e.Resume(ctx), nil
	})

	handle(s, "pick_and_enqueue_upload", func(ctx context.Context, a struct {
		FolderID string `json:"folderId"`
		Mode     string `json:"mode"`
	}) (any, error) {
		e, err := u.queue()
		if err != nil {
			return nil, err
		}
		if s.proxy.Server() == "" {
			return nil, proxy.NotConfigured()
		}
		if _, err := uuid.Parse(a.FolderID); err != nil {
			return nil, proxy.InvalidRequest("文件夹 id 不对")
		}
		if a.Mode != "files" && a.Mode != "folder" {
			return nil, proxy.InvalidRequest("mode 只能是 files 或 folder")
		}
		var paths []string
		if p, ok := s.smokePickForUpload(a.Mode); ok {
			// 自检的主流程：用复制好的样本代替系统对话框（smokeflow.go）。
			paths = p
		} else if paths, err = s.pickForUpload(a.Mode); err != nil {
			return nil, proxy.Internal("打不开系统对话框：" + err.Error())
		}
		res := PickResult{Added: []int64{}, Skipped: []upload.Skipped{}}
		if len(paths) == 0 {
			res.Cancelled = true
			return res, nil
		}
		var files []upload.LocalFile
		if a.Mode == "files" {
			files, res.Skipped = collectFiles(paths)
		} else {
			files, res.Skipped = collectFolder(paths[0])
		}
		added, skipped, err := e.Enqueue(ctx, files, a.FolderID)
		res.Added = added
		res.Skipped = append(res.Skipped, skipped...)
		if err != nil {
			return nil, shellError(err)
		}
		s.log.Info("上传按钮：加入队列", "mode", a.Mode, "added", len(added), "skipped", len(res.Skipped))
		return res, nil
	})
}

// PickResult 是 pick_and_enqueue_upload 的结果（web/src/lib/shell-upload.ts）。
type PickResult struct {
	Cancelled bool             `json:"cancelled"`
	Added     []int64          `json:"added"`
	Skipped   []upload.Skipped `json:"skipped"`
}

// imageFilter 是系统对话框的文件类型。
const imageFilter = "*.jpg;*.jpeg;*.tif;*.tiff;*.png"

// openDialog 建一个系统的打开对话框；主窗口看得见时挂在主窗口上。
func (s *Shell) openDialog() *application.OpenFileDialogStruct {
	d := s.app.Dialog.OpenFile()
	if s.window != nil && s.window.IsVisible() && !s.cfg.Background {
		d.AttachToWindow(s.window)
	}
	return d
}

// smokePickForUpload：自检准备了主流程的样本时，返回代替系统对话框的路径。
func (s *Shell) smokePickForUpload(mode string) ([]string, bool) {
	if s.smoke == nil {
		return nil, false
	}
	return s.smoke.flowUploadPaths(mode)
}

// pickForUpload 用系统对话框选文件（可多选）或一个文件夹。取消时返回空。
func (s *Shell) pickForUpload(mode string) ([]string, error) {
	d := s.openDialog().CanCreateDirectories(false).ShowHiddenFiles(false).ResolvesAliases(true)
	if mode == "folder" {
		dir, err := d.SetTitle("选择要上传的文件夹").CanChooseDirectories(true).CanChooseFiles(false).PromptForSingleSelection()
		if err != nil || dir == "" {
			return nil, err
		}
		return []string{dir}, nil
	}
	return d.SetTitle("选择要上传的图片").CanChooseFiles(true).CanChooseDirectories(false).
		AddFilter("图片（JPEG、PNG、TIFF）", imageFilter).PromptForMultipleSelection()
}

// collectFiles：选的文件（传到所选文件夹本身）。不支持的扩展名、读不了的放进 skipped。
func collectFiles(paths []string) ([]upload.LocalFile, []upload.Skipped) {
	var files []upload.LocalFile
	skipped := []upload.Skipped{}
	for _, p := range paths {
		if !upload.SupportedImage(p) {
			skipped = append(skipped, upload.Skipped{Path: p, Reason: "不支持的文件类型（只收 jpg、jpeg、tif、tiff、png）"})
			continue
		}
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			skipped = append(skipped, upload.Skipped{Path: p, Reason: "读不了这个文件"})
			continue
		}
		files = append(files, upload.LocalFile{Path: p, Size: st.Size(), MtimeNS: st.ModTime().UnixNano()})
	}
	return files, skipped
}

// collectFolder：选的文件夹连同它本身按相对路径逐层建子文件夹（和浏览器的"上传文件夹"一样）；忽略规则同热文件夹
// （隐藏、系统、临时文件，不跟随符号链接，≤ 32 层）。不支持的扩展名放进 skipped。
func collectFolder(root string) ([]upload.LocalFile, []upload.Skipped) {
	var files []upload.LocalFile
	skipped := []upload.Skipped{}
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			skipped = append(skipped, upload.Skipped{Path: dir, Reason: "读不了这个文件夹"})
			return
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.Type()&fs.ModeSymlink != 0 || upload.IgnoredName(name, ent.IsDir()) {
				continue
			}
			info, err := ent.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(dir, name)
			if ent.IsDir() {
				if depth < upload.MaxDepth {
					walk(path, rel+"/"+name, depth+1)
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if !upload.SupportedImage(name) {
				skipped = append(skipped, upload.Skipped{Path: path, Reason: "不支持的文件类型（只收 jpg、jpeg、tif、tiff、png）"})
				continue
			}
			files = append(files, upload.LocalFile{Path: path, RelDir: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()})
		}
	}
	walk(root, filepath.Base(filepath.Clean(root)), 1)
	return files, skipped
}

// uploadEvents 把队列的事件发给页面（Wails 的事件），并刷新托盘。
type uploadEvents struct{ s *Shell }

func (ev *uploadEvents) emit(name string, data ...any) {
	if app := ev.s.app; app != nil {
		app.Event.Emit(name, data...)
	}
}

func (ev *uploadEvents) refreshTray() {
	if t := ev.s.tray; t != nil {
		t.refresh()
	}
}

func (ev *uploadEvents) ItemChanged(it *upload.Item) {
	c := *it // 发出去的是副本：工作协程之后还会改这一项
	ev.emit("upload:item", &c)
}

func (ev *uploadEvents) ItemRemoved(id int64) {
	ev.emit("upload:item-removed", map[string]int64{"id": id})
}

func (ev *uploadEvents) Progress(id, sent, size int64) {
	ev.emit("upload:progress", map[string]int64{"id": id, "sent": sent, "size": size})
}

func (ev *uploadEvents) StateChanged(st upload.QueueStatus) {
	ev.emit("upload:state", st)
	ev.refreshTray()
}

func (ev *uploadEvents) AuthRequired() {
	ev.emit("upload:auth-required")
}

func (ev *uploadEvents) UpgradeRequired(details json.RawMessage) {
	ev.emit("upload:upgrade-required", map[string]json.RawMessage{"details": details})
}

func (ev *uploadEvents) HotfolderChanged(st upload.HotStatus) {
	ev.emit("hotfolder:state", st)
	ev.refreshTray()
}
