// Package appdata 管壳的本机数据目录（requirements 第 6 节"数据目录"）：
//
//	Windows  %LOCALAPPDATA%\Doclite\
//	macOS    ~/Library/Application Support/Doclite/
//
// 里面放 settings.json（服务器地址）、cookies.json（会话 Cookie 罐）、logs/（按天滚动），以后还有上传队列和图片缓存。
// `--smoke` 用其中的 smoke/ 子目录，布局相同，不碰正式的设置、会话和缓存。环境变量 DOCLITE_DATA_DIR 可以换掉
// 整个数据目录（开发、测试用，smoke 仍在它下面的 smoke/）。
package appdata

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// DirName 是数据目录的名字（productName）。
const DirName = "Doclite"

// Dir 返回数据目录（不创建）。
func Dir(smoke bool) (string, error) {
	base := os.Getenv("DOCLITE_DATA_DIR")
	if base == "" {
		var root string
		var err error
		switch runtime.GOOS {
		case "windows":
			// %LOCALAPPDATA%（os.UserConfigDir 在 Windows 上是漫游的 %APPDATA%，不用它）。
			root = os.Getenv("LOCALAPPDATA")
			if root == "" {
				root, err = os.UserCacheDir()
			}
		default:
			root, err = os.UserConfigDir()
		}
		if err != nil {
			return "", err
		}
		if root == "" {
			return "", errors.New("找不到本机数据目录")
		}
		base = filepath.Join(root, DirName)
	}
	if smoke {
		return filepath.Join(base, "smoke"), nil
	}
	return base, nil
}

// Settings 是壳的设置（settings.json）。
type Settings struct {
	// ServerURL 是已配置的服务器（规整后的 origin）；空表示还没配置。
	ServerURL string `json:"server_url"`
}

// LoadSettings 读设置；文件不存在时返回空设置。
func LoadSettings(dir string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// SaveSettings 写设置（先写临时文件再改名）。
func SaveSettings(dir string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, "settings.json"), data, 0o600)
}

// CookiesPath 是会话 Cookie 罐的文件。
func CookiesPath(dir string) string { return filepath.Join(dir, "cookies.json") }

// LogsDir 是日志目录（按天滚动，见 logfile.go）。
func LogsDir(dir string) string { return filepath.Join(dir, "logs") }

// ImageCacheDir 是本机图片缓存的目录（internal/imagecache）。
func ImageCacheDir(dir string) string { return filepath.Join(dir, "image-cache") }

// WriteFileAtomic 先写同目录下的临时文件再改名，避免写到一半断电留下坏文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
