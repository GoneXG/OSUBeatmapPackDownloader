package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubNetErr 模拟网络层错误（DNS/超时/连接被拒绝等），用于验证重查遇到网络错误后早停。
type stubNetErr struct{}

func (stubNetErr) Error() string   { return "dial tcp: connectex: connection refused" }
func (stubNetErr) Timeout() bool   { return false }
func (stubNetErr) Temporary() bool { return false }

func makePacks(n int) []Pack {
	packs := make([]Pack, 0, n)
	for i := 1; i <= n; i++ {
		packs = append(packs, Pack{Tag: fmt.Sprintf("S%d", i), Name: fmt.Sprintf("Pack %d", i)})
	}
	return packs
}

func TestLookupPackLinksQueriesEachPackOnceWithBoundedConcurrency(t *testing.T) {
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
	lookup := func(ctx context.Context, p Pack, cookie string) (string, bool, bool, error) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		callsMu.Lock()
		calls[p.Tag]++
		callsMu.Unlock()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return "https://packs.ppy.sh/" + p.Tag + ".zip", true, false, nil
	}

	outcomes := lookupPackLinks(context.Background(), packs, "osu_session=test", concurrency, lookup)

	if len(outcomes) != packCount {
		t.Fatalf("结果数量 = %d, 期望 %d（每个曲包一条）", len(outcomes), packCount)
	}
	for i, out := range outcomes {
		want := "https://packs.ppy.sh/" + packs[i].Tag + ".zip"
		if !out.found || out.href != want {
			t.Fatalf("第 %d 个结果与输入不一一对应: %+v（期望 %s）", i, out, want)
		}
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	for _, p := range packs {
		if calls[p.Tag] != 1 {
			t.Fatalf("曲包 %s 被查询 %d 次，期望恰好 1 次", p.Tag, calls[p.Tag])
		}
	}
	if got := atomic.LoadInt64(&peak); got > concurrency {
		t.Fatalf("在途查询峰值 %d 超过并发上限 %d", got, concurrency)
	} else if got < 2 {
		t.Fatalf("在途查询峰值只有 %d，说明未真正并发执行", got)
	}
}

func TestLookupPackLinksStopsAfterNetworkError(t *testing.T) {
	const packCount = 50
	packs := makePacks(packCount)

	var queries int64
	lookup := func(ctx context.Context, p Pack, cookie string) (string, bool, bool, error) {
		atomic.AddInt64(&queries, 1)
		return "", false, false, stubNetErr{}
	}

	// 并发 1：首个曲包即网络错误，后续曲包不得再发起查询。
	outcomes := lookupPackLinks(context.Background(), packs, "osu_session=test", 1, lookup)

	if got := atomic.LoadInt64(&queries); got != 1 {
		t.Fatalf("网络错误后仍发起了 %d 次查询，期望只查询到出错的那个曲包", got)
	}
	if outcomes[0].netErr == nil {
		t.Fatal("第一个曲包应记录网络错误")
	}
	for i := 1; i < packCount; i++ {
		out := outcomes[i]
		if out.netErr != nil || out.found || out.requiresLogin || out.href != "" {
			t.Fatalf("第 %d 个未查询的曲包应保持零值（保留在失败列表）: %+v", i, out)
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

func TestLookupPackLinksPrintsOneLinePerPack(t *testing.T) {
	const (
		packCount   = 6
		concurrency = 4
	)
	packs := makePacks(packCount)
	lookup := func(ctx context.Context, p Pack, cookie string) (string, bool, bool, error) {
		return "https://packs.ppy.sh/" + p.Tag + ".zip", true, false, nil
	}

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	os.Stdout = w
	outcomes := lookupPackLinks(context.Background(), packs, "osu_session=test", concurrency, lookup)
	os.Stdout = origStdout
	w.Close()
	raw, _ := io.ReadAll(r)
	r.Close()

	if len(outcomes) != packCount {
		t.Fatalf("结果数量 = %d, 期望 %d", len(outcomes), packCount)
	}
	lineRe := regexp.MustCompile(`^\s*查询官方存储地址 \((\d)/6\): S\d+$`)
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
