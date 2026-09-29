package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// proxyURL 由 -proxy 启动参数设置；为空时使用系统/环境变量代理。
var proxyURL string

var (
	clientOnce   sync.Once
	cachedClient *http.Client
)

// getHTTPClient 返回全局复用的 HTTP 客户端（支持显式代理与系统代理，复用连接）。
// 该客户端只用于访问不受站点拦截的 packs.ppy.sh（HEAD 连通性校验）。
func getHTTPClient() *http.Client {
	clientOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyFromEnvironment
		if p := strings.TrimSpace(proxyURL); p != "" {
			if u, err := url.Parse(p); err == nil && u.Host != "" {
				transport.Proxy = http.ProxyURL(u)
				msgf("已启用代理: %s", u.String())
			} else {
				msgf("警告: 代理地址 %q 无法解析，将按系统默认方式联网。", p)
			}
		}
		cachedClient = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	})
	return cachedClient
}

// BuildURL 根据分类与模式拼接曲包列表页 URL（T4）。
// mode 不影响列表页地址（模式通过 tag/名称过滤），这里仅校验参数合法性。
func BuildURL(catID int, mode string) (string, error) {
	cat, ok := CategoryMap[catID]
	if !ok {
		return "", fmt.Errorf("非法分类编号: %d", catID)
	}
	if len(cat.Modes) > 0 && mode == "" {
		return "", fmt.Errorf("分类 %q 需要选择子模式", cat.Name)
	}
	if len(cat.Modes) > 0 && !contains(cat.Modes, mode) {
		return "", fmt.Errorf("分类 %q 不支持模式 %q", cat.Name, mode)
	}
	siteType, ok := SiteTypeByCatID[catID]
	if !ok {
		return "", fmt.Errorf("分类 %d 未配置站点类型", catID)
	}
	return fmt.Sprintf("%s/beatmaps/packs?type=%s", osuSiteOrigin, siteType), nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var (
	linkRe           = regexp.MustCompile(`^https://osu\.ppy\.sh/`)
	tagRe            = regexp.MustCompile(`^([A-Z]+)([0-9]+)$`)
	osuPackURLPrefix = "https://packs.ppy.sh/"
	extensionRe      = regexp.MustCompile(`(?i)\.(7z|zip)$`)
)

// ---------- 名称变体与候选构造 ----------

// legacyModeLabels 把官网现代模式标签映射到 CDN 上使用的历史短名。
// 实测：osu! -> ""（直接去掉前缀）、osu!mania -> Mania、osu!taiko -> Taiko、
// osu!catch -> Catch the Beat（注意不是 "Catch"）。
// 顺序敏感：必须先匹配更长的标签（osu!mania / osu!taiko / osu!catch 在 osu! 之前）。
var legacyModeLabels = []struct{ modern, legacy string }{
	{"osu!mania", "Mania"},
	{"osu!taiko", "Taiko"},
	{"osu!catch", "Catch the Beat"},
	{"osu!", ""},
}

// linkStructure 描述一个曲包使用的「名称变体 + 扩展名」组合。
//
// 变体标签：
//   - modern：官网显示名原样（如 osu!mania Beatmap Pack #111）
//   - short ：内置派生表得到的历史短名（如 Mania Beatmap Pack #111）
//   - learned：运行时从实测链接学到的历史短名规则（Label 替换现代名末尾 Keep 个词）
//
// 名称段始终按曲包逐条派生，因此同一个结构可以套用到整段区段的曲包上。
type linkStructure struct {
	Variant   string
	Extension string
	Label     string // 仅 Variant == "learned" 时使用
	Keep      int    // 仅 Variant == "learned" 时使用
}

// shortVariantName 从官网显示名派生历史短名。
// 覆盖不了时原样返回（这类曲包靠学习结果覆盖或浏览器解析兜底）。
func shortVariantName(name string) string {
	for _, m := range legacyModeLabels {
		if !strings.HasPrefix(name, m.modern) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(name, m.modern))
		if m.legacy == "" {
			return normalizeSpace(rest)
		}
		return normalizeSpace(m.legacy + " " + rest)
	}
	return name
}

// nameVariants 返回曲包名称的现代形式与历史短名。
func nameVariants(name string) (modern, short string) {
	modern = normalizeSpace(name)
	return modern, shortVariantName(modern)
}

// applyLearnedLabel 按学习到的规则派生名称段：把现代名末尾 Keep 个词保留，前缀替换为 Label。
func applyLearnedLabel(modernName, label string, keep int) string {
	words := strings.Fields(normalizeSpace(modernName))
	if keep <= 0 || keep > len(words) {
		return normalizeSpace(modernName)
	}
	rest := strings.Join(words[len(words)-keep:], " ")
	if strings.TrimSpace(label) == "" {
		return rest
	}
	return normalizeSpace(label + " " + rest)
}

