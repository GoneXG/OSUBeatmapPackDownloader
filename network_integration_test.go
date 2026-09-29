package main

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveCDNHitsMeasuredSamples 用真实网络验证「候选构造 + HEAD 校验」：
// 设计文档记录的实测命中项必须返回 200，且错写形式必须落空（404）。
//
// 依赖外部网络，默认跳过；需要时显式开启：
//
//	$env:OPD_NETWORK_TEST="1"; go test -count=1 -run TestLiveCDNHitsMeasuredSamples -v .
func TestLiveCDNHitsMeasuredSamples(t *testing.T) {
	if os.Getenv("OPD_NETWORK_TEST") == "" {
		t.Skip("需要真实网络，设置 OPD_NETWORK_TEST=1 后再跑")
	}
	client := &http.Client{Timeout: 30 * time.Second}

	// 每一项都必须由「候选构造」产出，再交给 HEAD 校验确认存在。
	measured := []struct {
		tag  string
		name string
		// ext 是实测样本使用的扩展名；variant 是实测使用的名称变体标签。
		variant string
		ext     string
	}{
		{"SM111", "osu!mania Beatmap Pack #111", "short", ".7z"},
		{"ST200", "osu!taiko Beatmap Pack #200", "short", ".7z"},
		{"SC1", "osu!catch Beatmap Pack #1", "short", ".7z"},   // 历史短名是 "Catch the Beat"
		{"S1300", "osu! Beatmap Pack #1300", "short", ".zip"},  // 扩展名在此处翻到 .zip
		{"S1350", "osu! Beatmap Pack #1350", "modern", ".zip"}, // 名称变体在此处翻到现代形式
	}
	for _, tc := range measured {
		p := Pack{Tag: tc.tag, Name: tc.name}
		structure := linkStructure{Variant: tc.variant, Extension: tc.ext}
		inCandidates := false
		for _, cand := range CandidateStructures(p) {
			if cand == structure {
				inCandidates = true
				break
			}
		}
		if !inCandidates {
			t.Fatalf("%s 的实测结构 %+v 不在候选空间内", tc.tag, structure)
		}
		link := p.PackLinkURL(structure)
		status, err := checkLink(context.Background(), client, link)
		if err != nil {
			t.Fatalf("%s 校验出错: %v", tc.tag, err)
		}
		if status != linkOK {
			t.Fatalf("%s 的实测候选应命中（%s），实际 %v", tc.tag, link, status)
		}
	}

	// 反例：`Catch` 是被误用的历史短名，实测为 404，必须判成「不存在」而不是网络错误。
	wrong := "https://packs.ppy.sh/SC1%20-%20Catch%20Beatmap%20Pack%20%231.7z"
	status, err := checkLink(context.Background(), client, wrong)
	if err != nil {
		t.Fatalf("反例校验出错: %v", err)
	}
	if status != linkMissing {
		t.Fatalf("错写形式应判为不存在，实际 %v", status)
	}
}
