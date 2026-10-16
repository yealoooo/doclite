// Command webartifact 是打包前取界面的工具（Taskfile 的 frontend 任务调用，不打包进客户端）：按 WEB_VERSION 找到
// web 的产物包（npm run release 生成的 doclite-web-<版本>.tar.gz），核对包里清单的版本，解到 frontend/dist，清单写到
// frontend/manifest.json，再由 main.go embed。客户端只依赖这个产物包，不读 web 的源码。
//
//	go run ./cmd/webartifact                 产物包：DOCLITE_WEB_ARTIFACT（文件路径），没给就找
//	                                          ../web/release/doclite-web-<WEB_VERSION>.tar.gz
//	go run ./cmd/webartifact -optional       开发构建用：找不到产物包时清掉 frontend/dist、给出提示，不算失败
//	                                          （开发构建可以接 vite 的开发服务器，见 README"桌面客户端"）
//
// 找不到产物包、包坏了、版本对不上时退出码 1，并说明怎么办。以后拆仓库，只要把"找产物包"改成从发布地址下载。
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	artifactEnv = "DOCLITE_WEB_ARTIFACT"
	// 解包的上限：文件数、总大小（产物现在不到 100 个文件、几 MB）。
	maxFiles = 5000
	maxBytes = 512 << 20
)

var versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)

// manifest 是产物包里的 manifest.json（web/scripts/release.mjs 写的）。
type manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func main() {
	versionFile := flag.String("version-file", "WEB_VERSION", "记着要用的 web 版本的文件")
	out := flag.String("out", "frontend", "解到哪个目录（dist/ 和 manifest.json 放在它下面）")
	optional := flag.Bool("optional", false, "找不到产物包时不算失败（开发构建）")
	flag.Parse()

	if err := run(*versionFile, *out, *optional); err != nil {
		fmt.Fprintln(os.Stderr, "取界面失败："+err.Error())
		os.Exit(1)
	}
}

func run(versionFile, out string, optional bool) error {
	raw, err := os.ReadFile(versionFile)
	if err != nil {
		return fmt.Errorf("读不了 %s：%v", versionFile, err)
	}
	want := strings.TrimSpace(string(raw))
	if !versionRE.MatchString(want) {
		return fmt.Errorf("%s 里的版本 %q 不是 semver（例如 0.1.0）", versionFile, want)
	}
	artifact := os.Getenv(artifactEnv)
	if artifact == "" {
		artifact = filepath.Join("..", "web", "release", "doclite-web-"+want+".tar.gz")
	}
	if _, err := os.Stat(artifact); err != nil {
		msg := fmt.Sprintf("找不到 web %s 的产物包 %s。\n"+
			"  - 在 web/ 下运行 npm run release 生成（web/package.json 的版本要是 %s），或者\n"+
			"  - 用环境变量 %s 指定产物包的路径（例如别处构建好的 doclite-web-%s.tar.gz）。", want, artifact, want, artifactEnv, want)
		if optional {
			fmt.Fprintln(os.Stderr, "提示："+msg+"\n开发构建照样编译，但不嵌入界面（接 vite 开发服务器时不需要）。")
			return clearOut(out)
		}
		return errors.New(msg)
	}
	got, files, err := extract(artifact, out)
	if err != nil {
		return fmt.Errorf("产物包 %s 不能用：%v", artifact, err)
	}
	if got.Version != want {
		_ = clearOut(out)
		return fmt.Errorf("产物包 %s 里是 web %s，%s 要的是 %s。换成对的产物包，或者改 %s", artifact, got.Version,
			versionFile, want, versionFile)
	}
	fmt.Printf("界面：web %s（%s，%d 个文件）→ %s\n", got.Version, artifact, files, filepath.Join(out, "dist"))
	return nil
}

// clearOut 清掉解出来的界面（frontend/dist、frontend/manifest.json），留下 .gitkeep。
func clearOut(out string) error {
	if err := os.RemoveAll(filepath.Join(out, "dist")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(out, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// extract 把产物包解到 out 下的临时目录，核对完整（有清单、有 dist/index.html）后换掉 out/dist 和 out/manifest.json。
// 只认普通文件和目录，路径必须在 manifest.json 或 dist/ 下，不能有 ..、绝对路径。
func extract(artifact, out string) (*manifest, int, error) {
	f, err := os.Open(artifact)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, 0, fmt.Errorf("不是 gzip：%v", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, 0, err
	}
	tmp, err := os.MkdirTemp(out, ".webartifact-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(tmp)

	tr := tar.NewReader(gz)
	var m *manifest
	files := 0
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("tar 坏了：%v", err)
		}
		switch h.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, 0, fmt.Errorf("包里有不认识的条目 %s（类型 %c）", h.Name, h.Typeflag)
		}
		name := path.Clean(h.Name)
		if name != h.Name || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." ||
			strings.Contains(name, "\\") || (name != "manifest.json" && !strings.HasPrefix(name, "dist/")) {
			return nil, 0, fmt.Errorf("包里有不该有的路径 %q", h.Name)
		}
		files++
		total += h.Size
		if files > maxFiles || total > maxBytes || h.Size < 0 {
			return nil, 0, errors.New("包太大")
		}
		if name == "manifest.json" {
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, 0, err
			}
			m = &manifest{}
			if err := json.Unmarshal(data, m); err != nil {
				return nil, 0, fmt.Errorf("manifest.json 不是 JSON：%v", err)
			}
			if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), data, 0o644); err != nil {
				return nil, 0, err
			}
			continue
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, 0, err
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, 0, err
		}
		_, err = io.Copy(w, io.LimitReader(tr, h.Size))
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, 0, err
		}
	}
	if m == nil {
		return nil, 0, errors.New("没有 manifest.json")
	}
	if m.Name != "doclite-web" || !versionRE.MatchString(m.Version) {
		return nil, 0, fmt.Errorf("manifest.json 不对（name %q，version %q）", m.Name, m.Version)
	}
	if st, err := os.Stat(filepath.Join(tmp, "dist", "index.html")); err != nil || !st.Mode().IsRegular() {
		return nil, 0, errors.New("没有 dist/index.html")
	}
	if err := clearOut(out); err != nil {
		return nil, 0, err
	}
	if err := os.Rename(filepath.Join(tmp, "dist"), filepath.Join(out, "dist")); err != nil {
		return nil, 0, err
	}
	if err := os.Rename(filepath.Join(tmp, "manifest.json"), filepath.Join(out, "manifest.json")); err != nil {
		return nil, 0, err
	}
	return m, files - 1, nil
}
