package main

import (
	"context"
	"net"
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

// assertNoInterleavedLines 断言进度输出中每条信息独占一行（不出现两条信息挤在同一行）。
func assertNoInterleavedLines(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Count(line, "轮：") > 1 {
			t.Fatalf("进度信息出现同行交错: %q", line)
		}
	}
}

// TestRecoverFailedPacksContinuesUntilNoProgress 覆盖「只取得一部分地址时继续下一轮」：
// 第一轮只有部分曲包查到地址，重下成功后必须继续第二轮，且第二轮只查询仍失败的曲包
// （未取得地址的曲包重新查询，已成功的曲包不再查询）。
func TestRecoverFailedPacksContinuesUntilNoProgress(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3", "T4", "T5")

	// 每轮能查到官方地址的曲包：第 1 轮 T1/T2，第 2 轮 T3/T4，第 3 轮 T4（T5 始终查不到）。
	foundByRound := [][]string{{"T1", "T2"}, {"T3", "T4"}, {"T4"}}
	// 每轮重下后仍失败的曲包：第 1 轮全部成功，第 2 轮 T4 失败，第 3 轮 T4 仍失败（无进展）。
	stillFailedByRound := [][]string{{}, {"T4"}, {"T4"}}

	var (
		lookupCalls   []string
		downloadCalls [][]string
		promptCalls   int
		roundIdx      int
	)

	deps := packRecoveryDeps{
		concurrency: 1, // 顺序执行，便于断言每轮的查询集合
		lookup: func(_ context.Context, p Pack, _ string) (string, bool, bool, error) {
			lookupCalls = append(lookupCalls, p.Tag)
			if roundIdx < len(foundByRound) && contains(foundByRound[roundIdx], p.Tag) {
				return "https://osu.ppy.sh/beatmaps/packs/" + p.Tag + "/download", true, false, nil
			}
			return "", false, false, nil
		},
		download: func(_ context.Context, _ string, batch []aria2Item) []aria2Item {
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
		promptCookie: func() (string, bool) {
			promptCalls++
			return "osu_session=test", false
		},
	}

	var got []aria2Item
	out := captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, "osu_session=seed", deps)
	})

	wantLookups := []string{"T1", "T2", "T3", "T4", "T5", "T3", "T4", "T5", "T4", "T5"}
	if !reflect.DeepEqual(lookupCalls, wantLookups) {
		t.Fatalf("每轮查询的曲包不符合预期\n got: %v\nwant: %v", lookupCalls, wantLookups)
	}
	wantDownloads := [][]string{{"T1", "T2"}, {"T3", "T4"}, {"T4"}}
	if !reflect.DeepEqual(downloadCalls, wantDownloads) {
		t.Fatalf("每轮重下的曲包不符合预期\n got: %v\nwant: %v", downloadCalls, wantDownloads)
	}
	if want := []string{"T4", "T5"}; !reflect.DeepEqual(tagsOf(got), want) {
		t.Fatalf("最终失败列表不符合预期\n got: %v\nwant: %v", tagsOf(got), want)
	}
	if promptCalls != 0 {
		t.Fatalf("已提供 Cookie 时不应再向用户索要，实际索要 %d 次", promptCalls)
	}
	assertNoInterleavedLines(t, out)
	for _, want := range []string{
		"第 1 轮：剩余 5 个失败曲包，开始重查官方存储地址（并发 1）...",
		"第 1 轮：重下 2 个，成功 2 个，剩余失败 3 个",
		"第 2 轮：剩余 3 个失败曲包，开始重查官方存储地址（并发 1）...",
		"第 2 轮：重下 2 个，成功 1 个，剩余失败 2 个",
		"第 3 轮：剩余 2 个失败曲包，开始重查官方存储地址（并发 1）...",
		"第 3 轮：重下 1 个，成功 0 个，剩余失败 2 个",
		"本轮无进展，停止重试。",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少轮次进度行 %q\n实际输出:\n%s", want, out)
		}
	}
}

