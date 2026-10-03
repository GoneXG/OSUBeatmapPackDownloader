package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sevenZipFixture 真实的 7z 夹具（LZMA2 压缩、含嵌套目录、中文文件名与已知内容），
// 由 7-Zip 控制台版一次性生成后随仓库提交；内容见 testSevenZipFixtureContents。
const sevenZipFixture = "testdata/archive/sample.7z"

func testSevenZipFixtureContents() map[string]string {
	return map[string]string{
		"demo/plain.osz":      "sevenzip-fixture-two",
		"demo/曲包示例 - 中文名.osz": "sevenzip-fixture-one",
	}
}

// writeZipFixture 用 archive/zip 自造一个 zip 夹具；条目名按原样写入，
// 因此含非法 UTF-8 的名称会以原始字节落进压缩包，用来复现历史曲包。
func writeZipFixture(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建夹具目录失败: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 夹具失败: %v", err)
	}
	w := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := &zip.FileHeader{Name: name}
		h.SetMode(0o644)
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatalf("写入条目 %q 失败: %v", name, err)
		}
		if _, err := fw.Write([]byte(files[name])); err != nil {
			t.Fatalf("写入条目内容 %q 失败: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 zip 夹具失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭 zip 文件失败: %v", err)
	}
}

// readTree 读取目录下的全部文件：相对 slash 路径 -> 内容。
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", root, err)
	}
	return out
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func testItem(tag, name, link string) aria2Item {
	return aria2Item{Pack: Pack{Tag: tag, Name: name, DirectURL: link}}
}

// ---------- 1.1 解压器与格式分派 ----------

