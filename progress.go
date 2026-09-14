package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 进度显示模式（-progress 参数）。
type progressDisplayMode int

const (
	progressBarMode  progressDisplayMode = iota // 单行/多行原地刷新的进度块
	progressLineMode                            // 周期性整行文本（非交互输出的默认降级）
	progressOffMode                             // 不输出周期性进度
)

const (
	// progressBarWidth 整体进度条本体的字符数；配合后面的字段保证整行不超过 79 字符（含速度与 ETA）。
	progressBarWidth = 22
	// progressMaxLineWidth 原地刷新时每行最大字符数，避免在窄终端换行（bar 模式全部为 ASCII，字符数即列数）。
	progressMaxLineWidth = 79
	// progressTaskBarWidth 每个曲包行里的进度条本体字符数。
	progressTaskBarWidth = 8
	// progressMaxTaskLines 进度块最多列出的曲包行数（等于 aria2 的并发上限）。
	progressMaxTaskLines = 8
)

// ProgressMode 当前下载进度显示模式，由 -progress 决定，默认进度块。
var ProgressMode = progressBarMode

// ParseProgressMode 解析 -progress 的取值，返回模式与是否识别成功。
func ParseProgressMode(v string) (progressDisplayMode, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "bar", "":
		return progressBarMode, true
	case "line":
		return progressLineMode, true
	case "off", "none":
		return progressOffMode, true
	default:
		return progressBarMode, false
	}
}

// ResolveProgressMode 结合输出是否为交互终端决定最终模式：非交互输出时进度块自动降级为整行文本。
func ResolveProgressMode(requested progressDisplayMode, stdoutIsTerminal bool) progressDisplayMode {
	if requested == progressBarMode && !stdoutIsTerminal {
		return progressLineMode
	}
	return requested
}

// isStdoutTerminal 判断标准输出是否为终端（重定向到文件或管道时为 false）。
func isStdoutTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// progressSnapshot 一次进度采样：数量、字节、速度与逐曲包明细。
type progressSnapshot struct {
	done, active, total   int
	bytesDone, bytesTotal int64 // 0 表示拿不到字节数据
	speed                 int64 // 字节/秒，0 表示拿不到
	// tasks 为当前正在下载的曲包（按本批任务顺序排列），tasksOverflow 是被块高度上限截掉的数量。
	tasks         []taskProgress
	tasksOverflow int
}

// percent 返回整体百分比：有字节数据时按字节计算，否则按完成数量。
func (s progressSnapshot) percent() int {
	if s.bytesTotal > 0 {
		return clampPercent(int(s.bytesDone * 100 / s.bytesTotal))
	}
	if s.total > 0 {
		return clampPercent(s.done * 100 / s.total)
	}
	return 0
}

// eta 返回预计剩余时间；速度或总量未知时返回 false。
func (s progressSnapshot) eta() (time.Duration, bool) {
	return estimateETA(s.bytesDone, s.bytesTotal, s.speed)
}

// estimateETA 按「剩余字节 ÷ 速度」估算剩余时间；速度或总量未知时返回 false。
func estimateETA(done, total, speed int64) (time.Duration, bool) {
	if speed <= 0 || total <= 0 || done >= total {
		return 0, false
	}
	remain := float64(total-done) / float64(speed)
	return time.Duration(remain * float64(time.Second)), true
}

func clampPercent(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// FormatProgressBarFrame 生成整体进度行。整行只用 ASCII 字符：
// 中文 Windows 传统控制台（代码页 936）无法正确显示 Unicode 制表字符，
// 且 ASCII 能保证"清除上一帧"用的字符数等于实际列数。
func FormatProgressBarFrame(snap progressSnapshot, width int) string {
	if width <= 0 {
		width = progressBarWidth
	}
	pct := snap.percent()
	bar := renderProgressBar(pct, width)

	parts := []string{fmt.Sprintf("[%s] %3d%%", bar, pct)}
	if snap.total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", snap.done, snap.total))
	}
	if snap.active > 0 {
		parts = append(parts, fmt.Sprintf("DL:%d", snap.active))
	}
	if snap.bytesTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s", humanBytes(snap.bytesDone), humanBytes(snap.bytesTotal)))
	}
	if snap.speed > 0 {
		parts = append(parts, humanBytes(snap.speed)+"/s")
	}
	if eta, ok := snap.eta(); ok {
		parts = append(parts, "ETA "+formatDuration(eta))
	}

	frame := strings.Join(parts, "  ")
	if utf8.RuneCountInString(frame) > progressMaxLineWidth {
		frame = truncateRunes(frame, progressMaxLineWidth)
	}
	return frame
}

