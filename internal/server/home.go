package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
)

// The Home page: what was played lately, what is new in every library, and on the first run which folders on this
// computer hold videos.

const (
	homeRows   = 24  // items per row on the Home page
	historyMax = 200 // plays remembered
)

// homeItem is an item of a library as the Home page shows it: the item, and the library it is in.
type homeItem struct {
	Lib     string `json:"lib"`
	LibName string `json:"lib_name"`
	Played  string `json:"played,omitempty"`
	media.Item
}

// play is one entry of the play history (cache/history.json).
type play struct {
	Lib string    `json:"lib"`
	ID  string    `json:"id"`
	At  time.Time `json:"at"`
}

type history struct {
	mu     sync.Mutex
	loaded bool
	list   []play // newest first
}

func (a *App) historyFile() string { return filepath.Join(a.Cfg.CacheDir(), "history.json") }

func (a *App) plays() []play {
	h := &a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.loaded {
		h.loaded = true
		if raw, err := os.ReadFile(a.historyFile()); err == nil {
			_ = json.Unmarshal(raw, &h.list)
		}
	}
	return append([]play(nil), h.list...)
}

// recordPlay remembers what was played (the item a launch started with), most recent first, once each.
func (a *App) recordPlay(lib config.Library, rec *media.Item) {
	list := a.plays()
	list = slices.DeleteFunc(list, func(p play) bool { return p.Lib == lib.ID && p.ID == rec.ID })
	list = append([]play{{Lib: lib.ID, ID: rec.ID, At: time.Now().UTC()}}, list...)
	if len(list) > historyMax {
		list = list[:historyMax]
	}
	a.saveHistory(list)
}

func (a *App) saveHistory(list []play) {
	h := &a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	h.list, h.loaded = list, true
	raw, _ := json.Marshal(list)
	if os.MkdirAll(a.Cfg.CacheDir(), 0o755) == nil {
		tmp := a.historyFile() + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, a.historyFile())
		}
	}
}

// home is the Home page: recently played and recently added, over every library.
func (a *App) home(c *Ctx) (any, error) {
	libs := a.Cfg.Libraries()
	snaps := map[string]*media.Snapshot{}
	names := map[string]string{}
	for _, lib := range libs {
		if snap, err := a.store(lib).Get(); err == nil && snap != nil {
			snaps[lib.ID], names[lib.ID] = snap, lib.Name
		}
	}
	played := []homeItem{}
	plays := a.plays()
	if !isLoopbackConn(c.R) && a.Cfg.Settings().Password == "" {
		plays = nil // what someone played is theirs: not for whoever else can browse the libraries
	}
	for _, p := range plays {
		if snap := snaps[p.Lib]; snap != nil {
			if rec, ok := snap.ByID[p.ID]; ok {
				played = append(played, homeItem{Lib: p.Lib, LibName: names[p.Lib], Played: p.At.Format(time.RFC3339), Item: *rec})
				if len(played) == homeRows {
					break
				}
			}
		}
	}
	// The newest files of all libraries: a bounded selection, not a sort of everything.
	added := make([]homeItem, 0, homeRows+1)
	for id, snap := range snaps {
		for i := range snap.Data.Items {
			it := &snap.Data.Items[i]
			if len(added) == homeRows && it.MTime <= added[homeRows-1].MTime {
				continue
			}
			added = append(added, homeItem{Lib: id, LibName: names[id], Item: *it})
			sort.SliceStable(added, func(i, j int) bool { return added[i].MTime > added[j].MTime })
			if len(added) > homeRows {
				added = added[:homeRows]
			}
		}
	}
	return map[string]any{"played": played, "added": added}, nil
}

// ---------------------------------------------------------------- first run

// suggestion is a folder on this computer that holds videos and is not a library yet.
type suggestion struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Videos int    `json:"videos"`
	More   bool   `json:"more"` // at least Videos: the count stopped there
}

