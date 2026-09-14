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
	"sync/atomic"
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

const (
	// defaultLookupConcurrency 失败曲包链接重查的默认并发数。
	defaultLookupConcurrency = 4
	// maxLookupConcurrency 并发重查的硬上限，避免对 osu.ppy.sh 造成突发压力。
	maxLookupConcurrency = 8
)

// LookupConcurrency 失败曲包链接重查的并发数，可由 -lookup-concurrency 调整（取值 1~maxLookupConcurrency）。
var LookupConcurrency = defaultLookupConcurrency

// ClampLookupConcurrency 把 -lookup-concurrency 的输入收敛到 1~maxLookupConcurrency；
// 越界时返回边界值，并提示实际使用的并发数。
func ClampLookupConcurrency(n int) int {
	if n < 1 {
		msgf("提示: -lookup-concurrency=%d 超出范围(1~%d)，改用 %d。", n, maxLookupConcurrency, 1)
		return 1
	}
	if n > maxLookupConcurrency {
		msgf("提示: -lookup-concurrency=%d 超出范围(1~%d)，改用 %d。", n, maxLookupConcurrency, maxLookupConcurrency)
		return maxLookupConcurrency
	}
	return n
}

// packLookupFunc 查询单个曲包的官方存储地址，返回 (地址, 是否找到, 是否需要登录, 网络错误)。
type packLookupFunc func(ctx context.Context, p Pack, cookie string) (string, bool, bool, error)

// packLookupOutcome 单个曲包的重查结果。
type packLookupOutcome struct {
	href          string
	found         bool
	requiresLogin bool
	netErr        error
}

// maxCookieAttempts 恢复流程中向用户索要 Cookie 的最大次数（超过即停止重试）。
const maxCookieAttempts = 3

// packRecoveryDeps 失败曲包恢复循环的外部依赖，便于单元测试注入假实现。
//   - lookup：查询单个曲包的官方存储地址；
//   - download：对给定批次执行一次 aria2 下载，返回仍失败的曲包；
//   - promptCookie：向用户索要 Cookie，返回 (Cookie, 是否跳过)；
//   - concurrency：重查官方地址时的并发数。
type packRecoveryDeps struct {
	lookup       packLookupFunc
	download     func(ctx context.Context, cookie string, items []aria2Item) []aria2Item
	promptCookie func() (string, bool)
	concurrency  int
}

// lookupPackLinks 并发重查失败曲包的官方存储地址，结果按输入顺序一一对应返回。
// 固定数量的 worker 领取任务，保证在途查询数不超过 concurrency；
// 任一查询出现网络层错误时立即取消后续任务（已在途的查询允许自然返回），避免用户长时间干等。
func lookupPackLinks(ctx context.Context, packs []Pack, cookie string, concurrency int, lookup packLookupFunc) []packLookupOutcome {
	results := make([]packLookupOutcome, len(packs))
	if len(packs) == 0 {
		return results
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(packs) {
		concurrency = len(packs)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		next      int64 // 下一个待领取的任务下标
		completed int64 // 已完成数量，用于进度序号
		workers   sync.WaitGroup
		total     = len(packs)
	)
	for w := 0; w < concurrency; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if runCtx.Err() != nil {
					return
				}
				idx := int(atomic.AddInt64(&next, 1)) - 1
				if idx >= total || runCtx.Err() != nil {
					return
				}
				href, found, requiresLogin, err := lookup(runCtx, packs[idx], cookie)
				results[idx] = packLookupOutcome{href: href, found: found, requiresLogin: requiresLogin, netErr: err}
				seq := atomic.AddInt64(&completed, 1)
				// msgf 内部有互斥锁，保证整行输出不会与其他并发输出交错。
				msgf("      查询官方存储地址 (%d/%d): %s", seq, total, packs[idx].Tag)
				if err != nil && isNetworkFailure(err) {
					// 网络层错误：不再发起新查询，已在途的请求允许返回。
					cancel()
					return
				}
			}
		}()
	}
	workers.Wait()
	return results
}

