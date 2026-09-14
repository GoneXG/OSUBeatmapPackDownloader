//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// enableVirtualTerminalProcessing 对应 Win32 的 ENABLE_VIRTUAL_TERMINAL_PROCESSING，
// 打开后传统控制台（conhost）才会解析 ANSI 光标控制序列。
const enableVirtualTerminalProcessing = 0x0004

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// platformEnableVirtualTerminal 为当前控制台打开 ANSI 转义序列解析。
// 输出不是控制台（重定向到文件/管道）或系统不支持时返回错误，调用方据此回退为单行进度。
func platformEnableVirtualTerminal() error {
	handle := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	r, _, err := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return err
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return nil
	}
	r, _, err = procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
	if r == 0 {
		return err
	}
	return nil
}
