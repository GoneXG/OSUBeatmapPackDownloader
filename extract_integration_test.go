package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestExtractSessionSkipsInProgressDownload 用「达速慢」的本地服务跑一次真实 aria2 下载：
// aria2 会给正在下载的文件预分配完整体积并建立 .aria2 控制文件，此时不得被解压；
// 下载完成后同一个压缩包必须被解压——证明判定口径是「下载完成」而不是「文件存在」。
func TestExtractSessionSkipsInProgressDownload(t *testing.T) {
	aria2Path, err := CheckAria2()
	if err != nil {
		t.Skipf("未找到 aria2c，跳过本地集成测试: %v", err)
	}

	const (
		fileSize   = 4 << 20 // 4MiB，配合限速让下载持续数秒
		throughput = 1 << 20 // 1MiB/s
	)
	pack := Pack{Tag: "SM379", Name: "osu!mania Beatmap Pack #379"}
	name := pack.DownloadFileName()
	payload := zipBytesStored(t, "pack.osz", bytes.Repeat([]byte("osu!"), fileSize/4))

	srv := newBytesServer(map[string][]byte{name: payload}, throughput)
	defer srv.Close()
	item := aria2Item{URL: srv.URL + "/" + url.PathEscape(name), Pack: pack}

	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	extractDir := filepath.Join(tmp, "unzip")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origTick := extractScanTick
	extractScanTick = 20 * time.Millisecond
	t.Cleanup(func() { extractScanTick = origTick })

	cfg := ExtractConfig{Enabled: true, Dir: extractDir, Layout: extractLayoutFlat}
	sess := newExtractSession(cfg, targetDir, []aria2Item{item})
	sess.Start()

	archivePath := filepath.Join(targetDir, name)
	productPath := filepath.Join(extractDir, "pack.osz")

	ariaDone := make(chan []aria2Item, 1)
	go func() {
		ariaDone <- runAria2Pass(context.Background(), aria2Path, targetDir, []aria2Item{item})
	}()

	waitUntil(t, 30*time.Second, func() bool {
		fi, err := os.Stat(archivePath)
		return err == nil && fi.Size() > 0 && controlFileExists(archivePath)
	}, "aria2 应进入「正在下载」状态（预分配文件 + .aria2 控制文件）")

	// 仍在下载时：解压不得发生。先判控制文件仍在，再判产物不存在，避免与收尾瞬间竞争。
	if controlFileExists(archivePath) && pathExists(productPath) {
		t.Fatalf("正在下载的预分配文件不得被解压: %s", productPath)
	}

	failed := <-ariaDone
	stats := sess.Stop()
	if len(failed) != 0 {
		t.Fatalf("本地下载应全部成功，实际失败 %d 个: %+v", len(failed), tagsOf(failed))
	}
	if controlFileExists(archivePath) {
		t.Fatal("下载完成后不应残留 .aria2 控制文件")
	}
	if !pathExists(productPath) {
		t.Fatal("下载完成后应解压出产物")
	}
	if stats.Succeeded != 1 || stats.Failed != 0 {
		t.Fatalf("解压统计不符: %+v", stats)
	}
}

// TestExtractSessionWithRealDownloadSeparatesStats 用真实 aria2 下载「一个正常包 + 一个损坏包」：
// 下载失败数为 0，解压失败数为 1——解压结果与失败统计完全独立于下载结果；
// 进度行随完成数推进，结束后列出失败曲包。
func TestExtractSessionWithRealDownloadSeparatesStats(t *testing.T) {
	aria2Path, err := CheckAria2()
	if err != nil {
		t.Skipf("未找到 aria2c，跳过本地集成测试: %v", err)
	}

	good := Pack{Tag: "SM379", Name: "osu!mania Beatmap Pack #379"}
	broken := Pack{Tag: "SM378", Name: "osu!mania Beatmap Pack #378"}
	goodName, brokenName := good.DownloadFileName(), broken.DownloadFileName()

	goodBytes := zipBytesStored(t, "good.osz", []byte("good-content"))
	srv := newBytesServer(map[string][]byte{
		goodName:   goodBytes,
		brokenName: []byte("this-is-not-a-zip"),
	}, 0)
	defer srv.Close()
	items := []aria2Item{
		{URL: srv.URL + "/" + url.PathEscape(goodName), Pack: good},
		{URL: srv.URL + "/" + url.PathEscape(brokenName), Pack: broken},
	}

	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origTick := extractScanTick
	extractScanTick = 20 * time.Millisecond
	t.Cleanup(func() { extractScanTick = origTick })

	cfg := ExtractConfig{Enabled: true, Dir: filepath.Join(tmp, "unzip"), Layout: extractLayoutFlat}
	sess := newExtractSession(cfg, targetDir, items)

	var (
		failed []aria2Item
		stats  *extractStats
	)
	out := captureStdout(t, func() {
		sess.Start()
		failed = runAria2Pass(context.Background(), aria2Path, targetDir, items)
		stats = sess.Stop()
	})

	if len(failed) != 0 {
		t.Fatalf("两个文件都应下载成功，实际下载失败 %d 个: %+v", len(failed), tagsOf(failed))
	}
	if stats.Succeeded != 1 || stats.Failed != 1 {
		t.Fatalf("解压统计不符（下载失败数应为 0，解压失败数应为 1）: %+v", stats)
	}
	if !stats.SucceededArchives[goodName] {
		t.Fatalf("正常包应记为已成功解压: %+v", stats.SucceededArchives)
	}
	if got := readTree(t, cfg.Dir); !sameTree(got, map[string]string{"good.osz": "good-content"}) {
		t.Fatalf("解压产物不符: %#v", got)
	}
	if !strings.Contains(out, "解压: 已处理 1/2") || !strings.Contains(out, "解压: 已处理 2/2") {
		t.Fatalf("解压进度行应随完成数推进，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "解压失败曲包: SM378") {
		t.Fatalf("结束时应列出失败曲包，实际输出:\n%s", out)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// zipBytesStored 生成一个「不压缩」的 zip：体积可控，便于配合限速观察下载中状态。
func zipBytesStored(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: name, Method: zip.Store}
	h.SetMode(0o644)
	fw, err := w.CreateHeader(h)
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入 zip 条目失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// newBytesServer 起一个支持 Range 的静态字节服务；bytesPerSecond 为 0 表示不限速。
func newBytesServer(files map[string][]byte, bytesPerSecond int) *httptest.Server {
	start := time.Now()
	var (
		mu     sync.Mutex
		served int64
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := files[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		size := int64(len(payload))
		first, last := int64(0), size-1
		if rng := r.Header.Get("Range"); rng != "" {
			if !parseSingleRange(rng, size, &first, &last) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}
		length := last - first + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if first != 0 || last != size-1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, size))
			w.WriteHeader(http.StatusPartialContent)
		}
		flusher, _ := w.(http.Flusher)
		const chunkSize = 32 << 10
		for written := int64(0); written < length; {
			n := int64(chunkSize)
			if written+n > length {
				n = length - written
			}
			if bytesPerSecond > 0 {
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
			}
			off := first + written
			if _, err := w.Write(payload[off : off+n]); err != nil {
				return
			}
			written += n
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}
