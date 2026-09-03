package main

import (
	"fmt"
	"strings"
)

// ManualCookieInput 指导用户从浏览器复制 osu_sid 并粘贴。
// 返回 (osu_sid 值, 是否选择跳过)。用户直接回车视为跳过。
func ManualCookieInput() (string, bool) {
	fmt.Println("\n========== 手动获取 osu_sid ==========")
	fmt.Println("1. 用浏览器打开 https://osu.ppy.sh/home 并登录账号；")
	fmt.Println("2. 按 F12 打开开发者工具 -> Application/Cookie -> https://osu.ppy.sh；")
	fmt.Println("3. 找到名为 osu_sid 的条目，复制它的 Value；")
	fmt.Println("4. 回到本程序粘贴后回车（只粘贴 Value 即可；整段 osu_sid=xxx 也能自动解析）。")
	fmt.Println("   直接回车 = 跳过（放弃重试）。")
	fmt.Print("osu_sid 值: ")
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", true
	}
	raw := strings.TrimSpace(line)
	if raw == "" {
		return "", true
	}
	val := ParseOSUSid(raw)
	if val == "" {
		fmt.Println("未能从中识别 osu_sid，视为放弃重试。")
		return "", true
	}
	return val, false
}

// ParseOSUSid 兼容三种粘贴格式：
//  1. 仅值：       abcdef...
//  2. Cookie 键值： osu_sid=abcdef; other=x
//  3. 完整请求头：  Cookie: osu_sid=abcdef; other=x
func ParseOSUSid(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "Cookie:")
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if i := strings.Index(lower, "osu_sid="); i >= 0 {
		s = s[i+len("osu_sid="):]
		if j := strings.IndexAny(s, ";\r\n"); j >= 0 {
			s = s[:j]
		}
		return strings.TrimSpace(s)
	}
	// 没有等号键名：可能只粘贴了值，也可能整段是 Cookie。
	if strings.Contains(s, "=") {
		// 形如 osu_sid 之外的其他键值，无法识别 -> 空
		return ""
	}
	return s
}
