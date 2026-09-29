package main

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	// defaultVerifyRate 默认抽检比例。
	//
	// 为什么不默认全量校验：校验是逐个直链做 HEAD，实测约 4 个/秒（与站点 CDN 的往返延迟有关），
	// 常规分类 osu! 区段有近 2000 个曲包，全量校验要十几分钟。而命名规则是按区段成片变化的，
	// 抽检确认区段结构后即可推断整段，没必要逐条探测。
	// 抽样步长（默认 1/0.1 = 10）不超过 ±window（7）量级，保证区段边界翻转仍会被抽检或修复命中。
	// 需要逐条确认时用 -verify-rate 1。
	defaultVerifyRate = 0.1
	// defaultRepairWindow ±N 采样步长（实测 7 足以跨过区段边界）。
	defaultRepairWindow = 7
	// maxRepairRounds 修复循环的轮数上限，配合「无进展即停」防止死循环。
	maxRepairRounds = 20
	// maxRefineRounds 翻转点逐条确认的轮数上限（确认后锚点变密，一般 1~2 轮即收敛）。
	maxRefineRounds = 4
)

// packLinkState 一个曲包在修复流程中的链接状态。
type packLinkState struct {
	Pack       Pack
	Candidates []linkStructure // 候选结构；学习/填充后会收敛为单一结构
	Direct     string          // 浏览器直接解析出的真实链接（优先采用）
	URL        string          // 已采用的链接（通过校验才有值）
	Structure  linkStructure
	Verified   bool // 已通过连通性校验
	Failed     bool // 校验失败、等待解析兜底
	// Inferred 未被抽到，但按邻近已验证曲包的区段结构推断了链接（抽检模式下才有）。
	Inferred bool
	Status   linkCheckStatus
	NetErr   error
}

// Adopted 判断该曲包是否已有可采用的链接（逐条校验通过，或按区段结构推断）。
func (st packLinkState) Adopted() bool {
	return st.URL != "" && (st.Verified || st.Inferred)
}

// linkRepairDeps 修复循环的可注入依赖，便于单元测试。
type linkRepairDeps struct {
	// check 校验单个链接的连通性（并发由 concurrency 限制）。
	check func(ctx context.Context, link string) (linkCheckStatus, error)
	// resolve 经浏览器解析一批曲包的真实下载链接，结果与输入一一对应。
	resolve func(ctx context.Context, packs []Pack) []packResolveOutcome
	// sampleRate 抽检比例（0,1]；1 表示全量校验。
	sampleRate float64
	// concurrency 在途校验数上限。
	concurrency int
	// window ±window 采样步长。
	window int
}

// repairReport 修复循环的最终结果。
type repairReport struct {
	States    []packLinkState // 与输入曲包一一对应
	Failed    []Pack          // 终态失败（没有任何可用链接）
	NeedLogin bool            // 解析过程中检测到未登录
	Rounds    int
	Verified  int // 逐条校验通过的曲包数
	Inferred  int // 按区段结构推断出链接的曲包数
}

