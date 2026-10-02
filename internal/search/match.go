package search

import (
	"strings"
	"unicode"
)

// Doc is the searchable form of one file. Build it with NewDoc once and match it against many queries.
type Doc struct {
	Name, Dir    string // folded
	NameP, NameI string // folded pinyin of the name: full syllables and initials ("" without Han characters)
	DirP, DirI   string // the same for the folder path
	Tags         string // folded extension, codec, kind and resolution words, so "1080p" or "hevc" find files too
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
	n     int  // length in characters
	latin bool // only a-z and 0-9: may be pinyin
	han   bool // holds a Han character
}

// Query is a parsed search. Its words must all match (in any order); each may match in the name, the path, the
// tags, as pinyin, or loosely.
type Query struct{ toks []token }

// Parse splits a typed query into words.
func Parse(q string) Query {
	var out Query
	for _, w := range strings.Fields(Fold(q)) {
		t := token{s: w}
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
func (q Query) Empty() bool { return len(q.toks) == 0 }

// Score rates how well d answers the query; ok is false when some word matches nowhere. Higher is better.
func (q Query) Score(d *Doc) (score int, ok bool) {
	if len(q.toks) == 0 {
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
		r := []rune(text[:i])
		if p := r[len(r)-1]; !isWordRune(p) || unicode.Is(unicode.Han, p) {
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
			if near(w, t.s, limit) {
				up(250)
				break
			}
		}
	}
	return best
}

// subsequence finds the characters of t in order in text, as close together as it can, and scores the tightness. It
// refuses spreads wider than a few times the word, so a long name does not match everything.
func subsequence(text string, t token) int {
	tr := []rune(t.s)
	xr := []rune(text)
	bestSpan := -1
	for start := 0; start < len(xr); start++ {
		if xr[start] != tr[0] {
			continue
		}
		j, last := 1, start
		for k := start + 1; k < len(xr) && j < len(tr); k++ {
			if xr[k] == tr[j] {
				j++
				last = k
			}
		}
		if j == len(tr) {
			if span := last - start + 1; bestSpan < 0 || span < bestSpan {
				bestSpan = span
			}
		}
	}
	if bestSpan < 0 || bestSpan > len(tr)*3+2 {
		return 0
	}
	return 400 - (bestSpan-len(tr))*20
}

// near reports whether a and b are within the given number of edits (insert, delete, replace, swap of neighbours),
// where b may also be matched against the start of a, so a word typed with a slip and not finished still counts.
func near(a, b string, limit int) bool {
	ar, br := []rune(a), []rune(b)
	if len(ar) > len(br)+limit {
		ar = ar[:len(br)+limit]
	}
	if abs(len(ar)-len(br)) > limit {
		return false
	}
	return edits(ar, br) <= limit
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// edits is the optimal-string-alignment distance.
func edits(a, b []rune) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
