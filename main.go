package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

const backOption = "← 返回上级菜单"

var (
	flagDownloadDir = flag.String("dir", DownloadRoot, "下载根目录")
	flagProxy       = flag.String("proxy", "", "HTTP/HTTPS 代理，例如 http://127.0.0.1:7890；留空则使用系统代理")
	flagNoPause     = flag.Bool("nopause", false, "结束后不等待回车（脚本/自动化调用时使用）")
	flagLookupConc  = flag.Int("lookup-concurrency", defaultLookupConcurrency, "浏览器解析与链接校验的并发数（1~8）")
	flagProgress    = flag.String("progress", "bar", "下载进度显示方式: bar|line|off（非交互输出自动按 line 显示）")
	flagPacksFile   = flag.String("packs", "", "本地曲包列表文件（JSON 载荷）；提供后跳过浏览器抓取")
	flagVerifyRate  = flag.Float64("verify-rate", defaultVerifyRate, "链接抽检比例 0.01~1（默认 0.1；1 = 逐条全量校验）")
	flagUnzip       = flag.Bool("unzip", false, "下载完成后自动解压压缩包（默认关闭）")
	flagUnzipDir    = flag.String("unzip-dir", "", "解压目标目录（默认：下载目录的同级 unzip 目录）")
	flagUnzipLayout = flag.String("unzip-layout", "flat", "解压布局: flat（全部平铺，默认）| per-pack（每个曲包一个子目录）")
	flagUnzipDelete = flag.Bool("unzip-delete", false, "解压成功后删除对应压缩包（默认保留）")
)

