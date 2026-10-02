package search

import (
	"sort"
	"testing"
)

func doc(name, dir, tags string) Doc {
	np, ni := Pinyin(name)
	dp, di := Pinyin(dir)
	return NewDoc(name, dir, tags, np, ni, dp, di)
}

// find returns the names that match q, best first.
func find(q string, docs ...Doc) []string {
	type hit struct {
		name  string
		score int
	}
	var hits []hit
	pq := Parse(q)
	for i := range docs {
		if s, ok := pq.Score(&docs[i]); ok {
			hits = append(hits, hit{docs[i].Name, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

func TestFold(t *testing.T) {
	cases := map[string]string{
		"ＡＢＣ１２３":   "abc123",
		"カタカナ":     "かたかな",
		`Dir\Sub`:  "dir/sub",
		"Lüé":      "lve",
		"Ｍｉｘ　Ｔａｐｅ": "mix tape",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPinyin(t *testing.T) {
	full, ini := Pinyin("3D区动画 DOA")
	if full != "3dqudonghua doa" {
		t.Errorf("full = %q", full)
	}
	if ini != "3dqdh doa" {
		t.Errorf("initials = %q", ini)
	}
	if f, i := Pinyin("plain.mp4"); f != "" || i != "" {
		t.Errorf("no Han, no forms: %q %q", f, i)
	}
}

func TestChineseJapaneseAndPinyin(t *testing.T) {
	docs := []Doc{
		doc("3D区DOA动画整合计划.mp4", "3D", "mp4 avc1 1080p video"),
		doc("千刃花 第一集.mp4", "千刃花", "mp4 hevc 2160p 4k video"),
		doc("天平キツネ - ライブ.mkv", "天平キツネ", "mkv video"),
		doc("holiday photos.mp4", "travel/iceland", "mp4 video"),
		doc("相位土豆 日常.mp4", "相位土豆", "mp4 video"),
	}
	cases := []struct {
		q    string
		want string // the best match, "" for none
	}{
		{"动画", "3D区DOA动画整合计划.mp4"},        // CJK substring
		{"donghua", "3D区DOA动画整合计划.mp4"},   // full pinyin
		{"dhzh", "3D区DOA动画整合计划.mp4"},      // initials
		{"qianrenhua", "千刃花 第一集.mp4"},     // full pinyin of a title
		{"qrh", "千刃花 第一集.mp4"},            // initials
		{"千花", "千刃花 第一集.mp4"},             // characters in order with one missing
		{"キツネ", "天平キツネ - ライブ.mkv"},        // katakana
		{"きつね", "天平キツネ - ライブ.mkv"},        // hiragana finds katakana
		{"xwtd", "相位土豆 日常.mp4"},           // initials
		{"holiday", "holiday photos.mp4"}, // plain text
		{"holidya", "holiday photos.mp4"}, // a swapped pair of letters
		{"hldy", "holiday photos.mp4"},    // letters in order (fuzzy)
		{"iceland", "holiday photos.mp4"}, // found through the path
		{"4k", "千刃花 第一集.mp4"},             // tags
		{"hevc 千刃花", "千刃花 第一集.mp4"},       // several words, all must match
		{"hevc holiday", ""},              // ... and one that fails rejects the file
		{"zzzzqq", ""},                    // nothing
	}
	for _, c := range cases {
		got := find(c.q, docs...)
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%q: want no match, got %v", c.q, got)
		case c.want != "" && (len(got) == 0 || got[0] != Fold(c.want)):
			t.Errorf("%q: want %q first, got %v", c.q, Fold(c.want), got)
		}
	}
}

func TestExactBeatsLooseAndNameBeatsPath(t *testing.T) {
	a := doc("sunset.mp4", "videos", "")
	b := doc("a s u n s e t.mp4", "videos", "")
	c := doc("clip.mp4", "sunset", "")
	got := find("sunset", a, b, c)
	if len(got) < 2 || got[0] != "sunset.mp4" || got[1] != "clip.mp4" {
		t.Errorf("order = %v", got)
	}
}

func TestEmptyQueryMatchesNothing(t *testing.T) {
	d := doc("x.mp4", "", "")
	if _, ok := Parse("   ").Score(&d); ok {
		t.Error("an empty query must not match")
	}
}
