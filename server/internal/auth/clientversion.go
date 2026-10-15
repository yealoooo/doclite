package auth

import (
	"fmt"
	"net/http"
	"strings"

	"doclite/server/internal/config"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/semver"
)

// 客户端版本检查（requirements 第 6 节"版本"）：桌面客户端的每个请求都带 `X-Client-Version`（semver）。
//   - 没配置 MIN_CLIENT_VERSION：一律放行（请求头写什么都不看）；
//   - 请求没带这个头（浏览器）：放行；
//   - 带了但不是合法的 semver：400 `invalid_client_version`；
//   - 低于最低版本：426 `client_upgrade_required`，details 是
//     `{client_version, min_version, latest_version, download_url}`（后两个没配置时为 null）。
//
// 挂在所有 `/api` 路由的最外层、鉴权之前；`/api/health`、`/api/shell/*` 不检查（客户端要先能查到最新版本）。

const maxVersionHeaderLen = 64

// ClientVersionCheck 返回检查客户端版本的中间件。
func ClientVersionCheck(cv config.ClientVersions) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		if cv.MinVersion == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if versionChecked(r.URL.Path) {
				if err := checkClientVersion(cv, r.Header.Values("X-Client-Version")); err != nil {
					httpx.WriteError(w, r, err)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// versionChecked：`/api` 下除了 `/api/health`、`/api/shell`、`/api/shell/*` 都检查。
func versionChecked(path string) bool {
	if path != "/api" && !strings.HasPrefix(path, "/api/") {
		return false
	}
	return path != "/api/health" && path != "/api/shell" && !strings.HasPrefix(path, "/api/shell/")
}

type upgradeDetails struct {
	ClientVersion string  `json:"client_version"`
	MinVersion    string  `json:"min_version"`
	LatestVersion *string `json:"latest_version"`
	DownloadURL   *string `json:"download_url"`
}

func checkClientVersion(cv config.ClientVersions, values []string) error {
	if len(values) == 0 {
		return nil
	}
	raw := strings.TrimSpace(values[0])
	invalid := func() error {
		shown := fmt.Sprintf("%q", raw)
		if len(raw) > maxVersionHeaderLen {
			shown = "（太长，不显示）"
		}
		return httpx.NewError(http.StatusBadRequest, httpx.CodeInvalidClientVersion,
			"请求头 X-Client-Version 应为 semver 版本号（例如 1.2.0），实际是 "+shown)
	}
	if len(raw) > maxVersionHeaderLen {
		return invalid()
	}
	v, err := semver.Parse(raw)
	if err != nil {
		return invalid()
	}
	if semver.Compare(v, *cv.MinVersion) >= 0 {
		return nil
	}
	d := upgradeDetails{ClientVersion: v.String(), MinVersion: cv.MinVersion.String()}
	if cv.LatestVersion != nil {
		s := cv.LatestVersion.String()
		d.LatestVersion = &s
	}
	if cv.DownloadURL != "" {
		s := cv.DownloadURL
		d.DownloadURL = &s
	}
	return httpx.NewError(http.StatusUpgradeRequired, httpx.CodeUpgradeRequired,
		fmt.Sprintf("客户端版本 %s 低于服务器要求的最低版本 %s，请更新客户端", d.ClientVersion, d.MinVersion)).
		WithDetails(d)
}
