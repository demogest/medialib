package media

import (
	"fmt"
	"path"
	"strings"

	"github.com/demogest/medialib/internal/search"
)

// tagsOf lists the words a file can be found by besides its name: extension, kind, codec and resolution.
func tagsOf(it *Item) string {
	var b strings.Builder
	b.WriteString(strings.TrimPrefix(strings.ToLower(path.Ext(it.Name)), "."))
	b.WriteString(" " + it.Kind)
	if it.Codec != "" {
		b.WriteString(" " + it.Codec)
	}
	if it.Height > 0 {
		fmt.Fprintf(&b, " %dp", it.Height)
		switch {
		case it.Height >= 4000:
			b.WriteString(" 8k")
		case it.Height >= 2000:
			b.WriteString(" 4k 2160p")
		case it.Height >= 1400:
			b.WriteString(" 2k 1440p")
		}
	}
	return b.String()
}

// Docs is the searchable form of every item, in the order of Data.Items. Built once per loaded index.
func (s *Snapshot) Docs() []search.Doc {
	s.docsOnce.Do(func() {
		s.docs = make([]search.Doc, len(s.Data.Items))
		for i := range s.Data.Items {
			it := &s.Data.Items[i]
			s.docs[i] = search.NewDoc(it.Name, it.Dir, tagsOf(it), it.NamePinyin, it.NameInitials, it.DirPinyin, it.DirInitials)
		}
	})
	return s.docs
}