func main() {
	flag.Parse()
	proxyURL = strings.TrimSpace(*flagProxy)

	if err := run(); err != nil {
		fmt.Printf("\n程序中止: %v\n", err)
		pauseBeforeExit()
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("==============================================")
	fmt.Println("  osu! Beatmap Pack 曲包下载器")
	fmt.Println("==============================================")

	// 启动参数已在 main 中解析：-dir 指定下载根目录（所有文件混存于此）。
	if *flagDownloadDir != "" {
		DownloadRoot = filepath.Clean(*flagDownloadDir)
	}
	LookupConcurrency = ClampLookupConcurrency(*flagLookupConc)
	verifyRate := ClampVerifyRate(*flagVerifyRate)
	if mode, ok := ParseProgressMode(*flagProgress); ok {
		ProgressMode = mode
	} else {
		msgf("提示: -progress=%q 无法识别，改用 bar。可选: bar | line | off", *flagProgress)
		ProgressMode = progressBarMode
	}
	extractCfg := buildExtractConfig(*flagUnzip, *flagUnzipDir, *flagUnzipLayout, *flagUnzipDelete)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// ---------- 初始化目录 ----------
	msgf("正在初始化目录 %s 与 %s ...", UrlOutputDir, DownloadRoot)
	if err := EnsureDir(UrlOutputDir); err != nil {
		return fmt.Errorf("无法创建 %s: %w", UrlOutputDir, err)
	}
	if err := EnsureDir(DownloadRoot); err != nil {
		return fmt.Errorf("无法创建 %s: %w", DownloadRoot, err)
	}
	// failed.txt 是「本次运行」的失败记录：开跑前清空，避免用户看到上一轮的残留。
	_ = os.Remove(filepath.Join(UrlOutputDir, "failed.txt"))

	// ---------- 选择分类（子分类菜单支持返回上级） ----------
	choice, err := pickCategory()
	if err != nil {
		return err
	}
	cat := CategoryMap[choice.CatID]
	msgf("已选择: %s%s", cat.Name, modeSuffix(choice.Mode))

	// ---------- 构造列表页 URL ----------
	pageURL, err := BuildURL(choice.CatID, choice.Mode)
	if err != nil {
		return err
	}
	if !ValidatePageURL(pageURL) {
		return fmt.Errorf("生成的列表页 URL 非法: %s", pageURL)
	}
	msgf("列表页: %s", pageURL)

	// ---------- 取得曲包列表（浏览器桥接，或本地列表文件兜底） ----------
	var (
		srv   *bridgeServer
		packs []Pack
	)
	if *flagPacksFile != "" {
		payload, err := LoadPackListFile(*flagPacksFile)
		if err != nil {
			return err
		}
		msgf("已从本地列表文件读取 %d 个曲包：%s（跳过浏览器抓取）", len(payload.Packs), *flagPacksFile)
		packs = payload.ToPacks()
	} else {
		srv, packs, err = fetchPacksViaBrowser(ctx, choice.CatID, pageURL)
		if err != nil {
			return err
		}
		if srv != nil {
			defer srv.Close()
		}
	}

	// ---------- 按模式过滤 ----------
	matched := make([]Pack, 0, len(packs))
	for _, p := range packs {
		if !PackMatchesMode(choice.CatID, choice.Mode, p) {
			continue
		}
		matched = append(matched, p)
	}
	if len(matched) != len(packs) {
		msgf("按模式 %q 过滤后保留 %d 个曲包。", choice.Mode, len(matched))
	}
	packs = matched
	if len(packs) == 0 {
		return fmt.Errorf("该分类/模式下没有匹配的曲包")
	}
	msgf("抓取完成，共 %d 个曲包。", len(packs))
	if ctx.Err() != nil {
		return fmt.Errorf("用户中断（Ctrl+C），本次任务中止")
	}

	// ---------- 选定曲包区间（仅常规分类下生效；按 tag 编号/最新数量筛选） ----------
	if choice.CatID == 1 {
		packs, err = askPackRange(packs)
		if err != nil {
			return err
		}
		if len(packs) == 0 {
			return fmt.Errorf("区间内没有匹配的曲包")
		}
	}

	// ---------- 构造链接 + 抽检 + 定向修复 ----------
	msgf("正在构造并校验下载链接（抽检比例 %.0f%%，并发 %d）...", verifyRate*100, LookupConcurrency)
	report := repairPackLinks(ctx, packs, newRealRepairDeps(srv, LookupConcurrency, verifyRate))
	if report.NeedLogin {
		return fmt.Errorf("解析过程中检测到未登录：请在浏览器登录 osu! 后重新运行")
	}
	resolved := make([]Pack, 0, len(packs))
	for _, st := range report.States {
		if !st.Adopted() {
			continue
		}
		p := st.Pack
		p.DirectURL = st.URL
		resolved = append(resolved, p)
	}
	msgf("      链接来源：逐条校验 %d 个，按区段结构推断 %d 个。", report.Verified, report.Inferred)
	if report.Inferred > 0 {
		msgf("      推断依据实测规律（新区段=官网名+.zip，老区段=历史短名+.7z）；如需逐条确认请加 -verify-rate 1。")
		msgf("      推断错的链接会在下载阶段失败并自动经浏览器解析真实链接重试，不会静默留下坏文件。")
	}
	if len(report.Failed) > 0 {
		msgf("      有 %d 个曲包没有任何可用链接，将记入 failed.txt。", len(report.Failed))
	}
	if len(resolved) == 0 {
		if err := writeScrapeFailure(); err != nil {
			return err
		}
		SaveFailedLog(nil, "没有任何曲包取得可用下载链接")
		return fmt.Errorf("没有任何曲包取得可用下载链接")
	}
	packs = resolved

	// ---------- 写入 urls.txt ----------
	var urls []string
	items := make([]aria2Item, 0, len(packs))
	for _, p := range packs {
		urls = append(urls, p.DirectURL)
		items = append(items, aria2Item{URL: p.DirectURL, Pack: p})
	}
	urlsPath := filepath.Join(UrlOutputDir, "urls.txt")
	if err := WriteLines(urlsPath, urls); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", urlsPath, err)
	}
	if fi, err := os.Stat(urlsPath); err != nil || fi.Size() <= 10 {
		return fmt.Errorf("%s 内容过小，疑似无有效链接", urlsPath)
	}
	msgf("已写入 %s（%d 行, %d 字节）", urlsPath, len(urls), fileSize(urlsPath))

	// ---------- 选择下载方式 ----------
	method := AskUser(
		"请选择下载方式: [1] 调用 aria2 下载  [2] 仅保留链接文件",
		map[string]bool{"1": true, "2": true},
		3,
		"2",
	)
	msgf("      选择: %s", map[string]string{"1": "调用 aria2 下载", "2": "仅保留链接文件"}[method])

	if method == "2" {
		if srv != nil {
			srv.FinishResolve()
		}
		msgf("      已完成：链接保存在 %s，直接退出。", urlsPath)
		return nil
	}

	// ---------- 检查 aria2 ----------
	aria2Path, err := CheckAria2()
	if err != nil {
		msgf("%v", err)
		msgf("      未找到 aria2c，自动转为仅保留链接文件。")
		msgf("      提示: 将 aria2c.exe 放入 %s 目录后重跑即可下载。", ToolsDir)
		return nil
	}
	msgf("aria2c: %s", aria2Path)

	// ---------- 下载目标：全部混存到下载根目录 ----------
	targetDir, err := ResolveDownloadDir()
	if err != nil {
		return err
	}
	msgf("      下载目标目录（混存）: %s", targetDir)

	// ---------- 解压会话（默认关闭；仅保留链接文件时不生效） ----------
	extraction := prepareExtraction(extractCfg, method, targetDir, items)
	if extraction != nil {
		keep := "默认保留"
		if extractCfg.DeleteAfter {
			keep = "成功解压后删除"
		}
		msgf("      已启用自动解压: 目标 %s，布局 %s，压缩包%s。", extraction.root, extractCfg.Layout, keep)
		extraction.Start()
	}

	// ---------- 执行下载 ----------
	msgf("开始下载 %d 个曲包（失败时经浏览器重查真实链接）...", len(items))
	failedItems := ExecuteDownload(ctx, aria2Path, targetDir, items, srv)

	// 下载（含经浏览器重试）结束后再收尾解压，保证最后完成的压缩包也被处理。
	var extractStats *extractStats
	if extraction != nil {
		extractStats = extraction.Stop()
	}
	if ctx.Err() != nil {
		SaveFailedLog(failedItems, "")
		return fmt.Errorf("下载被中断（Ctrl+C）：已把 %d 个未完成曲包记入 failed.txt", len(failedItems))
	}
	if srv != nil {
		srv.FinishResolve()
	}

	// ---------- 保存失败日志 ----------
	SaveFailedLog(failedItems, "")

	// ---------- 端到端验收 ----------
	return e2eCheck(targetDir, len(items), len(failedItems), extractStats)
}

