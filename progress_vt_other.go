//go:build !windows

package main

// platformEnableVirtualTerminal 在非 Windows 平台上无需开启：终端默认解析 ANSI 转义序列。
func platformEnableVirtualTerminal() error {
	return nil
}
