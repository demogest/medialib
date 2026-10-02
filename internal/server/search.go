package server

import (
	"sort"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/search"
)

type searchHit struct {
	lib   config.Library
	snap  *media.Snapshot
	index int
	score int
}

// searchLibraries scores every item of the given libraries against the query, best first. The indexes are the ones the
// libraries hold right now, so files that an indexing run has listed but not yet processed are found too.
func (a *App) searchLibraries(libs []config.Library, q search.Query) []searchHit {
	var hits []searchHit
	for _, lib := range libs {
		snap, err := a.store(lib).Get()
		if err != nil || snap == nil {
			continue
		}
		docs := snap.Docs()
		for i := range docs {
			if score, ok := q.Score(&docs[i]); ok {
				hits = append(hits, searchHit{lib, snap, i, score})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	return hits
}

// GET /api/search?q=...[&lib=ID][&limit=N][&ids=1]
//
// Looks for files by name, folder, extension, codec or resolution, in Chinese by pinyin, and loosely. Without lib every
// library is searched. limit defaults to 30 and 0 means no limit. With ids=1 (and a lib) only the ids come back, in
// order of relevance: that is what the library view filters its covers by.
func (a *App) searchMedia(c *Ctx) (any, error) {
	q := search.Parse(c.Arg("q"))
	limit, err := c.Int("limit", 30)
	if err != nil {
		return nil, err
	}
	libs := a.Cfg.Libraries()
	if id := c.Arg("lib"); id != "" {
		lib, err := c.Library(id)
		if err != nil {
			return nil, err
		}
		libs = []config.Library{lib}
	}
	if q.Empty() {
		return map[string]any{"total": 0, "results": []any{}, "ids": []string{}}, nil
	}
	hits := a.searchLibraries(libs, q)
	total := len(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	if c.Arg("ids") == "1" {
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.snap.Data.Items[h.index].ID
		}
		return map[string]any{"total": total, "ids": ids}, nil
	}
	out := make([]map[string]any, len(hits))
	for i, h := range hits {
		it := h.snap.Data.Items[h.index]
		out[i] = map[string]any{
			"lib": h.lib.ID, "libName": h.lib.Name, "id": it.ID, "name": it.Name, "dir": it.Dir, "kind": it.Kind,
			"size": it.Size, "duration": it.Duration, "width": it.Width, "height": it.Height, "ver": it.Ver,
			"frames": it.Frames, "cover": it.Cover, "indexed": it.Indexed, "score": h.score,
		}
	}
	return map[string]any{"total": total, "results": out}, nil
}
