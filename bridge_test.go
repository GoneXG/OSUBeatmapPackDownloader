package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTestBridge 启动一个绑定到随机端口的桥接服务，测试结束自动关闭。
func newTestBridge(t *testing.T, token string) *bridgeServer {
	t.Helper()
	srv, err := startBridgeServer(bridgeConfig{token: token, port: pickFreePort(t), portAttempts: 8})
	if err != nil {
		t.Fatalf("启动桥接服务失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// pickFreePort 找一个当前空闲的端口号，避免与其它测试或本机服务冲突。
func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// postBridge 向桥接服务发送一个 JSON 请求。
func postBridge(t *testing.T, srv *bridgeServer, path, token, origin string, payload any) (int, string) {
	t.Helper()
	return postBridgeURL(t, srv.BaseURL(), path, token, origin, payload)
}

// postBridgeURL 向指定的桥接服务地址发送一个 JSON 请求（便于模拟脚本侧行为）。
func postBridgeURL(t *testing.T, baseURL, path, token, origin string, payload any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set(bridgeTokenHeader, token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestBridgeListensOnLoopbackOnly(t *testing.T) {
	srv := newTestBridge(t, "token-1")
	addr, ok := srv.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型异常: %T", srv.ln.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("桥接服务应只绑回环地址，实际 %s", addr.IP)
	}
	if srv.Port() != addr.Port {
		t.Fatalf("回报端口 %d 与实际监听端口 %d 不一致", srv.Port(), addr.Port)
	}
}

func TestBridgeFallsBackWhenPortBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	srv, err := startBridgeServer(bridgeConfig{token: "token-2", port: busyPort, portAttempts: 8})
	if err != nil {
		t.Fatalf("端口被占用时应回退到下一个可用端口，实际失败: %v", err)
	}
	defer srv.Close()

	if srv.Port() == busyPort {
		t.Fatalf("端口被占用时不应复用 %d", busyPort)
	}
	if srv.Port() <= busyPort {
		t.Fatalf("回退端口 %d 应大于被占用端口 %d", srv.Port(), busyPort)
	}
	if conn, err := net.DialTimeout("tcp", srv.BaseURL()[len("http://"):], time.Second); err != nil {
		t.Fatalf("回退后的端口不可连接: %v", err)
	} else {
		conn.Close()
	}
}

func TestBridgeRejectsMissingAndWrongToken(t *testing.T) {
	const token = "correct-token"
	srv := newTestBridge(t, token)

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "缺少凭据", token: ""},
		{name: "错误凭据", token: "wrong-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := postBridge(t, srv, "/v1/hello", tc.token, osuSiteOrigin,
				bridgeHandshake{Protocol: bridgeProtocolVersion, Type: "standard", LoggedIn: true})
			if code == http.StatusOK {
				t.Fatalf("无效凭据的请求不应被接受，实际状态码 %d", code)
			}
		})
	}

	// 被拒绝的请求不得产生任何副作用：既没有握手，也没有载荷。
	select {
	case hs := <-srv.handshakeC:
		t.Fatalf("被拒绝的请求不应触发握手: %+v", hs)
	default:
	}
	select {
	case p := <-srv.packsC:
		t.Fatalf("被拒绝的请求不应写入载荷: %+v", p)
	default:
	}
	select {
	case f := <-srv.failC:
		t.Fatalf("被拒绝的请求不应触发终止事件: %+v", f)
	default:
	}
}

func TestBridgeRejectsUnknownPath(t *testing.T) {
	srv := newTestBridge(t, "tok")
	code, body := postBridge(t, srv, "/v1/does-not-exist", "tok", osuSiteOrigin, map[string]any{"protocol": bridgeProtocolVersion})
	if code != http.StatusNotFound {
		t.Fatalf("未知路径应返回 404，实际 %d", code)
	}
	if !strings.Contains(body, "未知路径") {
		t.Fatalf("未知路径应给出可读提示，实际 %q", body)
	}
}

func TestBridgeOriginPolicy(t *testing.T) {
	const token = "origin-token"

	t.Run("来源允许", func(t *testing.T) {
		srv := newTestBridge(t, token)
		code, _ := postBridge(t, srv, "/v1/progress", token, osuSiteOrigin,
			bridgeProgress{Protocol: bridgeProtocolVersion, Stage: "scrape", Done: 1, Total: 2})
		if code != http.StatusOK {
			t.Fatalf("允许的来源应被接受，实际 %d", code)
		}
	})

	t.Run("来源不允许", func(t *testing.T) {
		srv := newTestBridge(t, token)
		code, _ := postBridge(t, srv, "/v1/progress", token, "https://evil.example.com",
			bridgeProgress{Protocol: bridgeProtocolVersion, Stage: "scrape", Done: 1, Total: 2})
		if code == http.StatusOK {
			t.Fatalf("非目标站点的来源应被拒绝，实际 %d", code)
		}
	})

	t.Run("来源缺失但有有效凭据", func(t *testing.T) {
		srv := newTestBridge(t, token)
		code, _ := postBridge(t, srv, "/v1/progress", token, "",
			bridgeProgress{Protocol: bridgeProtocolVersion, Stage: "scrape", Done: 1, Total: 2})
		if code != http.StatusOK {
			t.Fatalf("带外请求缺少来源头时应以凭据为唯一判据，实际 %d", code)
		}
	})
}

