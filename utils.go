package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var printMu sync.Mutex

var invalidNameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// EnsureDir 确保目录存在（T1）。
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// WriteLines 将每一行写入文件（T6/T10）。
func WriteLines(path string, lines []string) error {
	data := strings.Join(lines, "\n")
	if data != "" {
		data += "\n"
	}
	return os.WriteFile(path, []byte(data), 0o644)
}

// SanitizeName 清理 Windows 文件系统不允许的字符。
func SanitizeName(s string) string {
	s = invalidNameChars.ReplaceAllString(s, "_")
	s = strings.TrimSpace(s)
	if len(s) > 140 {
		s = s[:140]
	}
	return strings.TrimSpace(s)
}

// DownloadFileName 生成下载到本地的压缩包文件名。
func (p Pack) DownloadFileName() string {
	return SanitizeName(fmt.Sprintf("%s - %s.zip", p.Tag, p.Name))
}

// AbsOrRelJoin 将用户输入的路径解析为相对当前目录的绝对路径。
func AbsOrRelJoin(p string) (string, error) {
	if p == "" {
		p = "."
	}
	return filepath.Abs(filepath.FromSlash(p))
}

// msgf 向控制台打印带步骤前缀的信息。
func msgf(format string, args ...any) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf(format+"\n", args...)
}
