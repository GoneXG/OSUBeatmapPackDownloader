package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// errNoVirtualTerminal 模拟「终端不支持 ANSI 多行原地刷新」。
var errNoVirtualTerminal = errors.New("终端不支持 ANSI")

// etaRe 匹配完整的 ETA 文本（mm:ss 或 h:mm:ss），用于确认进度条未被截断。
var etaRe = regexp.MustCompile(`ETA (\d+:\d\d:\d\d|\d\d:\d\d)$`)

// captureStdout 在测试期间接管 os.Stdout，返回函数执行期间写入的内容。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	os.Stdout = orig
	data, _ := io.ReadAll(r)
	r.Close()
	return string(data)
}

func TestParseAndResolveProgressMode(t *testing.T) {
	cases := []struct {
		in   string
		want progressDisplayMode
		ok   bool
	}{
		{"bar", progressBarMode, true},
		{"LINE", progressLineMode, true},
		{" off ", progressOffMode, true},
		{"none", progressOffMode, true},
		{"weird", progressBarMode, false},
	}
	for _, c := range cases {
		got, ok := ParseProgressMode(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseProgressMode(%q) = (%v, %v), 期望 (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}

	// 非交互输出时进度条自动降级为整行文本，其它模式不受影响。
	if got := ResolveProgressMode(progressBarMode, false); got != progressLineMode {
		t.Errorf("非交互输出应降级为 line，实际 %v", got)
	}
	if got := ResolveProgressMode(progressBarMode, true); got != progressBarMode {
		t.Errorf("交互终端应保持 bar，实际 %v", got)
	}
	if got := ResolveProgressMode(progressOffMode, false); got != progressOffMode {
		t.Errorf("off 模式不应被改写，实际 %v", got)
	}
}

func TestFormatProgressBarFrameIsAsciiAndBounded(t *testing.T) {
	snap := progressSnapshot{
		done: 5, active: 2, total: 20,
		bytesDone: 1_500_000_000, bytesTotal: 2_600_000_000, speed: 8_000_000,
	}
	frame := FormatProgressBarFrame(snap, progressBarWidth)

	for _, r := range frame {
		if r > 127 {
			t.Fatalf("进度条应只含 ASCII 字符，发现 %q：%s", r, frame)
		}
		if r == '\r' || r == '\n' {
			t.Fatalf("进度条不应包含换行/回车控制字符：%q", frame)
		}
	}
	if width := utf8.RuneCountInString(frame); width > progressMaxLineWidth {
		t.Fatalf("进度条长度 %d 超过上限 %d：%s", width, progressMaxLineWidth, frame)
	}
	if !strings.Contains(frame, "57%") {
		t.Fatalf("有字节数据时应按字节算百分比（1500/2600 ≈ 57.7%%）：%s", frame)
	}
	if !strings.Contains(frame, "5/20") {
		t.Fatalf("进度条应包含完成数/总数：%s", frame)
	}
	if !etaRe.MatchString(frame) {
		t.Fatalf("有速度时应显示完整 ETA（不能被截断）：%s", frame)
	}

	// 没有字节数据时退回按数量计算百分比，且不出现字节/速度字段。
	countOnly := FormatProgressBarFrame(progressSnapshot{done: 1, total: 4}, progressBarWidth)
	if !strings.Contains(countOnly, "25%") || strings.Contains(countOnly, "ETA") {
		t.Fatalf("无字节数据时应按数量显示且不带 ETA：%s", countOnly)
	}
}

// stubVirtualTerminal 让测试控制终端是否支持多行原地刷新。
func stubVirtualTerminal(t *testing.T, err error) {
	t.Helper()
	orig := enableVirtualTerminal
	enableVirtualTerminal = func() error { return err }
	t.Cleanup(func() { enableVirtualTerminal = orig })
}

func TestProgressBlockFallsBackToSingleLineWithoutAnsi(t *testing.T) {
	stubVirtualTerminal(t, errNoVirtualTerminal)
	enableProgressBar()

	out := captureStdout(t, func() {
		drawProgressBlock([]string{"AAAAAAAA", "曲包行不该出现在回退模式"})
		drawProgressBlock([]string{"BB"})
		msgf("事件行")
	})

	if strings.Contains(out, "\x1b") {
		t.Fatalf("回退模式不应写入 ANSI 控制序列，输出: %q", out)
	}
	// 第二帧比第一帧短：必须用空格覆盖多出的 6 个字符，避免残留。
	if !strings.Contains(out, "\r        \r") {
		t.Fatalf("较短的新帧未清除上一帧残留，输出: %q", out)
	}
	if !strings.Contains(out, "事件行\n") {
		t.Fatalf("事件行未独占一行输出，输出: %q", out)
	}
	if !strings.HasSuffix(out, "\rBB") {
		t.Fatalf("打印事件后应重绘最新进度块，输出: %q", out)
	}
	// 在捕获环境内收尾，避免把清理序列写到真实 stdout。
	afterDisable := captureStdout(t, func() { disableProgressBar() })
	if strings.Contains(afterDisable, "\x1b") {
		t.Fatalf("回退模式收尾不应写入 ANSI 序列，输出: %q", afterDisable)
	}
}

func TestProgressBlockInPlaceErasesShrunkBlock(t *testing.T) {
	stubVirtualTerminal(t, nil)
	enableProgressBar()

	out := captureStdout(t, func() {
		drawProgressBlock([]string{"L1", "L2", "L3"})
		drawProgressBlock([]string{"N1"})
		msgf("事件行")
	})

	if !strings.Contains(out, "\rL1\nL2\nL3") {
		t.Fatalf("应整块绘制三行，输出: %q", out)
	}
	// 块从三行缩短到一行：必须自下而上擦除三行（\r\x1b[2K + \x1b[1A）。
	wantClear := "\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"
	if !strings.Contains(out, wantClear) {
		t.Fatalf("块变矮时未擦除多出的行，输出: %q", out)
	}
	if !strings.Contains(out, "事件行\n") {
		t.Fatalf("事件行未独占一行输出，输出: %q", out)
	}
	if !strings.HasSuffix(out, "\rN1") {
		t.Fatalf("打印事件后应重绘最新进度块，输出: %q", out)
	}

	afterDisable := captureStdout(t, func() { disableProgressBar() })
	if !strings.Contains(afterDisable, "\r\x1b[2K") {
		t.Fatalf("收尾时应清除进度块残留，输出: %q", afterDisable)
	}
}

func TestProgressOffModeIsSilent(t *testing.T) {
	items := []aria2Item{{URL: "https://example.invalid/a.zip", Pack: Pack{Tag: "S1", Name: "Pack 1"}}}
	out := captureStdout(t, func() {
		reportBatchProgress(context.Background(), t.TempDir(), items, progressOffMode, &summaryProgress{}, make(chan struct{}))
	})
	if out != "" {
		t.Fatalf("off 模式不应输出周期性进度，实际输出: %q", out)
	}
}

func TestSummaryProgressAggregation(t *testing.T) {
	sum := &summaryProgress{}
	if sum.note("[NOTICE] Downloading 2 item(s)") {
		t.Fatal("普通日志行不应被当作进度摘要")
	}
	if !sum.note("[#8d9a4c 1.2MiB/33MiB(4%) CN:16 DL:1.2MiB ETA:26s]") {
		t.Fatal("应识别 aria2 摘要行")
	}
	sum.note("[#aabbcc 2.5MiB/10MiB(25%) CN:8 DL:512KiB ETA:15s]")
	sumDone, sumTotal, sumSpeed, ok := sum.activeProgress()
	if !ok {
		t.Fatal("摘要行应能提供进度数据")
	}
	if want := int64(1258291 + 2621440); sumDone != want {
		t.Errorf("已下载字节 = %d, 期望 %d", sumDone, want)
	}
	if want := int64(34603008 + 10485760); sumTotal != want {
		t.Errorf("总字节 = %d, 期望 %d", sumTotal, want)
	}
	if want := int64(1258291 + 524288); sumSpeed != want {
		t.Errorf("速度 = %d, 期望 %d", sumSpeed, want)
	}

	// 同一任务的后续摘要行应覆盖前一行，而不是累加。
	sum.note("[#8d9a4c 2.2MiB/33MiB(7%) CN:16 DL:2.2MiB ETA:20s]")
	againDone, _, _, _ := sum.activeProgress()
	if want := int64(2306867 + 2621440); againDone != want {
		t.Errorf("同一任务的新摘要行应覆盖旧值，已下载 = %d, 期望 %d", againDone, want)
	}
}

func TestSummaryProgressDropsCompletedTasks(t *testing.T) {
	sum := &summaryProgress{}
	sum.note("[#8d9a4c 1.2MiB/33MiB(4%) CN:16 DL:1.2MiB ETA:26s]")
	if _, _, _, ok := sum.activeProgress(); !ok {
		t.Fatal("进行中的任务应计入进度")
	}
	// 完成后的任务由磁盘体积统计，摘要行不应继续计入，否则总量会翻倍。
	sum.note("[#8d9a4c 33MiB/33MiB(100%) CN:1 DL:0B]")
	if _, _, _, ok := sum.activeProgress(); ok {
		t.Fatal("已完成任务应改由磁盘统计，不应重复计入")
	}
}

func TestSummaryProgressWithoutDataDegradesToCounts(t *testing.T) {
	items := []aria2Item{{Pack: Pack{Tag: "S1", Name: "a"}}, {Pack: Pack{Tag: "S2", Name: "b"}}}
	st := batchStat{done: 1, active: 1}
	snap := collectProgress(context.Background(), t.TempDir(), items, st, &summaryProgress{})
	if snap.bytesTotal != 0 || snap.speed != 0 {
		t.Fatalf("拿不到摘要行时不应编造字节数据: %+v", snap)
	}
	if snap.percent() != 50 {
		t.Fatalf("应降级为按数量计算百分比，实际 %d%%", snap.percent())
	}
	line := FormatProgressLine(snap)
	if strings.Contains(line, "ETA") || strings.Contains(line, "KiB") {
		t.Fatalf("无字节数据时整行文本不应出现字节/ETA：%s", line)
	}
}

func TestCollectProgressIgnoresPreallocatedInProgressFiles(t *testing.T) {
	dir := t.TempDir()
	doneItem := aria2Item{Pack: Pack{Tag: "S1", Name: "done"}}
	activeItem := aria2Item{Pack: Pack{Tag: "S2", Name: "active"}}

	if err := os.WriteFile(filepath.Join(dir, doneItem.Pack.DownloadFileName()), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("写入已完成文件失败: %v", err)
	}
	// 进行中的文件会被 aria2 预分配，体积不可信，需要连同 .aria2 控制文件一起存在。
	activePath := filepath.Join(dir, activeItem.Pack.DownloadFileName())
	if err := os.WriteFile(activePath, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatalf("写入进行中文件失败: %v", err)
	}
	if err := os.WriteFile(activePath+".aria2", []byte("control"), 0o644); err != nil {
		t.Fatalf("写入控制文件失败: %v", err)
	}

	items := []aria2Item{doneItem, activeItem}
	st := statBatch(dir, items)
	snap := collectProgress(context.Background(), dir, items, st, &summaryProgress{})

	if snap.done != 1 || snap.active != 1 {
		t.Fatalf("任务计数应为 1 完成 / 1 进行中，实际 %+v", snap)
	}
	if snap.bytesTotal != 4096 {
		t.Fatalf("只应统计已完成文件的 4096 字节，实际 %d", snap.bytesTotal)
	}
}

func TestHumanBytesAndDuration(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KiB"},
		{33 * 1024 * 1024, "33.0MiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %s, 期望 %s", c.in, got, c.want)
		}
	}
}
