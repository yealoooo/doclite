package appdata

import (
	"reflect"
	"testing"
	"time"
)

func TestExpiredLogs(t *testing.T) {
	today := time.Date(2026, 10, 16, 23, 30, 0, 0, time.Local)
	if got := LogFileName(today); got != "doclite-2026-10-16.log" {
		t.Errorf("LogFileName = %q", got)
	}
	names := []string{
		"doclite-2026-10-16.log",
		"doclite-2026-10-03.log", // 第 14 天，保留
		"doclite-2026-10-02.log", // 第 15 天，删
		"doclite-2025-12-31.log",
		"doclite-bad.log",
		"other.txt",
		"doclite-2026-10-01.log.gz",
	}
	got := ExpiredLogs(names, today, 14)
	want := []string{"doclite-2025-12-31.log", "doclite-2026-10-02.log"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExpiredLogs = %v，应为 %v", got, want)
	}
}
