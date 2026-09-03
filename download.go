package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
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

// ExecuteDownload：调用 aria2 批量下载，返回最终失败的曲包。
// 直链失败时尝试用 Cookie 读取官方存储地址重试；Cookie 未生效会提示并允许重新粘贴。
func ExecuteDownload(ctx context.Context, aria2Path, targetDir, cookie string, items []aria2Item) []aria2Item {
	failed := runAria2Pass(ctx, aria2Path, targetDir, cookie, items)
	if len(failed) == 0 {
		return nil
	}

	msgf("      有 %d 个曲包直链下载失败，尝试获取官方存储地址重试...", len(failed))
	for attempt := 1; attempt <= 3 && len(failed) > 0; attempt++ {
		if cookie == "" {
			val, skip := ManualCookieInput()
			if skip {
				break
			}
			cookie = val
		}

		badCookie := false
		var fallbackItems []aria2Item
		for _, it := range failed {
			href, ok, requiresLogin := fetchRawDownloadURL(ctx, it.Pack, cookie)
			if requiresLogin {
				badCookie = true
				continue
			}
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
			var next []aria2Item
			for _, it := range failed {
				// 已用官方地址重试且成功 -> 不算失败；其余保留。
				if retried[it.Pack.Tag] && !still[it.Pack.Tag] {
					continue
				}
				next = append(next, it)
			}
			failed = next
			break
		}

		if badCookie {
			msgf("      Cookie 未生效：页面仍提示需要登录。请确认复制的是 osu_session 的 Value 或整段 Cookie（第 %d/3 次）。", attempt)
			cookie = ""
			continue
		}
		msgf("      Cookie 已生效，但官网未提供这些曲包的下载地址（可能已下架）。")
		break
	}
	if len(failed) > 0 {
		msgf("      最终失败 %d 个曲包，已记录到 failed.txt。", len(failed))
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

	// 并发策略：同时最多下载 8 个文件；批内文件少于 8 个时单文件 16 片并发，
	// 达到 8 个及以上时单文件降为 10 片，避免连接数过多。
	maxConcurrent := 8
	split := 10
	if len(items) < 8 {
		split = 16
	}

	args := []string{
		"--input-file=" + inputPath,
		"--dir=" + targetDir,
		fmt.Sprintf("--max-concurrent-downloads=%d", maxConcurrent),
		fmt.Sprintf("--split=%d", split),
		fmt.Sprintf("--max-connection-per-server=%d", split),
		"--continue=true",
		"--auto-file-renaming=false",
		"--allow-overwrite=false",
		"--user-agent=" + userAgent,
		"--summary-interval=1",
		"--console-log-level=notice",
	}
	if cookie != "" {
		args = append(args, "--header=Cookie: "+cookie)
	}

	msgf("      启动 aria2: %d 个任务 -> %s", len(items), targetDir)
	cmd := exec.CommandContext(ctx, aria2Path, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return items
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return items
	}
	if err := cmd.Start(); err != nil {
		msgf("      aria2 启动失败: %v", err)
		return items
	}

	var streamWg sync.WaitGroup
	streamWg.Add(2)
	go streamAria2Output(stdout, &streamWg)
	go streamAria2Output(stderr, &streamWg)

	done := make(chan struct{})
	go func() {
		streamWg.Wait()
		close(done)
	}()

	// 每 3 秒打印一次总体进度（统计已完成/进行中数量与下载字节数）。
	progressStop := make(chan struct{})
	go reportBatchProgress(ctx, targetDir, items, progressStop)

	waitErr := cmd.Wait()
	close(progressStop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	if waitErr != nil && ctx.Err() == nil {
		// aria2 对失败任务返回非零；具体失败项通过文件校验确定。
		msgf("      aria2 退出码异常: %v", waitErr)
	}

	var failed []aria2Item
	for _, it := range items {
		p := filepath.Join(targetDir, it.Pack.DownloadFileName())
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 || controlFileExists(p) {
			failed = append(failed, it)
		}
	}
	if len(failed) > 0 {
		tags := make([]string, 0, len(failed))
		for _, it := range failed {
			tags = append(tags, it.Pack.Tag)
		}
		msgf("      失败曲包: %s", strings.Join(tags, ", "))
	}
	msgf("      本批完成 %d/%d，失败 %d", len(items)-len(failed), len(items), len(failed))
	return failed
}

// controlFileExists 判断 aria2 是否仍持有该文件的 .aria2 控制文件（未完成/可续传）。
func controlFileExists(p string) bool {
	_, err := os.Stat(p + ".aria2")
	return err == nil
}

// streamAria2Output 实时转发 aria2 输出中与进度/结果相关的行。
func streamAria2Output(r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if aria2LineVisible(line) {
			msgf("    aria2 | %s", line)
		}
	}
}

// aria2LineVisible 只转发完成/失败/错误等关键行，避免刷屏。
func aria2LineVisible(line string) bool {
	if line == "" {
		return false
	}
	l := strings.ToLower(line)
	return strings.HasPrefix(line, "[#") ||
		strings.Contains(l, "download complete") ||
		strings.Contains(l, "download completed") ||
		strings.Contains(l, "download aborted") ||
		strings.Contains(l, "error") ||
		strings.Contains(l, "failed") ||
		strings.Contains(l, "warning")
}

type batchStat struct {
	done, active int
}

// statBatch 统计本批任务中已完成与正在下载的数量及已下载字节。
// aria2 下载中会为每个输出文件生成 <文件名>.aria2 控制文件，完成后删除。
func statBatch(targetDir string, items []aria2Item) batchStat {
	var st batchStat
	for _, it := range items {
		p := filepath.Join(targetDir, it.Pack.DownloadFileName())
		control := p + ".aria2"
		fi, err := os.Stat(p)
		switch {
		case err == nil:
			if _, cerr := os.Stat(control); cerr == nil {
				st.active++
			} else if fi.Size() > 0 {
				st.done++
			}
		default:
			if _, cerr := os.Stat(control); cerr == nil {
				st.active++
			}
		}
	}
	return st
}

// reportBatchProgress 周期打印 aria2 批处理总体进度。
// 说明：aria2 会预分配完整文件，因此不按文件字节数估算，只统计完成/进行中数量。
func reportBatchProgress(ctx context.Context, targetDir string, items []aria2Item, stop <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			st := statBatch(targetDir, items)
			if st.done >= len(items) && st.active == 0 {
				return
			}
			msgf("进度: 已完成 %d/%d，下载中 %d", st.done, len(items), st.active)
		}
	}
}

