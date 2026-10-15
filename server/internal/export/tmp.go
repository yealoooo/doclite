package export

import (
	"log/slog"
	"os"
	"path/filepath"
)

// TmpDir 是导出用的临时目录：TMP_DIR 下的 exports/（bundle 在里面建每次执行的 export-* 目录，结束时删掉）。
func TmpDir(base string) string {
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "exports")
}

// SweepTmp 启动时删掉导出临时目录里上次留下的东西（进程被杀时没删的 export-* 目录）。单个 server 进程（硬性规则
// 16），启动时没有别的执行在用它。
func SweepTmp(base string, log *slog.Logger) {
	dir := TmpDir(base)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && log != nil {
			log.Warn("清理导出临时目录失败", "path", filepath.Join(dir, e.Name()), "error", err)
		}
	}
	if len(entries) > 0 && log != nil {
		log.Info("清理了上次留下的导出临时文件", "count", len(entries))
	}
}
