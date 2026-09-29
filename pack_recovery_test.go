package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// recoveryPack 构造一个用于恢复流程测试的曲包。
func recoveryPack(tag string) Pack {
	return Pack{
		Tag:       tag,
		Name:      "osu! Beatmap Pack #" + tag,
		PageURL:   "https://osu.ppy.sh/beatmaps/packs/" + tag,
		DirectURL: "https://packs.ppy.sh/" + tag + ".zip",
	}
}

// recoveryItems 按 tag 顺序构造失败曲包列表。
func recoveryItems(tags ...string) []aria2Item {
	items := make([]aria2Item, 0, len(tags))
	for _, tag := range tags {
		p := recoveryPack(tag)
		items = append(items, aria2Item{URL: p.DirectURL, Pack: p})
	}
	return items
}

func tagsOf(items []aria2Item) []string {
	tags := make([]string, 0, len(items))
	for _, it := range items {
		tags = append(tags, it.Pack.Tag)
	}
	return tags
}

func packTags(packs []Pack) []string {
	tags := make([]string, 0, len(packs))
	for _, p := range packs {
		tags = append(tags, p.Tag)
	}
	return tags
}

// fakeResolver 按「本轮能解析出真实链接的曲包」构造浏览器解析依赖。
type fakeResolver struct {
	foundByRound [][]string
	roundIdx     *int
	calls        *[][]string
}

func (f fakeResolver) resolve(_ context.Context, packs []Pack) []packResolveOutcome {
	out := make([]packResolveOutcome, len(packs))
	if f.calls != nil {
		*f.calls = append(*f.calls, packTags(packs))
	}
	for i, p := range packs {
		if *f.roundIdx < len(f.foundByRound) && contains(f.foundByRound[*f.roundIdx], p.Tag) {
			out[i] = packResolveOutcome{
				Href:  "https://packs.ppy.sh/" + p.Tag + "%20-%20Real%20Pack.7z",
				Found: true,
			}
		}
	}
	return out
}

// assertNoInterleavedLines 断言进度输出中每条信息独占一行（不出现两条信息挤在同一行）。
func assertNoInterleavedLines(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Count(line, "轮：") > 1 {
			t.Fatalf("进度信息出现同行交错: %q", line)
		}
	}
}

// TestRecoverFailedPacksContinuesUntilNoProgress 覆盖「只解析到一部分链接时继续下一轮」：
// 第一轮只有部分曲包解析出链接，重下成功后必须继续第二轮，且第二轮只重查仍失败的曲包。
func TestRecoverFailedPacksContinuesUntilNoProgress(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3", "T4", "T5")

	// 每轮能解析出真实链接的曲包：第 1 轮 T1/T2，第 2 轮 T3/T4，第 3 轮 T4（T5 始终解析不到）。
	foundByRound := [][]string{{"T1", "T2"}, {"T3", "T4"}, {"T4"}}
	// 每轮重下后仍失败的曲包：第 1 轮全部成功，第 2 轮 T4 失败，第 3 轮 T4 仍失败（无进展）。
	stillFailedByRound := [][]string{{}, {"T4"}, {"T4"}}

	var (
		resolveCalls  [][]string
		downloadCalls [][]string
		roundIdx      int
	)

	deps := packRecoveryDeps{
		resolve: fakeResolver{foundByRound: foundByRound, roundIdx: &roundIdx, calls: &resolveCalls}.resolve,
		download: func(_ context.Context, batch []aria2Item) []aria2Item {
			downloadCalls = append(downloadCalls, tagsOf(batch))
			still := stillFailedByRound[roundIdx]
			roundIdx++
			var out []aria2Item
			for _, it := range batch {
				if contains(still, it.Pack.Tag) {
					out = append(out, it)
				}
			}
			return out
		},
	}

	var got []aria2Item
	out := captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, deps)
	})

	wantResolve := [][]string{{"T1", "T2", "T3", "T4", "T5"}, {"T3", "T4", "T5"}, {"T4", "T5"}}
	if !reflect.DeepEqual(resolveCalls, wantResolve) {
		t.Fatalf("每轮重查的曲包不符合预期\n got: %v\nwant: %v", resolveCalls, wantResolve)
	}
	wantDownloads := [][]string{{"T1", "T2"}, {"T3", "T4"}, {"T4"}}
	if !reflect.DeepEqual(downloadCalls, wantDownloads) {
		t.Fatalf("每轮重下的曲包不符合预期\n got: %v\nwant: %v", downloadCalls, wantDownloads)
	}
	if want := []string{"T4", "T5"}; !reflect.DeepEqual(tagsOf(got), want) {
		t.Fatalf("最终失败列表不符合预期\n got: %v\nwant: %v", tagsOf(got), want)
	}
	assertNoInterleavedLines(t, out)
	for _, want := range []string{
		"第 1 轮：剩余 5 个失败曲包，开始经浏览器解析真实下载链接...",
		"第 1 轮：重下 2 个，成功 2 个，剩余失败 3 个",
		"第 2 轮：剩余 3 个失败曲包，开始经浏览器解析真实下载链接...",
		"第 2 轮：重下 2 个，成功 1 个，剩余失败 2 个",
		"第 3 轮：剩余 2 个失败曲包，开始经浏览器解析真实下载链接...",
		"第 3 轮：重下 1 个，成功 0 个，剩余失败 2 个",
		"本轮无进展，停止重试。",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少轮次进度行 %q\n实际输出:\n%s", want, out)
		}
	}
}

