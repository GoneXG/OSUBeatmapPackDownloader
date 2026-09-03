package main

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// TryAutoLogin 用 rod 拉起浏览器，等待用户在 CookieTimeout 内登录 osu!，
// 成功后返回 osu_sid 值；失败返回 error（由 GetCookie 降级到手动）。
func TryAutoLogin() (string, error) {
	browserPath, found := launcher.LookPath()
	if !found {
		return "", fmt.Errorf("未找到 Chrome/Edge，无法自动登录")
	}

	launcherInst := launcher.New().Bin(browserPath).Headless(false)
	controlURL, err := launcherInst.Launch()
	if err != nil {
		return "", fmt.Errorf("浏览器启动失败: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return "", fmt.Errorf("连接浏览器失败: %w", err)
	}
	defer func() { _ = browser.Close() }()

	if _, err := browser.Page(proto.TargetCreateTarget{URL: "https://osu.ppy.sh/home"}); err != nil {
		return "", fmt.Errorf("打开 osu! 页面失败: %v", err)
	}

	deadline := time.Now().Add(CookieTimeout)
	lastReport := time.Now()
	for {
		if c, err := browser.GetCookies(); err == nil {
			for _, ck := range c {
				if ck.Name == "osu_sid" && ck.Value != "" {
					return ck.Value, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待登录超时(%s)", CookieTimeout)
		}
		if time.Since(lastReport) >= 10*time.Second {
			remain := time.Until(deadline).Round(time.Second)
			msgf("      等待登录中，剩余 %s ...", remain)
			lastReport = time.Now()
		}
		time.Sleep(time.Second)
	}
}
