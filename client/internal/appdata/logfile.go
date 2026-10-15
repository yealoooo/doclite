package appdata

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	logPrefix = "doclite-"
	logSuffix = ".log"
	// 日志保留的天数（按文件名里的日期算）。
	logKeepDays = 14
)

// LogFileName 是某一天的日志文件名：doclite-2026-10-16.log（按本机时区的日期）。
func LogFileName(t time.Time) string {
	return logPrefix + t.Format("2006-01-02") + logSuffix
}

// ExpiredLogs 从目录里的文件名中挑出该删的旧日志：日期早于 today 往前 keepDays 天的。认不出的文件不动。
func ExpiredLogs(names []string, today time.Time, keepDays int) []string {
	y, m, d := today.Date()
	cutoff := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(keepDays - 1))
	var out []string
	for _, name := range names {
		if !strings.HasPrefix(name, logPrefix) || !strings.HasSuffix(name, logSuffix) {
			continue
		}
		day, err := time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(name, logPrefix), logSuffix))
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// DailyLog 是按天滚动的日志文件（logs/doclite-YYYY-MM-DD.log），换天时自动换文件并删掉 14 天前的。
// 写失败（磁盘满、目录没权限）时丢弃这一条，不影响壳运行。
type DailyLog struct {
	mu   sync.Mutex
	dir  string
	day  string
	file *os.File
	now  func() time.Time
}

// OpenDailyLog 打开日志目录（不存在时创建）。
func OpenDailyLog(dir string) (*DailyLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &DailyLog{dir: dir, now: time.Now}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotateLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// Dir 是日志目录。
func (l *DailyLog) Dir() string { return l.dir }

func (l *DailyLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if LogFileName(l.now()) != l.day {
		_ = l.rotateLocked()
	}
	if l.file == nil {
		return len(p), nil
	}
	if _, err := l.file.Write(p); err != nil {
		return len(p), nil
	}
	return len(p), nil
}

// Close 关掉当前文件。
func (l *DailyLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *DailyLog) rotateLocked() error {
	now := l.now()
	name := LogFileName(now)
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.day = name
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.file = f
	if entries, err := os.ReadDir(l.dir); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		for _, old := range ExpiredLogs(names, now, logKeepDays) {
			_ = os.Remove(filepath.Join(l.dir, old))
		}
	}
	return nil
}
