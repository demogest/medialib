// Package media indexes libraries: it lists files, reads keyframes (from a local disk, a NAS or an S3 bucket),
// makes thumbnails, and keeps each library's index (library.json) and covers under cache/.
package media

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/search"
)

// Item is one media file in a library.
type Item struct {
	ID       string  `json:"id"`
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	Dir      string  `json:"dir"`
	Kind     string  `json:"kind"` // video | audio
	Size     int64   `json:"size"`
	MTime    string  `json:"mtime"`
	Added    string  `json:"added,omitempty"` // when a scan first found the file (its MTime for files found by the first scan)
	Ver      string  `json:"ver"`
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Codec    string  `json:"codec,omitempty"`
	FPS      float64 `json:"fps,omitempty"`
	Audio    bool    `json:"audio,omitempty"`
	Frames   int     `json:"frames,omitempty"`
	Cover    *int    `json:"cover,omitempty"`
	Indexed  bool    `json:"indexed"`
	Note     string  `json:"note,omitempty"`
	Error    string  `json:"error,omitempty"`

	// FailedWith marks an Error that comes from the file itself: it is the Tools.Stamp of the ffmpeg that failed on it.
	// Such a file is not tried again until it changes, ffmpeg changes, or someone asks. Empty for errors that may pass
	// by themselves (the file could not be opened). Not part of the API's JSON.
	FailedWith string `json:"-"`

	// Pinyin forms of the name and of the folder (see search.Pinyin), kept with the index so a search does not have to
	// convert every name again. Empty for names without Chinese characters. Not part of the API's JSON.
	NamePinyin, NameInitials string `json:"-"`
	DirPinyin, DirInitials   string `json:"-"`
}

// NewItem builds an item. Ids and versions only depend on the key, size and modification time, so the same file
// keeps its index whichever way it is read.
func NewItem(key, name, dir, kind string, size int64, mtime string) Item {
	it := newItem(key, name, dir, kind, size, mtime)
	it.fillPinyin()
	return it
}

// fillPinyin makes the search forms of the name and folder, where they are missing and needed.
func (it *Item) fillPinyin() {
	if it.NamePinyin == "" {
		it.NamePinyin, it.NameInitials = search.Pinyin(it.Name)
	}
	if it.DirPinyin == "" {
		it.DirPinyin, it.DirInitials = search.Pinyin(it.Dir)
	}
}

func newItem(key, name, dir, kind string, size int64, mtime string) Item {
	id := sha1.Sum([]byte(key))
	v := make([]byte, 0, len(key)+len(mtime)+24) // key|size|mtime
	v = append(append(strconv.AppendInt(append(append(v, key...), '|'), size, 10), '|'), mtime...)
	ver := sha1.Sum(v)
	return Item{ID: hex.EncodeToString(id[:6]), Key: key, Name: name, Dir: dir, Kind: kind, Size: size, MTime: mtime,
		Ver: hex.EncodeToString(ver[:5])}
}