func TestParsePackListPayloadRejectsBadInput(t *testing.T) {
	good := PackListPayload{
		Protocol: bridgeProtocolVersion,
		Type:     "standard",
		Total:    1,
		Packs:    []PayloadPack{{Tag: "S1", Name: "osu! Beatmap Pack #1", URL: "https://osu.ppy.sh/beatmaps/packs/S1"}},
	}
	if _, err := ParsePackListPayload(mustJSON(t, good)); err != nil {
		t.Fatalf("合法载荷不应被拒绝: %v", err)
	}

	cases := []struct {
		name    string
		payload PackListPayload
	}{
		{
			name:    "版本不匹配",
			payload: PackListPayload{Protocol: bridgeProtocolVersion + 1, Type: "standard", Total: 1, Packs: good.Packs},
		},
		{
			name:    "字段缺失",
			payload: PackListPayload{Protocol: bridgeProtocolVersion, Type: "standard", Total: 1, Packs: []PayloadPack{{Tag: "S1", URL: "https://osu.ppy.sh/beatmaps/packs/S1"}}},
		},
		{
			name:    "条目为空",
			payload: PackListPayload{Protocol: bridgeProtocolVersion, Type: "standard", Total: 0, Packs: nil},
		},
		{
			name:    "总数不一致",
			payload: PackListPayload{Protocol: bridgeProtocolVersion, Type: "standard", Total: 9, Packs: good.Packs},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePackListPayload(mustJSON(t, tc.payload)); err == nil {
				t.Fatalf("非法载荷应被拒绝: %+v", tc.payload)
			}
		})
	}

	t.Run("超大载荷", func(t *testing.T) {
		oversized := make([]byte, maxBridgePayloadBytes+16)
		if _, err := ParsePackListPayload(oversized); err == nil {
			t.Fatal("超过上限的载荷应被拒绝")
		}
	})
}