// fetchPacksViaBrowser 拉起浏览器、等待脚本握手与曲包列表载荷。
// 返回的桥接服务在后续解析阶段继续复用；用户中断或未检测到脚本时返回错误。
func fetchPacksViaBrowser(ctx context.Context, catID int, pageURL string) (*bridgeServer, []Pack, error) {
	siteType, ok := SiteTypeByCatID[catID]
	if !ok {
		return nil, nil, fmt.Errorf("分类 %d 未配置站点类型", catID)
	}
	srv, err := startBridgeServer(bridgeConfig{port: defaultBridgePort, resolveConcurrency: LookupConcurrency})
	if err != nil {
		return nil, nil, err
	}

	launchURL, err := bridgeJobURL(pageURL, srv.token, srv.Port(), siteType)
	if err != nil {
		srv.Close()
		return nil, nil, err
	}
	msgf("正在拉起浏览器并等待脚本握手（最多 %s）...", defaultHandshakeTimeout)
	if err := openBrowserFunc(launchURL); err != nil {
		msgf("      无法自动打开浏览器: %v", err)
		msgf("      请手动在浏览器中打开以下地址：")
		msgf("      %s", launchURL)
	} else {
		msgf("      已请求系统打开默认浏览器；若浏览器没有弹出，请手动打开：")
		msgf("      %s", launchURL)
	}

	hs, err := srv.WaitHandshake(ctx, defaultHandshakeTimeout)
	if err != nil {
		srv.Close()
		if errors.Is(err, errHandshakeIncomplete) {
			printHandshakeIncompleteHelp()
			return nil, nil, fmt.Errorf("桥接脚本未完成握手")
		}
		if errors.Is(err, errScriptNotDetected) {
			if n, reason := srv.Rejections(); n > 0 {
				msgf("      注意: 等待期间有 %d 个脚本请求被本地服务拒绝（最近原因：%s）。", n, reason)
				printRejectedRequestHelp(reason)
				return nil, nil, fmt.Errorf("桥接脚本请求被本地服务拒绝（%s）", reason)
			}
			printScriptMissingHelp()
			return nil, nil, fmt.Errorf("未检测到桥接脚本")
		}
		return nil, nil, err
	}
	if !hs.LoggedIn {
		srv.Close()
		msgf("      脚本报告当前浏览器会话未登录 osu!：%s", hs.Message)
		return nil, nil, fmt.Errorf("浏览器未登录 osu!，请登录后重新运行")
	}
	if hs.Message != "" {
		// 例如「脚本未完成握手但已回传列表」：功能可用，但要提醒更新脚本。
		msgf("      注意: %s", hs.Message)
	}
	msgf("      脚本已连接，正在抓取曲包列表（顺序翻页，页面右下角可看进度）...")

	payload, err := srv.WaitPayload(ctx)
	if err != nil {
		srv.Close()
		if errors.Is(err, errScriptHalted) {
			return nil, nil, err
		}
		return nil, nil, err
	}
	packs := payload.ToPacks()
	if len(packs) == 0 {
		srv.Close()
		return nil, nil, fmt.Errorf("脚本回传的曲包列表为空")
	}
	msgf("      脚本已回传 %d 个曲包。", len(packs))
	return srv, packs, nil
}

