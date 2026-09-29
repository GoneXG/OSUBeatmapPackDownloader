package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCandidateStructuresCoverMeasuredSamples 用实测样本作为夹具：
// 候选集合必须包含真实存在的那个文件名。
func TestCandidateStructuresCoverMeasuredSamples(t *testing.T) {
	cases := []struct {
		tag  string
		name string
		want string
	}{
		{"SM111", "osu!mania Beatmap Pack #111", "https://packs.ppy.sh/SM111%20-%20Mania%20Beatmap%20Pack%20%23111.7z"},
		{"S1300", "osu! Beatmap Pack #1300", "https://packs.ppy.sh/S1300%20-%20Beatmap%20Pack%20%231300.zip"},
		{"ST200", "osu!taiko Beatmap Pack #200", "https://packs.ppy.sh/ST200%20-%20Taiko%20Beatmap%20Pack%20%23200.7z"},
		{"SC1", "osu!catch Beatmap Pack #1", "https://packs.ppy.sh/SC1%20-%20Catch%20the%20Beat%20Beatmap%20Pack%20%231.7z"},
	}
	for _, tc := range cases {
		p := Pack{Tag: tc.tag, Name: tc.name}
		structures := CandidateStructures(p)
		if len(structures) != 4 {
			t.Fatalf("%s 应有 2 变体 × 2 扩展 = 4 个候选，实际 %d 个", tc.tag, len(structures))
		}
		found := false
		for _, st := range structures {
			if p.PackLinkURL(st) == tc.want {
				found = true
				break
			}
		}
		if !found {
			got := make([]string, 0, len(structures))
			for _, st := range structures {
				got = append(got, p.PackLinkURL(st))
			}
			t.Fatalf("%s 的候选集合未包含实测命中项\n want: %s\n got: %v", tc.tag, tc.want, got)
		}
	}
}

// TestLearnedLabelOverridesShortName 覆盖「用学习结果覆盖历史短名」：
// 内置派生猜错时，按实测链接学到的规则套用到同系列的其它曲包上。
func TestLearnedLabelOverridesShortName(t *testing.T) {
	p := Pack{Tag: "SC1", Name: "osu!catch Beatmap Pack #1"}
	real := "https://packs.ppy.sh/SC1%20-%20Catch%20Beatmap%20Pack%20%231.7z"

	st, ok := structureOfLink(p, real)
	if !ok {
		t.Fatal("实测链接应能被反推出结构")
	}
	if st.Variant != "learned" || st.Label != "Catch" || st.Keep != 3 || st.Extension != ".7z" {
		t.Fatalf("学习到的规则不符合预期: %+v", st)
	}
	// 内置派生会给出 "Catch the Beat ..."，学习结果必须覆盖它。
	if p.PackLinkURL(st) == packLinkURL(p.Tag, shortVariantName(p.Name), ".7z") {
		t.Fatalf("学习结果没有覆盖内置短名: %s", p.PackLinkURL(st))
	}
	q := Pack{Tag: "SC2", Name: "osu!catch Beatmap Pack #2"}
	if got, want := q.PackLinkURL(st), "https://packs.ppy.sh/SC2%20-%20Catch%20Beatmap%20Pack%20%232.7z"; got != want {
		t.Fatalf("学习到的规则未套用到同系列曲包\n got: %s\nwant: %s", got, want)
	}
}

// TestDownloadFileNameUsesFinalLinkExtension 覆盖「扩展名取自最终采用的链接」。
func TestDownloadFileNameUsesFinalLinkExtension(t *testing.T) {
	sevenZip := Pack{
		Tag:       "SM111",
		Name:      "osu!mania Beatmap Pack #111",
		DirectURL: "https://packs.ppy.sh/SM111%20-%20Mania%20Beatmap%20Pack%20%23111.7z",
	}
	if got, want := sevenZip.DownloadFileName(), "SM111 - osu!mania Beatmap Pack #111.7z"; got != want {
		t.Fatalf("`.7z` 曲包落盘名应以 .7z 结尾\n got: %s\nwant: %s", got, want)
	}
	zipPack := Pack{
		Tag:       "S1300",
		Name:      "osu! Beatmap Pack #1300",
		DirectURL: "https://packs.ppy.sh/S1300%20-%20Beatmap%20Pack%20%231300.zip",
	}
	if got, want := zipPack.DownloadFileName(), "S1300 - osu! Beatmap Pack #1300.zip"; got != want {
		t.Fatalf("`.zip` 曲包应保持旧行为\n got: %s\nwant: %s", got, want)
	}
	// 未确定链接时保持旧行为（.zip）。
	legacy := Pack{Tag: "S1", Name: "osu! Beatmap Pack #1"}
	if got, want := legacy.DownloadFileName(), "S1 - osu! Beatmap Pack #1.zip"; got != want {
		t.Fatalf("未确定链接时应保持 .zip\n got: %s\nwant: %s", got, want)
	}
}

// TestLoadPackListFileReusesPayloadValidation 覆盖无脚本兜底入口：
// 本地列表文件复用与桥接完全相同的载荷校验与后续构造流程。
func TestLoadPackListFileReusesPayloadValidation(t *testing.T) {
	fixture := `{
  "protocol": 1,
  "type": "standard",
  "total": 3,
  "packs": [
    {"tag": "SM111", "name": "osu!mania Beatmap Pack #111", "url": "https://osu.ppy.sh/beatmaps/packs/SM111"},
    {"tag": "S1300", "name": "osu! Beatmap Pack #1300", "url": "https://osu.ppy.sh/beatmaps/packs/S1300"},
    {"tag": "SC1", "name": "osu!catch Beatmap Pack #1", "url": "https://osu.ppy.sh/beatmaps/packs/SC1"}
  ]
}`
	path := filepath.Join(t.TempDir(), "packs.json")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写入夹具失败: %v", err)
	}

	payload, err := LoadPackListFile(path)
	if err != nil {
		t.Fatalf("读取夹具列表文件失败: %v", err)
	}
	packs := payload.ToPacks()
	if len(packs) != 3 {
		t.Fatalf("应读出 3 个曲包，实际 %d 个", len(packs))
	}

	// 与同一份载荷经桥接路径解析出的结果完全一致（同一套校验与构造代码）。
	viaBridge, err := ParsePackListPayload([]byte(fixture))
	if err != nil {
		t.Fatalf("同一载荷经桥接路径解析失败: %v", err)
	}
	bridgePacks := viaBridge.ToPacks()
	if len(bridgePacks) != len(packs) {
		t.Fatalf("两条入口的曲包数量不一致: %d vs %d", len(packs), len(bridgePacks))
	}
	for i := range packs {
		if packs[i] != bridgePacks[i] {
			t.Fatalf("第 %d 个曲包不一致: %+v vs %+v", i, packs[i], bridgePacks[i])
		}
		// 两条入口的候选构造也一致。
		if got, want := len(CandidateStructures(packs[i])), len(CandidateStructures(bridgePacks[i])); got != want {
			t.Fatalf("第 %d 个曲包候选数量不一致: %d vs %d", i, got, want)
		}
	}

	// 非法文件必须给出可读错误，且不产生部分结果。
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"protocol": 9, "type": "standard", "total": 1, "packs": []}`), 0o644); err != nil {
		t.Fatalf("写入非法夹具失败: %v", err)
	}
	if _, err := LoadPackListFile(bad); err == nil {
		t.Fatal("版本不匹配的列表文件应被拒绝")
	}
	if _, err := LoadPackListFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("缺失的列表文件应给出错误")
	}
}
