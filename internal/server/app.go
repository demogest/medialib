// Package server is the HTTP server: the web UI, the library API, and the connection / storage API.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/players"
	"github.com/demogest/medialib/internal/storage"
	"github.com/demogest/medialib/internal/tasks"
	"github.com/demogest/medialib/internal/update"
	"github.com/demogest/medialib/internal/version"
)

type linkKey struct{ lib, id string }
type link struct {
	url   string
	until time.Time
}

// App is everything the routes work on.
type App struct {
	Cfg      *config.Config
	Clients  *config.Clients
	Tasks    *tasks.Runner
	Storage  *storage.Storage
	Indexer  *media.Indexer
	Mode     string // "server" or "desktop"
	Listen   string // host:port the server is bound to
	Port     int
	Loopback bool        // bound to this computer only
	Log      *log.Logger // one line per request when set (MEDIALIB_LOG=1)
	Updates  *update.Updater
	Quit     func() // ends the program (closes the desktop window); nil when nothing can

	mu       sync.RWMutex
	players  []players.Player // every player, hidden and missing ones included
	moving   bool             // the index and covers are being moved: no indexing until that is done
	stores   map[string]*media.Store
	launched map[string]time.Time // recent player launches, see Launch
	links    map[linkKey]link
	jobs     map[string]*media.Job
	hist     history // what was played, for the Home page

	autoMu     sync.Mutex
	autoParent context.Context // the server's lifetime, for the automatic indexing loop
	autoStop   context.CancelFunc
}

// NewApp builds the application state over a loaded config.
func NewApp(cfg *config.Config, mode string) *App {
	clients := config.NewClients(cfg)
	runner := tasks.NewRunner()
	a := &App{
		Cfg: cfg, Clients: clients, Tasks: runner, Mode: mode,
		Storage: &storage.Storage{Clients: clients, Tasks: runner},
		Indexer: &media.Indexer{Cfg: cfg, Clients: clients},
		stores:  map[string]*media.Store{}, links: map[linkKey]link{}, jobs: map[string]*media.Job{},
	}
	a.DetectPlayers()
	return a
}

// ---------------------------------------------------------------- players

// DetectPlayers looks for players again (after the list was edited, or a player was installed).
func (a *App) DetectPlayers() {
	s := a.Cfg.Settings()
	list := players.Detect(s.Players, s.HiddenPlayers)
	a.mu.Lock()
	a.players = list
	a.mu.Unlock()
}

// Players are the players offered for playing.
func (a *App) Players() []players.Player {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return players.Playable(a.players)
}

// AllPlayers are every player, the ones removed from the list and the ones whose program is gone included.
func (a *App) AllPlayers() []players.Player {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]players.Player(nil), a.players...)
}

// ---------------------------------------------------------------- libraries

func (a *App) store(lib config.Library) *media.Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.stores[lib.ID]
	if !ok {
		s = media.NewStore(a.Cfg, lib)
		a.stores[lib.ID] = s
	}
	return s
}

func (a *App) dropStore(id string) {
	a.mu.Lock()
	delete(a.stores, id)
	a.mu.Unlock()
}

// Describe is a library as the API shows it.
func (a *App) Describe(lib config.Library) (map[string]any, *media.Snapshot) {
	snap, _ := a.store(lib).Get()
	count, updated := 0, any(nil)
	if snap != nil {
		count = len(snap.Data.Items)
		if snap.Data.Updated != "" {
			updated = snap.Data.Updated
		}
	}
	out := map[string]any{"id": lib.ID, "name": lib.Name, "type": lib.Type, "location": config.Location(lib), "items": count, "updated": updated}
	if snap != nil {
		out["covers"] = coverRefs(snap.Data.Items, 4)
	}
	out["reachable"] = a.reachable(lib)
	switch lib.Type {
	case "s3":
		conn, ok := a.Cfg.Connection(lib.Connection)
		out["connection"], out["bucket"], out["prefix"] = lib.Connection, lib.Bucket, lib.Prefix
		if ok {
			out["connection_name"] = conn.Name
		} else {
			out["connection_name"] = nil
		}
	case "local":
	default:
		out["convertible"] = true
	}
	return out, snap
}

