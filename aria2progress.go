package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// summaryTask 一个 aria2 任务（对应一个曲包）的进度明细。
type summaryTask struct {
	gid         string
	file        string // 来自紧接着摘要行的 FILE: 行（仅文件名）
	done, total int64
	speed       int64
}

// summaryProgress 从 aria2 的进度摘要块解析逐任务明细，例如：
//
//	[#8d9a4c 1.2MiB/33MiB(4%) CN:16 DL:1.2MiB ETA:26s]
//	FILE: C:/downloads/SM379 - osu!mania Beatmap Pack #379.zip
//
// 说明：这里刻意不使用 aria2 的 JSON-RPC。实测随程序分发的 aria2c 1.36 在
// `--enable-rpc` 下下载完成后不会退出（进程一直存活），会让整批下载卡在等待进程结束。
// 摘要块是 aria2 控制台的稳定输出，拿不到时进度会退化为按任务数量统计。
type summaryProgress struct {
	mu      sync.Mutex
	tasks   map[string]summaryTask // gid -> 任务
	pending string                 // 最近一个摘要行的 GID，等待其 FILE: 行补全文件名
}

var (
	// summaryTaskGroupRe 匹配摘要行里的一个任务组，如 `[#8d9a4c 1.2MiB/33MiB(4%)`。
	summaryTaskGroupRe = regexp.MustCompile(`\[#([0-9a-zA-Z]{6,})\s+([0-9.]+)\s*([KMGT]?i?B)/([0-9.]+)\s*([KMGT]?i?B)`)
	summarySpeedRe     = regexp.MustCompile(`DL:([0-9.]+)\s*([KMGT]?i?B)`)
	// summaryBannerRe 匹配摘要块的分隔行（download progress summary 标题、==== 与 ----）。
	summaryBannerRe = regexp.MustCompile(`(?i)^(\*+ download progress summary|=+\s*$|-{3,}\s*$)`)
)

// note 记录一行 aria2 输出；命中进度摘要（含 FILE: 行）返回 true（调用方据此不再原样打印）。
func (s *summaryProgress) note(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	if strings.HasPrefix(trimmed, "FILE:") {
		return s.noteFile(strings.TrimSpace(strings.TrimPrefix(trimmed, "FILE:")))
	}
	if summaryBannerRe.MatchString(trimmed) {
		s.mu.Lock()
		s.pending = ""
		s.mu.Unlock()
		return true
	}
	if strings.HasPrefix(trimmed, "[#") && len(summaryTaskGroupRe.FindAllStringSubmatch(trimmed, -1)) == 1 {
		// 摘要块里的任务行：一个任务占一行，紧随其后的是它的 FILE: 行。
		task, ok := parseSummaryTaskLine(trimmed)
		if !ok {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.storeLocked(task, true)
		return true
	}

	// 其余行可能是 aria2 的 console readout（以 [DL: 开头，可能被终端宽度截断、一行含多个 GID）：
	// 只用来刷新已知任务的字节数，不新建任务、也不影响 pending。
	groups := summaryTaskGroupRe.FindAllStringSubmatch(trimmed, -1)
	if len(groups) == 0 {
		return false
	}
	updated := false
	// 一行只含一个任务组时，DL: 速度可以明确归属该任务。
	speed := int64(0)
	if len(groups) == 1 {
		if sm := summarySpeedRe.FindStringSubmatch(trimmed); sm != nil {
			speed = parseAria2Size(sm[1], sm[2])
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range groups {
		old, ok := s.tasks[g[1]]
		if !ok {
			continue
		}
		old.done = parseAria2Size(g[2], g[3])
		old.total = parseAria2Size(g[4], g[5])
		if speed > 0 {
			old.speed = speed
		}
		s.tasks[g[1]] = old
		updated = true
	}
	return updated
}

// noteFile 处理 FILE: 行：把文件名绑定到最近一个摘要行的 GID。
func (s *summaryProgress) noteFile(path string) bool {
	if path == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == "" {
		return false
	}
	task, ok := s.tasks[s.pending]
	if !ok {
		return false
	}
	task.file = baseName(path)
	s.tasks[s.pending] = task
	s.pending = ""
	return true
}

// storeLocked 写入/更新一个任务；waitFile 为真时把该 GID 记为「等待 FILE: 行」。
// 调用方必须持有 s.mu。
func (s *summaryProgress) storeLocked(task summaryTask, waitFile bool) {
	if s.tasks == nil {
		s.tasks = map[string]summaryTask{}
	}
	if old, ok := s.tasks[task.gid]; ok {
		// 新摘要行不含文件名时保留已知文件名。
		if task.file == "" {
			task.file = old.file
		}
	}
	if task.total > 0 && task.done >= task.total {
		// 已下完的任务改由磁盘体积统计（避免与摘要行重复计数）。
		delete(s.tasks, task.gid)
		s.pending = ""
		return
	}
	s.tasks[task.gid] = task
	if waitFile {
		s.pending = task.gid
	}
}

// parseSummaryTaskLine 解析摘要块里的一行任务数据。
func parseSummaryTaskLine(line string) (summaryTask, bool) {
	m := summaryTaskGroupRe.FindStringSubmatch(line)
	if m == nil {
		return summaryTask{}, false
	}
	task := summaryTask{
		gid:   m[1],
		done:  parseAria2Size(m[2], m[3]),
		total: parseAria2Size(m[4], m[5]),
	}
	if sm := summarySpeedRe.FindStringSubmatch(line); sm != nil {
		task.speed = parseAria2Size(sm[1], sm[2])
	}
	return task, true
}

// activeTasks 返回仍在下载的任务（按 GID 排序，保证渲染顺序稳定）。
func (s *summaryProgress) activeTasks() []summaryTask {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return nil
	}
	out := make([]summaryTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].gid < out[j].gid })
	return out
}