// newRealRepairDeps 组装真实运行的修复依赖：HEAD 校验 + 经浏览器解析。
func newRealRepairDeps(srv *bridgeServer, concurrency int, rate float64) linkRepairDeps {
	client := getHTTPClient()
	var resolve func(context.Context, []Pack) []packResolveOutcome
	if srv != nil {
		resolve = func(ctx context.Context, packs []Pack) []packResolveOutcome {
			return srv.resolvePacks(ctx, packs)
		}
	}
	return linkRepairDeps{
		check: func(ctx context.Context, link string) (linkCheckStatus, error) {
			// 单次校验超时可控：网络层错误会被区分出来，而不是当成「链接不存在」。
			headCtx, cancel := context.WithTimeout(ctx, linkCheckTimeout)
			defer cancel()
			return checkLink(headCtx, client, link)
		},
		resolve:     resolve,
		concurrency: concurrency,
		sampleRate:  rate,
		window:      defaultRepairWindow,
	}
}

// linkCheckTimeout 单次 HEAD 校验的超时上限。
const linkCheckTimeout = 15 * time.Second

// ClampVerifyRate 把 -verify-rate 收敛到 0.01~1。
func ClampVerifyRate(rate float64) float64 {
	if rate <= 0 {
		msgf("提示: -verify-rate=%.3f 超出范围(0.01~1)，改用 %.2f（抽检）。", rate, defaultVerifyRate)
		return defaultVerifyRate
	}
	if rate > 1 {
		msgf("提示: -verify-rate=%.3f 超出范围(0.01~1)，改用 1（逐条全量校验）。", rate)
		return 1
	}
	if rate < 0.01 {
		msgf("提示: -verify-rate=%.3f 过小，改用 0.01。", rate)
		return 0.01
	}
	return rate
}

