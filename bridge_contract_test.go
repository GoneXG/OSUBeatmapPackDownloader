package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptJobFromURL 模拟浏览器脚本解析作业 URL 片段，取出凭据、端口与分类。
func scriptJobFromURL(t *testing.T, raw string) (token string, port int, siteType string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析启动 URL 失败: %v", err)
	}
	q, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("解析 URL 片段失败: %v", err)
	}
	token = q.Get("opd")
	siteType = q.Get("type")
	parsedPort, err := strconv.Atoi(q.Get("port"))
	if err != nil || parsedPort <= 0 {
		t.Fatalf("片段缺少可用端口: %q", u.Fragment)
	}
	if token == "" {
		t.Fatalf("片段缺少一次性凭据: %q", u.Fragment)
	}
	return token, parsedPort, siteType
}

// stubBrowser 打桩浏览器拉起：把启动 URL 交给测试里的「模拟脚本」。
func stubBrowser(t *testing.T) <-chan string {
	t.Helper()
	orig := openBrowserFunc
	t.Cleanup(func() { openBrowserFunc = orig })
	launched := make(chan string, 1)
	openBrowserFunc = func(target string) error {
		launched <- target
		return nil
	}
	return launched
}

// TestFetchPacksViaBrowserEndToEnd 用「模拟脚本」跑通程序侧完整流程：
// 拉起浏览器（打桩）→ 解析片段 → 按脚本顺序握手/上报 → 回传曲包列表。
// 这条链路覆盖「脚本不握手时程序必然超时」的那一类契约问题。
func TestFetchPacksViaBrowserEndToEnd(t *testing.T) {
	launched := stubBrowser(t)

	type outcome struct {
		srv   *bridgeServer
		packs []Pack
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		srv, packs, err := fetchPacksViaBrowser(context.Background(), 1, "https://osu.ppy.sh/beatmaps/packs?type=standard")
		done <- outcome{srv: srv, packs: packs, err: err}
	}()

	var launchURL string
	select {
	case launchURL = <-launched:
	case <-time.After(5 * time.Second):
		t.Fatal("程序未拉起浏览器")
	}
	token, port, siteType := scriptJobFromURL(t, launchURL)
	if siteType != "standard" {
		t.Fatalf("片段里的分类应为 standard，实际 %q", siteType)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	// 脚本侧的最小闭环，顺序与 userscript 一致：心跳 → 握手 → 进度 → 载荷。
	steps := []struct {
		path    string
		payload any
	}{
		{"/v1/heartbeat", bridgeProgress{Protocol: bridgeProtocolVersion}},
		{"/v1/hello", bridgeHandshake{Protocol: bridgeProtocolVersion, Type: siteType, LoggedIn: true}},
		{"/v1/progress", bridgeProgress{Protocol: bridgeProtocolVersion, Stage: "scrape", Message: "已抓取第 1 页"}},
		{"/v1/packs", PackListPayload{
			Protocol: bridgeProtocolVersion,
			Type:     siteType,
			Total:    2,
			Packs: []PayloadPack{
				{Tag: "SM111", Name: "osu!mania Beatmap Pack #111", URL: "https://osu.ppy.sh/beatmaps/packs/SM111"},
				{Tag: "SC1", Name: "osu!catch Beatmap Pack #1", URL: "https://osu.ppy.sh/beatmaps/packs/SC1"},
			},
		}},
	}
	for _, step := range steps {
		code, body := postBridgeURL(t, base, step.path, token, osuSiteOrigin, step.payload)
		if code != http.StatusOK {
			t.Fatalf("%s 应被接受，实际状态码 %d：%s", step.path, code, body)
		}
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("程序侧流程失败: %v", got.err)
		}
		defer got.srv.Close()
		if len(got.packs) != 2 {
			t.Fatalf("应回传 2 个曲包，实际 %d 个", len(got.packs))
		}
		if got.packs[0].Tag != "SM111" || got.packs[1].Tag != "SC1" {
			t.Fatalf("曲包内容不符合预期: %+v", got.packs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待程序返回超时")
	}
}

// TestFetchPacksViaBrowserStopsWhenNotLoggedIn 覆盖脚本上报未登录时的程序侧行为：
// 立即带可读原因终止（不进入后续流程），而不是一直等到超时。
func TestFetchPacksViaBrowserStopsWhenNotLoggedIn(t *testing.T) {
	launched := stubBrowser(t)

	done := make(chan error, 1)
	go func() {
		_, _, err := fetchPacksViaBrowser(context.Background(), 1, "https://osu.ppy.sh/beatmaps/packs?type=standard")
		done <- err
	}()

	var launchURL string
	select {
	case launchURL = <-launched:
	case <-time.After(5 * time.Second):
		t.Fatal("程序未拉起浏览器")
	}
	token, port, siteType := scriptJobFromURL(t, launchURL)
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	code, body := postBridgeURL(t, base, "/v1/hello", token, osuSiteOrigin,
		bridgeHandshake{Protocol: bridgeProtocolVersion, Type: siteType, LoggedIn: false, Message: "详情页提示需要登录"})
	if code != http.StatusOK {
		t.Fatalf("握手应被接受，实际 %d：%s", code, body)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("未登录时应返回错误")
		}
		if !strings.Contains(err.Error(), "未登录") {
			t.Fatalf("错误信息应说明未登录，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未登录时应立即返回，不应等待到超时")
	}
}

// TestUserscriptCoversBridgeProtocol 静态校验脚本与程序侧的协议契约：
// 脚本必须调用全部端点、协议版本必须一致，且 README 内嵌脚本与仓库文件逐字一致。
func TestUserscriptCoversBridgeProtocol(t *testing.T) {
	const scriptPath = "userscript/osu-pack-bridge.user.js"
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读取脚本失败: %v", err)
	}
	script := string(raw)

	// 端点清单来自 bridge.go 的 ServeHTTP：少任何一个都会让对应环节静默失效。
	for _, endpoint := range []string{
		"/v1/hello",
		"/v1/heartbeat",
		"/v1/progress",
		"/v1/packs",
		"/v1/fail",
		"/v1/poll",
		"/v1/resolve",
	} {
		if !strings.Contains(script, "'"+endpoint+"'") {
			t.Fatalf("脚本缺少协议端点调用: %s（本地进程会因此等待超时）", endpoint)
		}
	}

	wantVersion := "const PROTOCOL = " + strconv.Itoa(bridgeProtocolVersion) + ";"
	if !strings.Contains(script, wantVersion) {
		t.Fatalf("脚本协议版本与程序不一致，应包含 %q", wantVersion)
	}

	// README 内嵌的完整代码块必须与仓库文件一致，避免用户装到旧版本。
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("读取 README 失败: %v", err)
	}
	embedded, ok := fencedBlock(string(readme), "```javascript")
	if !ok {
		t.Fatal("README 中找不到 javascript 代码块")
	}
	if embedded != strings.TrimRight(script, "\r\n") {
		t.Fatal("README 内嵌的脚本与 userscript/osu-pack-bridge.user.js 不一致，请重新同步")
	}
}

// fencedBlock 取出 markdown 中第一个指定围栏起始的代码块内容。
func fencedBlock(markdown, fence string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if line == fence {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "```" {
			return strings.Join(lines[start+1:i], "\n"), true
		}
	}
	return "", false
}
