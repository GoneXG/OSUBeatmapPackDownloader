package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAria2LiveDownload 用本地慢速 HTTP 服务验证 aria2 真实下载与进度输出（需要 tools/aria2c.exe）。
func TestAria2LiveDownload(t *testing.T) {
	aria2Path := filepath.Join(ToolsDir, "aria2c.exe")
	if _, err := os.Stat(aria2Path); err != nil {
		t.Skip("未找到 tools/aria2c.exe，跳过")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "12582912") // 12 MiB
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 128*1024)
		for sent := 0; sent < 12*1024*1024; sent += len(chunk) {
			_, _ = w.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(80 * time.Millisecond)
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	items := []aria2Item{{
		URL:  ts.URL + "/demo.bin",
		Pack: Pack{Tag: "S0001", Name: "进度测试包"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	failed := runAria2Pass(ctx, aria2Path, dir, "", items)
	if len(failed) != 0 {
		t.Fatalf("aria2 出现失败项: %+v", failed)
	}
	p := filepath.Join(dir, "S0001 - 进度测试包.zip")
	if fi, err := os.Stat(p); err != nil || fi.Size() != 12*1024*1024 {
		t.Fatalf("下载文件校验失败: %v size=%v", err, fi)
	}
}