// coverRef names one cover image: /thumbs/<library>/<id>-<ver>-<i>.avif.
type coverRef struct {
	ID  string `json:"id"`
	Ver string `json:"ver"`
	I   int    `json:"i"`
}

// coverRefs are the covers of the newest videos that have one, for a library's tile.
func coverRefs(items []media.Item, n int) []coverRef {
	var top []*media.Item
	for i := range items {
		it := &items[i]
		if it.Frames == 0 || it.Kind != "video" {
			continue
		}
		pos := len(top)
		for pos > 0 && top[pos-1].MTime < it.MTime {
			pos--
		}
		if pos < n {
			top = append(top[:pos], append([]*media.Item{it}, top[pos:]...)...)
			if len(top) > n {
				top = top[:n]
			}
		}
	}
	out := make([]coverRef, len(top))
	for i, it := range top {
		c := 0
		if it.Cover != nil {
			c = *it.Cover
		}
		out[i] = coverRef{it.ID, it.Ver, c}
	}
	return out
}

// reachable reports whether a library's media can be read at all: its folder is there (a disk can be unplugged, a
// share offline), or its connection still exists.
func (a *App) reachable(lib config.Library) bool {
	switch lib.Type {
	case "local":
		st, err := os.Stat(config.LocalRoot(lib))
		return err == nil && st.IsDir()
	case "s3":
		_, ok := a.Cfg.Connection(lib.Connection)
		return ok
	}
	return true
}

func (a *App) indexing(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	j := a.jobs[id]
	return j != nil && media.Running(j.Snapshot().State)
}

// RemoveLibrary drops a library and its generated index and thumbnails (never media).
func (a *App) RemoveLibrary(id string) error {
	if a.indexing(id) {
		return config.Errorf("That library is being indexed; wait for it to finish.")
	}
	lib, err := a.Cfg.RemoveLibrary(id)
	if err != nil {
		return err
	}
	a.dropStore(id)
	a.mu.Lock()
	delete(a.jobs, id)
	a.mu.Unlock()
	cache, _ := filepath.Abs(a.Cfg.CacheDir())
	target, _ := filepath.Abs(a.Cfg.LibDir(lib))
	if filepath.Dir(target) == cache { // only ever the generated index and thumbnails, never media
		_ = os.RemoveAll(target)
	}
	return nil
}

// ConvertToS3 switches an rclone library to reading through the S3 API directly. Its index stays valid: ids and
// versions only depend on the object key, size and modification time, which are the same either way.
func (a *App) ConvertToS3(id string) (config.Library, error) {
	lib, ok := a.Cfg.Library(id)
	if !ok || lib.Type != "rclone" {
		return config.Library{}, config.Errorf("That library is not an rclone library.")
	}
	if a.indexing(id) {
		return config.Library{}, config.Errorf("That library is being indexed; wait for it to finish.")
	}
	remote := trimRight(lib.Remote, ':')
	draft, ok := a.Cfg.RcloneRemote(remote)
	if !ok {
		return config.Library{}, config.Errorf("rclone has no S3 remote named “%s”, so its credentials can't be reused.", remote)
	}
	conn, err := a.Cfg.AdoptDraft(draft)
	if err != nil {
		return config.Library{}, err
	}
	out, err := a.Cfg.ConvertToS3(id, conn.ID)
	if err != nil {
		return out, err
	}
	a.dropStore(id)
	return out, nil
}

func trimRight(s string, c byte) string {
	for len(s) > 0 && s[len(s)-1] == c {
		s = s[:len(s)-1]
	}
	return s
}

// ---------------------------------------------------------------- indexing jobs

// StartIndex begins indexing a library in the background (or returns the run already going).
func (a *App) StartIndex(lib config.Library, opt media.Options) *media.Job {
	a.mu.Lock()
	if j := a.jobs[lib.ID]; j != nil && media.Running(j.Snapshot().State) {
		a.mu.Unlock()
		return j
	}
	job := media.NewJob()
	if a.moving {
		a.mu.Unlock()
		job.Fail(errors.New("The index and covers are being moved to another folder; index again once that is done."))
		job.Finish()
		return job
	}
	a.jobs[lib.ID] = job
	a.mu.Unlock()
	go func() {
		defer job.Finish()
		if err := a.Indexer.Run(context.Background(), lib, opt, job); err != nil {
			job.Fail(err)
		}
	}()
	return job
}

