package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// aria2SummaryBlock 是本地实测（tools/aria2c.exe + 本地 HTTP 服务）抓到的摘要块样本：
// 每个任务一行摘要 + 一行 FILE:，最后一行是以 [DL: 开头、被终端宽度截断的 console readout。
var aria2SummaryBlock = []string{
	" *** Download Progress Summary as of Mon Sep 14 20:45:56 2026 *** ",
	"===============================================================================",
	"[#5a2691 3.2MiB/20MiB(16%) CN:1 DL:1.5MiB ETA:10s]",
	"FILE: C:/Users/cloud/AppData/Local/Temp/aria2-probe/dl/Pack A - test.zip",
	"-------------------------------------------------------------------------------",
	"[#cb42f4 3.1MiB/20MiB(15%) CN:1 DL:1.5MiB ETA:10s]",
	"FILE: C:/Users/cloud/AppData/Local/Temp/aria2-probe/dl/Pack B - test.zip",
	"-------------------------------------------------------------------------------",
	"[DL:4.5MiB][#5a2691 3.2MiB/20MiB(16%)][#cb42f4 3.1MiB/20MiB(15%)][#dbf186 3.0Mi",
}

func TestSummaryProgressTracksPerTaskFiles(t *testing.T) {
	sum := &summaryProgress{}
	for _, line := range aria2SummaryBlock {
		sum.note(line)
	}

	tasks := sum.activeTasks()
	if len(tasks) != 2 {
		t.Fatalf("应识别出 2 个任务（被截断的 readout 行不得新建任务），实际 %d 个: %+v", len(tasks), tasks)
	}
	// 结果按 GID 排序，保证渲染顺序稳定。
	if tasks[0].gid != "5a2691" || tasks[1].gid != "cb42f4" {
		t.Fatalf("任务应按 GID 排序，实际 %q, %q", tasks[0].gid, tasks[1].gid)
	}
	if tasks[0].file != "Pack A - test.zip" || tasks[1].file != "Pack B - test.zip" {
		t.Fatalf("FILE: 行未绑定到对应任务: %q, %q", tasks[0].file, tasks[1].file)
	}
	if tasks[0].done != parseAria2Size("3.2", "MiB") || tasks[0].total != parseAria2Size("20", "MiB") {
		t.Fatalf("任务字节解析错误: %+v", tasks[0])
	}
	if tasks[0].speed != parseAria2Size("1.5", "MiB") {
		t.Fatalf("任务速度解析错误: %+v", tasks[0])
	}
	if !sum.note("[DL:4.5MiB][#5a2691 3.2MiB/20MiB(16%)]") {
		t.Fatal("readout 行应被识别为进度输出（不再原样打印）")
	}
	if got := len(sum.activeTasks()); got != 2 {
		t.Fatalf("readout 行不应新建任务，实际 %d 个", got)
	}

	// 后续摘要行不含 FILE: 时，不能丢掉已知的文件名。
	sum.note("[#5a2691 5.2MiB/20MiB(26%) CN:1 DL:2.0MiB ETA:8s]")
	tasks = sum.activeTasks()
	if tasks[0].file != "Pack A - test.zip" {
		t.Fatalf("后续摘要行覆盖后文件名丢失: %+v", tasks[0])
	}
	if tasks[0].done != parseAria2Size("5.2", "MiB") {
		t.Fatalf("后续摘要行应覆盖旧值，实际 %+v", tasks[0])
	}

	// 摘要块结束（分隔行）后，孤立的 FILE: 行不应被当作进度数据。
	sum.note("-------------------------------------------------------------------------------")
	if sum.note("FILE: C:/tmp/unrelated.zip") {
		t.Fatal("没有前置摘要行时不应把 FILE: 行当成进度数据")
	}
	if got := len(sum.activeTasks()); got != 2 {
		t.Fatalf("孤立 FILE: 行不应新建任务，实际 %d 个", got)
	}
}

func TestSummaryProgressDropsCompletedTaskOnly(t *testing.T) {
	sum := &summaryProgress{}
	for _, line := range aria2SummaryBlock {
		sum.note(line)
	}
	sum.note("[#5a2691 20MiB/20MiB(100%) CN:1 DL:0B]")
	tasks := sum.activeTasks()
	if len(tasks) != 1 || tasks[0].gid != "cb42f4" {
		t.Fatalf("已完成的任务应移出、其余保留，实际 %+v", tasks)
	}
}

