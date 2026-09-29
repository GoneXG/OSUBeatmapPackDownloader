package main

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
)

// lazerPacks 构造一批「内置派生猜不出真实文件名」的曲包：
// 官网名是 osu!lazer Beatmap Pack #N，而站点实测文件名用的是 Lazer Beatmap Pack #N。
func lazerPacks(n int) []Pack {
	packs := make([]Pack, 0, n)
	for i := 1; i <= n; i++ {
		packs = append(packs, Pack{
			Tag:     fmt.Sprintf("L%d", i),
			Name:    fmt.Sprintf("osu!lazer Beatmap Pack #%d", i),
			PageURL: fmt.Sprintf("https://osu.ppy.sh/beatmaps/packs/L%d", i),
		})
	}
	return packs
}

func lazerRealLink(p Pack) string {
	return packLinkURL(p.Tag, "Lazer Beatmap Pack #"+p.Tag[1:], ".7z")
}

// decodeLink 把直链还原成「<tag> - <名称><扩展名>」形式，便于断言。
func decodeLink(link string) string {
	base := link
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if decoded, err := url.PathUnescape(base); err == nil {
		return decoded
	}
	return base
}

// lazerChecker 只承认「Lazer Beatmap Pack #N.7z」（真实文件名），
// extraOK 里的 tag 额外承认官网名 + .zip（用来模拟「该曲包本来就校验通过」）。
func lazerChecker(extraOK map[string]bool) func(context.Context, string) (linkCheckStatus, error) {
	return func(_ context.Context, link string) (linkCheckStatus, error) {
		base := decodeLink(link)
		if strings.Contains(base, "Lazer Beatmap Pack #") && strings.HasSuffix(base, ".7z") {
			return linkOK, nil
		}
		tag := base
		if i := strings.Index(base, " - "); i > 0 {
			tag = base[:i]
		}
		if extraOK[tag] && strings.HasSuffix(base, ".zip") {
			return linkOK, nil
		}
		return linkMissing, nil
	}
}

// resolverFor 只对指定 tag 返回解析到的真实链接，其余视为解析不到。
func resolverFor(tags ...string) (func(context.Context, []Pack) []packResolveOutcome, *[][]string) {
	allow := map[string]bool{}
	for _, tag := range tags {
		allow[tag] = true
	}
	calls := &[][]string{}
	return func(_ context.Context, packs []Pack) []packResolveOutcome {
		*calls = append(*calls, packTags(packs))
		out := make([]packResolveOutcome, len(packs))
		for i, p := range packs {
			if allow[p.Tag] {
				out[i] = packResolveOutcome{Href: lazerRealLink(p), Found: true}
			}
		}
		return out
	}, calls
}

// TestSampleIndicesCoversWholeList 覆盖抽检器：按比例抽样，但必须覆盖首尾区段。
func TestSampleIndicesCoversWholeList(t *testing.T) {
	const n = 100
	got := sampleIndices(n, 0.1)
	if len(got) == 0 {
		t.Fatal("抽样结果不应为空")
	}
	var head, tail bool
	for _, idx := range got {
		if idx < 10 {
			head = true
		}
		if idx >= 90 {
			tail = true
		}
	}
	if !head || !tail {
		t.Fatalf("抽样必须覆盖首尾区段，实际 %v", got)
	}

	if all := sampleIndices(n, 1); len(all) != n {
		t.Fatalf("比例 1 应等于全量校验，实际 %d 个", len(all))
	}
	if one := sampleIndices(n, 0.0001); len(one) == 0 || one[len(one)-1] != n-1 {
		t.Fatalf("极小比例也应至少覆盖末尾，实际 %v", one)
	}
}

