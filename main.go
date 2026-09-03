package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Printf("\n程序中止: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("==============================================")
	fmt.Println("  osu! Beatmap Pack 曲包下载器")
	fmt.Println("==============================================")

	// 可选启动参数：下载根目录。
	downloadRoot := flag.String("dir", DownloadRoot, "下载根目录")
	flag.Parse()
	if *downloadRoot != "" {
		DownloadRoot = filepath.Clean(*downloadRoot)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// ---------- T1 初始化目录 ----------
	msgf("[T1] 初始化目录 %s 与 %s ...", UrlOutputDir, DownloadRoot)
	if err := EnsureDir(UrlOutputDir); err != nil {
		return fmt.Errorf("无法创建 %s: %w", UrlOutputDir, err)
	}
	if err := EnsureDir(DownloadRoot); err != nil {
		return fmt.Errorf("无法创建 %s: %w", DownloadRoot, err)
	}

	// ---------- T2 选择保存方式 + 分类/模式 ----------
	style, customDir, err := pickSaveStyle()
	if err != nil {
		return err
	}
	choice, err := pickCategory()
	if err != nil {
		return err
	}
	cat := CategoryMap[choice.CatID]
	msgf("[T2] 已选择: %s%s", cat.Name, modeSuffix(cat.Name, choice.Mode))

	// ---------- T3 获取 Cookie ----------
	cookie := GetCookie()
	if !CookieValid(cookie) {
		msgf("      [Skip] 未获得 osu_sid（%s）。下载将尽量直连 packs.ppy.sh 进行。", cookie.Note)
	} else {
		msgf("      Cookie 来源: %s（osu_sid=%s...）", cookie.Source, truncate(cookie.Value, 8))
	}

	// ---------- T4 构造列表页 URL ----------
	pageURL, err := BuildURL(choice.CatID, choice.Mode)
	if err != nil {
		return fmt.Errorf("[T4] %w", err)
	}
	if !ValidatePageURL(pageURL) {
		return fmt.Errorf("[T4] 生成的 URL 非法: %s", pageURL)
	}
	msgf("[T4] 列表页: %s", pageURL)

	// ---------- T5 分页抓取 ----------
	msgf("[T5] 开始抓取（分页自动翻页，Ctrl+C 可中止）...")
	scrape := ScrapeLinks(ctx, choice.CatID, choice.Mode, cookie.Value)
	var scrapeFailedReason string
	if scrape.Failed {
		scrapeFailedReason = scrape.Reason
		msgf("      [WARN] 抓取失败: %s", scrape.Reason)
		msgf("      [Skip] 本次未生成下载列表。")
		if err := writeScrapeFailure(); err != nil {
			return err
		}
		SaveFailedLog(nil, scrapeFailedReason)
		return nil
	}
	packs := scrape.Packs
	msgf("[T5] 抓取完成，共 %d 个曲包。", len(packs))
	if ctx.Err() != nil {
		return fmt.Errorf("用户中断（Ctrl+C），本次任务中止")
	}

	// ---------- T6 写入 urls.txt ----------
	var links []string
	items := make([]aria2Item, 0, len(packs))
	for _, p := range packs {
		links = append(links, p.DirectURL)
		items = append(items, aria2Item{URL: p.DirectURL, Pack: p})
	}
	urlsPath := filepath.Join(UrlOutputDir, "urls.txt")
	if err := WriteLines(urlsPath, links); err != nil {
		return fmt.Errorf("[T6] 写入 %s 失败: %w", urlsPath, err)
	}
	if fi, err := os.Stat(urlsPath); err != nil || fi.Size() <= 10 {
		return fmt.Errorf("[T6] urls.txt 内容过小，疑似无有效链接")
	}
	msgf("[T6] 已写入 %s（%d 行, %d 字节）", urlsPath, len(links), fileSize(urlsPath))

	// ---------- T7 选择下载方式 ----------
	method := AskUser(
		"[T7] 请选择下载方式: [1] 调用 aria2 下载  [2] 仅保留链接文件",
		map[string]bool{"1": true, "2": true},
		3,
		"2",
	)
	msgf("      选择: %s", map[string]string{"1": "调用 aria2 下载", "2": "仅保留链接文件"}[method])

	if method == "2" {
		msgf("      已完成：链接保存在 %s，直接退出。", urlsPath)
		return nil
	}

	// ---------- T8 检查 aria2 ----------
	aria2Path, err := CheckAria2()
	if err != nil {
		msgf("[T8] %v", err)
		msgf("      [降级] 未找到 aria2c，自动转为选项 2：仅保留链接文件。")
		msgf("      提示: 将 aria2c.exe 放入 %s 目录后重跑即可下载。", ToolsDir)
		return nil
	}
	msgf("[T8] aria2c: %s", aria2Path)

	// ---------- 下载路径决策 ----------
	targetDir, err := ResolveTargetDir(style, cat, choice.Mode, customDir)
	if err != nil {
		return err
	}
	msgf("      下载目标目录: %s", targetDir)

	// ---------- T9 执行下载 ----------
	msgf("[T9] 开始下载 %d 个曲包（自动重试一次官方存储地址）...", len(items))
	failedItems := ExecuteDownload(ctx, aria2Path, targetDir, cookie.Value, items)
	if ctx.Err() != nil {
		SaveFailedLog(failedItems, "")
		return fmt.Errorf("下载被中断（Ctrl+C）：已把 %d 个未完成曲包记入 failed.txt", len(failedItems))
	}

	// ---------- T10 保存失败日志 ----------
	SaveFailedLog(failedItems, scrapeFailedReason)

	// ---------- 端到端验收 ----------
	return e2eCheck(targetDir, len(items), len(failedItems))
}

func pickSaveStyle() (SaveStyle, string, error) {
	idx, err := ShowMenu("请选择下载保存方式:", []string{
		"按曲包类型自动分目录（如 " + DownloadRoot + "常规_osu!/）",
		"统一保存到指定路径（请输入绝对/相对路径）",
	})
	if err != nil {
		return 0, "", fmt.Errorf("[T2] %w", err)
	}
	style := SaveStyle(idx + 1)
	if style == SaveStyleCustom {
		fmt.Print("请输入下载保存路径: ")
		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return 0, "", fmt.Errorf("读取路径失败: %w", err)
		}
		p := strings.TrimSpace(line)
		if p == "" {
			p = DownloadRoot
		}
		if abs, err := AbsOrRelJoin(p); err == nil {
			p = abs
		}
		if err := EnsureDir(p); err != nil {
			return 0, "", fmt.Errorf("无法创建 %s: %w", p, err)
		}
		msgf("已记录统一保存路径: %s", p)
		return style, p, nil
	}
	return style, "", nil
}

