package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/players"
	"github.com/demogest/medialib/internal/s3"
	"github.com/demogest/medialib/internal/storage"
)

func init() {
	// The registry on Windows is not to be trusted with these.
	for ext, typ := range map[string]string{
		".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".json": "application/json",
		".html": "text/html", ".mp4": "video/mp4", ".m4v": "video/x-m4v", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
		".webm": "video/webm", ".avi": "video/x-msvideo", ".wmv": "video/x-ms-wmv", ".flv": "video/x-flv", ".ts": "video/mp2t",
		".m2ts": "video/mp2t", ".mp3": "audio/mpeg", ".flac": "audio/flac", ".m4a": "audio/mp4", ".aac": "audio/aac",
		".wav": "audio/wav", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// Handler builds the HTTP handler for the whole application. web is the UI's files (index.html, css/, js/).
func (a *App) Handler(web fs.FS) http.Handler {
	mux := http.NewServeMux()
	def := func(pattern string, class int, fn routeFn) { mux.HandleFunc(pattern, a.guard(class, fn)) }

	// UI
	def("GET /{$}", open, func(c *Ctx) (any, error) { return nil, serveWeb(c, web, "index.html") })
	def("GET /static/{path...}", open, func(c *Ctx) (any, error) { return nil, serveWeb(c, web, c.P("path")) })
	def("GET /thumbs/{lib}/{name}", open, a.thumb)
	def("GET /api/system", open, func(c *Ctx) (any, error) { return a.SystemInfo(c.R), nil })

	// libraries
	def("GET /api/libraries", open, a.listLibraries)
	def("GET /api/library", open, a.getLibrary)
	def("GET /api/search", open, a.searchMedia)
	def("GET /api/index", open, func(c *Ctx) (any, error) { return map[string]any{"jobs": a.Jobs()}, nil })
	def("GET /api/players", open, func(c *Ctx) (any, error) { return a.playerList(), nil })
	def("GET /api/playlist.m3u8", open, a.playlist)
	def("GET /media/{rest...}", open, a.media)
	def("GET /subs/{lib}/{id}/{name}", open, a.subtitle)
	def("GET /art/{lib}", open, a.folderArt)
	def("POST /api/play", machine, a.play)
	def("POST /api/reveal", machine, a.reveal)
	def("POST /api/libraries", private, a.addLibrary)
	def("POST /api/libraries/remove", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		if err := a.RemoveLibrary(b.Str("id")); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "active": a.Cfg.Active()}, nil
	})
	def("POST /api/libraries/active", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		lib, err := c.Library(b.Str("id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, a.Cfg.SetActive(lib.ID)
	})
	def("POST /api/libraries/rename", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		if err := a.Cfg.RenameLibrary(b.Str("id"), b.Str("name")); err != nil {
			return nil, err
		}
		a.dropStore(b.Str("id"))
		return map[string]any{"ok": true}, nil
	})
	def("POST /api/libraries/update", private, a.updateLibrary)
	def("POST /api/libraries/convert", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		lib, err := a.ConvertToS3(b.Str("id"))
		if err != nil {
			return nil, err
		}
		d, _ := a.Describe(lib)
		return d, nil
	})
	def("POST /api/index", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		lib, err := c.Library(b.Str("id"))
		if err != nil {
			return nil, err
		}
		return a.StartIndex(lib, media.Options{Force: b.Bool("force"), Retry: b.Bool("retry")}).Snapshot(), nil
	})
	def("POST /api/pick-folder", machine, a.pickFolder)

	// connections
	def("GET /api/providers", private, func(c *Ctx) (any, error) { return map[string]any{"providers": s3.Providers}, nil })
	def("GET /api/connections", private, func(c *Ctx) (any, error) {
		list := []config.PublicConnection{}
		for _, x := range a.Cfg.Connections() {
			list = append(list, config.Public(x))
		}
		return map[string]any{"connections": list}, nil
	})
	def("POST /api/connections", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		rec, err := a.Cfg.AddConnection(formOf(b))
		if err != nil {
			return nil, err
		}
		return config.Public(rec), nil
	})
	def("PUT /api/connections/{id}", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		rec, err := a.Cfg.UpdateConnection(c.P("id"), formOf(b))
		if err != nil {
			return nil, err
		}
		a.Clients.Drop(c.P("id"))
		return config.Public(rec), nil
	})
	def("DELETE /api/connections/{id}", private, func(c *Ctx) (any, error) {
		if err := a.Cfg.RemoveConnection(c.P("id")); err != nil {
			return nil, err
		}
		a.Clients.Drop(c.P("id"))
		return map[string]any{"ok": true}, nil
	})
	def("POST /api/connections/test", private, a.testDraft)
	def("POST /api/connections/{conn}/test", private, func(c *Ctx) (any, error) {
		conn, ok := a.Cfg.Connection(c.P("conn"))
		if !ok {
			return nil, fail(404, "unknown connection")
		}
		cl, err := c.Client()
		if err != nil {
			return nil, err
		}
		return config.TestConnection(cl, conn.DefaultBucket), nil
	})
	def("GET /api/connections/importable", private, func(c *Ctx) (any, error) {
		list := a.Cfg.Importable()
		if list == nil {
			list = []config.Draft{}
		}
		return map[string]any{"sources": list}, nil
	})
	def("POST /api/connections/import", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		rec, err := a.Cfg.Adopt(b.Str("source"))
		if err != nil {
			return nil, err
		}
		return config.Public(rec), nil
	})

	// storage
	const B = "/api/s3/{conn}/b/{bucket}"
	def("GET /api/s3/{conn}/buckets", private, a.buckets)
	def("POST /api/s3/{conn}/buckets", private, a.createBucket)
	def("DELETE /api/s3/{conn}/buckets/{bucket}", private, func(c *Ctx) (any, error) {
		cl, err := c.Client()
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, cl.DeleteBucket(c.P("bucket"))
	})
	def("GET "+B+"/list", private, a.list)
	def("GET "+B+"/search", private, a.search)
	def("GET "+B+"/head", private, func(c *Ctx) (any, error) { return a.Storage.Head(c.P("conn"), c.P("bucket"), c.Arg("key")) })
	def("GET "+B+"/presign", private, a.presign)
	def("POST "+B+"/folder", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		p, err := a.Storage.MakeFolder(c.P("conn"), c.P("bucket"), b.Str("prefix"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"prefix": p}, nil
	})
	def("POST "+B+"/delete", private, a.deleteObjects)
	def("POST "+B+"/transfer", private, a.transfer)
	def("POST "+B+"/properties", private, a.properties)
	def("POST "+B+"/measure", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		t, err := a.Storage.Measure(c.P("conn"), c.P("bucket"), b.Str("prefix"))
		if err != nil {
			return nil, err
		}
		t.Wait(time.Second)
		return map[string]any{"task": t.Snapshot()}, nil
	})
	def("PUT "+B+"/object", private, a.upload)
	def("GET "+B+"/uploads", private, func(c *Ctx) (any, error) {
		u, err := a.Storage.IncompleteUploads(c.P("conn"), c.P("bucket"), c.Arg("prefix"))
		return map[string]any{"uploads": u}, err
	})
	def("POST "+B+"/uploads/abort", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		var items []s3.Upload
		for _, m := range b.Maps("items") {
			k, _ := m["key"].(string)
			id, _ := m["upload_id"].(string)
			items = append(items, s3.Upload{Key: k, UploadID: id})
		}
		n, err := a.Storage.AbortUploads(c.P("conn"), c.P("bucket"), items)
		return map[string]any{"aborted": n}, err
	})
	def("POST "+B+"/play", machine, a.playObjects)
	def("POST "+B+"/library", private, func(c *Ctx) (any, error) {
		b, err := c.Body()
		if err != nil {
			return nil, err
		}
		lib, err := a.Cfg.AddS3Library(c.P("conn"), c.P("bucket"), b.Str("prefix"), b.Str("name"))
		if err != nil {
			return nil, err
		}
		a.StartIndex(lib, media.Options{})
		d, _ := a.Describe(lib)
		return d, nil
	})
	def("GET /s3/{conn}/{bucket}/{key...}", private, a.object)

	// tasks
	def("GET /api/tasks", private, func(c *Ctx) (any, error) { return map[string]any{"tasks": a.Tasks.List()}, nil })
	def("POST /api/tasks/{id}/cancel", private, func(c *Ctx) (any, error) {
		if !a.Tasks.Cancel(c.P("id")) {
			return nil, fail(404, "unknown task")
		}
		return map[string]any{"ok": true}, nil
	})
	def("DELETE /api/tasks", private, func(c *Ctx) (any, error) { a.Tasks.Dismiss(""); return map[string]any{"ok": true}, nil })
	def("DELETE /api/tasks/{id}", private, func(c *Ctx) (any, error) { a.Tasks.Dismiss(c.P("id")); return map[string]any{"ok": true}, nil })

	a.settingsRoutes(def)
	return mux
}