var (
	downloadLinkClassFirst = regexp.MustCompile(`class="beatmap-pack-download__link"[^>]*href="([^"]+)"`)
	downloadLinkHrefFirst  = regexp.MustCompile(`href="([^"]+)"[^>]*class="beatmap-pack-download__link"`)
)

// fetchRawDownloadURL 带 Cookie 抓取 ?format=raw 页面并解析官方下载链接。
// 返回 (官方地址, 是否找到, 页面是否仍提示需要登录)。
func fetchRawDownloadURL(ctx context.Context, p Pack, cookie string) (string, bool, bool) {
	if cookie == "" {
		return "", false, false
	}
	rawURL := strings.TrimRight(p.PageURL, "/") + "?format=raw"
	body, status, err := HTTPGetWithCookie(ctx, rawURL, cookie)
	if err != nil || status != 200 {
		return "", false, false
	}
	lower := strings.ToLower(string(body))
	// 未登录时的提示可能是英文或中文（取决于 Accept-Language）。
	if strings.Contains(lower, "js-user-link") &&
		(strings.Contains(lower, "signed in") || strings.Contains(lower, "登录")) {
		return "", false, true
	}
	var href string
	if m := downloadLinkClassFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	} else if m := downloadLinkHrefFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	}
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false, false
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	} else if strings.HasPrefix(href, "/") {
		href = "https://osu.ppy.sh" + href
	}
	return href, true, false
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
		msgf("注意: 写入 failed.txt 失败: %v（不阻塞）", err)
		return
	}
	msgf("已将 %d 条失败记录写入 URL/failed.txt", len(lines))
}

// CountExpectedFiles 目标目录中实际下载完成的 zip 数量。
func CountExpectedFiles(dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, "*.zip"))
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range matches {
		if controlFileExists(m) {
			continue
		}
		if fi, err := os.Stat(m); err == nil && fi.Size() > 0 {
			n++
		}
	}
	return n
}
