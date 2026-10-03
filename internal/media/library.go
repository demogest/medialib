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
	ver := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%s", key, size, mtime)))
	return Item{ID: hex.EncodeToString(id[:])[:12], Key: key, Name: name, Dir: dir, Kind: kind, Size: size, MTime: mtime,
		Ver: hex.EncodeToString(ver[:])[:10]}
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
	text, err := encodeData(Data{Library: lib.ID, Name: lib.Name, Type: lib.Type, Location: config.Location(lib),
		Updated: time.Now().Format(time.RFC3339), Warnings: warnings, Items: items})
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
	return true, nil
}

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

// Store caches one library's index and reloads it whenever the indexer rewrites it.
type Store struct {
	cfg   *config.Config
	lib   config.Library
	mu    sync.Mutex
	stamp [2]fileStamp
	snap  *Snapshot
}

// NewStore makes a store over a library.
func NewStore(cfg *config.Config, lib config.Library) *Store { return &Store{cfg: cfg, lib: lib} }

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
	d, err := LoadLibrary(s.cfg, s.lib)
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
	s.stamp, s.snap = m, &Snapshot{Data: d, ByID: by}
	return s.snap, nil
}
