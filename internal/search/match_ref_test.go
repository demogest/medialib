package search

import (
	"fmt"
	"math/rand"
	"testing"
)

// The matchers are written for speed. These are the plain versions they replaced; the results must stay the same.

func refSubsequence(text string, t token) int {
	tr, xr := []rune(t.s), []rune(text)
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

func refEdits(a, b []rune) int {
	prev2, prev, cur := make([]int, len(b)+1), make([]int, len(b)+1), make([]int, len(b)+1)
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

func refNear(a, b string, limit int) bool {
	ar, br := []rune(a), []rune(b)
	if len(ar) > len(br)+limit {
		ar = ar[:len(br)+limit]
	}
	if abs(len(ar)-len(br)) > limit {
		return false
	}
	return refEdits(ar, br) <= limit
}

func randText(r *rand.Rand, alphabet []rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(out)
}

func TestMatchersAgreeWithTheirPlainVersions(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabets := [][]rune{[]rune("ab"), []rune("abcde "), []rune("holiday s01e02"), []rune("动画海贼王 ab"), []rune("aé�z")}
	for n := 0; n < 200000; n++ {
		al := alphabets[n%len(alphabets)]
		text := randText(r, al, r.Intn(40))
		word := randText(r, al, 1+r.Intn(10))
		if n%7 == 0 && len(text) > 0 {
			text += "\xff" + text[:len(text)/2] // invalid UTF-8 must be handled the same way too
		}
		tok := Parse(word).toks
		if len(tok) == 0 {
			continue
		}
		if got, want := subsequence(text, tok[0]), refSubsequence(text, tok[0]); got != want {
			t.Fatalf("subsequence(%q, %q) = %d, want %d", text, tok[0].s, got, want)
		}
		for limit := 1; limit <= 2; limit++ {
			if got, want := near(text, tok[0].r, limit), refNear(text, tok[0].s, limit); got != want {
				t.Fatalf("near(%q, %q, %d) = %v, want %v", text, tok[0].s, limit, got, want)
			}
		}
	}
	// a query longer than the stack buffers
	long := Parse(fmt.Sprintf("%070d", 7)).toks[0]
	if got, want := near(fmt.Sprintf("%069d1", 7), long.r, 2), refNear(fmt.Sprintf("%069d1", 7), long.s, 2); got != want {
		t.Fatalf("long near = %v, want %v", got, want)
	}
}

func benchDocs(n int) []Doc {
	docs := make([]Doc, n)
	for i := range docs {
		name := fmt.Sprintf("Some Show Name S%02dE%03d 1080p WEB-DL x265 [%d].mp4", i%37, i, i)
		if i%5 == 0 {
			name = fmt.Sprintf("动画 第%d集 海贼王.mkv", i)
		}
		docs[i] = doc(name, fmt.Sprintf("shows/season %d/disc %d", i%37, i%11), "mp4 video hevc 1080p")
	}
	return docs
}

// BenchmarkScore runs queries over 50,000 files, as one keystroke in a big library does.
func BenchmarkScore(b *testing.B) {
	docs := benchDocs(50000)
	for _, q := range []string{"show", "zzqx", "holidya", "haizei", "web s03"} {
		b.Run(q, func(b *testing.B) {
			pq := Parse(q)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for j := range docs {
					pq.Score(&docs[j])
				}
			}
		})
	}
}
