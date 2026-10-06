package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ncruces/zenity"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/players"
	"github.com/demogest/medialib/internal/tasks"
)

// linkLife is how long a copied link to an object of a bucket works (the longest a presigned link can).
const linkLife = 7 * 24 * time.Hour

// settingsRoutes are the Settings page's: options, players, the index folder, updates; and the links and playlists
// that point at the media itself rather than at this server.
func (a *App) settingsRoutes(def func(string, int, routeFn)) {
	def("PUT /api/settings", private, a.putSettings)
	def("POST /api/settings/cache-dir", private, a.moveCache)
	def("POST /api/open-folder", machine, a.openFolder)
	def("POST /api/pick-file", machine, a.pickFile)

	def("POST /api/players", machine, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		if _, err := a.Cfg.AddPlayer(b.Str("name"), b.Str("path"), strings.Fields(b.Str("args"))); err != nil {
			return nil, err
		}
		a.DetectPlayers()
		return a.playerList(), nil
	})
	def("DELETE /api/players/{id}", machine, func(c *Ctx) (any, error) {
		if err := a.Cfg.RemovePlayer(c.P("id")); err != nil {
			return nil, err
		}
		a.DetectPlayers()
		return a.playerList(), nil
	})
	def("POST /api/players/restore", machine, func(c *Ctx) (any, error) {
		if err := a.Cfg.RestorePlayers(); err != nil {
			return nil, err
		}
		a.DetectPlayers()
		return a.playerList(), nil
	})
	def("POST /api/players/detect", machine, func(c *Ctx) (any, error) { a.DetectPlayers(); return a.playerList(), nil })

	def("GET /api/home", open, a.home)
	def("DELETE /api/history", private, func(c *Ctx) (any, error) { a.saveHistory(nil); return map[string]any{"ok": true}, nil })
	def("GET /api/suggestions", private, a.suggestions)
	def("GET /api/link", open, a.link)
	def("POST /api/playlist/save", machine, a.savePlaylist)

	def("GET /api/update", private, func(c *Ctx) (any, error) {
		if a.Updates == nil {
			return nil, fail(404, "updates are not available in this build")
		}
		return a.Updates.Status(), nil
	})
	def("POST /api/update", machine, a.updateAction)
}

// playerList is the players for playing, and every player with its state for the Settings page.
func (a *App) playerList() map[string]any {
	list := []map[string]any{}
	for _, p := range a.Players() {
		list = append(list, map[string]any{"id": p.ID, "name": p.Name})
	}
	all := []map[string]any{}
	for _, p := range a.AllPlayers() {
		all = append(all, map[string]any{"id": p.ID, "name": p.Name, "path": p.Path, "custom": p.Custom, "hidden": p.Hidden, "missing": p.Missing})
	}
	return map[string]any{"players": list, "all": all, "default": a.Cfg.Settings().DefaultPlayer}
}

func (a *App) putSettings(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	o := config.Options{AutoIndex: b.IntP("auto_index"), Workers: b.IntP("workers"), ThumbQuality: b.IntP("thumb_quality"),
		FFmpeg: b.StrP("ffmpeg"), FFprobe: b.StrP("ffprobe"), Rclone: b.StrP("rclone"), Updates: b.StrP("updates"), DefaultPlayer: b.StrP("default_player")}
	if o.AutoIndex != nil && a.Cfg.Settings().AutoIndexEnv {
		return nil, config.Errorf("MEDIALIB_AUTO_INDEX sets automatic indexing on this computer; change it there.")
	}
	before := a.Cfg.Settings()
	if err := a.Cfg.SetOptions(o); err != nil {
		return nil, err
	}
	if a.Cfg.Settings().AutoIndex != before.AutoIndex {
		a.restartAutoIndex()
	}
	return a.SystemInfo(c.R), nil
}

// ---------------------------------------------------------------- the index and covers folder

