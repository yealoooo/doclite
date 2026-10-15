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

// Probe 探测一个服务器（server 是规整过的 origin）：`GET /api/health` 要返回 200 和 `{status, version}`，
// 再查 `GET /api/shell/latest`，客户端版本低于 min_version 时 upgrade_required。两个接口都不用登录、不检查客户端版本。
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
		return fail(describeNetError(err))
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
	res.Reachable = true
	if health.Version != "" {
		res.ServerVersion = &health.Version
	}

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
	return res
}

func (p *Proxy) probeGet(ctx context.Context, target string) (int, []byte, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("X-Client-Type", "desktop")
	hr.Header.Set("X-Client-Version", p.version)
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
