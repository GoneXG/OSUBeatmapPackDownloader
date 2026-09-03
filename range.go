package main

import (
	"regexp"
	"strconv"
	"strings"
)

// packSelector 描述对已抓取曲包列表的筛选方式。
type packSelector struct {
	all    bool // 全部保留
	latest int  // >0 时表示只保留最新的 N 个（按列表顺序，最前最新）
	lo, hi int  // 编号区间（含端点）
}

var (
	rangeBetweenRe = regexp.MustCompile(`^(\d+)\s*-\s*(\d+)$`)
	rangeFromRe    = regexp.MustCompile(`^(\d+)\s*-$`)
	rangeToRe      = regexp.MustCompile(`^-\s*(\d+)$`)
	rangeExactRe   = regexp.MustCompile(`^(\d+)$`)
	latestRe       = regexp.MustCompile(`^(?:最新|last|new|top)\s*(\d+)$`)
)

// parsePackSelector 解析用户输入的区间描述。
// 支持：回车/全部、100-500、-300、800-、42、最新50。
func parsePackSelector(input string) (packSelector, bool) {
	s := strings.ToLower(strings.TrimSpace(input))
	if s == "" || s == "全部" || s == "all" {
		return packSelector{all: true}, true
	}
	if m := latestRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 0 {
			return packSelector{latest: n}, true
		}
		return packSelector{}, false
	}
	if m := rangeBetweenRe.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a > b {
			a, b = b, a
		}
		return packSelector{lo: a, hi: b}, true
	}
	if m := rangeFromRe.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		return packSelector{lo: a, hi: 1 << 30}, true
	}
	if m := rangeToRe.FindStringSubmatch(s); m != nil {
		b, _ := strconv.Atoi(m[1])
		return packSelector{lo: 0, hi: b}, true
	}
	if m := rangeExactRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return packSelector{lo: n, hi: n}, true
	}
	return packSelector{}, false
}

// applyPackSelector 按选择器过滤曲包列表。
func applyPackSelector(packs []Pack, sel packSelector) []Pack {
	if sel.all {
		return packs
	}
	if sel.latest > 0 {
		if sel.latest > len(packs) {
			sel.latest = len(packs)
		}
		return packs[:sel.latest]
	}
	out := make([]Pack, 0, len(packs))
	for _, p := range packs {
		if n, ok := tagNumber(p.Tag); ok && n >= sel.lo && n <= sel.hi {
			out = append(out, p)
		}
	}
	return out
}

// tagNumber 提取 tag 中的数字部分，如 S1813 -> 1813、SC270 -> 270。
func tagNumber(tag string) (int, bool) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, false
	}
	return n, true
}

// numberExtent 返回列表内曲包编号的最小与最大值。
func numberExtent(packs []Pack) (int, int) {
	lo, hi := 1<<30, -1
	for _, p := range packs {
		if n, ok := tagNumber(p.Tag); ok {
			if n < lo {
				lo = n
			}
			if n > hi {
				hi = n
			}
		}
	}
	if hi < 0 {
		return 0, 0
	}
	return lo, hi
}