func TestExtractArchiveDispatchesByExtension(t *testing.T) {
	tmp := t.TempDir()

	zipPath := filepath.Join(tmp, "S1813 - Beatmap Pack #1813.zip")
	writeZipFixture(t, zipPath, map[string]string{
		"osu!mania Pack #379/曲包.osz": "zip-content",
		"readme.txt":                 "hi",
	})
	destZip := filepath.Join(tmp, "out-zip")
	out := extractArchive(zipPath, destZip, extractLayoutFlat)
	if out.Err != nil || out.Unsupported || out.Rejected != 0 {
		t.Fatalf("zip 解压结果异常: %+v", out)
	}
	wantZip := map[string]string{
		"osu!mania Pack #379/曲包.osz": "zip-content",
		"readme.txt":                 "hi",
	}
	if got := readTree(t, destZip); !sameTree(got, wantZip) {
		t.Fatalf("zip 产物不符: %#v", got)
	}

	dest7z := filepath.Join(tmp, "out-7z")
	out = extractArchive(sevenZipFixture, dest7z, extractLayoutFlat)
	if out.Err != nil || out.Unsupported || out.Rejected != 0 {
		t.Fatalf("7z 解压结果异常: %+v", out)
	}
	if got := readTree(t, dest7z); !sameTree(got, testSevenZipFixtureContents()) {
		t.Fatalf("7z 产物不符: %#v", got)
	}

	// 未知扩展名：记为格式不支持并跳过，不产生任何产物。
	unknown := filepath.Join(tmp, "S1 - Pack #1.rar")
	if err := os.WriteFile(unknown, []byte("not-an-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	destUnknown := filepath.Join(tmp, "out-unknown")
	out = extractArchive(unknown, destUnknown, extractLayoutFlat)
	if !out.Unsupported {
		t.Fatalf("未知扩展名应记为格式不支持: %+v", out)
	}
	if out.Err != nil {
		t.Fatalf("格式不支持不应记为读取错误: %v", out.Err)
	}
	if pathExists(destUnknown) {
		t.Fatalf("格式不支持不应产生任何产物目录: %s", destUnknown)
	}
}

// ---------- 1.2 路径逃逸校验 ----------

func TestExtractRejectsPathEscapeEntries(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	// 绝对路径条目故意指向临时目录内部：即使防护失效也不会写到系统位置，测试仍能发现越界。
	absolute := filepath.ToSlash(filepath.Join(tmp, "outside", "abs.txt"))
	zipPath := filepath.Join(tmp, "pack.zip")
	writeZipFixture(t, zipPath, map[string]string{
		"../escape.txt":       "nope",
		"nested/../../up.txt": "nope2",
		absolute:              "nope3",
		"/rooted.txt":         "nope4",
		"ok/good.osz":         "good",
	})

	out := extractArchive(zipPath, dest, extractLayoutFlat)
	if out.Err != nil {
		t.Fatalf("合法条目应照常解压，不应整包失败: %v", out.Err)
	}
	if out.Rejected != 4 {
		t.Fatalf("应拒绝 4 个越界条目，实际 %d（原因: %v）", out.Rejected, out.Reasons)
	}
	if len(out.Reasons) == 0 {
		t.Fatal("被拒绝的条目必须记录失败原因")
	}
	for _, p := range []string{
		filepath.Join(tmp, "escape.txt"),
		filepath.Join(tmp, "up.txt"),
		filepath.Join(tmp, "outside", "abs.txt"),
		filepath.Join(filepath.Dir(dest), "rooted.txt"),
	} {
		if pathExists(p) {
			t.Fatalf("解压目录之外不应产生文件: %s", p)
		}
	}
	got := readTree(t, dest)
	if !sameTree(got, map[string]string{"ok/good.osz": "good"}) {
		t.Fatalf("同一压缩包内的合法条目应继续解压: %#v", got)
	}
}

// ---------- 1.3 不覆盖策略与跳过统计 ----------

func TestExtractSkipsExistingTargets(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "a.osz"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(tmp, "pack.zip")
	writeZipFixture(t, zipPath, map[string]string{"a.osz": "new", "b.osz": "b"})

	out := extractArchive(zipPath, dest, extractLayoutFlat)
	if out.Err != nil {
		t.Fatalf("解压失败: %v", out.Err)
	}
	if out.Files != 1 || out.Skipped != 1 {
		t.Fatalf("应新增 1 个、跳过 1 个，实际 新增=%d 跳过=%d", out.Files, out.Skipped)
	}
	if got := readTree(t, dest); !sameTree(got, map[string]string{"a.osz": "original", "b.osz": "b"}) {
		t.Fatalf("已存在的目标文件不应被覆盖: %#v", got)
	}
}

// ---------- 2.2 两种布局 ----------

func TestExtractLayouts(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "SM379 - osu!mania Beatmap Pack #379.zip")
	writeZipFixture(t, zipPath, map[string]string{"曲包.osz": "one"})
	wantBase := strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))

	if got := destRootFor(tmp, zipPath, extractLayoutFlat); got != tmp {
		t.Fatalf("扁平布局应直接落在解压目录: %s", got)
	}
	if got := destRootFor(tmp, zipPath, extractLayoutPerPack); got != filepath.Join(tmp, wantBase) {
		t.Fatalf("按曲包布局应使用同名子目录: %s", got)
	}

	flatRoot := filepath.Join(tmp, "flat")
	if err := os.MkdirAll(flatRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := extractArchive(zipPath, flatRoot, extractLayoutFlat); out.Err != nil {
		t.Fatalf("扁平布局解压失败: %v", out.Err)
	}
	if got := readTree(t, flatRoot); !sameTree(got, map[string]string{"曲包.osz": "one"}) {
		t.Fatalf("扁平布局产物不符: %#v", got)
	}

	perRoot := filepath.Join(tmp, "per")
	if err := os.MkdirAll(perRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := extractArchive(zipPath, perRoot, extractLayoutPerPack); out.Err != nil {
		t.Fatalf("按曲包布局解压失败: %v", out.Err)
	}
	if got := readTree(t, perRoot); !sameTree(got, map[string]string{wantBase + "/曲包.osz": "one"}) {
		t.Fatalf("按曲包布局产物不符: %#v", got)
	}
}

// 按曲包布局下子目录已存在时沿用该目录，继续解压缺失文件而不删除已有内容。
func TestExtractPerPackLayoutReusesExistingSubdir(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "SM379 - pack.zip")
	writeZipFixture(t, zipPath, map[string]string{"new.osz": "new", "keep.osz": "from-archive"})
	base := strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	dest := filepath.Join(tmp, "out", base)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep.osz"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := extractArchive(zipPath, filepath.Join(tmp, "out"), extractLayoutPerPack)
	if out.Err != nil {
		t.Fatalf("解压失败: %v", out.Err)
	}
	if out.Skipped != 1 || out.Files != 1 {
		t.Fatalf("应沿用子目录：跳过 1、新增 1，实际 跳过=%d 新增=%d", out.Skipped, out.Files)
	}
	got := readTree(t, dest)
	if !sameTree(got, map[string]string{"keep.osz": "keep", "new.osz": "new"}) {
		t.Fatalf("子目录已有内容应保留: %#v", got)
	}
}