// buildExtractConfig 组装解压配置：-unzip-layout 无法识别时提示并回退扁平布局。
// 默认（-unzip 未指定）关闭解压，行为与改动前完全一致。
func buildExtractConfig(enabled bool, dir, layout string, deleteAfter bool) ExtractConfig {
	l, ok := ParseExtractLayout(layout)
	if !ok {
		msgf("提示: -unzip-layout=%q 无法识别，改用 flat。可选: flat | per-pack", layout)
	}
	return ExtractConfig{
		Enabled:     enabled,
		Dir:         strings.TrimSpace(dir),
		Layout:      l,
		DeleteAfter: deleteAfter,
	}
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
			return CategoryChoice{}, err
		}
		catID := idx + 1
		cat := CategoryMap[catID]
		if len(cat.Modes) == 0 {
			return CategoryChoice{CatID: catID}, nil
		}

		modeItems := append(append([]string{}, cat.Modes...), backOption)
		modeIdx, err := ShowMenu(fmt.Sprintf("分类「%s」- 请选择游戏模式:", cat.Name), modeItems)
		if err != nil {
			return CategoryChoice{}, err
		}
		if modeIdx == len(cat.Modes) {
			continue // 返回上级：重新选择分类
		}
		return CategoryChoice{CatID: catID, Mode: cat.Modes[modeIdx]}, nil
	}
}

// askPackRange 抓取完成后询问是否限定曲包区间（仅常规分类使用），返回筛选后的列表。
// 编号指曲包 tag 中的数字部分，例如 osu!mania 的 SM90-SM168 请输入 90-168。
func askPackRange(packs []Pack) ([]Pack, error) {
	if len(packs) == 0 {
		return packs, nil
	}
	loN, hiN := numberExtent(packs)
	for {
		fmt.Printf("\n当前共有 %d 个曲包（编号范围 %d ~ %d），可限定区间：\n", len(packs), loN, hiN)
		fmt.Println("  （编号指曲包 tag 里的数字，如 SM90-SM168 请输入 90-168）")
		fmt.Println("  直接回车      = 全部保留")
		fmt.Println("  输入 100-500  = 只保留编号 100 到 500")
		fmt.Println("  输入 -300     = 只保留编号 <= 300")
		fmt.Println("  输入 800-     = 只保留编号 >= 800")
		fmt.Println("  输入 最新50   = 只保留最新的 50 个（也可输入 last50）")
		fmt.Print("请输入区间: ")
		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("读取输入失败: %w", err)
		}
		sel, ok := parsePackSelector(line)
		if !ok {
			fmt.Println("无法识别该输入，请参考上方示例重新输入。")
			continue
		}
		filtered := applyPackSelector(packs, sel)
		if len(filtered) == 0 {
			fmt.Println("该区间内没有匹配的曲包，请重新输入。")
			continue
		}
		if len(filtered) != len(packs) {
			msgf("已按区间筛选，保留 %d 个曲包。", len(filtered))
		}
		return filtered, nil
	}
}