// FormatProgressLine 生成整行文本进度（非交互输出或 -progress line 时使用），不含控制字符。
func FormatProgressLine(snap progressSnapshot) string {
	parts := []string{fmt.Sprintf("进度: 已完成 %d/%d", snap.done, snap.total)}
	if snap.bytesTotal > 0 {
		// 有字节数据时额外给出按字节算的整体百分比，避免与"完成数"混淆。
		parts = append(parts, fmt.Sprintf("已下载 %s/%s（%d%%）",
			humanBytes(snap.bytesDone), humanBytes(snap.bytesTotal), snap.percent()))
	}
	if snap.active > 0 {
		parts = append(parts, fmt.Sprintf("下载中 %d", snap.active))
	}
	if snap.speed > 0 {
		parts = append(parts, humanBytes(snap.speed)+"/s")
	}
	if eta, ok := snap.eta(); ok {
		parts = append(parts, "ETA "+formatDuration(eta))
	}
	return strings.Join(parts, "，")
}

// FormatProgressBlock 生成一帧进度块：第 0 行为整体进度，其后每个正在下载的曲包各一行。
// barMode 为真时每行都保证是纯 ASCII 且不超过 progressMaxLineWidth（原地刷新按字符数擦除旧帧）。
func FormatProgressBlock(snap progressSnapshot, width int, barMode bool) []string {
	var lines []string
	if barMode {
		lines = append(lines, FormatProgressBarFrame(snap, width))
	} else {
		lines = append(lines, FormatProgressLine(snap))
	}
	for _, t := range snap.tasks {
		if barMode {
			lines = append(lines, formatTaskLineBar(t, progressMaxLineWidth))
		} else {
			lines = append(lines, formatTaskLineText(t))
		}
	}
	if snap.tasksOverflow > 0 {
		if barMode {
			lines = append(lines, fmt.Sprintf("  ... +%d more downloading", snap.tasksOverflow))
		} else {
			lines = append(lines, fmt.Sprintf("  ... 另有 %d 个曲包在下载", snap.tasksOverflow))
		}
	}
	return lines
}

// formatTaskLineBar 生成 bar 模式下的单个曲包进度行（纯 ASCII、名称超宽时截断）。
func formatTaskLineBar(t taskProgress, width int) string {
	pct := t.percent()
	prefix := fmt.Sprintf("  %3d%% [%s] ", pct, renderProgressBar(pct, progressTaskBarWidth))
	suffix := "  n/a"
	if t.total > 0 {
		suffix = fmt.Sprintf("  %s/%s", humanBytes(t.done), humanBytes(t.total))
	}
	if t.speed > 0 {
		suffix += "  " + humanBytes(t.speed) + "/s"
	}
	if eta, ok := t.eta(); ok {
		suffix += "  ETA " + formatDuration(eta)
	}
	return composeTaskLine(prefix, asciiOnly(t.name), suffix, width)
}

// formatTaskLineText 生成非交互输出下的单个曲包进度行（完整整行，不含控制字符）。
func formatTaskLineText(t taskProgress) string {
	parts := []string{fmt.Sprintf("曲包: %s", t.name)}
	if t.total > 0 {
		parts = append(parts, fmt.Sprintf("已下载 %s/%s（%d%%）",
			humanBytes(t.done), humanBytes(t.total), t.percent()))
	} else {
		parts = append(parts, "字节数未知")
	}
	if t.speed > 0 {
		parts = append(parts, humanBytes(t.speed)+"/s")
	}
	if eta, ok := t.eta(); ok {
		parts = append(parts, "ETA "+formatDuration(eta))
	}
	return strings.Join(parts, "，")
}

// composeTaskLine 组装曲包行：名称是唯一可伸缩字段，超宽时优先截断名称，
// 保证百分比、字节、速度与 ETA 这些关键字段不被截掉。
func composeTaskLine(prefix, name, suffix string, width int) string {
	if width <= 0 {
		width = progressMaxLineWidth
	}
	full := prefix + name + suffix
	if utf8.RuneCountInString(full) <= width {
		return full
	}
	available := width - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	if available < 4 {
		// 终端过窄：放弃名称，保留进度字段。
		return truncateRunes(prefix+suffix, width)
	}
	return prefix + truncateWithEllipsis(name, available) + suffix
}