// suggestions looks in the usual places for folders of videos: the user's Videos, Movies, Downloads and Desktop,
// and the top-level folders of other disks (D:\Films, /Volumes/Media/Shows, /mnt/nas/videos ...). The search is
// bounded in files and time; a big folder is counted to a few hundred.
func (a *App) suggestions(c *Ctx) (any, error) {
	ctx, cancel := context.WithTimeout(c.R.Context(), 4*time.Second)
	defer cancel()
	var taken []string
	for _, l := range a.Cfg.Libraries() {
		if l.Type == "local" {
			taken = append(taken, filepath.Clean(config.LocalRoot(l)))
		}
	}
	covered := func(p string) bool {
		for _, t := range taken {
			if config.SameFolder(p, t) || within(p, t) || within(t, p) {
				return true
			}
		}
		return false
	}
	var cands []string
	for _, p := range candidateFolders() {
		if p = filepath.Clean(p); !covered(p) && !slices.ContainsFunc(cands, func(x string) bool { return config.SameFolder(x, p) }) {
			cands = append(cands, p)
		}
	}
	out := make([]suggestion, len(cands))
	var wg sync.WaitGroup
	for i, p := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, more := countVideos(ctx, p, 300, 40000)
			out[i] = suggestion{Path: p, Name: folderName(p), Videos: n, More: more}
		}()
	}
	wg.Wait()
	found := []suggestion{}
	for _, s := range out {
		if s.Videos > 0 {
			found = append(found, s)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Videos > found[j].Videos })
	if len(found) > 8 {
		found = found[:8]
	}
	return map[string]any{"folders": found}, nil
}

func folderName(p string) string {
	n := filepath.Base(p)
	if n == "." || n == string(filepath.Separator) || n == "" || strings.HasSuffix(n, ":\\") {
		return p
	}
	return n
}

// candidateFolders are where videos usually are.
func candidateFolders() []string {
	var out []string
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, n := range []string{"Videos", "Movies", "Downloads", "Desktop", filepath.Join("OneDrive", "Videos")} {
			out = append(out, filepath.Join(home, n))
		}
	}
	for _, r := range diskRoots() {
		out = append(out, subdirs(r)...)
	}
	return out
}

// diskRoots are the other disks and mounted shares, whose top-level folders may be collections of videos (a
// variable: tests keep to their own folders).
var diskRoots = func() []string {
	var roots []string
	switch runtime.GOOS {
	case "windows":
		for d := 'D'; d <= 'Z'; d++ {
			roots = append(roots, string(d)+`:\`)
		}
	case "darwin":
		roots = subdirs("/Volumes")
	default:
		roots = append(subdirs("/mnt"), subdirs("/media")...)
		if u := os.Getenv("USER"); u != "" {
			roots = append(roots, subdirs(filepath.Join("/media", u))...)
			roots = append(roots, subdirs(filepath.Join("/run/media", u))...)
		}
	}
	return roots
}

var systemDirs = map[string]bool{"windows": true, "program files": true, "program files (x86)": true, "programdata": true, "$recycle.bin": true,
	"system volume information": true, "recovery": true, "perflogs": true, "users": true, "msocache": true, "boot": true, "config.msi": true}

// subdirs are the folders directly in dir (not hidden, not the system's own).
func subdirs(dir string) []string {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range list {
		n := e.Name()
		if (e.IsDir() || e.Type()&fs.ModeSymlink != 0) && !strings.HasPrefix(n, ".") && !strings.HasPrefix(n, "$") && !systemDirs[strings.ToLower(n)] {
			out = append(out, filepath.Join(dir, n))
		}
		if len(out) == 40 {
			break
		}
	}
	return out
}

// countVideos counts the video files under dir, stopping at limit videos, after visiting budget entries, or when
// ctx ends; more says it stopped early with some found.
func countVideos(ctx context.Context, dir string, limit, budget int) (n int, more bool) {
	visited := 0
	stop := fs.SkipAll
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if n >= limit || visited > budget || ctx.Err() != nil {
			more = n > 0
			return stop
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || systemDirs[strings.ToLower(d.Name())] || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if media.MediaKind(d.Name()) == "video" {
			n++
		}
		return nil
	})
	return n, more
}
