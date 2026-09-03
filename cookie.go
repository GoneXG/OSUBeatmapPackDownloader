package main

import (
	"fmt"
	"strings"
)

// CookieValid 判断 Cookie 是否满足“包含 osu_sid 且非空”的验收条件。
func CookieValid(c Cookie) bool {
	return strings.TrimSpace(c.Value) != ""
}

// GetCookie T3：优先 rod 自动登录，失败/超时自动降级到手动粘贴。
// 用户在手动提示处直接回车 => Skip（留空 Cookie，仅提示需人工登录）。
func GetCookie() Cookie {
	msgf("[T3] 尝试自动登录 osu!（将打开浏览器，请在 %s 内完成登录）...", CookieTimeout)
	val, err := TryAutoLogin()
	if err == nil && val != "" {
		msgf("      自动登录成功，已取得 osu_sid。")
		return Cookie{Value: val, Source: "auto"}
	}
	if err != nil {
		msgf("      自动登录不可用（%v），降级为手动粘贴 Cookie。", err)
	} else {
		msgf("      自动登录未取得 Cookie，降级为手动粘贴。")
	}
	return ManualPastePrompt()
}

// ManualPastePrompt 指导用户从浏览器复制 osu_sid 并粘贴。
func ManualPastePrompt() Cookie {
	fmt.Println("\n========== 手动获取 osu_sid ==========")
	fmt.Println("1. 用浏览器打开 https://osu.ppy.sh/home 并登录账号；")
	fmt.Println("2. 按 F12 打开开发者工具 -> Application/Cookie -> https://osu.ppy.sh；")
	fmt.Println("3. 找到名为 osu_sid 的条目，复制它的 Value；")
	fmt.Println("4. 回到本程序粘贴后回车。直接回车 = 跳过登录（Skip）。")
	fmt.Print("osu_sid 值: ")
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return Cookie{Source: "skipped", Note: "读取输入失败，视为跳过"}
	}
	raw := strings.TrimSpace(line)
	if raw == "" {
		return Cookie{Source: "skipped", Note: "需人工登录，本次留空 Cookie"}
	}
	val := ParseOSUSid(raw)
	if val == "" {
		fmt.Println("未能从中识别 osu_sid，将留空继续（下载多数曲包无需登录）。")
		return Cookie{Source: "manual", Note: "粘贴内容无法解析"}
	}
	return Cookie{Value: val, Source: "manual"}
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
