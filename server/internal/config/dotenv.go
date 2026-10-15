package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// loadDotEnv 读 path（不存在就跳过），把里面的变量设进环境变量；已经有的环境变量不覆盖。
//
// 格式：`KEY=VALUE`，一行一个；`#` 开头的行和空行忽略；可以有 `export ` 前缀；
// VALUE 两边的单引号或双引号去掉（不处理转义和行尾注释）。
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读 %s 失败：%w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%s 第 %d 行格式不对，应为 KEY=VALUE", path, lineNo)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}