func pickCategory() (CategoryChoice, error) {
	items := make([]string, 0, len(CategoryMap))
	for i := 1; i <= len(CategoryMap); i++ {
		items = append(items, CategoryMap[i].Name)
	}
	idx, err := ShowMenu("请选择曲包分类:", items)
	if err != nil {
		return CategoryChoice{}, fmt.Errorf("[T2] %w", err)
	}
	catID := idx + 1
	cat := CategoryMap[catID]
	if len(cat.Modes) == 0 {
		return CategoryChoice{CatID: catID}, nil
	}
	modeIdx, err := ShowMenu(fmt.Sprintf("分类「%s」- 请选择游戏模式:", cat.Name), cat.Modes)
	if err != nil {
		return CategoryChoice{}, fmt.Errorf("[T2] %w", err)
	}
	return CategoryChoice{CatID: catID, Mode: cat.Modes[modeIdx]}, nil
}

func modeSuffix(catName, mode string) string {
	if mode == "" {
		return "（全部）"
	}
	return " / " + mode
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func writeScrapeFailure() error {
	return WriteLines(filepath.Join(UrlOutputDir, "urls.txt"), []string{"# 抓取失败，无下载链接"})
}

func e2eCheck(targetDir string, total, failed int) error {
	done := CountExpectedFiles(targetDir)
	expected := total - failed
	fmt.Println("\n========== 端到端验收 ==========")
	msgf("待下载: %d, 失败: %d, 目标目录实际完成: %d", total, failed, done)
	if expected < 0 {
		expected = 0
	}
	if done >= expected {
		msgf("PASS")
		return nil
	}
	msgf("FAIL: 部分文件缺失（目标目录: %s）", targetDir)
	msgf("      失败明细已写入 %s", filepath.Join(UrlOutputDir, "failed.txt"))
	return fmt.Errorf("FAIL: 部分文件缺失（完成 %d，预期至少 %d）", done, expected)
}
