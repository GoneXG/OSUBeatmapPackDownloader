package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubStdin 用给定输入替换全局 stdinReader，测试结束后复原。
func stubStdin(t *testing.T, input string) {
	t.Helper()
	old := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(input))
	t.Cleanup(func() { stdinReader = old })
}

// completedPackFixture 在下载目录里放一个已完成（无 .aria2 控制文件）的 zip 曲包，返回其 aria2Item。
func completedPackFixture(t *testing.T, targetDir, tag string) aria2Item {
	t.Helper()
	pack := Pack{Tag: tag, Name: "Test Pack", DirectURL: "https://example.com/pack.zip"}
	archive := filepath.Join(targetDir, pack.DownloadFileName())
	writeZipFixture(t, archive, map[string]string{"demo.osz": "hello-" + tag})
	return aria2Item{URL: pack.DirectURL, Pack: pack}
}

func TestAskExtractAfterDownloadExtractsWhenConfirmed(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("准备下载目录失败: %v", err)
	}
	item := completedPackFixture(t, target, "SM1")
	extractDir := filepath.Join(root, "unzip")
	cfg := ExtractConfig{Dir: extractDir, Layout: extractLayoutFlat}

	stubStdin(t, "1\n")
	stats := askExtractAfterDownload(cfg, target, []aria2Item{item})
	if stats == nil {
		t.Fatalf("确认解压后应返回解压统计")
	}
	if stats.Succeeded != 1 || stats.Failed != 0 {
		t.Fatalf("解压统计不符: %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(extractDir, "demo.osz")); err != nil {
		t.Fatalf("确认解压后产物缺失: %v", err)
	}
}

func TestAskExtractAfterDownloadSkipsWhenDeclined(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("准备下载目录失败: %v", err)
	}
	item := completedPackFixture(t, target, "SM2")
	extractDir := filepath.Join(root, "unzip")
	cfg := ExtractConfig{Dir: extractDir, Layout: extractLayoutFlat}

	stubStdin(t, "2\n")
	if stats := askExtractAfterDownload(cfg, target, []aria2Item{item}); stats != nil {
		t.Fatalf("选择不解压时不应返回解压统计: %+v", stats)
	}
	if _, err := os.Stat(extractDir); !os.IsNotExist(err) {
		t.Fatalf("选择不解压时不应创建解压目录（err=%v）", err)
	}
}

func TestAskExtractAfterDownloadSkipsWhenNothingDownloaded(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("准备下载目录失败: %v", err)
	}
	extractDir := filepath.Join(root, "unzip")
	cfg := ExtractConfig{Dir: extractDir, Layout: extractLayoutFlat}
	missing := aria2Item{Pack: Pack{Tag: "SM3", Name: "Missing", DirectURL: "https://example.com/pack.zip"}}

	stubStdin(t, "1\n")
	if stats := askExtractAfterDownload(cfg, target, []aria2Item{missing}); stats != nil {
		t.Fatalf("没有已完成压缩包时不应解压: %+v", stats)
	}
	if _, err := os.Stat(extractDir); !os.IsNotExist(err) {
		t.Fatalf("没有已完成压缩包时不应创建解压目录（err=%v）", err)
	}
}

// TestAskExtractAfterDownloadIgnoresInProgressArchive 确认仍有 .aria2 控制文件的压缩包不被解压。
func TestAskExtractAfterDownloadIgnoresInProgressArchive(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("准备下载目录失败: %v", err)
	}
	item := completedPackFixture(t, target, "SM4")
	archive := filepath.Join(target, item.Pack.DownloadFileName())
	if err := os.WriteFile(archive+".aria2", []byte("control"), 0o644); err != nil {
		t.Fatalf("写入控制文件失败: %v", err)
	}
	extractDir := filepath.Join(root, "unzip")
	cfg := ExtractConfig{Dir: extractDir, Layout: extractLayoutFlat}

	stubStdin(t, "1\n")
	if stats := askExtractAfterDownload(cfg, target, []aria2Item{item}); stats != nil {
		t.Fatalf("下载未完成的压缩包不应解压: %+v", stats)
	}
	if _, err := os.Stat(extractDir); !os.IsNotExist(err) {
		t.Fatalf("下载未完成时不应创建解压目录（err=%v）", err)
	}
}