// variantNameFor 按结构派生某个曲包的名称段。
func variantNameFor(p Pack, st linkStructure) string {
	switch st.Variant {
	case "short":
		_, short := nameVariants(p.Name)
		return short
	case "learned":
		return applyLearnedLabel(p.Name, st.Label, st.Keep)
	default:
		return normalizeSpace(p.Name)
	}
}

// CandidateStructures 返回曲包的候选结构：2 个名称变体 × 2 个扩展名，共 4 个。
//
// 顺序按实测频率排列——候选顺序直接决定校验耗时（每多试一个候选就多一次 HEAD）：
//  1. 官网名 + .zip：新区段（实测 SM175 之后、S1350 之后）
//  2. 历史短名 + .7z：老区段（实测 S1–S1250、SM1–SM150、ST1–ST200、SC1–SC80）
//  3. 历史短名 + .zip：过渡带（实测 S1250–S1349、SM155–SM174）
//  4. 官网名 + .7z：实测未采到，仅作兜底
//
// 这样两个主要区段都只需 1 次 HEAD 即可命中。
func CandidateStructures(p Pack) []linkStructure {
	return []linkStructure{
		{Variant: "modern", Extension: ".zip"},
		{Variant: "short", Extension: ".7z"},
		{Variant: "short", Extension: ".zip"},
		{Variant: "modern", Extension: ".7z"},
	}
}

// packLinkURL 按「<tag> - <名称变体><扩展名>」规则拼接官方直链。
func packLinkURL(tag, variantName, extension string) string {
	fileName := fmt.Sprintf("%s - %s%s", tag, variantName, extension)
	return osuPackURLPrefix + url.PathEscape(fileName)
}

// PackLinkURL 返回该曲包在给定结构下的官方直链。
func (p Pack) PackLinkURL(st linkStructure) string {
	return packLinkURL(p.Tag, variantNameFor(p, st), st.Extension)
}

// linkVariantName 从已解析出的真实链接中取出名称段。
func linkVariantName(p Pack, link string) (string, string, bool) {
	ext := linkExtension(link)
	if ext == "" {
		return "", "", false
	}
	base := link
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if decoded, err := url.PathUnescape(base); err == nil {
		base = decoded
	}
	base = strings.TrimSuffix(base, ext)
	prefix := p.Tag + " - "
	if !strings.HasPrefix(base, prefix) {
		return "", "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(base, prefix)), ext, true
}

// deriveLearnedLabel 从「现代名」与「实测名称段」推断历史短名规则。
// 例：现代名 "osu!mania Beatmap Pack #111"、实测 "Mania Beatmap Pack #111"
// -> Label = "Mania"，Keep = 3（"Beatmap Pack #111"）。
func deriveLearnedLabel(modernName, resolvedName string) (string, int, bool) {
	m := strings.Fields(normalizeSpace(modernName))
	r := strings.Fields(normalizeSpace(resolvedName))
	if len(m) == 0 || len(r) == 0 {
		return "", 0, false
	}
	i, j := len(m)-1, len(r)-1
	for i >= 0 && j >= 0 && m[i] == r[j] {
		i--
		j--
	}
	keep := len(m) - (i + 1)
	if j < 0 {
		// 实测名称段只是现代名的后缀（没有可替换的前缀），无法学习成规则。
		return "", 0, false
	}
	if keep == 0 {
		// 现代名末尾没有可复用的固定部分（例如完全不同的命名），不学习。
		return "", 0, false
	}
	return strings.Join(r[:j+1], " "), keep, true
}

// structureOfLink 从已解析出的真实链接反推该曲包使用的结构（用于学习区段的变体与扩展名）。
func structureOfLink(p Pack, link string) (linkStructure, bool) {
	resolved, ext, ok := linkVariantName(p, link)
	if !ok {
		return linkStructure{}, false
	}
	modern, short := nameVariants(p.Name)
	switch resolved {
	case modern:
		return linkStructure{Variant: "modern", Extension: ext}, true
	case short:
		return linkStructure{Variant: "short", Extension: ext}, true
	}
	if label, keep, ok := deriveLearnedLabel(p.Name, resolved); ok {
		return linkStructure{Variant: "learned", Extension: ext, Label: label, Keep: keep}, true
	}
	return linkStructure{}, false
}

// linkExtension 返回链接的扩展名（.zip / .7z），无法识别时返回空串。
func linkExtension(link string) string {
	trimmed := strings.TrimSpace(link)
	if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
		trimmed = trimmed[:i]
	}
	m := extensionRe.FindString(trimmed)
	if m == "" {
		return ""
	}
	return strings.ToLower(m)
}

// ---------- 连通性校验（HEAD） ----------