// RunAutoIndex keeps every library up to date by itself while ctx lasts, a pass every "auto_index" minutes (none
// when that is 0). A change of the setting takes effect at once: see restartAutoIndex.
func (a *App) RunAutoIndex(ctx context.Context) {
	a.autoMu.Lock()
	a.autoParent = ctx
	a.autoMu.Unlock()
	a.restartAutoIndex()
}

func (a *App) restartAutoIndex() {
	a.autoMu.Lock()
	defer a.autoMu.Unlock()
	if a.autoStop != nil {
		a.autoStop()
		a.autoStop = nil
	}
	every := a.Cfg.Settings().AutoIndex
	if a.autoParent == nil || every <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(a.autoParent)
	a.autoStop = cancel
	go a.AutoIndex(ctx, time.Duration(every)*time.Minute)
}

// AutoIndex keeps every library up to date while ctx lasts, for a server nobody presses "Update index" on: a first
// pass soon after start (changes made while it was down), then one every interval after the previous pass ended, so
// passes never overlap however long they take. Libraries go one after another, sharing the machine and the store
// with whoever is browsing. One that cannot be reached is left alone: an unplugged disk is not a failed index.
func (a *App) AutoIndex(ctx context.Context, every time.Duration) {
	wait := min(time.Minute, every)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		for _, lib := range a.Cfg.Libraries() {
			if !a.reachable(lib) {
				continue
			}
			select {
			case <-a.StartIndex(lib, media.Options{}).Done():
			case <-ctx.Done():
				return
			}
		}
		wait = every
	}
}

// Jobs snapshots every indexing job.
func (a *App) Jobs() map[string]media.JobState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[string]media.JobState, len(a.jobs))
	for id, j := range a.jobs {
		out[id] = j.Snapshot()
	}
	return out
}

// ---------------------------------------------------------------- media

// Link is a presigned URL for an object of an S3 library, reused while it has more than 12 h left.
func (a *App) Link(lib config.Library, rec *media.Item) (string, error) {
	k := linkKey{lib.ID, rec.ID}
	a.mu.RLock()
	l := a.links[k]
	a.mu.RUnlock()
	if time.Now().Before(l.until) {
		return l.url, nil
	}
	u, err := media.Presign(a.Cfg, a.Clients, lib, rec.Key, 24*time.Hour)
	if err != nil {
		return "", err
	}
	now := time.Now()
	a.mu.Lock()
	if len(a.links) >= 4096 { // a long-running server streams many files: forget the links that ran out
		for key, l := range a.links {
			if now.After(l.until) {
				delete(a.links, key)
			}
		}
	}
	a.links[k] = link{u, now.Add(12 * time.Hour)}
	a.mu.Unlock()
	return u, nil
}

func (a *App) clearLinks() {
	a.mu.Lock()
	a.links = map[linkKey]link{}
	a.mu.Unlock()
}

// LocalPath is where an item of a local library lives.
func LocalPath(lib config.Library, rec *media.Item) string {
	return filepath.Join(config.LocalRoot(lib), filepath.FromSlash(rec.Key))
}

func mediaURL(host string, lib config.Library, rec *media.Item) string {
	return fmt.Sprintf("http://%s/media/%s/%s/%s", host, lib.ID, rec.ID, url.PathEscape(rec.Name))
}

func (a *App) selfHost() string { return fmt.Sprintf("127.0.0.1:%d", a.Port) }

