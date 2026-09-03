package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

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
	return fmt.Sprintf("https://osu.ppy.sh/beatmaps/packs?type=%s", siteType), nil
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
)

// PackDirectURL 根据 Tag+Name 构造 packs.ppy.sh 官方直链。
// 实测命名规则: "https://packs.ppy.sh/<tag> - <name>.zip"（路径段使用 %20 编码）。
func PackDirectURL(p Pack) string {
	fileName := fmt.Sprintf("%s - %s.zip", p.Tag, p.Name)
	return osuPackURLPrefix + url.PathEscape(fileName)
}

// HTTPGetWithCookie 携带 Cookie 抓取页面（T5 底层函数）。
// 返回 (页面字节, 状态码, 错误)。状态码 >=400 不视为错误，由调用方决定。
func HTTPGetWithCookie(ctx context.Context, pageURL, cookieHeader string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// fetchPageWithRetry 对一次 GET 做最多 3 次重试（仅网络错误/5xx/403 时）。
func fetchPageWithRetry(ctx context.Context, pageURL, cookie string) ([]byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		body, status, err := HTTPGetWithCookie(ctx, pageURL, cookie)
		if err != nil {
			lastErr = err
		} else if status == 200 {
			return body, status, nil
		} else {
			lastErr = fmt.Errorf("HTTP %d", status)
			if status != 403 && status < 500 {
				return body, status, lastErr
			}
		}
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return nil, 0, lastErr
}

// ScrapeLinks 抓取一个分类（分页）下所有曲包并做模式过滤（T5）。
func ScrapeLinks(ctx context.Context, catID int, mode, cookie string) ScrapeResult {
	baseURL, err := BuildURL(catID, mode)
	if err != nil {
		return ScrapeResult{Failed: true, Reason: err.Error()}
	}

	seen := map[string]bool{}
	var packs []Pack
	failReason := ""

	for pageNo := 1; pageNo <= 1000; pageNo++ {
		u := baseURL
		if pageNo > 1 {
			u = baseURL + fmt.Sprintf("&page=%d", pageNo)
		}
		body, status, err := fetchPageWithRetry(ctx, u, cookie)
		if err != nil {
			if status == 404 && pageNo > 1 {
				// 已翻到最后一页之后。
				break
			}
			failReason = fmt.Sprintf("抓取 %s 失败: %v", u, err)
			if status == 403 {
				failReason = "被站点拒绝(403)，可能需要有效 Cookie 或稍后重试"
			}
			break
		}
		pagePacks, hasNext, parseErr := extractPacks(body, baseURL, pageNo)
		if parseErr != nil {
			failReason = fmt.Sprintf("解析 %s 失败: %v", u, parseErr)
			break
		}
		added := 0
		for _, p := range pagePacks {
			if !PackMatchesMode(catID, mode, p) {
				continue
			}
			if seen[p.Tag] {
				continue
			}
			seen[p.Tag] = true
			p.DirectURL = PackDirectURL(p)
			packs = append(packs, p)
			added++
		}
		msgf("  第 %d 页: 本页匹配 %d 个曲包，累计 %d 个", pageNo, added, len(packs))
		if !hasNext || len(pagePacks) == 0 {
			break
		}
	}

	if failReason != "" {
		return ScrapeResult{Failed: true, Reason: failReason}
	}
	if len(packs) == 0 {
		return ScrapeResult{Failed: true, Reason: "该分类/模式下没有提取到任何曲包"}
	}
	return ScrapeResult{Packs: packs}
}

// extractPacks 解析列表页 HTML，返回曲包列表与“是否有下一页”。
func extractPacks(body []byte, baseURL string, currentPage int) ([]Pack, bool, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	var packs []Pack
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && strings.Contains(" "+attr(n, "class")+" ", " js-beatmap-pack ") {
			if tag := attr(n, "data-pack-tag"); tag != "" {
				if p, ok := packFromNode(n, baseURL, tag); ok {
					packs = append(packs, p)
				}
				// 曲包元素内不会再嵌套曲包元素。
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// 通过页面中的分页链接判断是否有下一页：存在页码 > 当前页 的链接即继续。
	hasNext := false
	for _, a := range allAnchors(doc) {
		href := attr(a, "href")
		if href == "" {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		q := parsed.Query()
		if raw := q.Get("page"); raw != "" {
			if n, convErr := strconv.Atoi(raw); convErr == nil && n > currentPage {
				hasNext = true
				break
			}
		}
	}
	return packs, hasNext, nil
}

func allAnchors(doc *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// packFromNode 从 js-beatmap-pack 节点中提取 tag/name/页面链接。
func packFromNode(root *html.Node, baseURL, tag string) (Pack, bool) {
	var name string
	var pageURL string
	var find func(*html.Node)
	find = func(n *html.Node) {
		if name != "" && pageURL != "" {
			return
		}
		if n.Type == html.ElementNode {
			if pageURL == "" && n.Data == "a" {
				if href := attr(n, "href"); href != "" && strings.Contains(href, "/beatmaps/packs/"+tag) {
					pageURL = href
				}
			}
			if name == "" && n.Data == "span" && strings.Contains(" "+attr(n, "class")+" ", " beatmap-pack__name ") {
				name = normalizeSpace(textContent(n))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if name == "" || tag == "" {
		return Pack{}, false
	}
	if pageURL == "" {
		pageURL = baseURL
		if !strings.Contains(baseURL, "/beatmaps/packs/") {
			base := "https://osu.ppy.sh"
			if baseURL != "" {
				if u, err := url.Parse(baseURL); err == nil {
					base = u.Scheme + "://" + u.Host
				}
			}
			pageURL = base + "/beatmaps/packs/" + tag
		}
	}
	return Pack{Tag: tag, Name: name, PageURL: pageURL}, true
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

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