// activeProgress 汇总所有已知任务的进度；没有任何数据时返回 false。
func (s *summaryProgress) activeProgress() (done, total, speed int64, ok bool) {
	tasks := s.activeTasks()
	if len(tasks) == 0 {
		return 0, 0, 0, false
	}
	for _, t := range tasks {
		done += t.done
		total += t.total
		speed += t.speed
	}
	return done, total, speed, true
}

// baseName 取路径的文件名；aria2 输出统一使用正斜杠，两种分隔符都兼容。
func baseName(path string) string {
	return filepath.Base(filepath.FromSlash(strings.ReplaceAll(path, `\`, "/")))
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

// taskProgress 渲染用的逐曲包进度条目。
type taskProgress struct {
	name        string // 曲包 tag + 名称；识别不出时用文件名或 GID 占位
	done, total int64
	speed       int64
}

// percent 该曲包自身的完成百分比。
func (t taskProgress) percent() int {
	if t.total <= 0 {
		return 0
	}
	return clampPercent(int(t.done * 100 / t.total))
}

// eta 该曲包的预计剩余时间；速度或总量未知时返回 false。
func (t taskProgress) eta() (time.Duration, bool) {
	return estimateETA(t.done, t.total, t.speed)
}

// progressTasks 把正在下载的 aria2 任务映射为渲染条目：
// 名称优先取本批任务清单里的「tag + 名称」，顺序按清单下标固定，清单外的任务按 GID 排序追加，
// 最多返回 progressMaxTaskLines 条，多出的数量通过 overflow 返回。
func progressTasks(items []aria2Item, summary *summaryProgress) (tasks []taskProgress, overflow int) {
	active := summary.activeTasks()
	if len(active) == 0 {
		return nil, 0
	}
	order := make(map[string]int, len(items))
	names := make(map[string]string, len(items))
	for i, it := range items {
		file := it.Pack.DownloadFileName()
		order[file] = i
		names[file] = displayNameOf(it.Pack)
	}

	type entry struct {
		task  summaryTask
		order int
	}
	entries := make([]entry, 0, len(active))
	for _, t := range active {
		idx, ok := order[t.file]
		if !ok {
			idx = len(items) // 清单外的任务排在最后
		}
		entries = append(entries, entry{task: t, order: idx})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].task.gid < entries[j].task.gid
	})

	for _, e := range entries {
		name := names[e.task.file]
		if name == "" {
			name = e.task.file
		}
		if name == "" {
			name = "曲包 " + e.task.gid
		}
		tasks = append(tasks, taskProgress{
			name:  name,
			done:  e.task.done,
			total: e.task.total,
			speed: e.task.speed,
		})
	}
	if len(tasks) > progressMaxTaskLines {
		overflow = len(tasks) - progressMaxTaskLines
		tasks = tasks[:progressMaxTaskLines]
	}
	return tasks, overflow
}

// displayNameOf 曲包在进度行里的显示名（与下载文件名同源，保证能对应上）。
func displayNameOf(p Pack) string {
	return p.DownloadFileName()
}

// collectProgress 汇总一次进度：完成数量与已完成文件的字节来自磁盘，
// 进行中任务的字节、速度与逐任务明细来自 aria2 摘要块；拿不到时只显示任务数量进度。
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
	snap.tasks, snap.tasksOverflow = progressTasks(items, summary)
	return snap
}