// Data is the content of a library's library.json.
type Data struct {
	Library  string   `json:"library"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Location string   `json:"location"`
	Updated  string   `json:"updated"`
	Warnings []string `json:"warnings"`
	Items    []Item   `json:"items"`
}

func libFile(cfg *config.Config, lib config.Library) string {
	return filepath.Join(cfg.LibDir(lib), indexFile)
}

// SaveLibrary writes the index atomically. It reports false when the replace was blocked (see below).
func SaveLibrary(cfg *config.Config, lib config.Library, recs map[string]Item, warnings []string) (bool, error) {
	items := make([]Item, 0, len(recs))
	for _, r := range recs {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if warnings == nil {
		warnings = []string{}
	}
	d := Data{Library: lib.ID, Name: lib.Name, Type: lib.Type, Location: config.Location(lib),
		Updated: time.Now().Format(time.RFC3339), Warnings: warnings, Items: items}
	text, err := encodeData(d)
	if err != nil {
		return false, err
	}
	path := libFile(cfg, lib)
	tmp := tmpFile(path)
	// Windows refuses to replace a file that anyone has open. Every medialib reader and writer takes the io lock,
	// so none of them can be holding the index at this point.
	l := newLock(cfg.LibDir(lib), "io")
	if _, err := l.Acquire(true); err != nil {
		return false, err
	}
	defer l.Release()
	if err := os.WriteFile(tmp, text, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		// A program that is not medialib (antivirus, backup, an editor) has the index open. Nothing is lost and
		// nothing needs retrying: library.tmp is complete and newer, LoadLibrary reads it in preference, and the
		// next save or load promotes it.
		return false, nil
	}
	_ = os.Remove(legacyPath(path)) // the plain library.json of an earlier version is superseded
	published.Store(path, &savedIndex{stamp: stampOf(path), data: &d})
	return true, nil
}

// savedIndex is an index this process has just written, kept so a Store in the same process takes it as it is
// instead of reading and decoding the file again (half a second for 50,000 files, every few seconds while indexing).
type savedIndex struct {
	stamp fileStamp
	data  *Data
}

var published sync.Map // index path -> *savedIndex

// LoadLibrary reads a library's index; a library never indexed is empty.
func LoadLibrary(cfg *config.Config, lib config.Library) (*Data, error) {
	path := libFile(cfg, lib)
	tmp := tmpFile(path)
	l := newLock(cfg.LibDir(lib), "io")
	if _, err := l.Acquire(true); err != nil {
		return nil, err
	}
	defer l.Release()
	if raw, err := os.ReadFile(tmp); err == nil { // a save whose replace was blocked, or a crash in the middle of a write
		if d, err := decodeData(raw); err == nil {
			if os.Rename(tmp, path) == nil {
				_ = os.Remove(legacyPath(path))
			}
			return d, nil
		}
		_ = os.Remove(tmp) // torn write: the index is still the truth
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(legacyPath(path)) // written by an earlier version
	}
	if errors.Is(err, os.ErrNotExist) {
		return &Data{Items: []Item{}, Warnings: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := decodeData(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return d, nil
}

// Snapshot is a loaded index with what the server needs to answer quickly.
type Snapshot struct {
	Data     *Data
	ByID     map[string]*Item
	Version  string // names this snapshot for Since: the store's nonce and gen
	gen      uint64
	floor    uint64            // the oldest gen Since can answer from
	seq      map[string]uint64 // item id -> the gen in which it last changed
	removed  []removal
	raw      json.RawMessage
	rawOnce  sync.Once
	docs     []search.Doc
	docsOnce sync.Once
}

// ItemsJSON is the items already serialized: a big library is encoded once per change, not once per request.
func (s *Snapshot) ItemsJSON() json.RawMessage {
	s.rawOnce.Do(func() {
		if b, err := json.Marshal(s.Data.Items); err == nil {
			s.raw = b
		} else {
			s.raw = json.RawMessage("[]")
		}
	})
	return s.raw
}

// removal is an item that left the index in a given gen.
type removal struct {
	id  string
	gen uint64
}

// maxRemovals bounds the removals a snapshot remembers; a page older than the oldest of them reloads in full.
const maxRemovals = 4096

// Since is what changed after the snapshot named version: the items added or changed, and the ids of those removed.
// ok is false when that is not known (another store, or too long ago): the page then loads everything.
func (s *Snapshot) Since(version string) (changed []Item, removed []string, ok bool) {
	nonce, g, found := strings.Cut(version, ".")
	gen, err := strconv.ParseUint(g, 10, 64)
	if !found || err != nil || nonce != s.nonce() || gen < s.floor || gen > s.gen {
		return nil, nil, false
	}
	changed, removed = []Item{}, []string{}
	for i := range s.Data.Items {
		if s.seq[s.Data.Items[i].ID] > gen {
			changed = append(changed, s.Data.Items[i])
		}
	}
	for _, r := range s.removed {
		if r.gen > gen {
			removed = append(removed, r.id)
		}
	}
	return changed, removed, true
}

func (s *Snapshot) nonce() string { n, _, _ := strings.Cut(s.Version, "."); return n }

// sameItem reports whether a page showing a would show b the same.
func sameItem(a, b *Item) bool {
	coverA, coverB := -1, -1
	if a.Cover != nil {
		coverA = *a.Cover
	}
	if b.Cover != nil {
		coverB = *b.Cover
	}
	return a.Ver == b.Ver && a.Key == b.Key && a.Indexed == b.Indexed && a.Frames == b.Frames && coverA == coverB &&
		a.Error == b.Error && a.Note == b.Note && a.Added == b.Added && a.Duration == b.Duration && a.Width == b.Width &&
		a.Height == b.Height && a.Codec == b.Codec && a.FPS == b.FPS && a.Audio == b.Audio
}

// numberSnapshot numbers a new snapshot after prev: which items changed in it, and which went away.
func numberSnapshot(nonce string, prev, next *Snapshot) {
	next.gen, next.floor = 1, 1
	if prev != nil {
		next.gen, next.floor = prev.gen+1, prev.floor
	}
	next.Version = nonce + "." + strconv.FormatUint(next.gen, 10)
	next.seq = make(map[string]uint64, len(next.Data.Items))
	for i := range next.Data.Items {
		it := &next.Data.Items[i]
		next.seq[it.ID] = next.gen
		if prev != nil {
			if old, ok := prev.ByID[it.ID]; ok && sameItem(old, it) {
				next.seq[it.ID] = prev.seq[it.ID]
			}
		}
	}
	if prev == nil {
		return
	}
	next.removed = append(next.removed, prev.removed...)
	for id := range prev.ByID {
		if _, ok := next.ByID[id]; !ok {
			next.removed = append(next.removed, removal{id, next.gen})
		}
	}
	if n := len(next.removed) - maxRemovals; n > 0 {
		next.floor = next.removed[n-1].gen // a page from before this cannot learn what went
		next.removed = append([]removal(nil), next.removed[n:]...)
	}
}

// Store caches one library's index and reloads it whenever the indexer rewrites it.
type Store struct {
	cfg   *config.Config
	lib   config.Library
	nonce string
	mu    sync.Mutex
	stamp [2]fileStamp
	snap  *Snapshot
}

// NewStore makes a store over a library.
func NewStore(cfg *config.Config, lib config.Library) *Store {
	return &Store{cfg: cfg, lib: lib, nonce: strconv.FormatInt(time.Now().UnixNano(), 36)}
}

// fileStamp tells versions of a file apart. The size counts as well as the time: FAT and exFAT (a portable copy on
// a USB stick) keep times to 2 seconds, so two saves in a row can share one.
type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(p string) fileStamp {
	if st, err := os.Stat(p); err == nil {
		return fileStamp{st.ModTime(), st.Size()}
	}
	return fileStamp{}
}

// Get returns the current index.
func (s *Store) Get() (*Snapshot, error) {
	path := libFile(s.cfg, s.lib)
	s.mu.Lock()
	defer s.mu.Unlock()
	// library.tmp counts too: a save whose replace was blocked lives there until it is promoted
	m := [2]fileStamp{stampOf(path), stampOf(tmpFile(path))}
	if m[0].mod.IsZero() {
		m[0] = stampOf(legacyPath(path)) // not converted yet
	}
	if s.snap != nil && m == s.stamp {
		return s.snap, nil
	}
	var d *Data
	var err error
	if v, ok := published.Load(path); ok && m[1] == (fileStamp{}) && v.(*savedIndex).stamp == m[0] {
		d = v.(*savedIndex).data // the file is exactly what this process wrote
		published.CompareAndDelete(path, v)
	} else {
		d, err = LoadLibrary(s.cfg, s.lib)
	}
	if err != nil {
		if s.snap != nil {
			return s.snap, nil // keep serving the last good index
		}
		return nil, err
	}
	by := make(map[string]*Item, len(d.Items))
	for i := range d.Items {
		by[d.Items[i].ID] = &d.Items[i]
	}
	next := &Snapshot{Data: d, ByID: by}
	numberSnapshot(s.nonce, s.snap, next)
	s.stamp, s.snap = m, next
	return s.snap, nil
}
