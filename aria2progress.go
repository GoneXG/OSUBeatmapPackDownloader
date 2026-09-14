package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// summaryProgress 从 aria2 的进度摘要行聚合字节与速度，例如：
//
//	[#8d9a4c 1.2MiB/33MiB(4%) CN:16 DL:1.2MiB ETA:26s]
//
// 说明：这里刻意不使用 aria2 的 JSON-RPC。实测随程序分发的 aria2c 1.36 在
// `--enable-rpc` 下下载完成后不会退出（进程一直存活），会让整批下载卡在等待进程结束。
// 摘要行是 aria2 控制台的稳定输出，拿不到时进度会退化为按任务数量统计。
type summaryProgress struct {
	mu    sync.Mutex
	tasks map[string]summaryTask
}

type summaryTask struct {
	done, total, speed int64
}

var (
	summaryTaskRe  = regexp.MustCompile(`\[#([0-9a-zA-Z]{6})\s+([0-9.]+)\s*([KMGT]?i?B)/([0-9.]+)\s*([KMGT]?i?B)`)
	summarySpeedRe = regexp.MustCompile(`DL:([0-9.]+)\s*([KMGT]?i?B)`)
)

// note 记录一行 aria2 输出；命中摘要行返回 true（调用方据此不再原样打印）。
func (s *summaryProgress) note(line string) bool {
	m := summaryTaskRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	speed := int64(0)
	if sm := summarySpeedRe.FindStringSubmatch(line); sm != nil {
		speed = parseAria2Size(sm[1], sm[2])
	}
	task := summaryTask{
		done:  parseAria2Size(m[2], m[3]),
		total: parseAria2Size(m[4], m[5]),
		speed: speed,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks == nil {
		s.tasks = map[string]summaryTask{}
	}
	if task.total > 0 && task.done >= task.total {
		// 已下完的任务改由磁盘体积统计（避免与摘要行重复计数）。
		delete(s.tasks, m[1])
		return true
	}
	s.tasks[m[1]] = task
	return true
}

// activeProgress 汇总所有已知任务的进度；没有任何数据时返回 false。
func (s *summaryProgress) activeProgress() (done, total, speed int64, ok bool) {
	if s == nil {
		return 0, 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return 0, 0, 0, false
	}
	for _, t := range s.tasks {
		done += t.done
		total += t.total
		speed += t.speed
	}
	return done, total, speed, true
}

// parseAria2Size 解析 aria2 的尺寸文本，如 1.2MiB / 33MiB / 512B。
func parseAria2Size(num, unit string) int64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0
	}
	const k = 1024
	var mult float64 = 1
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "KIB", "KB":
		mult = k
	case "MIB", "MB":
		mult = k * k
	case "GIB", "GB":
		mult = k * k * k
	case "TIB", "TB":
		mult = k * k * k * k
	}
	return int64(value * mult)
}

// collectProgress 汇总一次进度：完成数量与已完成文件的字节来自磁盘，
// 进行中任务的字节与速度来自 aria2 摘要行；拿不到时只显示任务数量进度。
func collectProgress(ctx context.Context, targetDir string, items []aria2Item, st batchStat, summary *summaryProgress) progressSnapshot {
	_ = ctx
	snap := progressSnapshot{done: st.done, active: st.active, total: len(items)}

	for _, it := range items {
		p := filepath.Join(targetDir, it.Pack.DownloadFileName())
		if controlFileExists(p) {
			// 仍在下载的文件会被 aria2 预分配，体积不可信，交给摘要行统计。
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Size() <= 0 {
			continue
		}
		snap.bytesDone += fi.Size()
		snap.bytesTotal += fi.Size()
	}

	if done, total, speed, ok := summary.activeProgress(); ok {
		snap.bytesDone += done
		snap.bytesTotal += total
		snap.speed = speed
	}
	return snap
}