func TestProgressTasksOrderNamingAndFallback(t *testing.T) {
	items := []aria2Item{
		{URL: "u1", Pack: Pack{Tag: "SM379", Name: "osu!mania Beatmap Pack #379"}},
		{URL: "u2", Pack: Pack{Tag: "SM378", Name: "osu!mania Beatmap Pack #378"}},
		{URL: "u3", Pack: Pack{Tag: "SM377", Name: "osu!mania Beatmap Pack #377"}},
	}

	sum := &summaryProgress{}
	// 故意让 GID 顺序与本批清单顺序相反：任务行顺序必须按清单顺序固定。
	sum.note("[#aaa111 1MiB/10MiB(10%) CN:1 DL:1MiB ETA:9s]")
	sum.note("FILE: C:/dl/SM377 - osu!mania Beatmap Pack #377.zip")
	sum.note("[#zzz999 2MiB/10MiB(20%) CN:1 DL:1MiB ETA:8s]")
	sum.note("FILE: C:/dl/unknown-pack.zip")
	sum.note("[#mmm555 3MiB/10MiB(30%) CN:1 DL:1MiB ETA:7s]")
	sum.note("FILE: C:/dl/SM379 - osu!mania Beatmap Pack #379.zip")
	sum.note("[#bbb222 4MiB/10MiB(40%) CN:1 DL:1MiB ETA:6s]")

	tasks, overflow := progressTasks(items, sum)
	if overflow != 0 {
		t.Fatalf("4 个任务不应触发上限，overflow=%d", overflow)
	}
	if len(tasks) != 4 {
		t.Fatalf("应有 4 个曲包行，实际 %d", len(tasks))
	}
	if tasks[0].name != "SM379 - osu!mania Beatmap Pack #379.zip" {
		t.Fatalf("清单内任务应显示 tag + 名称并按清单顺序排列，实际首行 %q", tasks[0].name)
	}
	if tasks[1].name != "SM377 - osu!mania Beatmap Pack #377.zip" {
		t.Fatalf("第二行应为清单中较后的曲包，实际 %q", tasks[1].name)
	}
	// 清单外的任务排在清单内任务之后，彼此按 GID 排序（bbb222 < zzz999）。
	if tasks[2].name != "曲包 bbb222" {
		t.Fatalf("既无文件名又不在清单里时应回退为 GID 占位名，实际 %q", tasks[2].name)
	}
	if tasks[3].name != "unknown-pack.zip" {
		t.Fatalf("清单外任务应回退为文件名并排在清单内任务之后，实际 %q", tasks[3].name)
	}
}

func TestProgressTasksRespectMaxLines(t *testing.T) {
	sum := &summaryProgress{}
	var names []string
	for i := 1; i <= 10; i++ {
		gid := fmt.Sprintf("%06d", i)
		names = append(names, gid)
		sum.note("[#" + gid + " 1MiB/10MiB(10%) CN:1 DL:1MiB ETA:9s]")
		sum.note("FILE: C:/dl/" + gid + ".zip")
	}
	tasks, overflow := progressTasks(nil, sum)
	if len(tasks) != progressMaxTaskLines {
		t.Fatalf("应限制为 %d 行，实际 %d", progressMaxTaskLines, len(tasks))
	}
	if overflow != len(names)-progressMaxTaskLines {
		t.Fatalf("溢出数量应为 %d，实际 %d", len(names)-progressMaxTaskLines, overflow)
	}
}

func perPackSnapshot() progressSnapshot {
	return progressSnapshot{
		done: 5, active: 2, total: 20,
		bytesDone: 1_500_000_000, bytesTotal: 2_600_000_000, speed: 8_000_000,
		tasks: []taskProgress{
			{name: "SM379 - mania #379.zip", done: 3_200_000, total: 20_000_000, speed: 1_500_000},
			{name: strings.Repeat("超长曲包名称", 20), done: 100, total: 0},
		},
		tasksOverflow: 1,
	}
}

func TestFormatProgressBlockBarLinesAreAsciiAndBounded(t *testing.T) {
	lines := FormatProgressBlock(perPackSnapshot(), progressBarWidth, true)
	if len(lines) != 4 { // 整体行 + 2 个曲包行 + 溢出提示行
		t.Fatalf("应有 4 行，实际 %d 行: %#v", len(lines), lines)
	}
	for i, line := range lines {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("第 %d 行含非 ASCII 字符 %q：%s", i, r, line)
			}
			if r == '\r' || r == '\n' {
				t.Fatalf("第 %d 行含控制字符：%q", i, line)
			}
		}
		if width := utf8.RuneCountInString(line); width > progressMaxLineWidth {
			t.Fatalf("第 %d 行长度 %d 超过上限 %d：%s", i, width, progressMaxLineWidth, line)
		}
	}
	if !strings.Contains(lines[1], "16%") || !strings.Contains(lines[1], "3.1MiB/19.1MiB") {
		t.Fatalf("曲包行应显示该曲包自身的百分比与字节：%s", lines[1])
	}
	if !strings.Contains(lines[1], "SM379 - mania #379.zip") {
		t.Fatalf("曲包行应显示曲包名：%s", lines[1])
	}
	if !strings.Contains(lines[2], "...") {
		t.Fatalf("超长曲包名应被截断并标记省略号：%s", lines[2])
	}
	if !strings.Contains(lines[3], "+1") {
		t.Fatalf("超出上限的任务应给出汇总提示：%s", lines[3])
	}
}

func TestFormatProgressBlockTextLinesHaveNoControlChars(t *testing.T) {
	lines := FormatProgressBlock(perPackSnapshot(), progressBarWidth, false)
	if len(lines) != 4 {
		t.Fatalf("应有 4 行，实际 %d 行: %#v", len(lines), lines)
	}
	for i, line := range lines {
		if strings.ContainsAny(line, "\r\n") {
			t.Fatalf("第 %d 行含控制字符：%q", i, line)
		}
	}
	if !strings.HasPrefix(lines[1], "曲包: SM379 - mania #379.zip") {
		t.Fatalf("逐曲包整行文本应包含曲包名：%s", lines[1])
	}
	if !strings.Contains(lines[1], "已下载 3.1MiB/19.1MiB（16%）") {
		t.Fatalf("逐曲包整行文本应包含该曲包自身的字节与百分比：%s", lines[1])
	}
}

func TestFormatProgressBlockWithoutTasksIsSingleLine(t *testing.T) {
	snap := progressSnapshot{done: 1, active: 1, total: 4}
	if lines := FormatProgressBlock(snap, progressBarWidth, true); len(lines) != 1 {
		t.Fatalf("没有逐任务数据时进度块应只有整体行，实际 %d 行", len(lines))
	}
	if lines := FormatProgressBlock(snap, progressBarWidth, false); len(lines) != 1 {
		t.Fatalf("没有逐任务数据时整行文本应只有整体行，实际 %d 行", len(lines))
	}
}