func TestBridgeHandshakeTimeoutIsRetryable(t *testing.T) {
	srv := newTestBridge(t, "hb-token")

	// 短超时：未收到握手时应给出「未检测到脚本」的可读结论。
	hs, err := srv.WaitHandshake(context.Background(), 30*time.Millisecond)
	if err == nil {
		t.Fatalf("超时时应返回错误，实际握手: %+v", hs)
	}
	if !strings.Contains(err.Error(), errScriptNotDetected.Error()) {
		t.Fatalf("超时结论应说明未检测到脚本，实际: %v", err)
	}

	// 保持可重试：脚本稍后握手时，同一服务上的下一次等待应立即成功。
	done := make(chan bridgeHandshake, 1)
	go func() {
		h, e := srv.WaitHandshake(context.Background(), 2*time.Second)
		if e != nil {
			t.Errorf("重试等待失败: %v", e)
		}
		done <- h
	}()
	code, _ := postBridge(t, srv, "/v1/hello", "hb-token", osuSiteOrigin,
		bridgeHandshake{Protocol: bridgeProtocolVersion, Type: "standard", LoggedIn: true})
	if code != http.StatusOK {
		t.Fatalf("握手请求应被接受，实际 %d", code)
	}
	select {
	case h := <-done:
		if !h.LoggedIn || h.Type != "standard" {
			t.Fatalf("握手内容不符合预期: %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("收到握手后等待应立即结束")
	}
}

// TestBridgeHandshakeIncompleteWhenTrafficSeen 覆盖「脚本连上了但没握手」：
// 此时不能再报「未检测到脚本」，而要给出可诊断的结论（脚本版本不匹配）。
func TestBridgeHandshakeIncompleteWhenTrafficSeen(t *testing.T) {
	srv := newTestBridge(t, "no-hello-token")

	// 脚本只发了心跳（旧版脚本的行为），没有走握手。
	code, _ := postBridge(t, srv, "/v1/heartbeat", "no-hello-token", osuSiteOrigin,
		bridgeProgress{Protocol: bridgeProtocolVersion})
	if code != http.StatusOK {
		t.Fatalf("心跳应被接受，实际 %d", code)
	}

	_, err := srv.WaitHandshake(context.Background(), 100*time.Millisecond)
	if err == nil {
		t.Fatal("没有握手时应超时返回错误")
	}
	if !errors.Is(err, errHandshakeIncomplete) {
		t.Fatalf("收到过脚本请求时应判定为「未完成握手」，实际: %v", err)
	}
	if errors.Is(err, errScriptNotDetected) {
		t.Fatalf("已收到脚本请求时不应再判定为「未检测到脚本」: %v", err)
	}
}

func TestBridgeJobURLKeepsTokenInFragmentOnly(t *testing.T) {
	const token = "fragment-only-token"
	pageURL := "https://osu.ppy.sh/beatmaps/packs?type=standard"
	got, err := bridgeJobURL(pageURL, token, 27183, "standard")
	if err != nil {
		t.Fatalf("生成作业 URL 失败: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("生成的 URL 无法解析: %v", err)
	}

	// 凭据只能出现在片段里；查询参数中不得出现。
	if strings.Contains(u.RawQuery, token) || strings.Contains(u.RawQuery, "opd") || strings.Contains(u.RawQuery, "port") {
		t.Fatalf("查询参数不应包含凭据或作业信息: %q", u.RawQuery)
	}
	if !strings.Contains(u.Fragment, token) {
		t.Fatalf("片段应携带凭据: %q", u.Fragment)
	}
	if !strings.Contains(u.Fragment, "port=27183") || !strings.Contains(u.Fragment, "type=standard") {
		t.Fatalf("片段应携带端口与分类: %q", u.Fragment)
	}
	// 片段不会被发送给站点：去掉片段后应还原为原始列表页地址。
	u.Fragment = ""
	if u.String() != pageURL {
		t.Fatalf("去掉片段后应还原为列表页地址\n got: %s\nwant: %s", u.String(), pageURL)
	}
}

func TestBridgeSubmitResolveRoundTrip(t *testing.T) {
	srv := newTestBridge(t, "resolve-token")
	packs := []Pack{
		{Tag: "SM111", PageURL: "https://osu.ppy.sh/beatmaps/packs/SM111"},
		{Tag: "SC1", PageURL: "https://osu.ppy.sh/beatmaps/packs/SC1"},
	}

	type reply struct {
		results []bridgeResolveResult
		err     error
	}
	done := make(chan reply, 1)
	go func() {
		results, err := srv.SubmitResolve(context.Background(), packs, 3*time.Second)
		done <- reply{results: results, err: err}
	}()

	// 模拟脚本：轮询取任务并回执。
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, body := postBridge(t, srv, "/v1/poll", "resolve-token", osuSiteOrigin,
			bridgeProgress{Protocol: bridgeProtocolVersion})
		if code != http.StatusOK {
			t.Fatalf("轮询失败，状态码 %d", code)
		}
		var resp struct {
			Tasks []bridgeTask `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("解析轮询响应失败: %v", err)
		}
		for _, task := range resp.Tasks {
			_, _ = postBridge(t, srv, "/v1/resolve", "resolve-token", osuSiteOrigin, bridgeResolveResult{
				ID:     task.ID,
				Tag:    task.Tag,
				Status: resolveStatusOK,
				Href:   "https://packs.ppy.sh/" + task.Tag + "%20-%20x.zip",
			})
		}
		if len(resp.Tasks) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待解析任务超时")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("解析回执失败: %v", r.err)
		}
		if len(r.results) != len(packs) {
			t.Fatalf("结果数量 = %d, 期望 %d", len(r.results), len(packs))
		}
		for i, res := range r.results {
			if res.Tag != packs[i].Tag || res.Status != resolveStatusOK {
				t.Fatalf("第 %d 个结果与输入不一一对应: %+v", i, res)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SubmitResolve 未返回")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

func TestBridgeHeartbeatAge(t *testing.T) {
	srv := newTestBridge(t, "beat-token")
	postBridge(t, srv, "/v1/heartbeat", "beat-token", osuSiteOrigin, bridgeProgress{Protocol: bridgeProtocolVersion})
	// 本机时钟粒度可能粗于一次回环请求，因此这里只断言「远未超时」。
	if age := srv.LastHeartbeatAge(); age < 0 || age > bridgeHeartbeatStale {
		t.Fatalf("刚收到心跳不应判定为失联，实际 %v", age)
	}
	if srv.HeartbeatStale() {
		t.Fatal("刚收到心跳不应判定为失联")
	}
	// 心跳缺失（页面被关闭或脚本停用）时应能被本地进程察觉。
	srv.lastBeat.Store(time.Now().Add(-2 * bridgeHeartbeatStale).UnixNano())
	if !srv.HeartbeatStale() {
		t.Fatal("心跳缺失超过上限时应判定为失联")
	}
}