// recoverFailedPacks 多轮重查失败曲包的官方存储地址并用新地址重下，直到失败列表为空、
// 某一轮毫无进展或遇到 Cookie/网络等终止条件为止；返回最终仍失败的曲包（保持输入顺序）。
//
// 推进条件（见 openspec/changes/continue-failed-pack-retry）：只有本轮有曲包被成功下载才进入下一轮，
// 成功项永久移出失败集合，因此进度严格单调、循环必然收敛；本轮未取得官方地址的曲包保留在
// 失败列表并进入下一轮重查，不作为终态。
func recoverFailedPacks(ctx context.Context, failed []aria2Item, cookie string, deps packRecoveryDeps) []aria2Item {
	if len(failed) == 0 {
		return nil
	}
	if deps.concurrency < 1 {
		deps.concurrency = 1
	}
	if deps.promptCookie == nil {
		deps.promptCookie = ManualCookieInput
	}

	// 已有 Cookie（例如抓取阶段用户粘贴过）算作第 1 次尝试，保证总尝试次数不超过 3 次。
	cookieAttempts := 0
	if cookie != "" {
		cookieAttempts = 1
	}
	for round := 1; len(failed) > 0; round++ {
		if ctx.Err() != nil {
			break
		}
		if cookie == "" {
			cookieAttempts++
			if cookieAttempts > maxCookieAttempts {
				msgf("      已连续 %d 次输入的 Cookie 未生效，停止重试剩余曲包。", maxCookieAttempts)
				break
			}
			val, skip := deps.promptCookie()
			if skip {
				break
			}
			cookie = val
		}

		// 每轮都把当前仍失败的全部曲包交给重查：本轮未取得地址的曲包也要在下一轮继续尝试。
		packs := make([]Pack, 0, len(failed))
		for _, it := range failed {
			packs = append(packs, it.Pack)
		}
		msgf("      第 %d 轮：剩余 %d 个失败曲包，开始重查官方存储地址（并发 %d）...", round, len(packs), deps.concurrency)
		outcomes := lookupPackLinks(ctx, packs, cookie, deps.concurrency, deps.lookup)

		badCookie := false
		netBroken := false
		var fallbackItems []aria2Item
		for i, out := range outcomes {
			if out.netErr != nil {
				netBroken = true
				continue
			}
			if out.requiresLogin {
				badCookie = true
				continue
			}
			if out.found && out.href != "" {
				fallbackItems = append(fallbackItems, aria2Item{URL: out.href, Pack: packs[i]})
			}
		}

		if netBroken {
			msgf("      查询官方存储地址失败: 无法连接 osu.ppy.sh（网络问题，重试 Cookie 无效）")
			printNetworkHelp()
			break
		}
		if len(fallbackItems) == 0 {
			if badCookie {
				cookie = deps.rejectCookie(cookieAttempts)
				continue
			}
			msgf("      Cookie 已生效，但官网未提供这些曲包的下载地址（可能已下架），本轮无进展。")
			break
		}

		msgf("      对 %d 个失败曲包使用官方存储地址重试...", len(fallbackItems))
		stillFailed := deps.download(ctx, cookie, fallbackItems)
		progressed := len(fallbackItems) - len(stillFailed)

		retried := map[string]bool{}
		for _, it := range fallbackItems {
			retried[it.Pack.Tag] = true
		}
		still := map[string]bool{}
		for _, it := range stillFailed {
			still[it.Pack.Tag] = true
		}
		remaining := make([]aria2Item, 0, len(failed))
		for _, it := range failed {
			// 已用官方地址重试且成功 -> 永久移出失败集合；其余保留。
			if retried[it.Pack.Tag] && !still[it.Pack.Tag] {
				continue
			}
			remaining = append(remaining, it)
		}
		failed = remaining
		msgf("      第 %d 轮：重下 %d 个，成功 %d 个，剩余失败 %d 个", round, len(fallbackItems), progressed, len(failed))

		if progressed == 0 {
			msgf("      本轮无进展，停止重试。")
			break
		}
		if badCookie {
			// 仍有曲包停在登录墙：清空 Cookie，下一轮重新向用户索要。
			cookie = deps.rejectCookie(cookieAttempts)
		}
	}
	return failed
}

// rejectCookie 在判定 Cookie 未生效时提示用户重新粘贴，并清空 Cookie 供下一轮重新索要。
func (deps packRecoveryDeps) rejectCookie(cookieAttempts int) string {
	msgf("      Cookie 未生效：页面仍提示需要登录。请确认复制的是 osu_session 的 Value 或整段 Cookie（第 %d/%d 次）。", cookieAttempts, maxCookieAttempts)
	return ""
}

// ExecuteDownload：调用 aria2 批量下载，返回最终失败的曲包。
// 直链失败时尝试用 Cookie 读取官方存储地址重试，多轮进行直到剩余曲包不再减少。
func ExecuteDownload(ctx context.Context, aria2Path, targetDir, cookie string, items []aria2Item) []aria2Item {
	failed := runAria2Pass(ctx, aria2Path, targetDir, cookie, items)
	if len(failed) == 0 {
		return nil
	}

	msgf("      有 %d 个曲包直链下载失败，尝试获取官方存储地址重试（并发 %d，多轮直到无进展）...", len(failed), LookupConcurrency)
	failed = recoverFailedPacks(ctx, failed, cookie, packRecoveryDeps{
		lookup: fetchRawDownloadURL,
		download: func(ctx context.Context, cookie string, batch []aria2Item) []aria2Item {
			return runAria2Pass(ctx, aria2Path, targetDir, cookie, batch)
		},
		promptCookie: ManualCookieInput,
		concurrency:  LookupConcurrency,
	})
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

	// 进度数据来自 aria2 自己的摘要行（见 aria2progress.go 的说明）：
	// 不启用 RPC，避免 aria2 在下载完成后不退出；拿不到摘要时退化为按任务数量统计。
	if cookie != "" {
		args = append(args, "--header=Cookie: "+cookie)
	}

	// 进度显示模式：-progress bar 在输出被重定向时自动降级为整行文本。
	mode := ResolveProgressMode(ProgressMode, isStdoutTerminal())
	if mode == progressBarMode {
		enableProgressBar()
	}
	summary := &summaryProgress{}

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
	go streamAria2Output(stdout, summary, &streamWg)
	go streamAria2Output(stderr, summary, &streamWg)

	done := make(chan struct{})
	go func() {
		streamWg.Wait()
		close(done)
	}()

	// 下载期间持续输出总体进度：bar 模式原地刷新一行，其余模式输出整行文本。
	progressStop := make(chan struct{})
	var progressWg sync.WaitGroup
	progressWg.Add(1)
	go func() {
		defer progressWg.Done()
		reportBatchProgress(ctx, targetDir, items, mode, summary, progressStop)
	}()

	waitErr := cmd.Wait()
	close(progressStop)
	progressWg.Wait()
	if mode == progressBarMode {
		disableProgressBar()
	}
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

// streamAria2Output 实时转发 aria2 输出中与结果相关的行。
// [#gid ...] 摘要行不直接打印，只交给进度聚合，避免每秒多行刷屏。
func streamAria2Output(r io.Reader, summary *summaryProgress, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// aria2 用 \r 原地刷新进度，必须按 \r 与 \n 一起切分，否则多段输出会被拼成一行。
	scanner.Split(splitAria2Lines)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if summary != nil && summary.note(line) {
			continue
		}
		if aria2LineVisible(line) {
			// aria2 会给输出加 ANSI 颜色，转发前去掉，避免污染重定向后的日志文件。
			msgf("    aria2 | %s", stripAnsiEscapes(line))
		}
	}
}

