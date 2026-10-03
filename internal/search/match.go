package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Doc is the searchable form of one file. Build it with NewDoc once and match it against many queries.
type Doc struct {
	Name, Dir    string // folded
	NameP, NameI string // folded pinyin of the name: full syllables and initials ("" without Han characters)
	DirP, DirI   string // the same for the folder path
	Tags         string // folded extension, codec, kind and resolution words, so "1080p" or "hevc" find files too
	Facts        Facts  // numbers for filters (dur>1h, size<2g ...); set them after NewDoc
	words        []string
}

// NewDoc folds the texts. The pinyin forms are taken as they are (they come from Pinyin and are already folded).
func NewDoc(name, dir, tags, nameP, nameI, dirP, dirI string) Doc {
	d := Doc{Name: Fold(name), Dir: Fold(dir), Tags: Fold(tags), NameP: nameP, NameI: nameI, DirP: dirP, DirI: dirI}
	d.words = wordsOf(d.Name)
	return d
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func wordsOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !isWordRune(r) || unicode.Is(unicode.Han, r) })
}

type token struct {
	s     string
	r     []rune // the characters of s
	n     int    // length in characters
	latin bool   // only a-z and 0-9: may be pinyin
	han   bool   // holds a Han character
}

// Query is a parsed search. Its words must all match (in any order); each may match in the name, the path, the
// tags, as pinyin, or loosely. Its filters (dur>1h, size<2g, date>=2024-05, res>=1080) must all hold.
type Query struct {
	toks    []token
	filters []filter
}

// Parse splits a typed query into words and filters.
func Parse(q string) Query {
	var out Query
	words, filters := splitFilters(strings.Fields(Fold(q)))
	out.filters = filters
	for _, w := range words {
		t := token{s: w, r: []rune(w)}
		t.latin = true
		for _, r := range w {
			t.n++
			if unicode.Is(unicode.Han, r) {
				t.han = true
			}
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				t.latin = false
			}
		}
		out.toks = append(out.toks, t)
	}
	return out
}

// Empty reports whether there is nothing to look for.
func (q Query) Empty() bool { return len(q.toks) == 0 && len(q.filters) == 0 }

// Score rates how well d answers the query; ok is false when some word matches nowhere or a filter fails. Higher is
// better.
func (q Query) Score(d *Doc) (score int, ok bool) {
	if q.Empty() || !q.filtersPass(d) {
		return 0, false
	}
	for _, t := range q.toks {
		s := matchToken(t, d)
		if s == 0 {
			return 0, false
		}
		score += s
	}
	// Among equals, the shorter name is the closer match.
	score -= min(len(d.Name), 400) / 8
	return score, true
}

// at scores a hit at byte position i of text: earlier is better, the start of a word better still.
func at(text string, i int, base int) int {
	s := base - min(i, 200)/2
	switch {
	case i == 0:
		s += 200
	default:
		if p, _ := utf8.DecodeLastRuneInString(text[:i]); !isWordRune(p) || unicode.Is(unicode.Han, p) {
			s += 100
		}
	}
	return s
}

func matchToken(t token, d *Doc) int {
	best := 0
	up := func(s int) {
		if s > best {
			best = s
		}
	}
	if i := strings.Index(d.Name, t.s); i >= 0 {
		s := at(d.Name, i, 1000)
		if d.Name == t.s {
			s += 400
		}
		up(s)
	}
	if i := strings.Index(d.Dir, t.s); i >= 0 {
		up(at(d.Dir, i, 600))
	}
	if i := strings.Index(d.Tags, t.s); i >= 0 {
		up(500)
	}
	if t.latin {
		if d.NameP != "" {
			if i := strings.Index(d.NameP, t.s); i >= 0 {
				up(at(d.NameP, i, 800))
			}
			if t.n >= 2 {
				if i := strings.Index(d.NameI, t.s); i >= 0 {
					up(at(d.NameI, i, 700))
				}
			}
		}
		if d.DirP != "" {
			if strings.Contains(d.DirP, t.s) {
				up(450)
			}
			if t.n >= 2 && strings.Contains(d.DirI, t.s) {
				up(400)
			}
		}
	}
	if best > 0 {
		return best
	}
	// Loose matching, only when nothing exact was found.
	minLen := 3
	if t.han {
		minLen = 2
	}
	if t.n >= minLen {
		if s := subsequence(d.Name, t); s > 0 {
			up(s)
		}
		if t.latin && d.NameP != "" {
			if s := subsequence(d.NameP, t); s > 0 {
				up(s - 60)
			}
		}
	}
	if best == 0 && t.latin && t.n >= 4 {
		limit := 1
		if t.n >= 8 {
			limit = 2
		}
		for _, w := range d.words {
			if near(w, t.r, limit) {
				up(250)
				break
			}
		}
	}
	return best
}

