package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ResolveDownloadDir 返回下载目标目录：所有文件直接混存到下载根目录。
func ResolveDownloadDir() (string, error) {
	abs, err := AbsOrRelJoin(DownloadRoot)
	if err != nil {
		return "", err
	}
	if err := EnsureDir(abs); err != nil {
		return "", fmt.Errorf("创建下载目录失败: %w", err)
	}
	return abs, nil
}

// CheckAria2 定位 aria2c：优先 ToolsDir，其次系统 PATH。
func CheckAria2() (string, error) {
	candidates := []string{
		filepath.Join(ToolsDir, "aria2c.exe"),
		filepath.Join(ToolsDir, "aria2c"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs, nil
		}
	}
	if p, err := exec.LookPath("aria2c"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("在 %s 和系统 PATH 中均未找到 aria2c", ToolsDir)
}

type aria2Item struct {
	URL  string
	Pack Pack
}

// ExecuteDownload T9：调用 aria2 批量下载，返回失败的曲包（含重试一次直链原始地址）。
func ExecuteDownload(ctx context.Context, aria2Path, targetDir, cookie string, items []aria2Item) []aria2Item {
	failed := runAria2Pass(ctx, aria2Path, targetDir, cookie, items)
	if len(failed) == 0 {
		return nil
	}

	// 第一次失败：若直链构造与服务器文件名不符，尝试从原始页面（format=raw）读取
	// 官方存储的下载地址（需要 Cookie），只对失败项做一次重试。
	var fallbackItems []aria2Item
	for _, it := range failed {
		href, ok := fetchRawDownloadURL(ctx, it.Pack, cookie)
		if ok && href != "" {
			fallbackItems = append(fallbackItems, aria2Item{URL: href, Pack: it.Pack})
		}
	}
	if len(fallbackItems) > 0 {
		msgf("      对 %d 个失败曲包使用官方存储地址重试...", len(fallbackItems))
		stillFailed := runAria2Pass(ctx, aria2Path, targetDir, cookie, fallbackItems)
		retried := map[string]bool{}
		for _, it := range fallbackItems {
			retried[it.Pack.Tag] = true
		}
		still := map[string]bool{}
		for _, it := range stillFailed {
			still[it.Pack.Tag] = true
		}
		var final []aria2Item
		for _, it := range failed {
			// 已用官方地址重试且成功 -> 不算失败；其余保留。
			if retried[it.Pack.Tag] && !still[it.Pack.Tag] {
				continue
			}
			final = append(final, it)
		}
		return final
	}
	return failed
}

// writeAria2Input 生成 aria2 输入文件：每条为 URL + 缩进的 out= 文件名。
func writeAria2Input(path string, items []aria2Item) error {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(it.URL)
		sb.WriteString("\n")
		sb.WriteString("  out=")
		sb.WriteString(it.Pack.DownloadFileName())
		sb.WriteString("\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func runAria2Pass(ctx context.Context, aria2Path, targetDir, cookie string, items []aria2Item) []aria2Item {
	if len(items) == 0 {
		return nil
	}
	inputPath := filepath.Join(UrlOutputDir, "aria2-input.txt")
	if err := writeAria2Input(inputPath, items); err != nil {
		msgf("      写入 aria2 输入文件失败: %v", err)
		return items
	}

	args := []string{
		"--input-file=" + inputPath,
		"--dir=" + targetDir,
		"--max-concurrent-downloads=8",
		"--split=4",
		"--max-connection-per-server=4",
		"--continue=true",
		"--auto-file-renaming=false",
		"--allow-overwrite=false",
		"--user-agent=" + userAgent,
		"--summary-interval=10",
		"--console-log-level=warn",
	}
	if cookie != "" {
		args = append(args, "--header=Cookie: osu_sid="+cookie)
	}

	msgf("      启动 aria2: %d 个任务 -> %s", len(items), targetDir)
	cmd := exec.CommandContext(ctx, aria2Path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == nil {
		// aria2 对失败任务返回非零；具体失败项通过文件校验确定。
		msgf("      aria2 退出码异常: %v", err)
	}
	if len(out) > 0 {
		msgf("      aria2 输出摘要:\n%s", strings.TrimSpace(string(out)))
	}

	var failed []aria2Item
	for _, it := range items {
		p := filepath.Join(targetDir, it.Pack.DownloadFileName())
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			failed = append(failed, it)
		}
	}
	msgf("      本批完成 %d/%d，失败 %d", len(items)-len(failed), len(items), len(failed))
	return failed
}

var (
	downloadLinkClassFirst = regexp.MustCompile(`class="beatmap-pack-download__link"[^>]*href="([^"]+)"`)
	downloadLinkHrefFirst  = regexp.MustCompile(`href="([^"]+)"[^>]*class="beatmap-pack-download__link"`)
)

// fetchRawDownloadURL 带 Cookie 抓取 ?format=raw 页面并解析官方下载链接。
func fetchRawDownloadURL(ctx context.Context, p Pack, cookie string) (string, bool) {
	if cookie == "" {
		return "", false
	}
	rawURL := strings.TrimRight(p.PageURL, "/") + "?format=raw"
	body, status, err := HTTPGetWithCookie(ctx, rawURL, cookie)
	if err != nil || status != 200 {
		return "", false
	}
	var href string
	if m := downloadLinkClassFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	} else if m := downloadLinkHrefFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	}
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	} else if strings.HasPrefix(href, "/") {
		href = "https://osu.ppy.sh" + href
	}
	return href, true
}

// SaveFailedLog T10：失败链接写入 failed.txt；写入失败仅警告。
func SaveFailedLog(failed []aria2Item, scrapeFailedReason string) {
	var lines []string
	for _, it := range failed {
		lines = append(lines, it.Pack.DirectURL)
	}
	if scrapeFailedReason != "" {
		lines = append(lines, "# 抓取失败: "+scrapeFailedReason)
	}
	if len(lines) == 0 {
		return
	}
	if err := WriteLines(filepath.Join(UrlOutputDir, "failed.txt"), lines); err != nil {
		msgf("[WARN] 写入 failed.txt 失败: %v（不阻塞）", err)
		return
	}
	msgf("[T10] 已将 %d 条失败记录写入 URL/failed.txt", len(lines))
}

// CountExpectedFiles 目标目录中实际下载完成的 zip 数量。
func CountExpectedFiles(dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, "*.zip"))
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Size() > 0 {
			n++
		}
	}
	return n
}
