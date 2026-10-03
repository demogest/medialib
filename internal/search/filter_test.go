package search

import "testing"

func TestFilterWords(t *testing.T) {
	cases := map[string]filter{
		"dur>1h":        {field: "dur", op: ">", num: 3600},
		"dur<90":        {field: "dur", op: "<", num: 5400}, // minutes
		"duration>=45m": {field: "dur", op: ">=", num: 2700},
		"length<=30s":   {field: "dur", op: "<=", num: 30},
		"size<2g":       {field: "size", op: "<", num: 2 << 30},
		"size>500mb":    {field: "size", op: ">", num: 500 << 20},
		"size>1.5gb":    {field: "size", op: ">", num: 1.5 * (1 << 30)},
		"size>=700":     {field: "size", op: ">=", num: 700 << 20}, // megabytes
		"date>=2024-05": {field: "date", op: ">=", date: "2024-05"},
		"date:2024":     {field: "date", op: "=", date: "2024"},
		"res>=4k":       {field: "res", op: ">=", num: 2160},
		"res>=1080p":    {field: "res", op: ">=", num: 1080},
		"height=720":    {field: "res", op: "=", num: 720},
	}
	for w, want := range cases {
		if got, ok := parseFilter(w); !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", w, got, ok, want)
		}
	}
	for _, w := range []string{"dur>soon", "size>2x", "date>24", "date>2024-5", "res>big", "duration", "size", "dur>"} {
		if _, ok := parseFilter(w); ok {
			t.Errorf("%s read as a filter", w)
		}
	}
}

func TestResolutionFollowsTheBadge(t *testing.T) {
	for _, c := range [][3]int{{1920, 1080, 1080}, {1920, 800, 1080}, {3840, 1600, 2160}, {1080, 1920, 1080}, {640, 480, 480},
		{1280, 720, 720}, {2560, 1440, 1440}, {7680, 4320, 4320}, {0, 1080, 1080}, {1920, 0, 0}} {
		if got := Resolution(c[0], c[1]); got != c[2] {
			t.Errorf("%dx%d: %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestFiltersNarrowAndCombineWithWords(t *testing.T) {
	mk := func(name string, f Facts) Doc { d := doc(name, "", "mp4 video"); d.Facts = f; return d }
	docs := []Doc{
		mk("long holiday.mp4", Facts{Duration: 7200, Size: 3 << 30, Res: 2160, MTime: "2024-12-31T23:59:59Z"}),
		mk("short holiday.mp4", Facts{Duration: 600, Size: 200 << 20, Res: 1080, MTime: "2025-01-01T00:00:00Z"}),
		mk("not indexed yet.mp4", Facts{Size: 50 << 20, MTime: "2024-05-31T12:00:00Z"}),
	}
	for q, want := range map[string]string{
		"dur>1h":                 "long holiday.mp4",
		"dur＞1h":                 "long holiday.mp4",  // typed with a Chinese or Japanese keyboard
		"dur<20m":                "short holiday.mp4", // a file with no duration is never in range
		"holiday size<1g":        "short holiday.mp4",
		"date>2024":              "short holiday.mp4", // after the whole of 2024
		"date=2024 res>=4k":      "long holiday.mp4",
		"date<=2024-05 size<100": "not indexed yet.mp4", // to the end of May
		"res>=1080 dur<1h":       "short holiday.mp4",
	} {
		got := find(q, docs...)
		if len(got) != 1 || got[0] != Fold(want) {
			t.Errorf("%q: %v, want [%s]", q, got, want)
		}
	}
	if q := Parse("size>1k"); q.HasWords() || q.Empty() || len(find("size>1k", docs...)) != 3 {
		t.Error("filters alone must list every file that passes them")
	}
	if got := find("dur>soon", docs...); len(got) != 0 {
		t.Errorf("a word that only looks like a filter is a word: %v", got)
	}
}
