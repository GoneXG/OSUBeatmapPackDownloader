package main

import "testing"

func TestParsePackSelector(t *testing.T) {
	cases := []struct {
		in             string
		ok, all        bool
		latest, lo, hi int
	}{
		{"", true, true, 0, 0, 0},
		{"全部", true, true, 0, 0, 0},
		{"100-500", true, false, 0, 100, 500},
		{"500-100", true, false, 0, 100, 500},
		{"-300", true, false, 0, 0, 300},
		{"800-", true, false, 0, 800, 1 << 30},
		{"42", true, false, 0, 42, 42},
		{"最新50", true, false, 50, 0, 0},
		{"last50", true, false, 50, 0, 0},
		{"abc", false, false, 0, 0, 0},
	}
	for _, c := range cases {
		sel, ok := parsePackSelector(c.in)
		if ok != c.ok {
			t.Errorf("%q ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if sel.all != c.all || sel.latest != c.latest || sel.lo != c.lo || sel.hi != c.hi {
			t.Errorf("%q => %+v", c.in, sel)
		}
	}
}

func TestApplyPackSelector(t *testing.T) {
	packs := []Pack{
		{Tag: "S10"}, {Tag: "SC5"}, {Tag: "S1"}, {Tag: "SM42"},
	}
	got := applyPackSelector(packs, packSelector{lo: 3, hi: 10})
	if len(got) != 2 || got[0].Tag != "S10" || got[1].Tag != "SC5" {
		t.Fatalf("range filter = %+v", got)
	}
	got = applyPackSelector(packs, packSelector{latest: 2})
	if len(got) != 2 || got[0].Tag != "S10" || got[1].Tag != "SC5" {
		t.Fatalf("latest filter = %+v", got)
	}
	got = applyPackSelector(packs, packSelector{lo: 42, hi: 42})
	if len(got) != 1 || got[0].Tag != "SM42" {
		t.Fatalf("exact filter = %+v", got)
	}
}

func TestTagNumber(t *testing.T) {
	if n, ok := tagNumber("S1813"); !ok || n != 1813 {
		t.Errorf("S1813 => %d,%v", n, ok)
	}
	if n, ok := tagNumber("SC270"); !ok || n != 270 {
		t.Errorf("SC270 => %d,%v", n, ok)
	}
	if _, ok := tagNumber("P260"); !ok {
		t.Error("P260 should parse")
	}
	if _, ok := tagNumber("bad"); ok {
		t.Error("bad should fail")
	}
}
