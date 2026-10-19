package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"doclite/client/internal/semver"
)

// ProbeResult 是探测服务器的结果（web/src/lib/shell.ts 的 ProbeResult）。
type ProbeResult struct {
	ServerURL     string  `json:"server_url"`
	Reachable     bool    `json:"reachable"`
	Error         *string `json:"error"`
	ServerVersion *string `json:"server_version"`
	// MinVersion、LatestVersion、DownloadURL 来自 `/api/shell/latest`；server 还没有这个接口（404）时都为 null，
	// 视为不限制版本。
	MinVersion      *string `json:"min_version"`
	LatestVersion   *string `json:"latest_version"`
	DownloadURL     *string `json:"download_url"`
	ClientVersion   string  `json:"client_version"`
	UpgradeRequired bool    `json:"upgrade_required"`
}

const probeTimeout = 8 * time.Second

// Probe 探测一个服务器（server 是规整过的 origin：server 的接口地址，客户端只经它访问 `/api`）：
//   - `GET /api/health` 要返回 200 和 `{status, version}`（不用登录、不检查客户端版本）；
//   - `GET /api/shell/latest` 给最低、最新版本和下载地址（server 还没有这个接口时 404，跳过）；
//   - 再用不带会话的 `GET /api/me` 问一次 server 收不收这个客户端版本：server 的版本检查在所有 `/api`（health、
//     shell 除外）最外层、鉴权之前，版本过低就回 426 `client_upgrade_required`，details 带最低版本和下载地址；
//     收的话是 401。升级页只信这里探测到的结果（不信页面带来的参数）。
func (p *Proxy) Probe(ctx context.Context, server string) *ProbeResult {
	res := &ProbeResult{ServerURL: server, ClientVersion: p.version}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	fail := func(msg string) *ProbeResult {
		res.Error = &msg
		return res
	}
	status, body, err := p.probeGet(ctx, server+"/api/health")
	if err != nil {
		return fail(DescribeNetError(err))
	}
	if status != http.StatusOK {
		return fail(fmt.Sprintf("%s/api/health 返回 HTTP %d，不像是 Doclite 服务器", server, status))
	}
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &health) != nil || health.Status == "" {
		return fail("对方不是 Doclite 服务器（/api/health 的响应不对）")
	}
	if health.Version != "" {
		res.ServerVersion = &health.Version
	}
	res.Reachable = true

	status, body, err = p.probeGet(ctx, server+"/api/shell/latest")
	switch {
	case err != nil:
		p.log.Warn("查客户端版本失败", "server", server, "err", err)
	case status == http.StatusOK:
		var latest struct {
			MinVersion    *string `json:"min_version"`
			LatestVersion *string `json:"latest_version"`
			DownloadURL   *string `json:"download_url"`
		}
		if json.Unmarshal(body, &latest) != nil {
			p.log.Warn("/api/shell/latest 的响应不是 JSON", "server", server)
			break
		}
		res.MinVersion = nonEmpty(latest.MinVersion)
		res.LatestVersion = nonEmpty(latest.LatestVersion)
		res.DownloadURL = nonEmpty(latest.DownloadURL)
	case status == http.StatusNotFound:
		// server 还没有这个接口：不限制版本。
	default:
		p.log.Warn("查客户端版本失败", "server", server, "status", status)
	}
	if res.MinVersion != nil {
		less, err := semver.Less(p.version, *res.MinVersion)
		if err != nil {
			p.log.Warn("比较客户端版本失败", "client", p.version, "min", *res.MinVersion, "err", err)
		}
		res.UpgradeRequired = less
	}
	p.probeVersionCheck(ctx, server, res)
	return res
}

// probeVersionCheck：不带会话 `GET /api/me`，server 回 426 就是要升级，用 426 的 details 补上探测不到的版本和下载地址。
func (p *Proxy) probeVersionCheck(ctx context.Context, server string, res *ProbeResult) {
	status, body, err := p.probeGet(ctx, server+"/api/me")
	if err != nil {
		p.log.Warn("查 server 收不收这个客户端版本失败", "server", server, "err", err)
		return
	}
	if status != http.StatusUpgradeRequired {
		return
	}
	var e struct {
		Code    string `json:"code"`
		Details struct {
			MinVersion    *string `json:"min_version"`
			LatestVersion *string `json:"latest_version"`
			DownloadURL   *string `json:"download_url"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &e) != nil || e.Code != "client_upgrade_required" {
		p.log.Warn("server 回了 426，但响应不是 client_upgrade_required", "server", server)
	}
	res.UpgradeRequired = true
	if res.MinVersion == nil {
		res.MinVersion = nonEmpty(e.Details.MinVersion)
	}
	if res.LatestVersion == nil {
		res.LatestVersion = nonEmpty(e.Details.LatestVersion)
	}
	if res.DownloadURL == nil {
		res.DownloadURL = nonEmpty(e.Details.DownloadURL)
	}
}

func (p *Proxy) probeGet(ctx context.Context, target string) (int, []byte, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	hr.Header.Set("Accept", "application/json")
	p.SetClientHeaders(hr.Header)
	resp, err := p.fetchClient.Do(hr)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}