// TestRecoverFailedPacksStopsWhenRoundMakesNoProgress 覆盖「某一轮成功数为 0 时立即停止」。
func TestRecoverFailedPacksStopsWhenRoundMakesNoProgress(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3")

	var (
		resolveCalls  int
		downloadCalls int
	)

	deps := packRecoveryDeps{
		resolve: func(_ context.Context, packs []Pack) []packResolveOutcome {
			resolveCalls++
			out := make([]packResolveOutcome, len(packs))
			for i, p := range packs {
				out[i] = packResolveOutcome{Href: "https://packs.ppy.sh/" + p.Tag + "%20-%20x.zip", Found: true}
			}
			return out
		},
		download: func(_ context.Context, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch // 本批全部仍然失败
		},
	}

	var got []aria2Item
	captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, deps)
	})

	if resolveCalls != 1 {
		t.Fatalf("无进展时应只重查一轮，实际 %d 轮", resolveCalls)
	}
	if downloadCalls != 1 {
		t.Fatalf("无进展时应只重下一轮，实际重下 %d 轮", downloadCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("无进展时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
}

// TestRecoverFailedPacksStopsOnNeedLogin 覆盖「解析过程中检测到未登录立即终止」：
// 不再发起后续解析与下载，剩余曲包原样保留。
func TestRecoverFailedPacksStopsOnNeedLogin(t *testing.T) {
	failed := recoveryItems("T1", "T2")

	var (
		resolveCalls  int
		downloadCalls int
	)
	deps := packRecoveryDeps{
		resolve: func(_ context.Context, packs []Pack) []packResolveOutcome {
			resolveCalls++
			out := make([]packResolveOutcome, len(packs))
			for i := range packs {
				out[i] = packResolveOutcome{RequiresLogin: true}
			}
			return out
		},
		download: func(_ context.Context, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch
		},
	}

	var got []aria2Item
	out := captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, deps)
	})

	if resolveCalls != 1 {
		t.Fatalf("未登录时应立即终止解析，实际重查 %d 轮", resolveCalls)
	}
	if downloadCalls != 0 {
		t.Fatalf("未登录时不应重下，实际重下 %d 轮", downloadCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("未登录时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
	if !strings.Contains(out, "解析过程中检测到未登录") {
		t.Fatalf("缺少未登录提示\n实际输出:\n%s", out)
	}
}

// TestRecoverFailedPacksStopsWhenNoLinkProvided 覆盖「已登录但官网未提供下载地址」：
// 判定为终态失败，不反复重试。
func TestRecoverFailedPacksStopsWhenNoLinkProvided(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3")

	var resolveCalls, downloadCalls int
	deps := packRecoveryDeps{
		resolve: func(_ context.Context, packs []Pack) []packResolveOutcome {
			resolveCalls++
			out := make([]packResolveOutcome, len(packs))
			for i := range packs {
				out[i] = packResolveOutcome{NoLink: true}
			}
			return out
		},
		download: func(_ context.Context, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch
		},
	}

	var got []aria2Item
	out := captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, deps)
	})

	if resolveCalls != 1 || downloadCalls != 0 {
		t.Fatalf("官网未提供下载地址时应只判定一次且不重下，实际重查 %d 次、重下 %d 轮", resolveCalls, downloadCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("终态失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
	if !strings.Contains(out, "官网未提供这些曲包的下载地址") {
		t.Fatalf("缺少终态失败提示\n实际输出:\n%s", out)
	}
}
