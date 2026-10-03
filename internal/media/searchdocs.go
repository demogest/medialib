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
	if res := search.Resolution(it.Width, it.Height); res > 0 {
		// the class the cover badge shows (a 1920x800 film is 1080p), and the plain height when that differs
		fmt.Fprintf(&b, " %dp", res)
		if it.Height != res {
			fmt.Fprintf(&b, " %dp", it.Height)
		}
		switch res {
		case 4320:
			b.WriteString(" 8k")
		case 2160:
			b.WriteString(" 4k")
		case 1440:
			b.WriteString(" 2k")
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
			s.docs[i].Facts = search.Facts{Duration: it.Duration, Size: it.Size, Res: search.Resolution(it.Width, it.Height), MTime: it.MTime}
		}
	})
	return s.docs
}