// TestRecoverFailedPacksStopsWhenRoundMakesNoProgress 覆盖「某一轮成功数为 0 时立即停止」：
// 不再发起新的查询与下载，返回的失败列表内容与顺序不变。
func TestRecoverFailedPacksStopsWhenRoundMakesNoProgress(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3")

	var (
		lookupCalls   int
		downloadCalls int
		promptCalls   int
	)

	deps := packRecoveryDeps{
		concurrency: 2,
		lookup: func(_ context.Context, p Pack, _ string) (string, bool, bool, error) {
			lookupCalls++
			return "https://osu.ppy.sh/beatmaps/packs/" + p.Tag + "/download", true, false, nil
		},
		download: func(_ context.Context, _ string, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch // 本批全部仍然失败
		},
		promptCookie: func() (string, bool) {
			promptCalls++
			return "osu_session=test", false
		},
	}

	var got []aria2Item
	captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, "osu_session=seed", deps)
	})

	if lookupCalls != 3 {
		t.Fatalf("无进展时应只查询一轮（3 个曲包），实际查询 %d 次", lookupCalls)
	}
	if downloadCalls != 1 {
		t.Fatalf("无进展时应只重下一轮，实际重下 %d 轮", downloadCalls)
	}
	if promptCalls != 0 {
		t.Fatalf("Cookie 有效时不应索要 Cookie，实际索要 %d 次", promptCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("无进展时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
}

// TestRecoverFailedPacksRejectsInvalidCookieThreeTimes 覆盖 Cookie 交互：
// 连续 3 次「页面仍要求登录」后停止循环，总尝试次数不超过 3 次；已提供的 Cookie 计入尝试次数。
func TestRecoverFailedPacksRejectsInvalidCookieThreeTimes(t *testing.T) {
	failed := recoveryItems("T1", "T2")

	cases := []struct {
		name          string
		initialCookie string
		wantPrompts   int
	}{
		{name: "未预置 Cookie 时索要 3 次", initialCookie: "", wantPrompts: 3},
		{name: "预置 Cookie 计入尝试次数时再索要 2 次", initialCookie: "osu_session=scraped", wantPrompts: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				lookupRounds  int
				promptCalls   int
				downloadCalls int
			)

			deps := packRecoveryDeps{
				concurrency: 1,
				lookup: func(_ context.Context, _ Pack, _ string) (string, bool, bool, error) {
					lookupRounds++
					return "", false, true, nil // 页面仍提示需要登录
				},
				download: func(_ context.Context, _ string, batch []aria2Item) []aria2Item {
					downloadCalls++
					return batch
				},
				promptCookie: func() (string, bool) {
					promptCalls++
					return "osu_session=stale", false
				},
			}

			var got []aria2Item
			out := captureStdout(t, func() {
				got = recoverFailedPacks(context.Background(), failed, tc.initialCookie, deps)
			})

			if promptCalls != tc.wantPrompts {
				t.Fatalf("应索要 Cookie %d 次后停止，实际 %d 次", tc.wantPrompts, promptCalls)
			}
			// 总尝试次数（含预置 Cookie）不超过 maxCookieAttempts 次，每轮都重查全部失败曲包。
			if lookupRounds != maxCookieAttempts*len(failed) {
				t.Fatalf("应共尝试 %d 轮重查，实际查询 %d 次", maxCookieAttempts, lookupRounds)
			}
			if downloadCalls != 0 {
				t.Fatalf("没有取得官方地址时不应重下，实际重下 %d 轮", downloadCalls)
			}
			if !reflect.DeepEqual(got, failed) {
				t.Fatalf("Cookie 无效时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
			}
			if !strings.Contains(out, "已连续 3 次输入的 Cookie 未生效，停止重试剩余曲包。") {
				t.Fatalf("缺少 Cookie 尝试次数用尽提示\n实际输出:\n%s", out)
			}
		})
	}
}

// TestRecoverFailedPacksStopsWhenUserSkipsCookie 覆盖用户放弃重试：不再查询、不再下载。
func TestRecoverFailedPacksStopsWhenUserSkipsCookie(t *testing.T) {
	failed := recoveryItems("T1", "T2")

	var lookupCalls, downloadCalls int
	deps := packRecoveryDeps{
		concurrency: 2,
		lookup: func(_ context.Context, p Pack, _ string) (string, bool, bool, error) {
			lookupCalls++
			return "https://osu.ppy.sh/beatmaps/packs/" + p.Tag + "/download", true, false, nil
		},
		download: func(_ context.Context, _ string, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch
		},
		promptCookie: func() (string, bool) { return "", true },
	}

	var got []aria2Item
	captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, "", deps)
	})

	if lookupCalls != 0 || downloadCalls != 0 {
		t.Fatalf("用户跳过 Cookie 后不应再查询或下载，实际查询 %d 次、重下 %d 轮", lookupCalls, downloadCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("跳过 Cookie 时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
}

// TestRecoverFailedPacksStopsOnNetworkError 覆盖网络层错误：立即停止后续查询与下载，
// 未取得官方地址的曲包保留在失败列表中。
func TestRecoverFailedPacksStopsOnNetworkError(t *testing.T) {
	failed := recoveryItems("T1", "T2", "T3")

	var lookupCalls, downloadCalls int
	deps := packRecoveryDeps{
		concurrency: 1, // 顺序执行，确保网络错误后不再领取新任务
		lookup: func(_ context.Context, p Pack, _ string) (string, bool, bool, error) {
			lookupCalls++
			if p.Tag == "T1" {
				return "", false, false, &net.DNSError{Err: "no such host", Name: "osu.ppy.sh"}
			}
			return "https://osu.ppy.sh/beatmaps/packs/" + p.Tag + "/download", true, false, nil
		},
		download: func(_ context.Context, _ string, batch []aria2Item) []aria2Item {
			downloadCalls++
			return batch
		},
		promptCookie: func() (string, bool) { return "", true },
	}

	var got []aria2Item
	captureStdout(t, func() {
		got = recoverFailedPacks(context.Background(), failed, "osu_session=seed", deps)
	})

	if lookupCalls != 1 {
		t.Fatalf("网络层错误后应停止发起新查询，实际查询 %d 次", lookupCalls)
	}
	if downloadCalls != 0 {
		t.Fatalf("网络层错误后不应重下，实际重下 %d 轮", downloadCalls)
	}
	if !reflect.DeepEqual(got, failed) {
		t.Fatalf("网络层错误时失败列表应原样返回\n got: %#v\nwant: %#v", got, failed)
	}
}
