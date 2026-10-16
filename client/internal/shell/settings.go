package shell

// M7-11：客户端的设置页（web 的 /settings，只在客户端里出现）用到的壳的功能（能力 settings，约定见 client/BRIDGE.md）：
//   - local_dirs：数据目录、日志目录、本机图片缓存目录的路径和大小（只用来显示）；
//   - open_local_dir {dir}：用系统的文件管理器（访达、资源管理器）打开其中一个。dir 只能是 data、logs、cache 三个名字之一，
//     路径由壳自己算，页面给不了路径；
//   - 托盘菜单的"设置…"：显示主窗口并发事件 tray:open-settings，页面跳到设置页（和 tray:error 一样由页面订阅）。

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"doclite/client/internal/appdata"
	"doclite/client/internal/proxy"
)

// 本机目录的名字（open_local_dir 的 dir，local_dirs 的键）。
const (
	localDirData  = "data"
	localDirLogs  = "logs"
	localDirCache = "cache"
)

// traySettingsEvent 是托盘菜单"设置…"发给页面的事件（没有数据），页面收到后跳到设置页。
const traySettingsEvent = "tray:open-settings"

// localDirPath 按名字算出本机目录。只认 data、logs、cache 这三个名字（大小写、前后空白、路径写法一律不认），
// 认不出时 ok 为 false。路径都在数据目录里，由壳自己算，和页面给的任何东西无关。
func localDirPath(dataDir, name string) (path string, ok bool) {
	switch name {
	case localDirData:
		return dataDir, true
	case localDirLogs:
		return appdata.LogsDir(dataDir), true
	case localDirCache:
		return appdata.ImageCacheDir(dataDir), true
	}
	return "", false
}

// LocalDir 是一个本机目录的路径和大小（local_dirs）。
type LocalDir struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	FileCount int    `json:"file_count"`
}

// LocalDirs 是 local_dirs 的结果：data 是整个数据目录（含日志、图片缓存、上传队列库、自检的 smoke/），logs 是日志目录，
// cache 是本机图片缓存（大小取缓存自己的统计，同 cache_stats，不去逐个数缓存里的文件）。
type LocalDirs struct {
	Data  LocalDir `json:"data"`
	Logs  LocalDir `json:"logs"`
	Cache LocalDir `json:"cache"`
}

// localDirs 算三个目录的路径和大小。数据目录逐个数文件，但跳过图片缓存目录（它可能有几十万个切块），改加缓存的统计。
func (s *Shell) localDirs(ctx context.Context) LocalDirs {
	dataDir := s.cfg.DataDir
	cacheDir := appdata.ImageCacheDir(dataDir)
	res := LocalDirs{
		Data:  LocalDir{Path: dataDir},
		Logs:  LocalDir{Path: appdata.LogsDir(dataDir)},
		Cache: LocalDir{Path: cacheDir},
	}
	if s.images != nil {
		st := s.images.Stats()
		res.Cache.SizeBytes, res.Cache.FileCount = st.TotalBytes, st.FileCount
	}
	res.Logs.SizeBytes, res.Logs.FileCount = dirSize(ctx, res.Logs.Path, "")
	res.Data.SizeBytes, res.Data.FileCount = dirSize(ctx, dataDir, cacheDir)
	res.Data.SizeBytes += res.Cache.SizeBytes
	res.Data.FileCount += res.Cache.FileCount
	return res
}

// dirSize 数目录下的普通文件（大小之和、个数），不跟符号链接；skip 非空时跳过这个子目录。读不了的项跳过；
// ctx 取消时返回已经数到的。
func dirSize(ctx context.Context, root, skip string) (int64, int) {
	var size int64
	var count int
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skip != "" && path == skip {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			count++
		}
		return nil
	})
	return size, count
}

// openLocalDir 是 open_local_dir：按名字算出目录，交给系统的文件管理器打开。自检时不打开（不抢焦点），只记日志。
func (s *Shell) openLocalDir(name string) error {
	path, ok := localDirPath(s.cfg.DataDir, name)
	if !ok {
		return proxy.InvalidRequest("dir 只能是 data、logs、cache")
	}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		err = errors.New("不是目录")
	}
	if err != nil {
		return proxy.InternalDetail("打不开这个目录", err)
	}
	if s.smoke != nil {
		s.log.Info("自检：open_local_dir 不打开文件管理器", "dir", name)
		return nil
	}
	open := s.openDir
	if open == nil {
		open = s.openDirInFileManager
	}
	if err := open(path); err != nil {
		return proxy.InternalDetail("没能打开文件管理器", err)
	}
	s.log.Info("用文件管理器打开本机目录", "dir", name)
	return nil
}

// openDirInFileManager 用系统的文件管理器打开目录（macOS 访达、Windows 资源管理器）。
func (s *Shell) openDirInFileManager(path string) error {
	if s.app == nil {
		return errors.New("应用还没起来")
	}
	return s.app.Env.OpenFileManager(path, false)
}

// registerSettingsCommands 登记设置页的命令（registerCommands 调用）。
func (s *Shell) registerSettingsCommands() {
	handle(s, "local_dirs", func(ctx context.Context, _ noArgs) (any, error) { return s.localDirs(ctx), nil })
	handle(s, "open_local_dir", func(_ context.Context, a struct {
		Dir string `json:"dir"`
	}) (any, error) {
		return nil, s.openLocalDir(a.Dir)
	})
}

// openSettings 是托盘菜单的"设置…"：显示主窗口，通知页面跳到设置页。页面不在登录后的界面上（客户端自带的页面）时
// 没人订阅，只是显示窗口；在登录页上时页面跳过去会先要求登录。
func (s *Shell) openSettings() {
	s.log.Info("托盘菜单：设置")
	s.showWindow(true)
	if s.app != nil {
		s.app.Event.Emit(traySettingsEvent, nil)
	}
}
