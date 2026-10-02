// Package search matches a typed query against file names and paths: plain text in any script, Chinese by pinyin
// (full or initials), kana without regard to hiragana or katakana, and typos or half-remembered names by fuzzy
// matching. Everything here is pure computation over strings; the media package keeps the prepared forms with each
// indexed file and the server asks the questions.
package search

import (
	"strings"
	"unicode"

	pinyin "github.com/mozillazg/go-pinyin"
)

// Fold brings text to the form comparisons are made in: lower case, full-width letters and digits as ordinary ones,
// katakana as hiragana, "ü" as "v" (how pinyin is typed), backslashes as slashes, accents and other marks dropped.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x3000:
			r = ' '
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ASCII
			r -= 0xFEE0
		case r >= 0x30A1 && r <= 0x30F6: // katakana -> hiragana
			r -= 0x60
		case r == 'ü' || r == 'Ü':
			r = 'v'
		case r == '\\':
			r = '/'
		}
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		if r >= 0xE0 && r <= 0x17F { // accented Latin letters stand for their plain ones
			if p, ok := accentMap[r]; ok {
				r = p
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The accented letters of Latin-1 Supplement and Latin Extended-A that are worth folding, and what they fold to.
const (
	accented = "àáâãäåāăąçćĉċčďđèéêëēĕėęěĝğġģĥħìíîïĩīĭįıĵķĺļľłñńņňòóôõöøōŏőŕŗřśŝşšţťŧùúûüũūŭůűųŵýÿŷźżž"
	plain    = "aaaaaaaaaccccddeeeeeeeeegggghhiiiiiiiiijklllnnnnoooooooooorrrssssttttuuuuuuuuuuwyyyzzz"
)

var accentMap = func() map[rune]rune {
	m := make(map[rune]rune)
	p := []rune(plain)
	for i, r := range []rune(accented) {
		m[r] = p[i]
	}
	return m
}()

// HasHan reports whether s holds any Han (Chinese) character.
func HasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

var pyArgs = func() pinyin.Args {
	a := pinyin.NewArgs()
	a.Style = pinyin.Normal
	return a
}()

// Pinyin returns two folded forms of s in which every Han character is replaced: by its toneless pinyin syllable
// (full, "dong hua" written "donghua") and by that syllable's first letter (initials, "dh"). Other characters are
// kept, folded. Both are empty when s has no Han character, so callers store them only where they are needed.
//
// A character with several readings gets its most common one; a query that needs another reading still finds the
// file by its characters, by its initials in most cases, or by fuzzy matching.
func Pinyin(s string) (full, initials string) {
	if !HasHan(s) {
		return "", ""
	}
	var f, i strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			if p := pinyin.SinglePinyin(r, pyArgs); len(p) > 0 && p[0] != "" {
				syl := Fold(p[0])
				f.WriteString(syl)
				i.WriteByte(syl[0])
				continue
			}
		}
		g := Fold(string(r))
		f.WriteString(g)
		i.WriteString(g)
	}
	return f.String(), i.String()
}