// moveCache moves the index and covers to another folder (a background task), or with "check" only says where
// they would go and how much there is to move.
func (a *App) moveCache(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	src := a.Cfg.CacheDir()
	dst := a.Cfg.DefaultCacheDir()
	if !b.Bool("default") {
		if dst, err = cacheTarget(b.Str("path"), src); err != nil {
			return nil, err
		}
	}
	if config.SameFolder(filepath.Clean(dst), filepath.Clean(src)) {
		return nil, config.Errorf("The index and covers are already in %s.", dst)
	}
	if within(dst, src) || within(src, dst) {
		return nil, config.Errorf("Choose a folder that is neither inside the current one nor holding it.")
	}
	if !emptyOrMissing(dst) {
		return nil, config.Errorf("%s has files in it; choose an empty folder.", dst)
	}
	files, size := measure(src)
	if b.Bool("check") {
		return map[string]any{"from": src, "to": dst, "files": files, "bytes": size}, nil
	}
	a.mu.Lock()
	busy := a.moving
	for _, j := range a.jobs {
		busy = busy || media.Running(j.Snapshot().State)
	}
	if !busy {
		a.moving = true
	}
	a.mu.Unlock()
	if busy {
		return nil, config.Errorf("A library is being indexed (or the index is already moving); try again once that is done.")
	}
	t := a.Tasks.Start("move", "Move the index and covers to "+dst, func(t *tasks.Task) error {
		defer func() { a.mu.Lock(); a.moving = false; a.mu.Unlock() }()
		t.SetTotal(files)
		if err := moveTree(t, src, dst); err != nil {
			return err
		}
		t.Progress(files, size)
		if err := a.Cfg.SetCacheDir(dst); err != nil {
			return err
		}
		a.mu.Lock()
		a.stores = map[string]*media.Store{} // read again from the new place
		a.mu.Unlock()
		if err := os.RemoveAll(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fail("Some files could not be removed from %s: %v", src, err)
		}
		t.Line("The index and covers are in " + dst)
		return nil
	})
	t.Wait(time.Second)
	return map[string]any{"task": t.Snapshot(), "to": dst}, nil
}

// cacheTarget is where a chosen folder puts the index and covers: the folder itself when it is empty or new, else a
// "medialib" folder inside it (a whole disk, or a folder of documents, is a fine choice to make).
func cacheTarget(p, current string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if p == "" {
		return "", config.Errorf("Choose a folder.")
	}
	p = filepath.Clean(config.ExpandVars(p))
	if !filepath.IsAbs(p) {
		return "", config.Errorf("Enter a full path, like %s.", map[bool]string{true: `D:\medialib`, false: "/srv/medialib"}[runtime.GOOS == "windows"])
	}
	if emptyOrMissing(p) || config.SameFolder(p, current) {
		return p, nil
	}
	return filepath.Join(p, "medialib"), nil
}

func emptyOrMissing(dir string) bool {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	return err == io.EOF && len(names) == 0
}

// within reports whether path is inside dir.
func within(path, dir string) bool {
	p, d := filepath.Clean(path), filepath.Clean(dir)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p, d = strings.ToLower(p), strings.ToLower(d)
	}
	return strings.HasPrefix(p, strings.TrimSuffix(d, string(filepath.Separator))+string(filepath.Separator))
}

func measure(dir string) (files, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return files, size
}

// renameDir is os.Rename, which tests make fail to take the way across disks.
var renameDir = os.Rename

// moveTree moves a folder: one rename on the same disk, else a copy of every file followed by the switch (the
// caller removes the old folder once the new one is in use).
func moveTree(t *tasks.Task, src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dst, 0o755) // nothing indexed yet
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst) // an empty folder chosen as the target
	if renameDir(src, dst) == nil {
		return nil
	}
	var done, moved int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := t.Check(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyOne(p, target, info); err != nil {
			return err
		}
		done++
		moved += info.Size()
		t.Progress(done, moved)
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(dst) // the old folder is still complete and in use
		return err
	}
	return nil
}