// ansiEscapeRe 匹配 ANSI 转义序列（颜色/光标控制等）。
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripAnsiEscapes 去掉字符串中的 ANSI 转义序列。
func stripAnsiEscapes(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// splitAria2Lines 以 \r 或 \n 作为行分隔符切分 aria2 输出。
func splitAria2Lines(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// aria2LineVisible 只转发完成/失败/错误等关键行，避免刷屏。
// 进度摘要行（[#gid ...]）由进度条/进度行统一呈现，不再逐行转发。
func aria2LineVisible(line string) bool {
	if line == "" {
		return false
	}
	l := strings.ToLower(line)
	return strings.Contains(l, "download complete") ||
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

// reportBatchProgress 周期性输出批次进度。
// bar 模式每秒原地刷新一行进度条；line 模式每 3 秒输出一行完整文本；off 模式不输出周期进度。
// 说明：aria2 会预分配完整文件，进行中任务的字节数来自 RPC 或摘要行，已完成部分才按磁盘体积统计。
var (
	// 进度采样间隔：bar 模式原地刷新，line 模式输出整块文本；集成测试会调小以缩短运行时间。
	progressBarTickInterval  = time.Second
	progressLineTickInterval = 3 * time.Second
)

func reportBatchProgress(ctx context.Context, targetDir string, items []aria2Item, mode progressDisplayMode, summary *summaryProgress, stop <-chan struct{}) {
	if mode == progressOffMode {
		return
	}
	interval := progressBarTickInterval
	if mode == progressLineMode {
		interval = progressLineTickInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	emit := func() bool {
		st := statBatch(targetDir, items)
		snap := collectProgress(ctx, targetDir, items, st, summary)
		if mode == progressBarMode {
			drawProgressBlock(FormatProgressBlock(snap, progressBarWidth, true))
		} else {
			msgLines(FormatProgressBlock(snap, progressBarWidth, false))
		}
		return st.done >= len(items) && st.active == 0
	}

	if emit() {
		return
	}
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if emit() {
				return
			}
		}
	}
}

var (
	downloadLinkClassFirst = regexp.MustCompile(`class="beatmap-pack-download__link"[^>]*href="([^"]+)"`)
	downloadLinkHrefFirst  = regexp.MustCompile(`href="([^"]+)"[^>]*class="beatmap-pack-download__link"`)
)

// fetchRawDownloadURL 带 Cookie 抓取 ?format=raw 页面并解析官方下载链接。
// 返回 (官方地址, 是否找到, 页面是否仍提示需要登录, 网络错误)。
func fetchRawDownloadURL(ctx context.Context, p Pack, cookie string) (string, bool, bool, error) {
	if cookie == "" {
		return "", false, false, nil
	}
	rawURL := strings.TrimRight(p.PageURL, "/") + "?format=raw"
	body, status, err := HTTPGetWithCookie(ctx, rawURL, cookie)
	if err != nil {
		return "", false, false, err
	}
	if status != 200 {
		return "", false, false, nil
	}
	lower := strings.ToLower(string(body))
	// 未登录时的提示可能是英文或中文（取决于 Accept-Language）。
	if strings.Contains(lower, "js-user-link") &&
		(strings.Contains(lower, "signed in") || strings.Contains(lower, "登录")) {
		return "", false, true, nil
	}
	var href string
	if m := downloadLinkClassFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	} else if m := downloadLinkHrefFirst.FindSubmatch(body); m != nil {
		href = string(m[1])
	}
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false, false, nil
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	} else if strings.HasPrefix(href, "/") {
		href = "https://osu.ppy.sh" + href
	}
	return href, true, false, nil
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