// TestRepairVerifiesFlipBoundaryInsteadOfInferring 覆盖翻转点处理：
// 相邻两个抽样锚点结构不同时，它们之间那一段必须逐条确认，不能按某一侧结构推断。
// （实测 S1298/S1299 就落在这种盲区里：按最近锚点推断会得到 404 链接。）
func TestRepairVerifiesFlipBoundaryInsteadOfInferring(t *testing.T) {
	const packCount = 20
	const flip = 10 // 第 1~10 个用老结构，第 11~20 个用新结构
	packs := make([]Pack, 0, packCount)
	expect := make(map[string]string, packCount)
	for i := 1; i <= packCount; i++ {
		p := Pack{
			Tag:     fmt.Sprintf("S%d", i),
			Name:    fmt.Sprintf("osu! Beatmap Pack #%d", i),
			PageURL: fmt.Sprintf("https://osu.ppy.sh/beatmaps/packs/S%d", i),
		}
		packs = append(packs, p)
		if i <= flip {
			expect[p.Tag] = packLinkURL(p.Tag, "Beatmap Pack #"+p.Tag[1:], ".7z")
		} else {
			expect[p.Tag] = packLinkURL(p.Tag, "osu! Beatmap Pack #"+p.Tag[1:], ".zip")
		}
	}

	check := func(_ context.Context, link string) (linkCheckStatus, error) {
		for _, want := range expect {
			if link == want {
				return linkOK, nil
			}
		}
		return linkMissing, nil
	}
	deps := linkRepairDeps{
		check: check,
		resolve: func(_ context.Context, probe []Pack) []packResolveOutcome {
			return make([]packResolveOutcome, len(probe))
		},
		concurrency: 4,
		sampleRate:  defaultVerifyRate,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	if len(report.Failed) != 0 {
		t.Fatalf("翻转点两侧都有真实链接，不应有失败曲包，实际 %v", packTags(report.Failed))
	}
	// 抽样锚点是下标 0/10/19：只有 0 与 10 之间跨越了翻转点，那一段（S2..S10）
	// 必须逐条确认；10 与 19 之间结构一致，允许推断。
	for _, tag := range []string{"S2", "S5", "S9", "S10"} {
		if st := stateOf(report, tag); !st.Verified {
			t.Fatalf("%s 跨翻转点，必须逐条校验通过而不是推断，实际 %+v", tag, st)
		}
	}
	if report.Verified+report.Inferred != packCount {
		t.Fatalf("全部曲包都应有链接来源，实际 校验 %d + 推断 %d", report.Verified, report.Inferred)
	}
	for _, p := range packs {
		st := stateOf(report, p.Tag)
		if !st.Adopted() {
			t.Fatalf("%s 应已有可用链接（校验或推断），实际 %+v", p.Tag, st)
		}
		if st.URL != expect[p.Tag] {
			t.Fatalf("%s 采用了错误结构\n got: %s\nwant: %s", p.Tag, st.URL, expect[p.Tag])
		}
	}
}

// TestCandidateOrderMinimizesHeadChecks 记录各实测区段命中所需的前置候选数：
// 候选顺序直接决定校验耗时，两个主要区段都必须 1 次 HEAD 命中。
func TestCandidateOrderMinimizesHeadChecks(t *testing.T) {
	cases := []struct {
		tag     string
		name    string
		variant string
		ext     string
		wantPos int // 命中项在候选里的下标 = 命中前的失败 HEAD 次数
	}{
		// 新区段：官网名 + .zip，必须第一个候选就命中。
		{"SM185", "osu!mania Beatmap Pack #185", "modern", ".zip", 0},
		{"S1350", "osu! Beatmap Pack #1350", "modern", ".zip", 0},
		// 老区段：历史短名 + .7z，第二个候选命中。
		{"SM111", "osu!mania Beatmap Pack #111", "short", ".7z", 1},
		{"ST200", "osu!taiko Beatmap Pack #200", "short", ".7z", 1},
		{"SC1", "osu!catch Beatmap Pack #1", "short", ".7z", 1},
		// 过渡带：历史短名 + .zip。
		{"S1300", "osu! Beatmap Pack #1300", "short", ".zip", 2},
	}
	for _, tc := range cases {
		p := Pack{Tag: tc.tag, Name: tc.name}
		pos := -1
		for i, st := range CandidateStructures(p) {
			if st.Variant == tc.variant && st.Extension == tc.ext {
				pos = i
				break
			}
		}
		if pos != tc.wantPos {
			t.Fatalf("%s 的命中候选下标应为 %d，实际 %d（候选顺序变化会显著影响校验耗时）",
				tc.tag, tc.wantPos, pos)
		}
	}
}

// TestDefaultVerifyRateIsSampling 锁定「默认抽检而不是全量」：
// 全量校验近 2000 个链接要十几分钟，属于刻意避免的默认行为。
func TestDefaultVerifyRateIsSampling(t *testing.T) {
	if defaultVerifyRate >= 1 {
		t.Fatalf("默认抽检比例应为抽样值（<1），实际 %v", defaultVerifyRate)
	}
	if defaultVerifyRate <= 0 {
		t.Fatalf("默认抽检比例必须为正，实际 %v", defaultVerifyRate)
	}
	// 抽样步长不应显著超过 ±window，否则区段边界翻转会落在抽样盲区。
	stride := int(math.Round(1 / defaultVerifyRate))
	if stride > 2*defaultRepairWindow {
		t.Fatalf("默认抽样步长 %d 超过 ±%d 修复窗口的覆盖范围", stride, defaultRepairWindow)
	}
	// rate<=0 时应回落到该默认值。
	if got := normalizeRepairDeps(linkRepairDeps{}).sampleRate; got != defaultVerifyRate {
		t.Fatalf("未指定抽检比例时应回落到默认值 %v，实际 %v", defaultVerifyRate, got)
	}
}

// TestRepairInfersLinksForUnsampledPacks 覆盖抽检模式的核心收益：
// 只逐条校验被抽到的锚点，其余曲包按邻近锚点的区段结构推断，且推断结果确实被采用。
func TestRepairInfersLinksForUnsampledPacks(t *testing.T) {
	const packCount = 20
	packs := make([]Pack, 0, packCount)
	for i := 1; i <= packCount; i++ {
		packs = append(packs, Pack{
			Tag:     fmt.Sprintf("SM%d", i),
			Name:    fmt.Sprintf("osu!mania Beatmap Pack #%d", i),
			PageURL: fmt.Sprintf("https://osu.ppy.sh/beatmaps/packs/SM%d", i),
		})
	}

	var checks int
	deps := linkRepairDeps{
		// 本区段实测使用「历史短名 + .7z」。
		check: func(_ context.Context, link string) (linkCheckStatus, error) {
			checks++
			base := decodeLink(link)
			if strings.Contains(base, "Mania Beatmap Pack #") && strings.HasSuffix(base, ".7z") {
				return linkOK, nil
			}
			return linkMissing, nil
		},
		resolve: func(_ context.Context, probe []Pack) []packResolveOutcome {
			return make([]packResolveOutcome, len(probe))
		},
		concurrency: 4,
		sampleRate:  0.1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	// 只抽到 3 个锚点（首个、等距中点、末个），其老区段各需 2 次 HEAD。
	if report.Verified != 3 {
		t.Fatalf("抽检 10%% 应逐条校验 3 个锚点，实际 %d 个", report.Verified)
	}
	if report.Inferred != packCount-3 {
		t.Fatalf("其余曲包应全部由推断补齐，实际推断 %d 个", report.Inferred)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("推断模式下不应有失败曲包，实际 %v", packTags(report.Failed))
	}
	if checks >= packCount {
		t.Fatalf("抽检模式不应逐条校验全部曲包，实际 HEAD %d 次", checks)
	}

	// 未被抽到的曲包也要拿到按区段结构构造的链接，并且标记为推断。
	// 抽到的锚点是下标 0/10/19（即 SM1/SM11/SM20），这里取三个未被抽到的曲包。
	for _, tag := range []string{"SM2", "SM5", "SM19"} {
		st := stateOf(report, tag)
		if !st.Adopted() {
			t.Fatalf("%s 应采用推断链接，实际 %+v", tag, st)
		}
		if st.Verified {
			t.Fatalf("%s 未被抽到，不应标记为逐条校验通过", tag)
		}
		if !st.Inferred {
			t.Fatalf("%s 应标记为推断", tag)
		}
		want := packLinkURL(tag, "Mania Beatmap Pack #"+tag[2:], ".7z")
		if st.URL != want {
			t.Fatalf("%s 的推断链接不符合区段结构\n got: %s\nwant: %s", tag, st.URL, want)
		}
	}
}

// TestRepairConvergesWhenSampleIsClean 覆盖「抽检无错即结束」：不触发任何解析。
func TestRepairConvergesWhenSampleIsClean(t *testing.T) {
	packs := makePacks(5)
	resolveCalls := 0
	deps := linkRepairDeps{
		check: func(_ context.Context, link string) (linkCheckStatus, error) {
			// 官网名 + .zip 就是这批曲包的命中项，首轮抽检应当全部通过。
			if strings.HasSuffix(decodeLink(link), ".zip") {
				return linkOK, nil
			}
			return linkMissing, nil
		},
		resolve: func(_ context.Context, p []Pack) []packResolveOutcome {
			resolveCalls++
			return make([]packResolveOutcome, len(p))
		},
		concurrency: 4,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})
	if resolveCalls != 0 {
		t.Fatalf("抽检全部通过时不应调用浏览器解析，实际 %d 次", resolveCalls)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("不应有失败曲包，实际 %v", packTags(report.Failed))
	}
	if report.Rounds != 1 {
		t.Fatalf("应在第 1 轮收敛，实际 %d 轮", report.Rounds)
	}
}

// TestRepairFillsRegionAndKeepsVerifiedLinks 覆盖 4.2「结构一致→填充」与 4.3「只重写校验失败的链接」。
func TestRepairFillsRegionAndKeepsVerifiedLinks(t *testing.T) {
	packs := lazerPacks(6)
	// 第 2、4 个曲包本来就校验通过（官网名 + .zip）。
	extraOK := map[string]bool{"L2": true, "L4": true}
	// 失败曲包 L3、L5 经浏览器解析出真实链接，且解析出的结构一致。
	resolve, resolveCalls := resolverFor("L3", "L5")

	deps := linkRepairDeps{
		check:       lazerChecker(extraOK),
		resolve:     resolve,
		concurrency: 3,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	verifiedBefore := map[string]string{}
	for _, st := range report.States {
		if st.Verified {
			verifiedBefore[st.Pack.Tag] = st.URL
		}
	}

	// 结构一致时，L3 与 L5 之间的曲包被按其结构批量填充；L3/L5 自身采用解析出的直接链接。
	for _, tag := range []string{"L3", "L5"} {
		st := stateOf(report, tag)
		if !st.Verified || st.URL != lazerRealLink(st.Pack) {
			t.Fatalf("%s 应采用浏览器解析出的真实链接: %+v", tag, st)
		}
	}
	// L4 本来就通过校验，必须保持不变（不被新结构覆盖）。
	if got := stateOf(report, "L4"); !got.Verified || got.URL != packLinkURL("L4", "osu!lazer Beatmap Pack #4", ".zip") {
		t.Fatalf("区间内已通过校验的链接必须保持不变: %+v", got)
	}
	// 首尾解析不到真实链接的曲包记为终态失败。
	if got := packTags(report.Failed); len(got) != 2 || got[0] != "L1" {
		t.Fatalf("解析不到的曲包应记为终态失败，实际 %v", got)
	}
	if len(*resolveCalls) == 0 {
		t.Fatal("定向修复应调用浏览器解析")
	}
	// 窗口去重：同一轮里每个下标只提交一次。
	seen := map[string]bool{}
	for _, batch := range *resolveCalls {
		for _, tag := range batch {
			if seen[tag] {
				t.Fatalf("同一轮窗口内的曲包 %s 被重复提交", tag)
			}
		}
		break
	}
}

// TestRepairDoesNotFillWhenStructuresDiffer 覆盖 4.2「结构不同→不填充」。
func TestRepairDoesNotFillWhenStructuresDiffer(t *testing.T) {
	packs := lazerPacks(3)
	resolve := func(_ context.Context, probe []Pack) []packResolveOutcome {
		out := make([]packResolveOutcome, len(probe))
		for i, p := range probe {
			switch p.Tag {
			case "L1":
				out[i] = packResolveOutcome{Href: lazerRealLink(p), Found: true} // .7z
			case "L3":
				out[i] = packResolveOutcome{Href: packLinkURL(p.Tag, "Lazer Beatmap Pack #3", ".zip"), Found: true}
			}
		}
		return out
	}
	deps := linkRepairDeps{
		check: func(_ context.Context, link string) (linkCheckStatus, error) {
			if strings.Contains(decodeLink(link), "Lazer Beatmap Pack #") {
				return linkOK, nil
			}
			return linkMissing, nil
		},
		resolve:     resolve,
		concurrency: 2,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	if got := stateOf(report, "L2"); got.Verified {
		t.Fatalf("相邻失败链接结构不一致时不得填充中间的曲包: %+v", got)
	}
	if got := stateOf(report, "L2"); !got.Failed {
		t.Fatalf("未填充的曲包应保留在失败列表中: %+v", got)
	}
}

// TestRepairReverifiesFilledLinks 覆盖 4.2「结构一致→填充」+ 4.3「填充结果必须复验」：
// 填充出来的坏链接会在复验中被识别。
func TestRepairReverifiesFilledLinks(t *testing.T) {
	packs := lazerPacks(3)
	resolve, _ := resolverFor("L1", "L3")
	deps := linkRepairDeps{
		// 只承认首尾曲包的真实链接：L2 被填充出的链接不存在。
		check: func(_ context.Context, link string) (linkCheckStatus, error) {
			base := decodeLink(link)
			if strings.Contains(base, "Lazer Beatmap Pack #") &&
				(strings.HasSuffix(base, "#1.7z") || strings.HasSuffix(base, "#3.7z")) {
				return linkOK, nil
			}
			return linkMissing, nil
		},
		resolve:     resolve,
		concurrency: 1,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	if got := stateOf(report, "L1"); !got.Verified {
		t.Fatalf("首端曲包应复验通过: %+v", got)
	}
	if got := stateOf(report, "L3"); !got.Verified {
		t.Fatalf("末端曲包应复验通过: %+v", got)
	}
	if got := stateOf(report, "L2"); got.Verified || !got.Failed {
		t.Fatalf("填充出的坏链接必须在复验中被识别为失败: %+v", got)
	}
	if got := packTags(report.Failed); len(got) != 1 || got[0] != "L2" {
		t.Fatalf("终态失败应只剩 L2，实际 %v", got)
	}
}

// TestRepairStopsWhenRoundMakesNoProgress 覆盖 4.4「本轮无进展即终止」且不会死循环。
func TestRepairStopsWhenRoundMakesNoProgress(t *testing.T) {
	packs := lazerPacks(20)
	resolveCalls := 0
	deps := linkRepairDeps{
		check: func(context.Context, string) (linkCheckStatus, error) { return linkMissing, nil },
		resolve: func(_ context.Context, probe []Pack) []packResolveOutcome {
			resolveCalls++
			return make([]packResolveOutcome, len(probe))
		},
		concurrency: 4,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})

	if resolveCalls == 0 {
		t.Fatal("应至少尝试一轮定向修复")
	}
	if resolveCalls >= maxRepairRounds {
		t.Fatalf("无进展时应提前终止，实际尝试了 %d 轮", resolveCalls)
	}
	if len(report.Failed) != len(packs) {
		t.Fatalf("无进展时全部曲包应记为终态失败，实际 %d/%d", len(report.Failed), len(packs))
	}
}

// TestRepairAbortsOnNeedLogin 覆盖 4.4「解析过程中遇到未登录立即终止」。
func TestRepairAbortsOnNeedLogin(t *testing.T) {
	packs := lazerPacks(4)
	resolveCalls := 0
	deps := linkRepairDeps{
		check: func(context.Context, string) (linkCheckStatus, error) { return linkMissing, nil },
		resolve: func(_ context.Context, probe []Pack) []packResolveOutcome {
			resolveCalls++
			out := make([]packResolveOutcome, len(probe))
			for i := range probe {
				out[i] = packResolveOutcome{RequiresLogin: true}
			}
			return out
		},
		concurrency: 2,
		sampleRate:  1,
	}
	var report repairReport
	captureStdout(t, func() {
		report = repairPackLinks(context.Background(), packs, deps)
	})
	if !report.NeedLogin {
		t.Fatal("检测到未登录时应上报 NeedLogin")
	}
	if resolveCalls != 1 {
		t.Fatalf("未登录时应立即终止，实际尝试了 %d 轮", resolveCalls)
	}
}

func stateOf(report repairReport, tag string) packLinkState {
	for _, st := range report.States {
		if st.Pack.Tag == tag {
			return st
		}
	}
	return packLinkState{}
}