// subsequence finds the characters of t in order in text, as close together as it can, and scores the tightness. It
// refuses spreads wider than a few times the word, so a long name does not match everything.
//
// It runs for every file on most keystrokes, so it reads text in place: no copies, and no look further from a
// starting point than the widest spread it would accept.
func subsequence(text string, t token) int {
	tr := t.r
	// One greedy pass says whether the characters occur in order at all, which most names fail.
	j := 0
	for _, c := range text {
		if c == tr[j] {
			if j++; j == len(tr) {
				break
			}
		}
	}
	if j < len(tr) {
		return 0
	}
	widest := len(tr)*3 + 2
	bestSpan := -1
	for i, start := 0, 0; i < len(text); start++ {
		c, w := utf8.DecodeRuneInString(text[i:])
		i += w
		if c != tr[0] {
			continue
		}
		j, k, span := 1, start, 1
		for p := i; j < len(tr) && p < len(text) && k-start+1 < widest; {
			x, xw := utf8.DecodeRuneInString(text[p:])
			p += xw
			k++
			if x == tr[j] {
				j++
				span = k - start + 1
			}
		}
		if j == len(tr) && (bestSpan < 0 || span < bestSpan) {
			bestSpan = span
		}
	}
	if bestSpan < 0 || bestSpan > widest {
		return 0
	}
	return 400 - (bestSpan-len(tr))*20
}

// near reports whether a and b are within the given number of edits (insert, delete, replace, swap of neighbours),
// where b may also be matched against the start of a, so a word typed with a slip and not finished still counts.
func near(a string, b []rune, limit int) bool {
	var buf [64]rune
	ar := buf[:0]
	for _, r := range a {
		if len(ar) == len(b)+limit {
			break
		}
		ar = append(ar, r)
	}
	if abs(len(ar)-len(b)) > limit {
		return false
	}
	// Each character of b that a does not hold at all costs an insertion or a replacement of its own.
	var ascii [2]uint64
	for _, r := range ar {
		if r < 128 {
			ascii[r>>6] |= 1 << (r & 63)
		}
	}
	missing := 0
	for _, r := range b {
		if r < 128 && ascii[r>>6]&(1<<(r&63)) == 0 || r >= 128 && !containsRune(ar, r) {
			if missing++; missing > limit {
				return false
			}
		}
	}
	return withinEdits(ar, b, limit)
}

func containsRune(rs []rune, r rune) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// withinEdits reports whether the optimal-string-alignment distance of a and b is at most limit. It stops as soon as
// no row can come back under the limit: a cell is reached from the row above (or, by a swap, the one above that, at
// a cost of one), so min(this row, the row above + 1) never goes down from one row to the next.
func withinEdits(a, b []rune, limit int) bool {
	var bufs [3][65]int
	prev2, prev, cur := bufs[0][:], bufs[1][:], bufs[2][:]
	if len(b)+1 > len(prev) {
		prev2, prev, cur = make([]int, len(b)+1), make([]int, len(b)+1), make([]int, len(b)+1)
	}
	prev2, prev, cur = prev2[:len(b)+1], prev[:len(b)+1], cur[:len(b)+1]
	for j := range prev {
		prev[j] = j
	}
	prevMin := 0
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
			rowMin = min(rowMin, v)
		}
		if min(rowMin, prevMin+1) > limit {
			return false
		}
		prevMin = rowMin
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)] <= limit
}