// sampleIndices 按比例抽样下标。
// 抽样必须覆盖整个列表而不是只取前缀：首个与末个元素一定入选，中间等距分布。
func sampleIndices(n int, rate float64) []int {
	if n <= 0 {
		return nil
	}
	if rate > 1 {
		rate = 1
	}
	if rate <= 0 {
		rate = defaultVerifyRate
	}
	k := int(math.Round(rate * float64(n)))
	if k < 1 {
		k = 1
	}
	if k >= n {
		return allIndices(n)
	}
	out := make([]int, 0, k+1)
	seen := make(map[int]bool, k+1)
	for j := 0; j < k; j++ {
		idx := int(float64(j) * float64(n) / float64(k))
		if idx >= n {
			idx = n - 1
		}
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	// 保证末段被覆盖。
	if !seen[n-1] {
		out = append(out, n-1)
	}
	sort.Ints(out)
	return out
}

func allIndices(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// probeIndices 返回一组失败下标的 ±window 邻近下标（含自身），并按窗口去重。
func probeIndices(failures []int, n, window int) []int {
	if window < 1 {
		window = defaultRepairWindow
	}
	seen := make(map[int]bool, len(failures)*3)
	out := make([]int, 0, len(failures)*3)
	for _, f := range failures {
		for _, delta := range []int{-window, 0, window} {
			idx := f + delta
			if idx < 0 || idx >= n || seen[idx] {
				continue
			}
			seen[idx] = true
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// repairPackLinks 构造并校验曲包链接，随后用「抽检 + 定向修复」循环收敛。
//
// 收敛条件：一次抽检没有发现失败链接（无错即结束），或某一轮失败集合没有减少
// （无进展即终止，把剩余曲包记为终态失败）。解析过程中遇到未登录立即终止。
func repairPackLinks(ctx context.Context, packs []Pack, deps linkRepairDeps) repairReport {
	deps = normalizeRepairDeps(deps)
	states := make([]packLinkState, len(packs))
	for i, p := range packs {
		states[i] = packLinkState{Pack: p, Candidates: CandidateStructures(p)}
	}
	report := repairReport{States: states}

	// 构造阶段：先用候选空间的第一个候选建立链接，再统一校验。
	for round := 1; round <= maxRepairRounds; round++ {
		if ctx.Err() != nil {
			break
		}
		sample := sampleIndices(len(states), deps.sampleRate)
		verifyStatesSeeded(ctx, states, sample, deps, linkStructure{})
		failures := failingAmong(states, sample)
		report.Rounds = round
		if len(failures) == 0 {
			msgf("      链接校验收敛：本次抽检 %d 个曲包，未发现失败链接。", len(sample))
			break
		}

		msgf("      第 %d 轮：抽检发现 %d 个失败链接，开始定向修复（±%d 采样）...",
			round, len(failures), deps.window)
		before := countFailedStates(states)
		login, touched := repairRound(ctx, states, failures, deps)
		if login {
			report.NeedLogin = true
			msgf("      解析过程中检测到未登录：请先在浏览器登录 osu! 后重试。")
			break
		}
		verifyStates(ctx, states, touched, deps)
		after := countFailedStates(states)
		if after >= before {
			msgf("      第 %d 轮无进展（失败 %d -> %d），停止修复循环。", round, before, after)
			break
		}
		msgf("      第 %d 轮：失败链接 %d -> %d", round, before, after)
	}

	// 抽检模式下，没被抽到也没失败的曲包按邻近已验证曲包的区段结构推断链接：
	// 命名规则成片变化，抽样确认区段结构后整段可以照此构造，不必逐条 HEAD。
	// 但「结构发生翻转」的那一段不能推断——抽样锚点分属两种结构时，中间曲包
	// 到底属于哪一边无从判断（实测 S1298/S1299 就落在这种盲区里）。
	// 这些区段必须逐条确认，代价被限制在每个翻转点最多一次抽样步长。
	for round := 0; round < maxRefineRounds; round++ {
		todo := boundaryVerifyIndices(states)
		if len(todo) == 0 {
			break
		}
		msgf("      区段结构翻转处需要逐条确认：本次追加校验 %d 个曲包。", len(todo))
		verifyStatesSeeded(ctx, states, todo, deps, linkStructure{})
	}
	report.Inferred = assignInferred(states)
	report.States = states
	for _, st := range states {
		switch {
		case st.Verified:
			report.Verified++
		case st.Adopted():
			// 已在上面的 Inferred 计数里
		default:
			report.Failed = append(report.Failed, st.Pack)
		}
	}
	// 网络层错误必须与「链接不存在」区分开：受影响曲包只是本轮判不出来，
	// 给出可读原因与排查建议，让用户重试即可，而不是以为曲包真的下架了。
	if netErr := firstNetworkError(states); netErr != nil {
		msgf("      链接校验遇到网络层错误：%s", briefError(netErr))
		msgf("      受影响的曲包无法判定链接是否存在，已保守地记入失败列表；网络恢复后重跑即可。")
		msgf("      校验目标主机: %s", hostOf(osuPackURLPrefix))
		printNetworkHelp()
	}
	return report
}

// verifiedAnchors 返回已逐条校验通过的曲包下标。
func verifiedAnchors(states []packLinkState) []int {
	out := make([]int, 0, len(states))
	for i := range states {
		if states[i].Verified && states[i].URL != "" {
			out = append(out, i)
		}
	}
	return out
}

// boundaryVerifyIndices 找出不能用推断、必须逐条校验的曲包下标。
//
// 判定依据：相邻两个已验证锚点使用了**不同**结构时，它们之间那一段必然包含一次
// 名称变体或扩展名的翻转，具体位置无从推断，只能逐条确认；两端没有参照的曲包同理。
// 结构一致的区段可以整段沿用锚点的结构，因此这部分额外开销只发生在翻转点附近。
func boundaryVerifyIndices(states []packLinkState) []int {
	anchors := verifiedAnchors(states)
	if len(anchors) == 0 {
		return nil
	}
	out := make([]int, 0, len(states)/4)
	appendRun := func(lo, hi int) {
		for i := lo; i <= hi; i++ {
			if i < 0 || i >= len(states) {
				continue
			}
			st := states[i]
			if st.Verified || st.Failed {
				continue
			}
			out = append(out, i)
		}
	}
	// 首个锚点之前没有参照。
	appendRun(0, anchors[0]-1)
	for k := 0; k+1 < len(anchors); k++ {
		lo, hi := anchors[k], anchors[k+1]
		if states[lo].Structure == states[hi].Structure {
			continue
		}
		appendRun(lo+1, hi-1)
	}
	// 末个锚点之后没有参照。
	appendRun(anchors[len(anchors)-1]+1, len(states)-1)
	return out
}

// assignInferred 给「没被抽到、也没失败」的曲包按最近的已验证邻居推断结构，
// 返回推断出链接的曲包数量。没有任何已验证锚点时不做推断（避免整批盲猜）。
func assignInferred(states []packLinkState) int {
	anchors := verifiedAnchors(states)
	if len(anchors) == 0 {
		return 0
	}
	inferred := 0
	for i := range states {
		st := &states[i]
		if st.Verified || st.Failed || st.URL != "" {
			continue
		}
		ref := states[nearestAnchor(anchors, i)]
		st.URL = st.Pack.PackLinkURL(ref.Structure)
		st.Structure = ref.Structure
		st.Inferred = true
		inferred++
	}
	return inferred
}

// nearestAnchor 返回 anchors 中距 i 最近的下标；同距时取靠前的一个
// （区段通常从前往后延伸，偏向前一个锚点更符合实际翻转位置）。
func nearestAnchor(anchors []int, i int) int {
	best := anchors[0]
	bestDist := absInt(best - i)
	for _, a := range anchors[1:] {
		d := absInt(a - i)
		if d < bestDist || (d == bestDist && a < best) {
			best, bestDist = a, d
		}
	}
	return best
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// firstNetworkError 返回第一个网络层校验错误。
func firstNetworkError(states []packLinkState) error {
	for _, st := range states {
		if st.NetErr != nil && isNetworkFailure(st.NetErr) {
			return st.NetErr
		}
	}
	return nil
}

// repairRound 执行一轮定向修复：
//  1. 对失败链接取 ±window 邻近曲包（含自身，窗口去重）交浏览器解析；
//  2. 自身解析出真实链接的失败曲包直接采用该链接；
//  3. 相邻两个失败链接解析出的结构一致时，按其结构填充两者之间「当前校验失败」的曲包；
//  4. 返回需要复验的下标（填充结果必须复验）。
func repairRound(ctx context.Context, states []packLinkState, failures []int, deps linkRepairDeps) (bool, []int) {
	probe := probeIndices(failures, len(states), deps.window)
	probePacks := make([]Pack, 0, len(probe))
	for _, idx := range probe {
		probePacks = append(probePacks, states[idx].Pack)
	}
	outcomes := deps.resolve(ctx, probePacks)

	resolved := make(map[int]string, len(probe))
	learned := make(map[int]linkStructure, len(probe))
	for i, idx := range probe {
		if i >= len(outcomes) {
			break
		}
		out := outcomes[i]
		if out.RequiresLogin {
			return true, nil
		}
		if !out.Found || out.Href == "" {
			continue
		}
		resolved[idx] = out.Href
		if st, ok := structureOfLink(states[idx].Pack, out.Href); ok {
			learned[idx] = st
		}
	}

	touched := make(map[int]bool, len(failures)*2)

	// 1) 自身就解析出真实链接的失败曲包：直接采用该链接（复验通过后才生效）。
	for _, idx := range failures {
		if href := resolved[idx]; href != "" && !states[idx].Verified {
			states[idx].Direct = href
			touched[idx] = true
		}
	}

	// 2) 相邻失败链接结构一致时，按其结构填充两者之间的曲包。
	for a := 0; a+1 < len(failures); a++ {
		i, k := failures[a], failures[a+1]
		sti, ok1 := learned[i]
		stk, ok2 := learned[k]
		if !ok1 || !ok2 || sti != stk {
			continue
		}
		for idx := i; idx <= k; idx++ {
			// 只重写当前校验失败的链接：已通过校验的保持不变。
			if states[idx].Verified {
				continue
			}
			states[idx].Candidates = []linkStructure{sti}
			states[idx].Structure = sti
			touched[idx] = true
		}
	}

	if len(touched) == 0 {
		return false, nil
	}
	out := make([]int, 0, len(touched))
	for idx := range touched {
		out = append(out, idx)
	}
	sort.Ints(out)
	return false, out
}

// preferStructure 把 seed 放到候选列表最前面（去重）：
// 「沿用邻段结构」时命中只需 1 次 HEAD，未命中再按固定顺序退让，不影响正确性。
func preferStructure(cands []linkStructure, seed linkStructure) []linkStructure {
	if seed.Variant == "" {
		return cands
	}
	out := make([]linkStructure, 0, len(cands)+1)
	out = append(out, seed)
	for _, c := range cands {
		if c == seed {
			continue
		}
		out = append(out, c)
	}
	return out
}

// verifyStatesSeeded 逐条校验给定下标（并发受 deps.concurrency 限制），
// 每个曲包优先沿用「上一批命中」的结构。命名规则成片一致时几乎每包只发 1 次 HEAD，
// 这也正是实测吞吐从「每包 2~4 次 HEAD」降到「每包约 1 次」的原因。
// 结构不一致时自动退回完整候选空间，因此逐条确认的结论不受影响。
func verifyStatesSeeded(ctx context.Context, states []packLinkState, indices []int, deps linkRepairDeps, seed linkStructure) {
	if len(indices) == 0 {
		return
	}
	batch := deps.concurrency
	if batch < 1 {
		batch = 1
	}
	for start := 0; start < len(indices); start += batch {
		end := start + batch
		if end > len(indices) {
			end = len(indices)
		}
		chunk := indices[start:end]
		for _, idx := range chunk {
			st := &states[idx]
			if st.Verified || st.Failed || len(st.Candidates) == 1 {
				continue
			}
			st.Candidates = preferStructure(st.Candidates, seed)
		}
		verifyStates(ctx, states, chunk, deps)
		for i := len(chunk) - 1; i >= 0; i-- {
			if s := states[chunk[i]]; s.Verified {
				seed = s.Structure
				break
			}
		}
	}
}

// verifyStates 并发校验给定下标的曲包，在途校验数不超过 deps.concurrency。
func verifyStates(ctx context.Context, states []packLinkState, indices []int, deps linkRepairDeps) {
	if len(indices) == 0 {
		return
	}
	// 曲包很多时逐条打印会刷屏（1873 个曲包就是 1873 行），改为按比例输出进度；
	// 小批量（≤50）仍逐条打印，方便观察与测试。失败项一律立即输出。
	logEvery := int64(1)
	if len(indices) > 50 {
		logEvery = 25
	}
	workers := deps.concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	var (
		next int64
		done int64
		wg   sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				pos := int(atomic.AddInt64(&next, 1)) - 1
				if pos >= len(indices) {
					return
				}
				st := &states[indices[pos]]
				verifyOneState(ctx, st, deps)
				seq := atomic.AddInt64(&done, 1)
				mark := "✓"
				if !st.Verified {
					mark = "✗"
				}
				if mark == "✗" || seq%logEvery == 0 || seq == int64(len(indices)) {
					msgf("      校验链接 (%d/%d): %s %s", seq, len(indices), st.Pack.Tag, mark)
				}
			}
		}()
	}
	wg.Wait()
}

// verifyOneState 依次校验一个曲包的候选链接；命中即采用。
// 浏览器直接解析出的链接优先，校验失败则退回候选空间。
func verifyOneState(ctx context.Context, st *packLinkState, deps linkRepairDeps) {
	st.Verified = false
	st.Failed = false
	st.URL = ""
	st.NetErr = nil

	if st.Direct != "" {
		status, err := deps.check(ctx, st.Direct)
		if err == nil && status == linkOK {
			st.URL = st.Direct
			st.Structure = linkStructure{Variant: "resolved"}
			st.Status = linkOK
			st.Verified = true
			return
		}
		if err != nil && isNetworkFailure(err) {
			st.NetErr = err
			st.Status = linkError
			return
		}
		// 直接链接不可用：退回候选空间继续尝试。
		st.Direct = ""
		st.Status = status
	}

	for _, st2 := range st.Candidates {
		link := st.Pack.PackLinkURL(st2)
		status, err := deps.check(ctx, link)
		if err != nil && isNetworkFailure(err) {
			st.NetErr = err
			st.Status = linkError
			return
		}
		if status == linkOK {
			st.URL = link
			st.Structure = st2
			st.Status = linkOK
			st.Verified = true
			return
		}
		st.Status = status
		if ctx.Err() != nil {
			return
		}
	}
	st.Failed = true
}

// failingAmong 返回给定下标中当前校验失败的曲包下标（保持顺序）。
func failingAmong(states []packLinkState, indices []int) []int {
	out := make([]int, 0, len(indices))
	for _, idx := range indices {
		if idx >= 0 && idx < len(states) && states[idx].Failed && !states[idx].Verified {
			out = append(out, idx)
		}
	}
	return out
}

func countFailedStates(states []packLinkState) int {
	n := 0
	for _, st := range states {
		if st.Failed && !st.Verified {
			n++
		}
	}
	return n
}

func normalizeRepairDeps(deps linkRepairDeps) linkRepairDeps {
	if deps.sampleRate <= 0 {
		deps.sampleRate = defaultVerifyRate
	}
	if deps.concurrency < 1 {
		deps.concurrency = defaultLookupConcurrency
	}
	if deps.window < 1 {
		deps.window = defaultRepairWindow
	}
	if deps.check == nil {
		deps.check = func(context.Context, string) (linkCheckStatus, error) { return linkMissing, nil }
	}
	if deps.resolve == nil {
		deps.resolve = func(_ context.Context, packs []Pack) []packResolveOutcome {
			return make([]packResolveOutcome, len(packs))
		}
	}
	return deps
}
