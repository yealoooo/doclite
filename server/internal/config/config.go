// Package config 读环境变量（变量名沿用上一版，每一项的含义见 server/.env.example）。
//
// 读取顺序：先读当前目录下的 `.env`（有的话；不覆盖已有的环境变量），再读环境变量。空字符串当作没设置。
// 含密码、密钥的项（DATABASE_URL、INIT_ADMIN_PASSWORD、MASTER_KEY、S3_SECRET_KEY）不要打到日志里。
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"doclite/server/internal/platform/semver"
)

// 默认值。
const (
	DefaultListenAddr      = "0.0.0.0:9080"
	DefaultSessionTTLHours = 12
	MaxSessionTTLHours     = 24 * 30
	DefaultAdminName       = "系统管理员"
	DefaultMaxUploadBytes  = 512 << 20
	DefaultMaxImagePixels  = 300_000_000

	DefaultJobConcurrency      = 2
	DefaultJobHeartbeatSecs    = 10
	DefaultJobRetryBackoffSecs = 30
	DefaultExportCleanupSecs   = 600
)

// Config 是 server 的全部配置。
type Config struct {
	// DatabaseURL 是 PostgreSQL 连接串（DATABASE_URL，必填）。含密码。
	DatabaseURL string
	// ListenAddr 是 HTTP 监听地址（LISTEN_ADDR）。
	ListenAddr string
	// TmpDir 是临时文件目录（TMP_DIR，上传、导出用）。
	TmpDir string
	// LogLevel 是日志级别（LOG_LEVEL：debug、info、warn、error，默认 info）。
	LogLevel slog.Level
	// SessionTTL 是会话有效期（SESSION_TTL_HOURS，1–720，默认 12），从登录起算，不续期。
	SessionTTL time.Duration
	// CookieSecure：会话 Cookie 是否带 Secure（COOKIE_SECURE，默认 false）。
	CookieSecure bool
	// InitAdmin 是初始管理员（INIT_ADMIN_USERNAME、INIT_ADMIN_PASSWORD、INIT_ADMIN_DISPLAY_NAME）；
	// 只在库里一个用户都没有时使用。两项都没设置时为 nil。含密码。
	InitAdmin *InitAdmin
	// MasterKey 是主密钥（MASTER_KEY，必填），加密存储配置的 secret_key（M1-3）。
	MasterKey MasterKey
	// InitialStorage 是首次启动登记的默认存储（S3_*，M1-3 使用）。都没设置时为 nil。含密钥。
	InitialStorage *InitialStorage
	// ClientVersions 是客户端版本检查（MIN_CLIENT_VERSION、CLIENT_LATEST_VERSION、CLIENT_DOWNLOAD_URL）。
	ClientVersions ClientVersions
	// MaxUploadBytes 是上传原图的大小上限（MAX_UPLOAD_BYTES，默认 512 MiB），超过 413。
	MaxUploadBytes int64
	// MaxImagePixels 是原图像素总数的上限（MAX_IMAGE_PIXELS，默认 3 亿），超过 400 invalid_image。
	MaxImagePixels int64
	// JobConcurrency 是后台任务同时最多执行几个（JOB_CONCURRENCY，0–16，默认 2；0 表示这个进程不执行后台任务，
	// 也不做导出物的到期清理）。
	JobConcurrency int
	// JobHeartbeat 是后台任务的心跳间隔（JOB_HEARTBEAT_SECS，1–300 秒，默认 10）。
	JobHeartbeat time.Duration
	// JobRetryBackoff 是第一次重试前等多久（JOB_RETRY_BACKOFF_SECS，1–3600 秒，默认 30），之后每次 × 4，最多 1 小时。
	JobRetryBackoff time.Duration
	// ExportCleanupInterval 是导出物到期清理的间隔（EXPORT_CLEANUP_INTERVAL_SECS，10–86400 秒，默认 600）。
	ExportCleanupInterval time.Duration
}

// InitAdmin 是初始管理员。
type InitAdmin struct {
	Username    string
	Password    string
	DisplayName string
}