func copyOne(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// openFolder shows the settings folder, or the index and covers folder, in the file manager.
func (a *App) openFolder(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	dir := a.Cfg.Home()
	if b.Str("what") == "cache" {
		dir = a.Cfg.CacheDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := players.Open(dir); err != nil {
		return nil, fmt.Errorf("no file manager could be started (%w)", err)
	}
	return map[string]any{"ok": true, "path": dir}, nil
}

// pickFile shows the system's file dialog to choose a program (a player, ffmpeg ...). An empty path means the user cancelled.
func (a *App) pickFile(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	title := b.Str("title")
	if title == "" || len(title) > 120 {
		title = "Choose a program"
	}
	go players.FocusTitled(title)
	opts := []zenity.Option{zenity.Title(title)}
	if runtime.GOOS == "windows" {
		opts = append(opts, zenity.FileFilters{{Name: "Programs", Patterns: []string{"*.exe"}, CaseFold: true}})
	}
	p, err := zenity.SelectFile(opts...)
	if errors.Is(err, zenity.ErrCanceled) {
		return map[string]any{"path": ""}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": p}, nil
}

// ---------------------------------------------------------------- links and playlists

// link is where a file really is: its path on this computer's disk for a local library, a presigned link that
// works from anywhere for one in a bucket.
func (a *App) link(c *Ctx) (any, error) {
	lib, err := c.Library(c.Arg("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	rec, ok := snap.ByID[c.Arg("id")]
	if !ok {
		return nil, fail(404, "unknown media id")
	}
	if lib.Type == "local" {
		return map[string]any{"url": LocalPath(lib, rec), "kind": "path"}, nil
	}
	u, err := media.Presign(a.Cfg, a.Clients, lib, rec.Key, linkLife)
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": u, "kind": "link", "expires": time.Now().Add(linkLife).UTC().Format(time.RFC3339)}, nil
}

// savePlaylist writes a playlist of real links (file paths, presigned links) where the user chooses: one that works
// in any player, and without medialib running.
func (a *App) savePlaylist(c *Ctx) (any, error) {
	lib, err := c.Library(c.Arg("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	recs := a.selectItems(snap, c)
	if len(recs) == 0 {
		return nil, fail(400, "nothing to put in a playlist")
	}
	if len(recs) > 5000 {
		recs = recs[:5000]
	}
	name := lib.Name
	if d := strings.Trim(c.Arg("dir"), "/"); d != "" {
		name = filepath.Base(d)
	}
	const title = "Save the playlist"
	go players.FocusTitled(title)
	p, err := zenity.SelectFileSave(zenity.Title(title), zenity.Filename(safeName(name)+".m3u8"), zenity.ConfirmOverwrite(),
		zenity.FileFilters{{Name: "Playlists", Patterns: []string{"*.m3u8", "*.m3u"}, CaseFold: true}})
	if errors.Is(err, zenity.ErrCanceled) {
		return map[string]any{"path": ""}, nil
	}
	if err != nil {
		return nil, err
	}
	if filepath.Ext(p) == "" {
		p += ".m3u8"
	}
	entries := make([]players.Entry, 0, len(recs))
	for _, r := range recs {
		target := LocalPath(lib, r)
		if lib.Type != "local" {
			if target, err = media.Presign(a.Cfg, a.Clients, lib, r.Key, linkLife); err != nil {
				return nil, err
			}
		}
		entries = append(entries, players.Entry{Target: target, Name: r.Name, Duration: r.Duration})
	}
	if err := os.WriteFile(p, []byte(players.M3U(entries)), 0o644); err != nil {
		return nil, err
	}
	out := map[string]any{"path": p, "count": len(entries)}
	if lib.Type != "local" {
		out["expires"] = time.Now().Add(linkLife).UTC().Format(time.RFC3339)
	}
	return out, nil
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if s = strings.TrimSpace(s); s == "" {
		return "playlist"
	}
	return s
}

// ---------------------------------------------------------------- updates

// updateAction checks for an update, or installs one and restarts medialib.
func (a *App) updateAction(c *Ctx) (any, error) {
	if a.Updates == nil {
		return nil, fail(404, "updates are not available in this build")
	}
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	switch b.Str("action") {
	case "check":
		return a.Updates.Check(c.R.Context(), 0), nil
	case "install":
		st := a.Updates.Status()
		if !st.Ready && !st.CanInstall {
			return nil, config.Errorf("%s", orStr(st.Why, "There is no update to install."))
		}
		if a.Quit == nil {
			return nil, config.Errorf("This copy of medialib cannot restart itself.")
		}
		go func() {
			if err := a.Updates.Prepare(context.Background()); err != nil {
				return // the status says what went wrong
			}
			if a.Updates.Finish(true) == nil {
				a.Quit()
			}
		}()
		time.Sleep(200 * time.Millisecond) // long enough to be downloading when the answer is read
		return a.Updates.Status(), nil
	}
	return nil, config.Errorf("Unknown update action.")
}

// RunUpdates looks for a new version as medialib starts, and then once a day while ctx lasts (unless updates are off), and with "auto"
// downloads it: a program file is replaced at once and the next start is the new version; a setup program runs when
// medialib closes (AtExit).
func (a *App) RunUpdates(ctx context.Context) {
	if a.Updates == nil {
		return
	}
	go func() {
		time.Sleep(3 * time.Second)
		a.Updates.Cleanup() // what the previous update left, once that copy is surely gone
	}()
	go func() {
		// Every launch looks: at once, so the sidebar can say so by the time the window is up. A copy left running
		// (a server) looks again once a day.
		wait := time.Duration(0)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = time.Hour
			mode := a.Cfg.Settings().Updates
			if mode == "off" {
				continue
			}
			st := a.Updates.Check(ctx, 24*time.Hour)
			if mode == "auto" && st.Available && st.CanInstall && !st.Ready {
				_ = a.Updates.Prepare(ctx)
			}
		}
	}()
}