func modeSuffix(mode string) string {
	if mode == "" {
		return "（全部）"
	}
	return " / " + mode
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

func e2eCheck(targetDir string, total, failed int, extraction *extractStats) error {
	// 未启用解压时维持原有口径（统计下载目录里的压缩包）；
	// 启用解压时，压缩包存在或该曲包已成功解压都算完成（可能已删除压缩包）。
	done := countVerifiableFiles(targetDir, extraction)
	expected := total - failed
	fmt.Println("\n========== 端到端验收 ==========")
	msgf("待下载: %d, 失败: %d, 目标目录实际完成: %d", total, failed, done)
	if expected < 0 {
		expected = 0
	}
	if done >= expected && !(expected == 0 && total > 0) {
		msgf("PASS")
		return nil
	}
	if total > 0 && done == 0 {
		msgf("注意: 没有任何曲包下载成功，请查看上方失败原因与 failed.txt。")
	}
	msgf("FAIL: 部分文件缺失（目标目录: %s）", targetDir)
	msgf("      失败明细已写入 %s", filepath.Join(UrlOutputDir, "failed.txt"))
	return fmt.Errorf("FAIL: 部分文件缺失（完成 %d，预期至少 %d）", done, expected)
}

// printScriptMissingHelp 打印「未检测到脚本」时的安装与排查指引。
func printScriptMissingHelp() {
	msgf("      未检测到桥接脚本，本次抓取无法进行。请按顺序排查：")
	msgf("        1) 是否已安装脚本管理器（Tampermonkey / Violentmonkey）？")
	msgf("        2) 是否已安装桥接脚本 userscript/osu-pack-bridge.user.js？（README「脚本安装」章节有安装链接与完整代码）")
	msgf("        3) 脚本是否处于启用状态？安装后请确认 osu.ppy.sh 页面上的脚本已开启。")
	msgf("        4) 若浏览器未自动打开，请手动打开上面打印的地址，页面右下角会出现「osu! Pack Bridge」浮层。")
	msgf("      安装完成、脚本启用后，重新运行本程序即可。")
	msgf("      兜底：也可以用 -packs <本地列表文件> 跳过浏览器抓取（见 README）。")
}

// printRejectedRequestHelp 打印「脚本确实连上了，但请求被本地服务拒绝」时的排查指引。
// 与「未检测到脚本」不同：这里能确定脚本已经发出请求，问题出在鉴权或版本上。
func printRejectedRequestHelp(reason string) {
	msgf("      桥接脚本已经连上本地服务，但请求被拒绝（%s）。请按顺序排查：", reason)
	msgf("        1) 浏览器里是否开着上一次运行留下的旧页面？旧页面的凭据早已失效，会不断被拒；请关掉旧标签页后重试。")
	msgf("        2) 确认脚本版本与仓库 userscript/osu-pack-bridge.user.js 一致（脚本管理器点「检查更新」或重新安装），更新后刷新页面。")
	msgf("        3) 若刚更新过程序，请关掉所有 osu! 曲包页面再重新运行，让程序带着新凭据打开新页面。")
	msgf("      脚本与程序都来自同一仓库；两者的协议版本与凭据必须配对。")
}

// printHandshakeIncompleteHelp 打印「脚本已连上但未完成握手」时的排查指引。
// 这种情况说明脚本确实在跟本程序通信（心跳/进度能收到），只是协议没对齐，
// 最常见的原因是浏览器里装的是旧版脚本。
func printHandshakeIncompleteHelp() {
	msgf("      桥接脚本已连上本地服务（能收到它的心跳/进度），但没有完成握手。")
	msgf("      这通常说明浏览器里安装的是旧版脚本，与本程序协议不一致。请按顺序排查：")
	msgf("        1) 打开脚本管理器的脚本管理页，确认 osu! Pack Bridge 的版本与仓库 userscript/osu-pack-bridge.user.js 一致；")
	msgf("        2) 在脚本管理器里对该脚本点「检查更新」（或删掉后按 README 重新安装）；")
	msgf("        3) 更新后刷新 osu.ppy.sh 页面，再重新运行本程序。")
	msgf("      脚本与程序都来自同一仓库；两者的协议版本必须一致（当前程序支持协议 1）。")
}

// printNetworkHelp 打印连不上站点时的排查建议。
func printNetworkHelp() {
	msgf("      无法访问站点，请按顺序排查：")
	msgf("        1) 浏览器能否打开 https://osu.ppy.sh/beatmaps/packs?type=standard；打不开说明是本机网络问题；")
	msgf("        2) 若在受限环境（IDE/沙箱内置终端、虚拟机、公司网络）里运行，请改用普通 PowerShell 或 CMD 直接运行本程序；")
	msgf("        3) 已经能上网但程序连不上时，多半是防火墙/安全软件拦截，放行本程序即可；")
	msgf("        4) 需要代理时启动加参数，例如: .\\osu-pack-downloader.exe -proxy http://127.0.0.1:7890")
}

// isInteractiveConsole 判断标准输入是否为控制台（双击运行、交互终端为 true；管道/重定向为 false）。
func isInteractiveConsole() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// pauseBeforeExit 在出错后等待回车，避免双击运行时窗口一闪而过、看不到错误信息。
func pauseBeforeExit() {
	if *flagNoPause || !isInteractiveConsole() {
		return
	}
	fmt.Print("\n按回车键关闭窗口...")
	_, _ = stdinReader.ReadString('\n')
}