// ---------- 1.4 条目名不是合法 UTF-8 ----------

func TestExtractNonUTF8EntryName(t *testing.T) {
	tmp := t.TempDir()
	rawName := string([]byte{0xff, 0xfe, 'a', '.', 'o', 's', 'z'})
	zipPath := filepath.Join(tmp, "broken-name.zip")
	writeZipFixture(t, zipPath, map[string]string{rawName: "raw-name-content"})

	first := filepath.Join(tmp, "out1")
	out := extractArchive(zipPath, first, extractLayoutFlat)
	if out.Err != nil {
		t.Fatalf("名称不可解码不应让整包失败: %v", out.Err)
	}
	if out.Files != 1 || out.Rejected != 0 {
		t.Fatalf("应正常落盘 1 个文件: %+v", out)
	}

	second := filepath.Join(tmp, "out2")
	if out := extractArchive(zipPath, second, extractLayoutFlat); out.Err != nil {
		t.Fatalf("第二次解压失败: %v", out.Err)
	}
	got1, got2 := readTree(t, first), readTree(t, second)
	if !sameTree(got1, got2) {
		t.Fatalf("同一压缩包两次解压的文件名应稳定一致: %#v vs %#v", got1, got2)
	}
	if len(got1) != 1 {
		t.Fatalf("应恰好产出一个文件: %#v", got1)
	}
	for name, content := range got1 {
		if name == "" || content != "raw-name-content" {
			t.Fatalf("产物名与内容不符: %q=%q", name, content)
		}
	}
}

// ---------- 2.1 解压目录推导 ----------

