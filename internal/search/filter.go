package search

import (
	"regexp"
	"strconv"
)

// Facts are the numbers a file can be filtered by. MTime is the second-precision UTC time of the index
// ("2026-09-29T11:37:57Z"), so a date can be compared as a prefix.
type Facts struct {
	Duration float64 // seconds
	Size     int64   // bytes
	Res      int     // resolution class, see Resolution
	MTime    string
}

// Resolution is the class a picture size is known by (2160 for 4K, 1080 ...), from its long side as the cover badges
// have it, so a 1920x800 film is 1080p and a 3840x1600 one is 4K. Below 720p it is the short side. Only a height
// known: that height. 0 when unknown.
func Resolution(w, h int) int {
	if h <= 0 {
		return 0
	}
	if w <= 0 {
		return h
	}
	hi, lo := max(w, h), min(w, h)
	switch {
	case hi >= 7600:
		return 4320
	case hi >= 3800:
		return 2160
	case hi >= 2500:
		return 1440
	case hi >= 1900:
		return 1080
	case hi >= 1260:
		return 720
	}
	return lo
}

// A filter is a word like dur>1h, size<=2g, date>=2024-05 or res>=1080: a field, a comparison and a value.
type filter struct {
	field string // dur | size | date | res
	op    string // < <= > >= =
	num   float64
	date  string // for date: 2024, 2024-05 or 2024-05-01
}

var filterWord = regexp.MustCompile(`^(dur|duration|length|len|size|date|time|res|height)(<=|>=|<|>|=|:)(.+)$`)

var fieldOf = map[string]string{"dur": "dur", "duration": "dur", "length": "dur", "len": "dur", "size": "size",
	"date": "date", "time": "date", "res": "res", "height": "res"}

var (
	durValue  = regexp.MustCompile(`^(\d+(?:\.\d+)?)(s|sec|m|min|h|hr)?$`)
	sizeValue = regexp.MustCompile(`^(\d+(?:\.\d+)?)(b|k|kb|kib|m|mb|mib|g|gb|gib|t|tb|tib)?$`)
	dateValue = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
	resValue  = regexp.MustCompile(`^(\d+)p?$`)
)

var (
	durUnit  = map[string]float64{"": 60, "s": 1, "sec": 1, "m": 60, "min": 60, "h": 3600, "hr": 3600}
	sizeUnit = map[string]float64{"": 1 << 20, "b": 1, "k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10, "m": 1 << 20, "mb": 1 << 20,
		"mib": 1 << 20, "g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30, "t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40}
	resName = map[string]float64{"sd": 480, "hd": 720, "fhd": 1080, "2k": 1440, "qhd": 1440, "4k": 2160, "uhd": 2160, "8k": 4320}
)

// parseFilter reads one folded word as a filter. A word that only looks like one (dur>soon) is not: it stays a word.
func parseFilter(w string) (filter, bool) {
	m := filterWord.FindStringSubmatch(w)
	if m == nil {
		return filter{}, false
	}
	f := filter{field: fieldOf[m[1]], op: m[2]}
	if f.op == ":" {
		f.op = "="
	}
	v := m[3]
	switch f.field {
	case "dur": // a bare number is minutes
		x := durValue.FindStringSubmatch(v)
		if x == nil {
			return filter{}, false
		}
		n, _ := strconv.ParseFloat(x[1], 64)
		f.num = n * durUnit[x[2]]
	case "size": // a bare number is megabytes
		x := sizeValue.FindStringSubmatch(v)
		if x == nil {
			return filter{}, false
		}
		n, _ := strconv.ParseFloat(x[1], 64)
		f.num = n * sizeUnit[x[2]]
	case "date":
		if !dateValue.MatchString(v) {
			return filter{}, false
		}
		f.date = v
	case "res":
		if n, ok := resName[v]; ok {
			f.num = n
		} else if x := resValue.FindStringSubmatch(v); x != nil {
			f.num, _ = strconv.ParseFloat(x[1], 64)
		} else {
			return filter{}, false
		}
	}
	return f, true
}

func compare[T int | float64 | string](a T, op string, b T) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return a == b
}

// keep reports whether a file passes the filter. A file without the number (not indexed yet, no video) never does.
func (f filter) keep(x *Facts) bool {
	switch f.field {
	case "dur":
		return x.Duration > 0 && compare(x.Duration, f.op, f.num)
	case "size":
		return compare(float64(x.Size), f.op, f.num)
	case "res":
		return x.Res > 0 && compare(float64(x.Res), f.op, f.num)
	case "date":
		// Compared at the precision given: date=2024 is all of 2024, date>2024-05 starts in June.
		if len(x.MTime) < len(f.date) {
			return false
		}
		return compare(x.MTime[:len(f.date)], f.op, f.date)
	}
	return false
}

// HasWords reports whether the query looks for text, not only filters: only then does relevance order mean anything.
func (q Query) HasWords() bool { return len(q.toks) > 0 }

// filtersPass applies the query's filters to d.
func (q Query) filtersPass(d *Doc) bool {
	for _, f := range q.filters {
		if !f.keep(&d.Facts) {
			return false
		}
	}
	return true
}

// splitFilters takes the filters out of a list of folded words.
func splitFilters(words []string) (rest []string, filters []filter) {
	for _, w := range words {
		if f, ok := parseFilter(w); ok {
			filters = append(filters, f)
		} else {
			rest = append(rest, w)
		}
	}
	return rest, filters
}