// truncateWithEllipsis 按字符数截断并用 ASCII 省略号标记。
func truncateWithEllipsis(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

// asciiOnly 把非 ASCII 字符替换为 '?'。
// 原地刷新按字符数擦除旧帧，非 ASCII 字符在中文控制台里占两列，必须排除以保证列数与字符数一致。
func asciiOnly(s string) string {
	ascii := true
	for _, r := range s {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r > 127 {
			b.WriteRune('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func renderProgressBar(percent, width int) string {
	if width <= 0 {
		return ""
	}
	filled := percent * width / 100
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// humanBytes 把字节数格式化为带单位的简短文本。
func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := -1
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return fmt.Sprintf("%.1f%s", value, units[i])
}

// formatDuration 把时长格式化为 mm:ss 或 h:mm:ss。
func formatDuration(d time.Duration) string {
	seconds := int64(d.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// ---------- 原地刷新的进度块渲染 ----------

var (
	progressStateMu    sync.Mutex
	progressBarOn      bool     // 是否启用原地刷新（bar 模式且输出为终端）
	progressFrameBlock []string // 最近一帧的每一行，供 msgf 打印普通信息后重绘
	progressDrawnLines []int    // 最近一帧每行已绘制字符数，用于清除
	progressInPlace    bool     // 终端是否支持多行原地刷新（ANSI 光标控制）
)

// enableVirtualTerminal 打开当前终端的 ANSI 转义解析；由平台相关文件实现，测试可替换。
var enableVirtualTerminal = platformEnableVirtualTerminal

// enableProgressBar 打开进度块渲染，并探测终端是否支持多行原地刷新。
func enableProgressBar() {
	progressStateMu.Lock()
	defer progressStateMu.Unlock()
	progressBarOn = true
	progressFrameBlock = nil
	progressDrawnLines = nil
	progressInPlace = enableVirtualTerminal() == nil
}

// disableProgressBar 清除进度块残留并停止重绘。
func disableProgressBar() {
	printMu.Lock()
	clearProgressLocked()
	printMu.Unlock()

	progressStateMu.Lock()
	progressBarOn = false
	progressFrameBlock = nil
	progressDrawnLines = nil
	progressInPlace = false
	progressStateMu.Unlock()
}

// drawProgressBlock 绘制一帧进度块。终端不支持多行原地刷新时，退化为只绘制整体进度行。
func drawProgressBlock(lines []string) {
	if len(lines) == 0 {
		return
	}
	printMu.Lock()
	defer printMu.Unlock()
	// 先清除上一帧，避免行数或内容变短时残留旧内容。
	clearProgressLocked()

	progressStateMu.Lock()
	inPlace := progressInPlace
	progressStateMu.Unlock()

	if !inPlace {
		frame := lines[0]
		fmt.Print("\r" + frame)
		progressStateMu.Lock()
		progressFrameBlock = []string{frame}
		progressDrawnLines = []int{utf8.RuneCountInString(frame)}
		progressStateMu.Unlock()
		return
	}
	writeProgressBlockLocked(lines)
}

// writeProgressBlockLocked 输出整块内容并把光标停在最后一行。调用方必须持有 printMu。
func writeProgressBlockLocked(lines []string) {
	widths := make([]int, len(lines))
	fmt.Print("\r")
	for i, line := range lines {
		if i > 0 {
			fmt.Print("\n")
		}
		fmt.Print(line)
		widths[i] = utf8.RuneCountInString(line)
	}
	progressStateMu.Lock()
	progressFrameBlock = append([]string(nil), lines...)
	progressDrawnLines = widths
	progressStateMu.Unlock()
}

// clearProgressLocked 清除已绘制的进度块。调用方必须持有 printMu。
func clearProgressLocked() {
	progressStateMu.Lock()
	drawn := progressDrawnLines
	inPlace := progressInPlace
	progressDrawnLines = nil
	progressStateMu.Unlock()

	if len(drawn) == 0 {
		return
	}
	if !inPlace {
		// 回退模式只有一行，用空格覆盖即可（不写 ANSI 序列）。
		fmt.Print("\r" + strings.Repeat(" ", drawn[0]) + "\r")
		return
	}
	// 支持多行时自下而上擦除整块，最后把光标留在块首行行首。
	for i := len(drawn) - 1; i >= 0; i-- {
		fmt.Print("\r\x1b[2K")
		if i > 0 {
			fmt.Print("\x1b[1A")
		}
	}
}

// redrawProgressLocked 重绘最近一帧。调用方必须持有 printMu。
func redrawProgressLocked() {
	progressStateMu.Lock()
	on := progressBarOn
	inPlace := progressInPlace
	block := append([]string(nil), progressFrameBlock...)
	progressStateMu.Unlock()

	if !on || len(block) == 0 {
		return
	}
	if !inPlace {
		fmt.Print("\r" + block[0])
		progressStateMu.Lock()
		progressDrawnLines = []int{utf8.RuneCountInString(block[0])}
		progressStateMu.Unlock()
		return
	}
	writeProgressBlockLocked(block)
}

// msgLines 把多行进度作为一个整块打印：持锁输出，避免与并发查询日志交错。
func msgLines(lines []string) {
	if len(lines) == 0 {
		return
	}
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLocked()
	for _, line := range lines {
		fmt.Printf("%s\n", line)
	}
	redrawProgressLocked()
}
