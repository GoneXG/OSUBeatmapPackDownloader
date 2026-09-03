package main

import (
	"strings"
	"testing"
)

const listingFixture = `
<html><body>
<div class="beatmap-packs js-accordion">
  <div class="beatmap-pack js-beatmap-pack js-accordion__item" data-pack-tag="S1813">
    <a href="https://osu.ppy.sh/beatmaps/packs/S1813" class="beatmap-pack__header js-accordion__item-header">
      <div><span class="beatmap-pack__name">osu! Beatmap Pack #1813</span></div>
    </a>
  </div>
  <div class="beatmap-pack js-beatmap-pack js-accordion__item" data-pack-tag="ST406">
    <a href="https://osu.ppy.sh/beatmaps/packs/ST406" class="beatmap-pack__header js-accordion__item-header">
      <div><span class="beatmap-pack__name">osu!taiko Beatmap Pack #406</span></div>
    </a>
  </div>
</div>
<nav><a href="https://osu.ppy.sh/beatmaps/packs?type=standard&amp;page=2">2</a></nav>
</body></html>
`

func TestExtractPacks(t *testing.T) {
	packs, hasNext, err := extractPacks([]byte(listingFixture), "https://osu.ppy.sh/beatmaps/packs?type=standard", 1)
	if err != nil {
		t.Fatalf("extractPacks error: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("want 2 packs, got %d", len(packs))
	}
	if packs[0].Tag != "S1813" || !strings.Contains(packs[0].Name, "osu! Beatmap Pack") {
		t.Errorf("unexpected first pack: %+v", packs[0])
	}
	if packs[0].PageURL != "https://osu.ppy.sh/beatmaps/packs/S1813" {
		t.Errorf("unexpected page url: %s", packs[0].PageURL)
	}
	if !hasNext {
		t.Error("want hasNext=true")
	}
}

func TestPackMatchesMode(t *testing.T) {
	cases := []struct {
		catID int
		mode  string
		pack  Pack
		match bool
	}{
		{1, "osu!", Pack{Tag: "S1813"}, true},
		{1, "osu!catch", Pack{Tag: "S1813"}, false},
		{1, "osu!catch", Pack{Tag: "SC101"}, true},
		{1, "osu!taiko", Pack{Tag: "ST406"}, true},
		{1, "osu!mania", Pack{Tag: "SM377"}, true},
		{1, "osu!", Pack{Tag: "ST406"}, false},
		{3, "osu!mania 7k", Pack{Name: "osu!mania 7K World Cup 2022: Grand Finals"}, true},
		{3, "osu!mania 4k", Pack{Name: "osu!mania 4K World Cup 2024: Semifinals"}, true},
		{3, "osu!", Pack{Name: "osu! World Cup 2024: Grand Finals"}, true},
		{3, "osu!catch", Pack{Name: "osu!taiko World Cup 2023: Round 1"}, false},
		{4, "osu!mania", Pack{Name: "Project Loved: August 2026 (osu!mania)"}, true},
		{6, "osu!catch", Pack{Name: "Beatmap Spotlights: Winter 2026 (osu!catch)"}, true},
		{6, "osu!", Pack{Name: "Beatmap Spotlights: Winter 2026 (osu!mania)"}, false},
	}
	for _, c := range cases {
		if got := PackMatchesMode(c.catID, c.mode, c.pack); got != c.match {
			t.Errorf("PackMatchesMode(%d,%q,%+v)=%v, want %v", c.catID, c.mode, c.pack, got, c.match)
		}
	}
}

func TestPackDirectURL(t *testing.T) {
	p := Pack{Tag: "S1813", Name: "osu! Beatmap Pack #1813"}
	want := "https://packs.ppy.sh/S1813%20-%20osu%21%20Beatmap%20Pack%20%231813.zip"
	if got := PackDirectURL(p); got != want {
		t.Errorf("PackDirectURL = %s, want %s", got, want)
	}
}

func TestBuildURL(t *testing.T) {
	u, err := BuildURL(1, "osu!")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://osu.ppy.sh/beatmaps/packs?type=standard" {
		t.Errorf("unexpected url: %s", u)
	}
	if _, err := BuildURL(1, "bad-mode"); err == nil {
		t.Error("want error for bad mode")
	}
	if _, err := BuildURL(99, ""); err == nil {
		t.Error("want error for bad cat id")
	}
}

func TestParseOSUSid(t *testing.T) {
	if got := ParseOSUSid("abc123"); got != "abc123" {
		t.Errorf("raw value parse: %q", got)
	}
	if got := ParseOSUSid("osu_sid=abc123; token=x"); got != "abc123" {
		t.Errorf("cookie pair parse: %q", got)
	}
	if got := ParseOSUSid("Cookie: osu_sid=abc123; other=1"); got != "abc123" {
		t.Errorf("full header parse: %q", got)
	}
	if got := ParseOSUSid("  osu_sid=abc123  "); got != "abc123" {
		t.Errorf("spaces parse: %q", got)
	}
}