// Playlist is an M3U with stable URLs; with an empty host a local library lists its file paths instead.
func (a *App) Playlist(lib config.Library, recs []*media.Item, host string) string {
	entries := make([]players.Entry, len(recs))
	for i, r := range recs {
		target := mediaURL(orStr(host, a.selfHost()), lib, r)
		if host == "" && lib.Type == "local" {
			target = LocalPath(lib, r)
		}
		entries[i] = players.Entry{Target: target, Name: r.Name, Duration: r.Duration}
	}
	return players.M3U(entries)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Play opens items in a player on this computer.
func (a *App) Play(lib config.Library, playerID string, recs []*media.Item) (string, error) {
	entries := make([]players.Entry, 0, len(recs))
	for _, r := range recs {
		if lib.Type == "local" {
			path := LocalPath(lib, r)
			if len(recs) == 1 && !players.Reachable(path) {
				return "", fmt.Errorf("File not reachable: %s", path) // the file itself: no HTTP hop, sidecar subtitles load
			}
			entries = append(entries, players.Entry{Target: path, Name: r.Name, Duration: r.Duration})
		} else {
			entries = append(entries, players.Entry{Target: mediaURL(a.selfHost(), lib, r), Name: r.Name, Duration: r.Duration})
		}
	}
	return a.Launch(playerID, entries)
}

// Launch opens entries in a player: one directly, several as a playlist.
func (a *App) Launch(playerID string, entries []players.Entry) (string, error) {
	// A repeated request for the very same thing a moment after the first (a key held down, a double click, an
	// impatient second try while the player is still starting) must not start another player.
	key := playerID + "\x00" + fmt.Sprint(len(entries)) + "\x00" + entries[0].Target
	a.mu.Lock()
	if a.launched == nil {
		a.launched = map[string]time.Time{}
	}
	if t, ok := a.launched[key]; ok && time.Since(t) < 6*time.Second {
		a.mu.Unlock()
		name := playerID
		for _, p := range a.Players() {
			if p.ID == playerID {
				name = p.Name
			}
		}
		return name, nil
	}
	a.launched[key] = time.Now()
	a.mu.Unlock()
	return players.Launch(a.Players(), playerID, entries, filepath.Join(a.Cfg.CacheDir(), "playlists", "now-playing.m3u8"))
}

// SystemInfo is what the Settings page shows. r is the request asking: what it may change depends on who asks.
func (a *App) SystemInfo(r *http.Request) map[string]any {
	s := a.Cfg.Settings()
	where := func(name, def string) any {
		if name == "" {
			name = def
		}
		if p := config.FindProgram(name); p != "" {
			return p
		}
		return nil
	}
	all := a.AllPlayers()
	ps := make([]map[string]any, len(all))
	for i, p := range all {
		var path any
		if p.Path != "" {
			path = p.Path
		}
		ps[i] = map[string]any{"id": p.ID, "name": p.Name, "path": path, "custom": p.Custom, "hidden": p.Hidden, "missing": p.Missing}
	}
	local := r != nil && isLoopbackConn(r)
	canEdit := local || s.Password != ""
	info := map[string]any{
		"version": version.Version, "runtime": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"ffmpeg": where(s.FFmpeg, "ffmpeg"), "ffprobe": where(s.FFprobe, "ffprobe"), "rclone": where(s.Rclone, "rclone"),
		"config_dir": a.Cfg.Home(), "config_file": a.Cfg.Path(), "cache_dir": a.Cfg.CacheDir(), "cache_custom": a.Cfg.CacheDir() != a.Cfg.DefaultCacheDir(),
		"port": a.Port, "listen": a.Listen, "mode": a.Mode,
		"players": ps, "default_player": s.DefaultPlayer, "auto_index": s.AutoIndex, "auto_index_env": s.AutoIndexEnv,
		"workers": s.Workers, "thumb_quality": s.ThumbQuality, "tools": map[string]string{"ffmpeg": s.FFmpeg, "ffprobe": s.FFprobe, "rclone": s.Rclone},
		"updates": s.Updates,
		// What this browser may do: change settings (this computer, or signed in), and act on this computer's screen.
		"can_edit": canEdit, "on_machine": local,
	}
	if !canEdit { // someone who may only watch learns what is there, not where it is on this computer
		for _, k := range []string{"ffmpeg", "ffprobe", "rclone"} {
			info[k] = info[k] != nil
		}
		for _, p := range ps {
			p["path"] = nil
		}
		for _, k := range []string{"config_dir", "config_file", "cache_dir", "tools", "listen"} {
			delete(info, k)
		}
	}
	return info
}

// Serve runs the HTTP server on a listener until ctx ends.
func (a *App) Serve(ctx context.Context, ln net.Listener, web fs.FS) error {
	a.Listen = ln.Addr().String()
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		a.Port = tcp.Port
	}
	mux := http.NewServeMux()
	mux.Handle("/", a.Handler(web))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeText(w, 200, "ok") })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		_ = srv.Close()
		return nil
	}
}