// ---------------------------------------------------------------- UI files

var (
	etagMu sync.Mutex
	etags  = map[string]string{}
)

func serveWeb(c *Ctx, web fs.FS, name string) error {
	if !fs.ValidPath(name) || name == "" {
		return fail(404, "not found")
	}
	f, err := web.Open(name)
	if err != nil {
		return notFound(c)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return notFound(c)
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return fail(500, "cannot serve "+name)
	}
	etagMu.Lock()
	tag, have := etags[name]
	etagMu.Unlock()
	if !have || st.ModTime().After(time.Time{}) {
		h := sha1.New()
		_, _ = io.Copy(h, rs)
		_, _ = rs.Seek(0, io.SeekStart)
		tag = `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
		etagMu.Lock()
		etags[name] = tag
		etagMu.Unlock()
	}
	c.W.Header().Set("ETag", tag)
	c.W.Header().Set("Cache-Control", "no-cache")
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		if !strings.Contains(ct, "charset") && (strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json") || strings.Contains(ct, "svg")) {
			ct += "; charset=utf-8"
		}
		c.W.Header().Set("Content-Type", ct)
	}
	http.ServeContent(c.W, c.R, name, st.ModTime(), rs)
	return nil
}

func notFound(c *Ctx) error {
	writeText(c.W, 404, "not found")
	return nil
}

var thumbName = regexp.MustCompile(`^[0-9a-f]{12}-[0-9a-f]{10}-\d+\.(avif|webp|jpg)$`)
var libID = regexp.MustCompile(`^[a-z0-9-]+$`)

func (a *App) thumb(c *Ctx) (any, error) {
	lib, ok := a.Cfg.Library(c.P("lib"))
	if !ok || !libID.MatchString(c.P("lib")) || !thumbName.MatchString(c.P("name")) {
		return nil, notFound(c)
	}
	// The page asks for .avif. A thumbnail made before AVIF (WebP, JPEG) answers to the same name.
	name := c.P("name")
	stem := strings.TrimSuffix(name, path.Ext(name))
	var f *os.File
	var err error
	for _, ext := range []string{path.Ext(name), ".avif", ".webp", ".jpg"} {
		if f, err = os.Open(path.Join(a.Cfg.LibDir(lib), "thumbs", stem+ext)); err == nil {
			name = stem + ext
			break
		}
	}
	if f == nil || err != nil {
		return nil, notFound(c)
	}
	defer f.Close()
	st, _ := f.Stat()
	c.W.Header().Set("Content-Type", map[string]string{".avif": "image/avif", ".webp": "image/webp", ".jpg": "image/jpeg"}[path.Ext(name)])
	c.W.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(c.W, c.R, c.P("name"), st.ModTime(), f)
	return nil, nil
}

// ---------------------------------------------------------------- libraries

func (a *App) listLibraries(c *Ctx) (any, error) {
	libs := a.Cfg.Libraries()
	out := make([]map[string]any, len(libs))
	for i, l := range libs {
		out[i], _ = a.Describe(l)
	}
	return map[string]any{"libraries": out, "active": a.Cfg.Active()}, nil
}

func (a *App) getLibrary(c *Ctx) (any, error) {
	lib, err := c.Library(c.Arg("lib"))
	if err != nil {
		return nil, err
	}
	d, snap := a.Describe(lib)
	if snap == nil {
		return nil, errors.New("the library index could not be read")
	}
	d["warnings"] = snap.Data.Warnings
	d["version"] = snap.Version
	art := map[string]string{} // folder -> the name of its picture; the page asks /art for it
	for dir, key := range snap.Data.Art {
		art[dir] = path.Base(key)
	}
	d["art"] = art
	// A page that has a version already gets only what changed since: while a scan runs, it asks every few seconds,
	// and a whole big library each time would be megabytes.
	if since := c.Arg("since"); since != "" {
		if changed, removed, ok := snap.Since(since); ok {
			d["changed"], d["removed"] = changed, removed
			delete(d, "items") // the count from Describe: a full answer puts the items there
			return d, nil
		}
	}
	d["items"] = snap.ItemsJSON()
	return d, nil
}

func (a *App) addLibrary(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	var lib config.Library
	if b.Str("type") == "s3" {
		lib, err = a.Cfg.AddS3Library(b.Str("connection"), b.Str("bucket"), b.Str("prefix"), b.Str("name"))
	} else {
		lib, err = a.Cfg.AddLocalLibrary(b.Str("path"), b.Str("name"))
	}
	if err != nil {
		return nil, err
	}
	d, _ := a.Describe(lib)
	return d, nil
}

func (a *App) updateLibrary(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	id := b.Str("id")
	if a.indexing(id) {
		return nil, fail(400, "That library is being indexed; wait for it to finish.")
	}
	lib, moved, err := a.Cfg.UpdateLibrary(id, config.LibraryEdit{Name: b.StrP("name"), Path: b.StrP("path"),
		Connection: b.StrP("connection"), Bucket: b.StrP("bucket"), Prefix: b.StrP("prefix")})
	if err != nil {
		return nil, err
	}
	a.dropStore(id)
	a.clearLinks()
	if moved {
		a.StartIndex(lib, media.Options{}) // its old index describes another place
	}
	d, _ := a.Describe(lib)
	d["moved"] = moved
	return d, nil
}

func (a *App) selectItems(snap *media.Snapshot, c *Ctx) []*media.Item {
	var recs []*media.Item
	if ids := c.Arg("ids"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if r, ok := snap.ByID[id]; ok {
				recs = append(recs, r)
			}
		}
	} else {
		d := strings.Trim(c.Arg("dir"), "/")
		for i := range snap.Data.Items {
			r := &snap.Data.Items[i]
			if d == "" || r.Dir == d || strings.HasPrefix(r.Dir, d+"/") {
				recs = append(recs, r)
			}
		}
	}
	hide := map[string]bool{}
	for _, e := range strings.Split(c.Arg("hide"), ",") {
		if e = strings.ToLower(strings.Trim(strings.TrimSpace(e), ". ")); e != "" {
			hide[e] = true
		}
	}
	if len(hide) > 0 { // the UI's type filter
		kept := recs[:0:0]
		for _, r := range recs {
			if !hide[strings.ToLower(strings.TrimPrefix(path.Ext(r.Name), "."))] {
				kept = append(kept, r)
			}
		}
		recs = kept
	}
	return recs
}

func (a *App) playlist(c *Ctx) (any, error) {
	lib, err := c.Library(c.Arg("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	text := a.Playlist(lib, a.selectItems(snap, c), c.R.Host)
	c.W.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	_, _ = io.WriteString(c.W, text)
	return nil, nil
}

var mediaPath = regexp.MustCompile(`^(?:([a-z0-9-]+)/)?([0-9a-f]{12})(?:/.*)?$`)

func (a *App) media(c *Ctx) (any, error) {
	// /media/<library>/<id>/<name>; the older /media/<id>/<name> is looked up in every library.
	m := mediaPath.FindStringSubmatch(c.P("rest"))
	if m == nil {
		return nil, notFound(c)
	}
	var libs []config.Library
	if m[1] != "" {
		if l, ok := a.Cfg.Library(m[1]); ok {
			libs = append(libs, l)
		}
	} else {
		libs = a.Cfg.Libraries()
	}
	for _, lib := range libs {
		snap, err := a.store(lib).Get()
		if err != nil {
			continue
		}
		rec, ok := snap.ByID[m[2]]
		if !ok {
			continue
		}
		if lib.Type == "local" {
			return nil, serveLocalFile(c, LocalPath(lib, rec))
		}
		target, err := a.Link(lib, rec)
		if err != nil {
			writeText(c.W, 502, err.Error())
			return nil, nil
		}
		c.W.Header().Set("Cache-Control", "no-store")
		http.Redirect(c.W, c.R, target, http.StatusFound)
		return nil, nil
	}
	writeText(c.W, 404, "unknown media id")
	return nil, nil
}

// subtitle serves one of an item's subtitle files: from the folder, or by a link into the bucket. A player on a phone
// or another computer gets it this way.
func (a *App) subtitle(c *Ctx) (any, error) {
	lib, err := c.Library(c.P("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	rec, ok := snap.ByID[c.P("id")]
	if !ok || !slices.Contains(rec.Subs, c.P("name")) {
		writeText(c.W, 404, "no such subtitle file")
		return nil, nil
	}
	key := rec.SubKey(c.P("name"))
	if lib.Type == "local" {
		return nil, serveLocalFile(c, filepath.Join(config.LocalRoot(lib), filepath.FromSlash(key)))
	}
	target, err := media.Presign(a.Cfg, a.Clients, lib, key, 24*time.Hour)
	if err != nil {
		writeText(c.W, 502, err.Error())
		return nil, nil
	}
	c.W.Header().Set("Cache-Control", "no-store")
	http.Redirect(c.W, c.R, target, http.StatusFound)
	return nil, nil
}

// folderArt serves a folder's own picture (its poster.jpg or folder.jpg): from the folder, or by a link into the
// bucket.
func (a *App) folderArt(c *Ctx) (any, error) {
	lib, err := c.Library(c.P("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	key, ok := snap.Data.Art[c.Arg("dir")]
	if !ok {
		writeText(c.W, 404, "this folder has no picture")
		return nil, nil
	}
	if lib.Type == "local" {
		c.W.Header().Set("Cache-Control", "no-cache")
		return nil, serveLocalFile(c, filepath.Join(config.LocalRoot(lib), filepath.FromSlash(key)))
	}
	target, err := media.Presign(a.Cfg, a.Clients, lib, key, 24*time.Hour)
	if err != nil {
		writeText(c.W, 502, err.Error())
		return nil, nil
	}
	c.W.Header().Set("Cache-Control", "private, max-age=3600")
	http.Redirect(c.W, c.R, target, http.StatusFound)
	return nil, nil
}

// serveLocalFile streams a file with Range support, so players can seek.
func serveLocalFile(c *Ctx, p string) error {
	f, err := os.Open(p)
	if err != nil {
		writeText(c.W, 404, "file not reachable")
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeText(c.W, 404, "file not reachable")
		return nil
	}
	name := path.Base(strings.ReplaceAll(p, `\`, "/"))
	inert(c.W.Header(), mime.TypeByExtension(path.Ext(name)))
	http.ServeContent(c.W, c.R, name, st.ModTime(), f)
	return nil
}

// inert keeps a file served from a bucket or a folder from acting as part of medialib: opened in a tab, an HTML or
// SVG file someone put in a bucket would otherwise run its scripts as this site, with the run of its API. It renders
// in a sandbox of its own instead, scripts off; nothing is guessed from the content. Pictures, video and sound in the
// page are unaffected (the policy only applies to a document), and so is a PDF, which the browser's viewer would not
// show in a sandbox (and whose scripts never run as this site anyway).
func inert(h http.Header, contentType string) {
	h.Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(strings.ToLower(contentType), "application/pdf") {
		h.Set("Content-Security-Policy", "sandbox")
	}
}

func (a *App) play(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	lib, err := c.Library(b.Str("lib"))
	if err != nil {
		return nil, err
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	var recs []*media.Item
	for _, id := range b.Strs("ids") {
		if len(recs) == 500 {
			break
		}
		if r, ok := snap.ByID[id]; ok {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return nil, fail(400, "nothing to play")
	}
	player := b.Str("player")
	if player == "" {
		player = a.Cfg.Settings().DefaultPlayer
	}
	name, err := a.Play(lib, player, recs)
	if err != nil {
		return nil, err
	}
	a.recordPlay(lib, recs[0])
	return map[string]any{"ok": true, "player": name, "count": len(recs)}, nil
}

// revealFile shows a file in the system's file manager (a variable so tests need not open one).
var revealFile = players.Reveal

// reveal opens the folder of an item of a local library in Explorer, Finder or the file manager, the file selected.
func (a *App) reveal(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	lib, err := c.Library(b.Str("lib"))
	if err != nil {
		return nil, err
	}
	if lib.Type != "local" {
		return nil, fail(400, "Only the files of a local library are on this computer's disk.")
	}
	snap, err := a.store(lib).Get()
	if err != nil {
		return nil, err
	}
	rec, ok := snap.ByID[b.Str("id")]
	if !ok {
		return nil, fail(404, "unknown media id")
	}
	path := LocalPath(lib, rec)
	if !players.Reachable(path) {
		return nil, fail(404, "File not reachable: "+path)
	}
	if err := revealFile(path); err != nil {
		return nil, fmt.Errorf("no file manager could be started (%w)", err)
	}
	return map[string]any{"ok": true, "path": path}, nil
}

// ---------------------------------------------------------------- connections

func formOf(b Body) config.ConnectionForm {
	return config.ConnectionForm{
		ID: b.Str("id"), Name: b.StrP("name"), Provider: b.StrP("provider"), Endpoint: b.StrP("endpoint"), Region: b.StrP("region"),
		AccessKey: b.StrP("access_key"), SecretKey: b.StrP("secret_key"), SecretKeyEnv: b.StrP("secret_key_env"),
		SessionToken: b.StrP("session_token"), Addressing: b.StrP("addressing"), VerifyTLS: b.BoolP("verify_tls"),
		DefaultBucket: b.StrP("default_bucket"), ClearSecret: b.Bool("clear_secret"),
	}
}

// testDraft tries a connection form before it is saved. A blank secret falls back to the stored one of `id`.
func (a *App) testDraft(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	var existing *config.Connection
	if x, ok := a.Cfg.Connection(b.Str("id")); ok {
		existing = &x
	}
	form := formOf(b)
	form.ID = ""
	rec, err := config.CleanConnection(form, existing)
	if err != nil {
		return nil, err
	}
	client, err := s3.New(config.ToS3(rec))
	if err != nil {
		return nil, config.Errorf("%s", err)
	}
	defer client.Close()
	bucket := b.Str("bucket")
	if bucket == "" {
		bucket = rec.DefaultBucket
	}
	return config.TestConnection(client, bucket), nil
}

func (a *App) pickFolder(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	title := b.Str("title")
	if title == "" || len(title) > 120 {
		title = "Choose a media folder"
	}
	picked, err := pickFolder(title)
	if err != nil {
		return nil, fail(500, "No folder dialog available here; type the path instead.")
	}
	return map[string]any{"path": picked}, nil
}

// ---------------------------------------------------------------- storage

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (a *App) buckets(c *Ctx) (any, error) {
	conn, _ := a.Cfg.Connection(c.P("conn"))
	list, err := a.Storage.Buckets(c.P("conn"))
	if err != nil {
		// A key limited to one bucket cannot list buckets: fall back to the default bucket set on the connection.
		var e *s3.Error
		if errors.As(err, &e) && (e.Code == "AccessDenied" || e.Code == "AllAccessDisabled") && conn.DefaultBucket != "" {
			return map[string]any{"buckets": []s3.Bucket{{Name: conn.DefaultBucket, Created: ""}}, "limited": true}, nil
		}
		return nil, err
	}
	return map[string]any{"buckets": list, "limited": false}, nil
}

func (a *App) createBucket(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(b.Str("name"))
	if !bucketName.MatchString(name) {
		return nil, fail(400, "Bucket names are 3 to 63 characters: lowercase letters, digits, dots and hyphens.")
	}
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	if err := cl.CreateBucket(name); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "name": name}, nil
}

func (a *App) list(c *Ctx) (any, error) {
	limit, err := c.Int("limit", 500)
	if err != nil {
		return nil, err
	}
	return a.Storage.List(c.P("conn"), c.P("bucket"), c.Arg("prefix"), c.Arg("token"), limit, c.ArgOr("delimiter", "/"))
}

func (a *App) search(c *Ctx) (any, error) {
	limit, err := c.Int("limit", 300)
	if err != nil {
		return nil, err
	}
	return a.Storage.Search(c.P("conn"), c.P("bucket"), c.Arg("prefix"), c.Arg("q"), limit)
}

func dispositionOf(key string) string {
	return "attachment; filename*=UTF-8''" + url.PathEscape(key[strings.LastIndex(key, "/")+1:])
}

func (a *App) presign(c *Ctx) (any, error) {
	key := c.Arg("key")
	expires, err := c.Int("expires", 3600)
	if err != nil {
		return nil, err
	}
	var extra map[string]string
	if c.Arg("download") != "" {
		extra = map[string]string{"response-content-disposition": dispositionOf(key)}
	}
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	u, err := cl.Presign("GET", c.P("bucket"), key, expires, extra)
	if err != nil {
		return nil, config.Errorf("%s", err)
	}
	return map[string]any{"url": u, "expires": expires}, nil
}

func (a *App) deleteObjects(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	keys, prefixes := b.Strs("keys"), b.Strs("prefixes")
	if len(keys) == 0 && len(prefixes) == 0 {
		return nil, fail(400, "Nothing selected.")
	}
	t, err := a.Storage.Delete(c.P("conn"), c.P("bucket"), keys, prefixes)
	if err != nil {
		return nil, err
	}
	t.Wait(1500 * time.Millisecond)
	return map[string]any{"task": t.Snapshot()}, nil
}

func (a *App) transfer(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	var items []storage.Item
	for _, m := range b.Maps("items") {
		from, _ := m["from"].(string)
		to, _ := m["to"].(string)
		if from == "" || to == "" {
			return nil, fail(400, "Every item needs a source and a destination.")
		}
		items = append(items, storage.Item{From: from, To: to})
	}
	if len(items) == 0 {
		return nil, fail(400, "Nothing selected.")
	}
	toConn := b.Str("to_conn")
	if toConn != "" {
		if _, ok := a.Cfg.Connection(toConn); !ok {
			return nil, fail(404, "Unknown destination connection.")
		}
	}
	skip := true
	if v := b.BoolP("skip_existing"); v != nil {
		skip = *v
	}
	t, err := a.Storage.Transfer(c.P("conn"), c.P("bucket"), items, toConn, b.Str("to_bucket"), b.Bool("move"), skip)
	if err != nil {
		return nil, err
	}
	t.Wait(1500 * time.Millisecond)
	return map[string]any{"task": t.Snapshot()}, nil
}

func (a *App) properties(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	key := b.Str("key")
	if key == "" {
		return nil, fail(400, "'key'")
	}
	var meta map[string]string
	hasMeta := false
	if m, ok := b["metadata"].(map[string]any); ok {
		hasMeta, meta = true, map[string]string{}
		for k, v := range m {
			meta[k] = fmt.Sprint(v)
		}
	}
	return a.Storage.SetProperties(c.P("conn"), c.P("bucket"), key, b.StrP("content_type"), meta, hasMeta, b.StrP("cache_control"), b.StrP("content_disposition"))
}

func (a *App) upload(c *Ctx) (any, error) {
	key := c.Arg("key")
	length := c.R.ContentLength
	if length < 0 {
		return nil, fail(411, "The upload needs a Content-Length.")
	}
	ctype := strings.TrimSpace(strings.SplitN(c.R.Header.Get("Content-Type"), ";", 2)[0])
	switch ctype {
	case "application/octet-stream", "application/x-www-form-urlencoded":
		ctype = ""
	}
	return a.Storage.Upload(c.P("conn"), c.P("bucket"), key, length, c.R.Body, ctype, c.ArgOr("overwrite", "1") != "0")
}

func (a *App) playObjects(c *Ctx) (any, error) {
	b, err := c.Body()
	if err != nil {
		return nil, err
	}
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	var entries []players.Entry
	for _, k := range b.Strs("keys") {
		if len(entries) == 500 {
			break
		}
		u, err := cl.Presign("GET", c.P("bucket"), k, 24*3600, nil)
		if err != nil {
			return nil, err
		}
		entries = append(entries, players.Entry{Target: u, Name: k[strings.LastIndex(k, "/")+1:]})
	}
	if len(entries) == 0 {
		return nil, fail(400, "nothing to play")
	}
	player := b.Str("player")
	if player == "" {
		player = a.Cfg.Settings().DefaultPlayer
	}
	name, err := a.Launch(player, entries)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "player": name, "count": len(entries)}, nil
}

// object streams an object out of a bucket, with Range support so players and the preview can seek.
func (a *App) object(c *Ctx) (any, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	bucket, key := c.P("bucket"), c.P("key")
	download := c.Arg("download") != ""
	if c.R.Method == http.MethodHead {
		info, err := cl.HeadObject(bucket, key)
		if err != nil {
			return nil, err
		}
		ct := info.ContentType
		if ct == "" {
			ct = storage.GuessType(key)
		}
		c.W.Header().Set("Content-Type", ct)
		c.W.Header().Set("Content-Length", fmt.Sprint(info.Size))
		c.W.Header().Set("X-Object-Size", fmt.Sprint(info.Size))
		c.W.Header().Set("Accept-Ranges", "bytes")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	st, err := cl.GetObjectStream(ctx, bucket, key, c.R.Header.Get("Range"))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	h := st.Headers
	ct := h.Get("Content-Type")
	switch ct {
	case "", "binary/octet-stream", "application/octet-stream":
		ct = storage.GuessType(key)
	}
	out := c.W.Header()
	out.Set("Content-Type", ct)
	inert(out, ct)
	for _, name := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified"} {
		if v := h.Get(name); v != "" {
			out.Set(name, v)
		}
	}
	out.Set("Accept-Ranges", "bytes")
	if download {
		out.Set("Content-Disposition", dispositionOf(key))
	}
	c.W.WriteHeader(st.Status)
	buf := make([]byte, 256<<10)
	for {
		n, rerr := st.Read(buf)
		if n > 0 {
			if _, werr := c.W.Write(buf[:n]); werr != nil {
				return nil, nil // the player seeked or closed
			}
		}
		if rerr != nil {
			return nil, nil
		}
	}
}