// InitialStorage 是 S3_* 的原文，取值是否合法在登记时（M1-3）检查。
type InitialStorage struct {
	Endpoint       string
	PublicEndpoint string
	Bucket         string
	Region         string
	PathPrefix     string
	AccessKey      string
	SecretKey      string
	ForcePathStyle *bool
	URLExpireHours *int
}

// ClientVersions 是客户端版本的配置；MinVersion 为 nil 表示不限制。
type ClientVersions struct {
	MinVersion    *semver.Version
	LatestVersion *semver.Version
	DownloadURL   string
}

// Load 读 `.env`（当前目录）和环境变量。
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}
	return FromLookup(os.LookupEnv)
}

// FromLookup 按给定的查找函数读配置（测试用）。
func FromLookup(lookup func(string) (string, bool)) (*Config, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	c := &Config{}

	c.DatabaseURL = get("DATABASE_URL")
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL 未设置（参考 server/.env.example）")
	}

	c.ListenAddr = get("LISTEN_ADDR")
	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return nil, fmt.Errorf("LISTEN_ADDR 格式不对，应为 IP:端口，例如 127.0.0.1:9080，实际是 %q", c.ListenAddr)
	}

	c.TmpDir = get("TMP_DIR")
	if c.TmpDir == "" {
		c.TmpDir = filepath.Join(os.TempDir(), "doclite")
	}

	level, err := parseLogLevel(get("LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	c.LogLevel = level

	hours, err := intInRange("SESSION_TTL_HOURS", get("SESSION_TTL_HOURS"), DefaultSessionTTLHours, 1, MaxSessionTTLHours)
	if err != nil {
		return nil, err
	}
	c.SessionTTL = time.Duration(hours) * time.Hour

	if v := get("COOKIE_SECURE"); v != "" {
		b, err := parseBool("COOKIE_SECURE", v)
		if err != nil {
			return nil, err
		}
		c.CookieSecure = b
	}

	username, password := get("INIT_ADMIN_USERNAME"), ""
	if v, ok := lookup("INIT_ADMIN_PASSWORD"); ok && strings.TrimSpace(v) != "" {
		password = v // 密码不去首尾空白，原样使用
	}
	switch {
	case username == "" && password == "":
	case username != "" && password != "":
		name := get("INIT_ADMIN_DISPLAY_NAME")
		if name == "" {
			name = DefaultAdminName
		}
		c.InitAdmin = &InitAdmin{Username: username, Password: password, DisplayName: name}
	default:
		return nil, fmt.Errorf("INIT_ADMIN_USERNAME 和 INIT_ADMIN_PASSWORD 要么都设置，要么都不设置")
	}

	mk := get("MASTER_KEY")
	if mk == "" {
		return nil, fmt.Errorf("MASTER_KEY 未设置。它用来加密存储配置里的 secret_key：用 `%s` 生成一个，"+
			"写进 server/.env 或环境变量（参考 server/.env.example）。设置后不要再改，换了之后已保存的 secret_key 就解不开了",
			MasterKeyHint)
	}
	if c.MasterKey, err = ParseMasterKey(mk); err != nil {
		return nil, err
	}

	if c.InitialStorage, err = initialStorage(get); err != nil {
		return nil, err
	}

	if c.ClientVersions, err = clientVersions(get); err != nil {
		return nil, err
	}

	if c.MaxUploadBytes, err = int64InRange("MAX_UPLOAD_BYTES", get("MAX_UPLOAD_BYTES"), DefaultMaxUploadBytes, 1, 1<<40); err != nil {
		return nil, err
	}
	if c.MaxImagePixels, err = int64InRange("MAX_IMAGE_PIXELS", get("MAX_IMAGE_PIXELS"), DefaultMaxImagePixels, 1, 1<<40); err != nil {
		return nil, err
	}
	if c.JobConcurrency, err = intInRange("JOB_CONCURRENCY", get("JOB_CONCURRENCY"), DefaultJobConcurrency, 0, 16); err != nil {
		return nil, err
	}
	secs := func(key string, def, min, max int) (time.Duration, error) {
		n, err := intInRange(key, get(key), def, min, max)
		return time.Duration(n) * time.Second, err
	}
	if c.JobHeartbeat, err = secs("JOB_HEARTBEAT_SECS", DefaultJobHeartbeatSecs, 1, 300); err != nil {
		return nil, err
	}
	if c.JobRetryBackoff, err = secs("JOB_RETRY_BACKOFF_SECS", DefaultJobRetryBackoffSecs, 1, 3600); err != nil {
		return nil, err
	}
	if c.ExportCleanupInterval, err = secs("EXPORT_CLEANUP_INTERVAL_SECS", DefaultExportCleanupSecs, 10, 86400); err != nil {
		return nil, err
	}
	return c, nil
}

func parseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(v) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL 应为 debug、info、warn、error 之一，实际是 %q", v)
}

