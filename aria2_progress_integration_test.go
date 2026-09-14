package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRunAria2PassShowsEveryDownloadingPack 用本地 HTTP 服务 + tools/aria2c.exe 跑一次真实下载，
// 验证非交互输出里整体进度一行、每个正在下载的曲包各一行，且不含控制字符。
// 没有 aria2c（或无法启动）时跳过：这类环境由解析/渲染单测覆盖。
func TestRunAria2PassShowsEveryDownloadingPack(t *testing.T) {
	aria2Path, err := CheckAria2()
	if err != nil {
		t.Skipf("未找到 aria2c，跳过本地集成测试: %v", err)
	}

	const (
		fileSize     = 6 << 20 // 每个文件 6MiB
		throughput   = 4 << 20 // 全局限速 4MiB/s → 两个文件约 3 秒，足够采样若干帧
		tickInterval = 200 * time.Millisecond
	)
	packs := []Pack{
		{Tag: "SM379", Name: "osu!mania Beatmap Pack #379"},
		{Tag: "SM378", Name: "osu!mania Beatmap Pack #378"},
	}
	files := map[string]int{}
	items := make([]aria2Item, 0, len(packs))
	for _, p := range packs {
		name := p.DownloadFileName()
		files[name] = fileSize
		items = append(items, aria2Item{Pack: p})
	}

	srv := newThrottledFileServer(files, throughput)
	defer srv.Close()
	for i := range items {
		items[i].URL = srv.URL + "/" + url.PathEscape(items[i].Pack.DownloadFileName())
	}

	origMode, origInterval := ProgressMode, progressLineTickInterval
	ProgressMode, progressLineTickInterval = progressLineMode, tickInterval
	t.Cleanup(func() { ProgressMode, progressLineTickInterval = origMode, origInterval })

	targetDir := t.TempDir()
	var failed []aria2Item
	out := captureStdout(t, func() {
		failed = runAria2Pass(context.Background(), aria2Path, targetDir, "", items)
	})

	if len(failed) != 0 {
		t.Fatalf("本地下载应全部成功，实际失败 %d 个: %+v", len(failed), tagsOf(failed))
	}
	if !strings.Contains(out, "进度: 已完成") {
		t.Fatalf("输出应包含整体进度行: %q", out)
	}
	for _, it := range items {
		want := "曲包: " + it.Pack.DownloadFileName()
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少曲包进度行 %q\n实际输出:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("非交互输出不应包含回车控制字符: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "\x1b") {
			t.Fatalf("非交互输出不应包含 ANSI 序列: %q", line)
		}
	}
	t.Logf("实际进度输出:\n%s", out)
}

// newThrottledFileServer 起一个支持 Range 请求、带全局限速的静态文件服务。
// 限速保证下载持续数秒，让进度采样能观测到进行中的任务。
func newThrottledFileServer(files map[string]int, bytesPerSecond int) *httptest.Server {
	start := time.Now()
	var (
		mu     sync.Mutex
		served int64
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, ok := files[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		first, last := int64(0), int64(size-1)
		if rng := r.Header.Get("Range"); rng != "" {
			if !parseSingleRange(rng, int64(size), &first, &last) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}
		length := last - first + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if first != 0 || last != int64(size-1) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, size))
			w.WriteHeader(http.StatusPartialContent)
		}
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 64<<10)
		for written := int64(0); written < length; {
			n := int64(len(chunk))
			if written+n > length {
				n = length - written
			}
			mu.Lock()
			allowed := int64(float64(bytesPerSecond) * time.Since(start).Seconds())
			over := served + n - allowed
			mu.Unlock()
			if over > 0 {
				time.Sleep(time.Duration(float64(over) / float64(bytesPerSecond) * float64(time.Second)))
			}
			mu.Lock()
			served += n
			mu.Unlock()
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			written += n
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

// parseSingleRange 解析 `bytes=start-end` / `bytes=start-` / `bytes=-suffix` 形式的单段 Range。
func parseSingleRange(rng string, size int64, first, last *int64) bool {
	spec := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rng), "bytes="))
	if i := strings.Index(spec, ","); i >= 0 {
		spec = spec[:i] // 只处理第一段
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return false
	}
	if parts[0] == "" {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 {
			return false
		}
		if n > size {
			n = size
		}
		*first, *last = size-n, size-1
		return true
	}
	from, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || from < 0 || from >= size {
		return false
	}
	to := size - 1
	if parts[1] != "" {
		to, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || to < from {
			return false
		}
		if to >= size {
			to = size - 1
		}
	}
	*first, *last = from, to
	return true
}
