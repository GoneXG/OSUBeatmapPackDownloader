package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubNetErr 模拟网络层错误（DNS/超时/连接被拒绝等），用于验证网络错误不被当成「不存在」。
type stubNetErr struct{}

func (stubNetErr) Error() string   { return "dial tcp: connectex: connection refused" }
func (stubNetErr) Timeout() bool   { return false }
func (stubNetErr) Temporary() bool { return false }

func makePacks(n int) []Pack {
	packs := make([]Pack, 0, n)
	for i := 1; i <= n; i++ {
		packs = append(packs, Pack{
			Tag:     fmt.Sprintf("S%d", i),
			Name:    fmt.Sprintf("osu! Beatmap Pack #%d", i),
			PageURL: fmt.Sprintf("https://osu.ppy.sh/beatmaps/packs/S%d", i),
		})
	}
	return packs
}

// TestCheckLinkDistinguishesStatuses 覆盖 200 / 404 / 超时三种响应。
func TestCheckLinkDistinguishesStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(500 * time.Millisecond):
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	cases := []struct {
		path string
		want linkCheckStatus
	}{
		{"/ok", linkOK},
		{"/missing", linkMissing},
		{"/slow", linkError},
		{"/server-error", linkError},
	}
	for _, tc := range cases {
		got, err := checkLink(context.Background(), client, srv.URL+tc.path)
		if got != tc.want {
			t.Fatalf("checkLink(%s) = %v (err=%v), 期望 %v", tc.path, got, err, tc.want)
		}
		if got == linkError && !isNetworkFailure(err) && err == nil {
			t.Fatalf("网络层错误必须带上错误信息: %s", tc.path)
		}
	}
}

// TestNetworkErrorIsNotTreatedAsMissing 覆盖「网络错误不被当成不存在」：
// 注入的 check 返回网络错误时，曲包必须保留为待重试，而不是被判成链接不存在。
func TestNetworkErrorIsNotTreatedAsMissing(t *testing.T) {
	packs := makePacks(3)
	var calls int64
	deps := linkRepairDeps{
		check: func(context.Context, string) (linkCheckStatus, error) {
			atomic.AddInt64(&calls, 1)
			return linkError, stubNetErr{}
		},
		concurrency: 1,
		sampleRate:  1,
	}

	report := repairPackLinks(context.Background(), packs, deps)
	for i, st := range report.States {
		if st.Verified {
			t.Fatalf("网络错误不应让第 %d 个曲包被判为可用: %+v", i, st)
		}
		if st.NetErr == nil {
			t.Fatalf("第 %d 个曲包应记录网络错误", i)
		}
	}
	if len(report.Failed) != len(packs) {
		t.Fatalf("网络错误时所有曲包都应记为未取得链接，实际 %d/%d", len(report.Failed), len(packs))
	}
}

// TestVerifyStatesBoundedConcurrency 断言在途校验数不超过并发上限，且每个曲包恰好被校验一次。
func TestVerifyStatesBoundedConcurrency(t *testing.T) {
	const (
		packCount   = 24
		concurrency = 4
	)
	packs := makePacks(packCount)

	var (
		inFlight int64
		peak     int64
		callsMu  sync.Mutex
		calls    = map[string]int{}
	)
	states := make([]packLinkState, len(packs))
	for i, p := range packs {
		states[i] = packLinkState{Pack: p, Candidates: CandidateStructures(p)[:1]}
	}
	deps := linkRepairDeps{
		check: func(ctx context.Context, link string) (linkCheckStatus, error) {
			cur := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			mu := strings.Split(link, "%20")
			callsMu.Lock()
			calls[mu[0]]++
			callsMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			return linkOK, nil
		},
		concurrency: concurrency,
		sampleRate:  1,
	}

	captureStdout(t, func() {
		verifyStates(context.Background(), states, allIndices(len(states)), deps)
	})

	if got := atomic.LoadInt64(&peak); got > concurrency {
		t.Fatalf("在途校验峰值 %d 超过并发上限 %d", got, concurrency)
	} else if got < 2 {
		t.Fatalf("在途校验峰值只有 %d，说明未真正并发执行", got)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(calls) != packCount {
		t.Fatalf("应恰好校验 %d 个不同链接，实际 %d 个", packCount, len(calls))
	}
}

// TestVerifyStatesPrintsOneLinePerPack 断言进度输出每条独占一行（不与其他输出交错）。
func TestVerifyStatesPrintsOneLinePerPack(t *testing.T) {
	const (
		packCount   = 6
		concurrency = 4
	)
	packs := makePacks(packCount)
	states := make([]packLinkState, len(packs))
	for i, p := range packs {
		states[i] = packLinkState{Pack: p, Candidates: CandidateStructures(p)[:1]}
	}
	deps := linkRepairDeps{
		check:       func(context.Context, string) (linkCheckStatus, error) { return linkOK, nil },
		concurrency: concurrency,
		sampleRate:  1,
	}

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	os.Stdout = w
	verifyStates(context.Background(), states, allIndices(len(states)), deps)
	os.Stdout = origStdout
	w.Close()
	raw, _ := io.ReadAll(r)
	r.Close()

	lineRe := regexp.MustCompile(`^\s*校验链接 \((\d)/6\): S\d+ ✓$`)
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	if len(lines) != packCount {
		t.Fatalf("进度输出行数 = %d, 期望 %d；实际输出:\n%s", len(lines), packCount, raw)
	}
	seen := map[string]bool{}
	for _, line := range lines {
		m := lineRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			t.Fatalf("进度行不是完整的一行（可能与其他输出交错）: %q", line)
		}
		if seen[m[1]] {
			t.Fatalf("进度序号 %s 重复出现", m[1])
		}
		seen[m[1]] = true
	}
	for i := 1; i <= packCount; i++ {
		if !seen[fmt.Sprintf("%d", i)] {
			t.Fatalf("缺少进度序号 %d，实际: %v", i, seen)
		}
	}
}

func TestClampLookupConcurrency(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, {-3, 1}, {1, 1}, {4, 4}, {8, 8}, {20, 8},
	}
	for _, c := range cases {
		if got := ClampLookupConcurrency(c.in); got != c.want {
			t.Errorf("ClampLookupConcurrency(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}
