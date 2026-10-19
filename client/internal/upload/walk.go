package upload

import (
	"io/fs"
	"os"
	"path/filepath"
)

// WalkImages 逐层列出 dir（相对路径 rel，`/` 连接；第 depth 层）下的文件，热文件夹和上传按钮选的文件夹共用一套规则
// （requirements 第 6 节）：不跟随符号链接，跳过 [IgnoredName] 和 Windows 上带隐藏、系统属性的文件和目录，最多
// [MaxDepth] 层。enter 不为 nil 时在读每个目录（含 dir 本身）之前调用，返回 false 不读它；file 收到每个普通文件
// （RelDir 是它所在目录的相对路径；扩展名由调用方判断）。返回读不了的目录。
func WalkImages(dir, rel string, depth int, enter func(dir string) bool, file func(f LocalFile)) (unreadable []string) {
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if enter != nil && !enter(dir) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			unreadable = append(unreadable, dir)
			return
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.Type()&fs.ModeSymlink != 0 || IgnoredName(name, ent.IsDir()) {
				continue
			}
			info, err := ent.Info()
			if err != nil || hiddenOrSystem(info) {
				continue
			}
			path := filepath.Join(dir, name)
			if ent.IsDir() {
				if depth < MaxDepth {
					sub := name
					if rel != "" {
						sub = rel + "/" + name
					}
					walk(path, sub, depth+1)
				}
				continue
			}
			if info.Mode().IsRegular() {
				file(LocalFile{Path: path, RelDir: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()})
			}
		}
	}
	walk(dir, rel, depth)
	return unreadable
}
