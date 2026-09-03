package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

// ShowMenu 打印编号菜单并返回用户选择的条目下标（从 0 开始）。
// 非法输入会循环重试，直到输入合法或用户 Ctrl+C 退出。
func ShowMenu(title string, items []string) (int, error) {
	for {
		fmt.Printf("\n%s\n", title)
		for i, it := range items {
			fmt.Printf("  [%d] %s\n", i+1, it)
		}
		fmt.Print("请输入编号: ")
		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("读取输入失败: %w", err)
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		fmt.Printf("输入无效，请输入 1-%d 之间的数字。\n", len(items))
	}
}

// AskUser 简单问答。retries 次非法输入后返回 fallback。
func AskUser(prompt string, allowed map[string]bool, retries int, fallback string) string {
	for i := 0; i < retries; i++ {
		fmt.Printf("%s: ", prompt)
		line, err := stdinReader.ReadString('\n')
		if err == nil {
			ans := strings.TrimSpace(line)
			if allowed[ans] {
				return ans
			}
		}
		fmt.Printf("输入无效（仅允许 %s）。\n", strings.Join(keysOf(allowed), " 或 "))
	}
	msgf("已重试 %d 次仍未得到有效输入，按默认值 %q 继续。", retries, fallback)
	return fallback
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
