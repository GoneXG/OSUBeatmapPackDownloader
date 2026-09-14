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
	progressBarMode  progressDisplayMode = iota // 单行原地刷新的进度条
	progressLineMode                            // 周期性整行文本（非交互输出的默认降级）
	progressOffMode                             // 不输出周期性进度
)

const (
	// progressBarWidth 进度条本体的字符数；配合后面的字段保证整行不超过 79 字符（含速度与 ETA）。
	progressBarWidth = 22
	// progressMaxLineWidth 进度条整行最大字符数，避免在窄终端换行。
	progressMaxLineWidth = 79
)

// ProgressMode 当前下载进度显示模式，由 -progress 决定，默认进度条。
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

// ResolveProgressMode 结合输出是否为交互终端决定最终模式：非交互输出时进度条自动降级为整行文本。
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

// progressSnapshot 一次进度采样：数量、字节与速度。
type progressSnapshot struct {
	done, active, total   int
	bytesDone, bytesTotal int64 // 0 表示拿不到字节数据
	speed                 int64 // 字节/秒，0 表示拿不到
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
	if s.speed <= 0 || s.bytesTotal <= 0 || s.bytesDone >= s.bytesTotal {
		return 0, false
	}
	remain := float64(s.bytesTotal-s.bytesDone) / float64(s.speed)
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

// FormatProgressBarFrame 生成进度条整行内容。整行只用 ASCII 字符：
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

// ---------- 单行进度条渲染 ----------

var (
	progressStateMu sync.Mutex
	progressBarOn   bool   // 是否启用单行进度条（bar 模式且输出为终端）
	progressFrame   string // 最近一帧内容，供 msgf 打印普通信息后重绘
	progressDrawn   int    // 当前已绘制帧的字符数，用于清除
)

// enableProgressBar 打开单行进度条渲染。
func enableProgressBar() {
	progressStateMu.Lock()
	defer progressStateMu.Unlock()
	progressBarOn = true
	progressFrame = ""
	progressDrawn = 0
}

// disableProgressBar 清除进度条残留并停止重绘。
func disableProgressBar() {
	printMu.Lock()
	clearProgressLocked()
	printMu.Unlock()

	progressStateMu.Lock()
	progressBarOn = false
	progressFrame = ""
	progressDrawn = 0
	progressStateMu.Unlock()
}

// drawProgressBar 绘制一帧进度条（由进度循环调用，内部加锁）。
func drawProgressBar(frame string) {
	if frame == "" {
		return
	}
	printMu.Lock()
	defer printMu.Unlock()

	// 先清除上一帧，避免短帧残留旧字符。
	clearProgressLocked()
	fmt.Print("\r")
	fmt.Print(frame)

	progressStateMu.Lock()
	progressFrame = frame
	progressDrawn = utf8.RuneCountInString(frame)
	progressStateMu.Unlock()
}

// clearProgressLocked 清除已绘制的进度条行。调用方必须持有 printMu。
func clearProgressLocked() {
	progressStateMu.Lock()
	drawn := progressDrawn
	progressDrawn = 0
	progressStateMu.Unlock()

	if drawn > 0 {
		fmt.Print("\r" + strings.Repeat(" ", drawn) + "\r")
	}
}

// redrawProgressLocked 重绘最近一帧。调用方必须持有 printMu。
func redrawProgressLocked() {
	progressStateMu.Lock()
	on := progressBarOn
	frame := progressFrame
	progressStateMu.Unlock()

	if !on || frame == "" {
		return
	}
	fmt.Print("\r" + frame)
	progressStateMu.Lock()
	progressDrawn = utf8.RuneCountInString(frame)
	progressStateMu.Unlock()
}