func TestDefaultExtractDir(t *testing.T) {
	tmp := t.TempDir()

	// 默认布局规则：下载目录的同级 unzip 目录。
	defaultLike := filepath.Join(tmp, "download")
	if got, want := DefaultExtractDir(defaultLike), filepath.Join(tmp, "unzip"); got != want {
		t.Fatalf("默认下载目录推导错误: got %s want %s", got, want)
	}
	// 自定义下载目录：D:\osu曲包 -> D:\unzip\
	if got, want := DefaultExtractDir(`D:\osu曲包`), `D:\unzip`; got != want {
		t.Fatalf("自定义下载目录推导错误: got %s want %s", got, want)
	}
	// 盘符根目录：D:\ -> D:\unzip\
	if got, want := DefaultExtractDir(`D:\`), `D:\unzip`; got != want {
		t.Fatalf("盘符根目录推导错误: got %s want %s", got, want)
	}
}

// ---------- 2.3 解压目录创建失败 ----------

func TestExtractRootCreationFailureKeepsDownloadResult(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := testItem("SM379", "osu!mania Beatmap Pack #379", "https://example.com/SM379%20-%20pack.zip")
	writeZipFixture(t, filepath.Join(targetDir, item.Pack.DownloadFileName()), map[string]string{"a.osz": "a"})

	// 用一个普通文件挡住解压目录的父路径，制造确定的「无法创建目录」。
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := ExtractConfig{Enabled: true, Dir: filepath.Join(blocker, "unzip"), Layout: extractLayoutFlat}
	sess := newExtractSession(cfg, targetDir, []aria2Item{item})

	var stats *extractStats
	out := captureStdout(t, func() {
		sess.Start()
		stats = sess.Stop()
	})
	if stats.SetupErr == "" {
		t.Fatalf("解压目录创建失败必须报告错误: %+v", stats)
	}
	if stats.Processed != 0 || stats.Succeeded != 0 {
		t.Fatalf("目录创建失败时应停止解压: %+v", stats)
	}
	if !strings.Contains(out, "解压目录创建失败") {
		t.Fatalf("应输出解压目录创建失败原因，实际输出:\n%s", out)
	}
	// 下载结果（压缩包）不受解压失败影响。
	if !pathExists(filepath.Join(targetDir, item.Pack.DownloadFileName())) {
		t.Fatal("解压失败不应影响已完成的下载")
	}
}

// ---------- 1.5 / 3.2 / 3.6 失败隔离与进度 ----------

func TestExtractSessionIsolatesFailuresAndReportsProgress(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	extractDir := filepath.Join(tmp, "unzip")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	good := testItem("SM379", "osu!mania Beatmap Pack #379", "https://example.com/pack-379.zip")
	broken := testItem("SM378", "osu!mania Beatmap Pack #378", "https://example.com/pack-378.zip")
	items := []aria2Item{good, broken}

	writeZipFixture(t, filepath.Join(targetDir, good.Pack.DownloadFileName()), map[string]string{"good.osz": "good"})
	if err := os.WriteFile(filepath.Join(targetDir, broken.Pack.DownloadFileName()), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ExtractConfig{Enabled: true, Dir: extractDir, Layout: extractLayoutFlat}
	sess := newExtractSession(cfg, targetDir, items)

	var stats *extractStats
	out := captureStdout(t, func() {
		sess.Start()
		stats = sess.Stop()
	})

	// 损坏包失败不阻塞后续曲包：正常包仍然解压成功。
	if stats.Processed != 2 || stats.Succeeded != 1 || stats.Failed != 1 || stats.Unsupported != 0 {
		t.Fatalf("统计不符: %+v", stats)
	}
	if got := readTree(t, extractDir); !sameTree(got, map[string]string{"good.osz": "good"}) {
		t.Fatalf("正常曲包应解压成功: %#v", got)
	}
	if !pathExists(filepath.Join(targetDir, broken.Pack.DownloadFileName())) {
		t.Fatal("解压失败必须保留原压缩包以便重试")
	}

	// 进度行随完成数推进。
	for _, want := range []string{"解压: 已处理 0/2", "解压: 已处理 1/2", "解压: 已处理 2/2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少进度行 %q，实际输出:\n%s", want, out)
		}
	}
	// 结束后列出失败曲包。
	if !strings.Contains(out, "解压失败曲包: SM378") {
		t.Fatalf("结束时应列出失败曲包，实际输出:\n%s", out)
	}
}

// 非 .zip/.7z 的压缩包记为「格式不支持」并跳过：不产出任何文件，原文件保留，且不计入失败。
func TestExtractSessionCountsUnsupportedFormat(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rarPath := filepath.Join(targetDir, "SM377 - pack.rar")
	if err := os.WriteFile(rarPath, []byte("rar-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := testItem("SM377", "osu!mania Beatmap Pack #377", "https://example.com/pack-377.rar")
	cfg := ExtractConfig{Enabled: true, Dir: filepath.Join(tmp, "unzip"), Layout: extractLayoutFlat}
	sess := newExtractSession(cfg, targetDir, []aria2Item{item})

	out := captureStdout(t, func() {
		sess.processOne(item, rarPath, filepath.Base(rarPath))
		sess.reportSummary()
	})
	stats := sess.snapshot()
	if stats.Processed != 1 || stats.Unsupported != 1 || stats.Failed != 0 {
		t.Fatalf("格式不支持应单独计数: %+v", stats)
	}
	if !pathExists(rarPath) {
		t.Fatal("格式不支持必须保留原文件不动")
	}
	if pathExists(cfg.Dir) {
		t.Fatal("格式不支持不应产生任何解压产物")
	}
	if !strings.Contains(out, "格式不支持") || !strings.Contains(out, "SM377") {
		t.Fatalf("应列出格式不支持的曲包，实际输出:\n%s", out)
	}
}

// ---------- 3.3 解压成功后删除压缩包 ----------

func TestDeleteArchiveOnlyAfterSuccessfulExtract(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	extractDir := filepath.Join(tmp, "unzip")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := testItem("SM379", "osu!mania Beatmap Pack #379", "https://example.com/pack-379.zip")
	broken := testItem("SM378", "osu!mania Beatmap Pack #378", "https://example.com/pack-378.zip")
	items := []aria2Item{good, broken}

	goodPath := filepath.Join(targetDir, good.Pack.DownloadFileName())
	brokenPath := filepath.Join(targetDir, broken.Pack.DownloadFileName())
	writeZipFixture(t, goodPath, map[string]string{"good.osz": "good"})
	if err := os.WriteFile(brokenPath, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ExtractConfig{Enabled: true, Dir: extractDir, Layout: extractLayoutFlat, DeleteAfter: true}
	sess := newExtractSession(cfg, targetDir, items)
	var stats *extractStats
	captureStdout(t, func() {
		sess.Start()
		stats = sess.Stop()
	})

	if stats.Succeeded != 1 || stats.Failed != 1 {
		t.Fatalf("统计不符: %+v", stats)
	}
	if pathExists(goodPath) {
		t.Fatal("解压成功后应删除压缩包")
	}
	if !pathExists(brokenPath) {
		t.Fatal("解压失败必须保留压缩包")
	}
	if got := readTree(t, extractDir); !sameTree(got, map[string]string{"good.osz": "good"}) {
		t.Fatalf("解压产物应保留: %#v", got)
	}
}

// ---------- 3.4 仅保留链接文件模式 ----------

func TestLinkOnlyModeNeverCreatesExtractDir(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "download")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := testItem("SM379", "osu!mania Beatmap Pack #379", "https://example.com/pack-379.zip")
	writeZipFixture(t, filepath.Join(targetDir, item.Pack.DownloadFileName()), map[string]string{"a.osz": "a"})

	extractDir := filepath.Join(tmp, "unzip")
	cfg := ExtractConfig{Enabled: true, Dir: extractDir, Layout: extractLayoutFlat}
	if sess := prepareExtraction(cfg, "2", targetDir, []aria2Item{item}); sess != nil {
		t.Fatal("仅保留链接文件时不应创建解压会话")
	}
	if pathExists(extractDir) {
		t.Fatalf("仅保留链接文件时不应创建解压目录: %s", extractDir)
	}
}

// ---------- 3.5 端到端验收口径 ----------

func TestCountVerifiableFiles(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "download")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.7z"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 未完成（仍在下载）的压缩包不计入任何口径。
	if err := os.WriteFile(filepath.Join(dir, "c.zip"), []byte("xxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.zip.aria2"), []byte("ctl"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := CountExpectedFiles(dir); got != 2 {
		t.Fatalf("未启用解压时口径应保持不变（.zip 与 .7z 都计入，进行中不计）: %d", got)
	}
	if got := countVerifiableFiles(dir, nil); got != 2 {
		t.Fatalf("未启用解压时 countVerifiableFiles 应等同 CountExpectedFiles: %d", got)
	}

	// 启用解压且选择删除压缩包：压缩包已不在磁盘，但本次已成功解压，仍应算完成。
	stats := &extractStats{SucceededArchives: map[string]bool{"d.7z": true}}
	if got := countVerifiableFiles(dir, stats); got != 3 {
		t.Fatalf("启用解压时「已成功解压」应计入验收: %d", got)
	}
	// 压缩包仍在且同时已解压时不能重复计数。
	stats = &extractStats{SucceededArchives: map[string]bool{"a.zip": true}}
	if got := countVerifiableFiles(dir, stats); got != 2 {
		t.Fatalf("压缩包存在与已解压不应重复计数: %d", got)
	}
}

// ---------- 4.1 启动参数与默认行为 ----------

func TestBuildExtractConfig(t *testing.T) {
	cfg := buildExtractConfig(false, "", "", false)
	if cfg.Enabled || cfg.Layout != extractLayoutFlat || cfg.DeleteAfter || cfg.Dir != "" {
		t.Fatalf("未指定新参数时必须保持默认（不解压、扁平、保留）: %+v", cfg)
	}

	cfg = buildExtractConfig(true, "  D:\\out  ", "per-pack", true)
	if !cfg.Enabled || cfg.Layout != extractLayoutPerPack || !cfg.DeleteAfter || cfg.Dir != `D:\out` {
		t.Fatalf("参数解析不符: %+v", cfg)
	}

	out := captureStdout(t, func() {
		cfg = buildExtractConfig(true, "", "bogus", false)
	})
	if cfg.Layout != extractLayoutFlat {
		t.Fatalf("无法识别的布局应回退 flat: %+v", cfg)
	}
	if !strings.Contains(out, "无法识别") {
		t.Fatalf("无法识别的布局应给出提示，实际输出:\n%s", out)
	}
}