func intInRange(key, v string, def, min, max int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 应为 %d 到 %d 之间的整数，实际是 %q", key, min, max, v)
	}
	return n, nil
}

func int64InRange(key, v string, def, min, max int64) (int64, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 应为 %d 到 %d 之间的整数，实际是 %q", key, min, max, v)
	}
	return n, nil
}

// parseBool 接受 true/false、1/0、yes/no、on/off，不区分大小写。
func parseBool(key, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s 应为 true 或 false，实际是 %q", key, v)
}

var (
	s3Required = []string{"S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY"}
	s3Optional = []string{"S3_PUBLIC_ENDPOINT", "S3_REGION", "S3_PATH_PREFIX", "S3_FORCE_PATH_STYLE", "S3_URL_EXPIRE_HOURS"}
)

// initialStorage 读 S3_*：都不设置时为 nil；设置了任何一项就必须设置 S3_REQUIRED 的四项。
func initialStorage(get func(string) string) (*InitialStorage, error) {
	var set, missing []string
	for _, k := range append(append([]string{}, s3Required...), s3Optional...) {
		if get(k) != "" {
			set = append(set, k)
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	for _, k := range s3Required {
		if get(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("设置了 %s 但缺少 %s：首次启动登记默认存储时，%s 要么都设置，要么都不设置",
			strings.Join(set, "、"), strings.Join(missing, "、"), strings.Join(s3Required, "、"))
	}
	s := &InitialStorage{
		Endpoint:       get("S3_ENDPOINT"),
		PublicEndpoint: get("S3_PUBLIC_ENDPOINT"),
		Bucket:         get("S3_BUCKET"),
		Region:         get("S3_REGION"),
		PathPrefix:     get("S3_PATH_PREFIX"),
		AccessKey:      get("S3_ACCESS_KEY"),
		SecretKey:      get("S3_SECRET_KEY"),
	}
	if v := get("S3_FORCE_PATH_STYLE"); v != "" {
		b, err := parseBool("S3_FORCE_PATH_STYLE", v)
		if err != nil {
			return nil, err
		}
		s.ForcePathStyle = &b
	}
	if v := get("S3_URL_EXPIRE_HOURS"); v != "" {
		n, err := intInRange("S3_URL_EXPIRE_HOURS", v, 0, 1, 168)
		if err != nil {
			return nil, err
		}
		s.URLExpireHours = &n
	}
	return s, nil
}

func clientVersions(get func(string) string) (ClientVersions, error) {
	var cv ClientVersions
	parse := func(key string) (*semver.Version, error) {
		v := get(key)
		if v == "" {
			return nil, nil
		}
		ver, err := semver.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("%s 应为 semver 版本号（例如 1.2.0），实际是 %q：%v", key, v, err)
		}
		return &ver, nil
	}
	var err error
	if cv.MinVersion, err = parse("MIN_CLIENT_VERSION"); err != nil {
		return cv, err
	}
	if cv.LatestVersion, err = parse("CLIENT_LATEST_VERSION"); err != nil {
		return cv, err
	}
	if cv.MinVersion != nil && cv.LatestVersion != nil && semver.Compare(*cv.LatestVersion, *cv.MinVersion) < 0 {
		return cv, fmt.Errorf("CLIENT_LATEST_VERSION（%s）不能低于 MIN_CLIENT_VERSION（%s）", cv.LatestVersion, cv.MinVersion)
	}
	cv.DownloadURL = get("CLIENT_DOWNLOAD_URL")
	return cv, nil
}