// linkCheckStatus 表示一次链接连通性校验的结果。
type linkCheckStatus int

const (
	// linkOK 链接存在（2xx）。
	linkOK linkCheckStatus = iota
	// linkMissing 链接明确不存在（4xx，例如 404）。
	linkMissing
	// linkError 网络层错误（超时/DNS/连接被拒等），无法判定链接是否存在。
	linkError
)

func (s linkCheckStatus) String() string {
	switch s {
	case linkOK:
		return "存在"
	case linkMissing:
		return "不存在"
	default:
		return "网络错误"
	}
}

// checkLink 用 HEAD 校验单个链接的连通性，开销最小。
// 「不存在」与「网络层错误」严格区分：网络错误绝不能被当成「不存在」。
func checkLink(ctx context.Context, client *http.Client, link string) (linkCheckStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return linkError, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return linkError, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return linkOK, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return linkMissing, nil
	default:
		return linkError, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// ---------- 模式过滤 ----------

// PackMatchesMode 判断曲包是否属于所选子模式。
// 各分类的过滤依据：
//
//	1 常规       -> tag 前缀 S / SC / ST / SM
//	3 锦标赛     -> 名称关键词（osu! / osu!catch / osu!taiko / osu!mania 4k / 7k）
//	4/6 社区喜爱、聚光灯 -> 名称中的 "(osu!x)" 后缀
func PackMatchesMode(catID int, mode string, p Pack) bool {
	if mode == "" {
		return true
	}
	switch catID {
	case 1:
		letters := tagLetters(p.Tag)
		switch strings.ToLower(mode) {
		case "osu!":
			return letters == "S"
		case "osu!catch":
			return letters == "SC"
		case "osu!taiko":
			return letters == "ST"
		case "osu!mania":
			return letters == "SM"
		}
		return false
	case 3:
		return tournamentModeMatches(mode, p.Name)
	case 4, 6:
		return suffixModeMatches(mode, p.Name)
	default:
		return true
	}
}

func tagLetters(tag string) string {
	if m := tagRe.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

func tournamentModeMatches(mode, name string) bool {
	lower := strings.ToLower(name)
	switch strings.ToLower(mode) {
	case "osu!mania 7k":
		return strings.Contains(lower, "osu!mania 7k")
	case "osu!mania 4k":
		// 老版本部分曲包名不含 4K 字样（如 osu!mania World Cup 2015），归入 4K。
		return strings.Contains(lower, "osu!mania 4k") ||
			(strings.Contains(lower, "osu!mania") && !strings.Contains(lower, "osu!mania 7k"))
	case "osu!catch":
		return strings.Contains(lower, "osu!catch")
	case "osu!taiko":
		return strings.Contains(lower, "osu!taiko")
	default: // osu!
		return strings.Contains(lower, "osu!") &&
			!strings.Contains(lower, "osu!catch") &&
			!strings.Contains(lower, "osu!taiko") &&
			!strings.Contains(lower, "osu!mania")
	}
}

func suffixModeMatches(mode, name string) bool {
	lower := strings.ToLower(name)
	suffix := ""
	switch strings.ToLower(mode) {
	case "osu!":
		suffix = "(osu!)"
	case "osu!catch":
		suffix = "(osu!catch)"
	case "osu!taiko":
		suffix = "(osu!taiko)"
	case "osu!mania":
		suffix = "(osu!mania)"
	}
	return suffix != "" && strings.Contains(lower, suffix)
}

// ValidatePageURL 校验列表页 URL 前缀（T4 验收）。
func ValidatePageURL(u string) bool {
	return linkRe.MatchString(u)
}

// ---------- 错误与文本工具 ----------

// isNetworkFailure 判断错误是否属于“连不上站点”层级（DNS/超时/连接被拒绝等）。
func isNetworkFailure(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// briefError 把底层错误压缩成简短可读的中文原因。
func briefError(err error) string {
	if err == nil {
		return "未知错误"
	}
	raw := err.Error()
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "forbidden by its access permissions"):
		return "连接被系统拒绝(WSAEACCES)，通常由防火墙/安全软件或受限沙箱环境导致"
	case strings.Contains(lower, "no such host"):
		return "域名解析失败，请检查 DNS 或网络"
	case strings.Contains(lower, "proxyconnect") || strings.Contains(lower, "proxy"):
		return "代理连接失败，请检查代理地址与端口"
	case strings.Contains(lower, "connection refused"):
		return "连接被拒绝，请检查网络或代理"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "连接超时，请检查网络或代理"
	case strings.HasPrefix(raw, "HTTP "):
		return raw
	default:
		if len(raw) > 160 {
			raw = raw[:160] + "…"
		}
		return raw
	}
}

// hostOf 返回 URL 的 host，解析失败时返回原字符串。
func hostOf(u string) string {
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return u
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
