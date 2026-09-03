package main

import (
	"fmt"
	"strings"
)

const (
	// osuSessionCookieName 是 osu! 官网当前实际使用的会话 Cookie 名。
	osuSessionCookieName = "osu_session"
	// legacyOSUSidCookieName 旧版程序/教程中误用的名字，粘贴时兼容并自动纠正。
	legacyOSUSidCookieName = "osu_sid"
)

// ManualCookieInput 指导用户从浏览器复制 osu_session Cookie 并粘贴。
// 返回 (可直接放入 Cookie 请求头的字符串, 是否选择跳过)。用户直接回车视为跳过。
func ManualCookieInput() (string, bool) {
	fmt.Println("\n========== 手动获取 osu_session Cookie ==========")
	fmt.Println("1. 用浏览器打开 https://osu.ppy.sh/home 并登录账号；")
	fmt.Println("2. 按 F12 打开开发者工具 -> Application -> Cookies -> https://osu.ppy.sh；")
	fmt.Println("3. 找到名为 osu_session 的条目，复制它的 Value；")
	fmt.Println("4. 回到本程序粘贴后回车（只粘贴 Value 即可；整段 osu_session=xxx 或完整 Cookie 请求头也能自动解析）。")
	fmt.Println("   直接回车 = 跳过（放弃重试）。")
	fmt.Print("osu_session Cookie: ")
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", true
	}
	raw := strings.TrimSpace(line)
	if raw == "" {
		return "", true
	}
	header := ParseCookieHeader(raw)
	if header == "" {
		fmt.Println("未能从中识别 osu_session Cookie，视为放弃重试。")
		return "", true
	}
	return header, false
}

// ParseCookieHeader 兼容常见粘贴格式，返回可直接用于 Cookie 请求头的整段字符串：
//  1. 仅 Value：    abcdef...                  -> osu_session=abcdef...
//  2. Cookie 键值： osu_session=abcdef; other=x -> 规范化后原样保留
//  3. 完整请求头：  Cookie: osu_session=abcdef; ... -> 同 2
//
// 兼容旧写法 osu_sid=...（自动纠正为 osu_session）；
// 粘贴内容中找不到 osu_session/osu_sid 时返回空串。
func ParseCookieHeader(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 去掉可能粘贴进来的 "Cookie:" 前缀。
	if len(s) >= 7 && strings.EqualFold(s[:7], "Cookie:") {
		s = strings.TrimSpace(s[7:])
		if s == "" {
			return ""
		}
	}

	// 只粘贴了 Value（无等号键名）：按官网实际的 osu_session 名构造请求头。
	if !strings.Contains(s, "=") {
		s = strings.Trim(strings.TrimSpace(s), `"`)
		if s == "" {
			return ""
		}
		return osuSessionCookieName + "=" + s
	}

	// 粘贴的是 key=value 列表（可能含多组 Cookie 或整段请求头）。
	var kept []string
	foundSession := false
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq <= 0 {
			continue // 无键名的孤立片段无法解析，跳过
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		if key == "" || val == "" {
			continue
		}
		if strings.EqualFold(key, legacyOSUSidCookieName) ||
			strings.EqualFold(key, osuSessionCookieName) {
			foundSession = true
			key = osuSessionCookieName
		}
		kept = append(kept, key+"="+val)
	}
	if !foundSession {
		return ""
	}
	return strings.Join(kept, "; ")
}
