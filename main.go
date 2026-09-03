package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
)

const backOption = "← 返回上级菜单"

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

	// 可选启动参数：下载根目录（所有文件混存于此）。
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

	// ---------- T2 选择分类（子分类菜单支持返回上级） ----------
	choice, err := pickCategory()
	if err != nil {
		return err
	}
	cat := CategoryMap[choice.CatID]
	msgf("[T2] 已选择: %s%s", cat.Name, modeSuffix(choice.Mode))

	// ---------- T4 构造列表页 URL ----------
	pageURL, err := BuildURL(choice.CatID, choice.Mode)
	if err != nil {
		return fmt.Errorf("[T4] %w", err)
	}
	if !ValidatePageURL(pageURL) {
		return fmt.Errorf("[T4] 生成的 URL 非法: %s", pageURL)
	}
	msgf("[T4] 列表页: %s", pageURL)

	// ---------- T5 抓取链接：先无 Cookie 直连，失败再手动粘贴 Cookie 重爬 ----------
	msgf("[T5] 先尝试无 Cookie 直连抓取（分页自动翻页，Ctrl+C 可中止）...")
	cookie := ""
	scrape := ScrapeLinks(ctx, choice.CatID, choice.Mode, "")
	if scrape.Failed && ctx.Err() == nil {
		msgf("      直连抓取失败: %s", scrape.Reason)
		msgf("      将改为手动粘贴 osu_sid Cookie 后重新爬取真实链接。")
		for attempt := 1; attempt <= 3; attempt++ {
			val, skip := ManualCookieInput()
			if skip {
				break
			}
			cookie = val
			msgf("      使用 Cookie 重新抓取（第 %d 次）...", attempt)
			scrape = ScrapeLinks(ctx, choice.CatID, choice.Mode, cookie)
			if !scrape.Failed {
				msgf("      带 Cookie 抓取成功。")
				break
			}
			msgf("      带 Cookie 抓取仍失败: %s", scrape.Reason)
		}
	}

	var scrapeFailedReason string
	if scrape.Failed {
		scrapeFailedReason = scrape.Reason
		msgf("      [WARN] 抓取失败: %s", scrapeFailedReason)
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

	// ---------- 下载目标：全部混存到下载根目录 ----------
	targetDir, err := ResolveDownloadDir()
	if err != nil {
		return err
	}
	msgf("      下载目标目录（混存）: %s", targetDir)

	// ---------- T9 执行下载 ----------
	msgf("[T9] 开始下载 %d 个曲包（自动重试一次官方存储地址）...", len(items))
	failedItems := ExecuteDownload(ctx, aria2Path, targetDir, cookie, items)
	if ctx.Err() != nil {
		SaveFailedLog(failedItems, "")
		return fmt.Errorf("下载被中断（Ctrl+C）：已把 %d 个未完成曲包记入 failed.txt", len(failedItems))
	}

	// ---------- T10 保存失败日志 ----------
	SaveFailedLog(failedItems, scrapeFailedReason)

	// ---------- 端到端验收 ----------
	return e2eCheck(targetDir, len(items), len(failedItems))
}

// pickCategory 选择曲包分类；带子模式时允许在子菜单“返回上级”重新选分类。
func pickCategory() (CategoryChoice, error) {
	catNames := make([]string, 0, len(CategoryMap))
	for i := 1; i <= len(CategoryMap); i++ {
		catNames = append(catNames, CategoryMap[i].Name)
	}

	for {
		idx, err := ShowMenu("请选择曲包分类:", catNames)
		if err != nil {
			return CategoryChoice{}, fmt.Errorf("[T2] %w", err)
		}
		catID := idx + 1
		cat := CategoryMap[catID]
		if len(cat.Modes) == 0 {
			return CategoryChoice{CatID: catID}, nil
		}

		modeItems := append(append([]string{}, cat.Modes...), backOption)
		modeIdx, err := ShowMenu(fmt.Sprintf("分类「%s」- 请选择游戏模式:", cat.Name), modeItems)
		if err != nil {
			return CategoryChoice{}, fmt.Errorf("[T2] %w", err)
		}
		if modeIdx == len(cat.Modes) {
			continue // 返回上级：重新选择分类
		}
		return CategoryChoice{CatID: catID, Mode: cat.Modes[modeIdx]}, nil
	}
}

func modeSuffix(mode string) string {
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
