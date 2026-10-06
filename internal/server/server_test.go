package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
)

type env struct {
	app  *App
	h    http.Handler
	cfg  *config.Config
	home string
}

func setup(t *testing.T, loopback bool) *env {
	t.Helper()
	home := t.TempDir()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.AddLocalLibrary(t.TempDir(), "Videos"); err != nil { // a library to start from, id "videos"
		t.Fatal(err)
	}
	app := NewApp(cfg, "server")
	app.Loopback = loopback
	web := fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}, "js/main.js": {Data: []byte("export {}")}}
	return &env{app: app, h: app.Handler(web), cfg: cfg, home: home}
}

func (e *env) do(method, path string, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8766"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" && method != "HEAD" {
		req.Header.Set("X-Medialib", "1")
	}
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func remote(r *http.Request) { r.RemoteAddr = "203.0.113.9:4000"; r.Host = "media.example.com" }

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

func TestServesTheUI(t *testing.T) {
	e := setup(t, true)
	if rec := e.do("GET", "/", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui") || rec.Header().Get("ETag") == "" {
		t.Errorf("index: %d %q", rec.Code, rec.Body.String())
	}
	rec := e.do("GET", "/static/js/main.js", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || strings.Count(rec.Header().Get("Content-Type"), "charset") != 1 {
		t.Errorf("js: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	tag := rec.Header().Get("ETag")
	if again := e.do("GET", "/static/js/main.js", "", func(r *http.Request) { r.Header.Set("If-None-Match", tag) }); again.Code != 304 {
		t.Errorf("a revalidation should be 304, got %d", again.Code)
	}
}

func TestStaticPathTraversalIsRefused(t *testing.T) {
	e := setup(t, true)
	_ = os.WriteFile(filepath.Join(e.home, "secret.txt"), []byte("x"), 0o600)
	for _, p := range []string{"/static/..%2Fsecret.txt", "/static/%2e%2e/secret.txt", "/static/js/..%2F..%2Fsecret.txt", "/static/missing.js"} {
		if rec := e.do("GET", p, ""); rec.Code != 404 && rec.Code != 301 {
			t.Errorf("%s -> %d", p, rec.Code)
		}
	}
}

func TestChangesNeedTheCustomHeader(t *testing.T) {
	e := setup(t, true)
	if rec := e.do("POST", "/api/libraries", "{}", func(r *http.Request) { r.Header.Del("X-Medialib") }); rec.Code != 403 {
		t.Errorf("got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/libraries", `{"path": "/definitely/not/here"}`); rec.Code != 400 {
		t.Errorf("a bad folder is a 400, got %d", rec.Code)
	}
}

func TestForeignHostAndCrossSiteAreRefused(t *testing.T) {
	e := setup(t, true)
	if rec := e.do("GET", "/api/libraries", "", func(r *http.Request) { r.Host = "evil.example" }); rec.Code != 403 {
		t.Errorf("DNS rebinding: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/libraries", "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); rec.Code != 403 {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/libraries", "", func(r *http.Request) { r.Host = "localhost:8766" }); rec.Code != 200 {
		t.Errorf("localhost is fine: %d", rec.Code)
	}
}

func TestOtherComputersGetTheLibraryButNotTheStorage(t *testing.T) {
	e := setup(t, false)
	if rec := e.do("GET", "/api/libraries", "", remote); rec.Code != 200 {
		t.Errorf("libraries: %d", rec.Code)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/connections"}, {"GET", "/api/s3/x/buckets"}, {"GET", "/api/tasks"}, {"POST", "/api/libraries"}, {"POST", "/api/play"},
	} {
		if rec := e.do(c.method, c.path, "{}", remote); rec.Code != 403 {
			t.Errorf("%s %s -> %d", c.method, c.path, rec.Code)
		}
	}
	// A reverse proxy on this computer must not make every visitor look local.
	if rec := e.do("GET", "/api/connections", "", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.9") }); rec.Code != 403 {
		t.Errorf("proxied request treated as local: %d", rec.Code)
	}
}

func TestPasswordUnlocksRemoteUse(t *testing.T) {
	t.Setenv("MEDIALIB_PASSWORD", "hunter2")
	e := setup(t, false)
	if rec := e.do("GET", "/api/libraries", "", remote); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no password: %d", rec.Code)
	}
	bad := func(r *http.Request) { remote(r); r.SetBasicAuth("me", "wrong") }
	if rec := e.do("GET", "/api/libraries", "", bad); rec.Code != 401 {
		t.Errorf("wrong password: %d", rec.Code)
	}
	good := func(r *http.Request) { remote(r); r.SetBasicAuth("anyone", "hunter2") }
	if rec := e.do("GET", "/api/connections", "", good); rec.Code != 200 {
		t.Errorf("signed in: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/play", "{}", good); rec.Code != 403 {
		t.Errorf("playing happens on the server's own screen: %d", rec.Code)
	}
}

func TestConnectionsNeverExposeTheirSecret(t *testing.T) {
	e := setup(t, true)
	rec := e.do("POST", "/api/connections", `{"name":"Mock","provider":"minio","endpoint":"http://127.0.0.1:9","access_key":"AK","secret_key":"TOPSECRET"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if list := e.do("GET", "/api/connections", ""); strings.Contains(list.Body.String(), "TOPSECRET") || !strings.Contains(list.Body.String(), `"has_secret":true`) {
		t.Errorf("%s", list.Body.String())
	}
	raw, _ := os.ReadFile(filepath.Join(e.home, "config.json"))
	if !strings.Contains(string(raw), "TOPSECRET") {
		t.Error("the secret should be kept in config.json")
	}
	if rec := e.do("POST", "/api/connections", `{"provider":"other","endpoint":"","access_key":"x","secret_key":"y"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "endpoint") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/connections/test", `{"provider":"other","endpoint":"http://127.0.0.1:1","access_key":"a","secret_key":"b"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Cannot reach") {
		t.Errorf("an unreachable store is reported, not a crash: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLocalLibraryServesItsMediaWithRanges(t *testing.T) {
	e := setup(t, true)
	folder := t.TempDir()
	content := []byte("0123456789abcdefghij")
	if err := os.WriteFile(filepath.Join(folder, "clip one.mp4"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	add := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(folder)+`, "name": "Clips"}`))
	id, _ := add["id"].(string)
	if id != "clips" || add["reachable"] != true {
		t.Fatalf("%v", add)
	}
	lib, _ := e.cfg.Library(id)
	it := media.NewItem("clip one.mp4", "clip one.mp4", "", "video", int64(len(content)), "2026-01-01T00:00:00Z")
	it.Indexed, it.Duration = true, 12.5
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{it.ID: it}, nil); err != nil {
		t.Fatal(err)
	}
	got := decode(t, e.do("GET", "/api/library?lib="+id, ""))
	if items, _ := got["items"].([]any); len(items) != 1 || got["warnings"] == nil {
		t.Errorf("%v", got)
	}
	rec := e.do("GET", "/media/"+id+"/"+it.ID+"/clip%20one.mp4", "", func(r *http.Request) { r.Header.Set("Range", "bytes=5-9") })
	if rec.Code != 206 || rec.Body.String() != "56789" || !strings.HasPrefix(rec.Header().Get("Content-Range"), "bytes 5-9/20") {
		t.Errorf("range: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := e.do("GET", "/media/"+it.ID+"/x.mp4", ""); rec.Code != 200 || rec.Body.Len() != len(content) {
		t.Errorf("the older URL form still works: %d", rec.Code)
	}
	if rec := e.do("GET", "/media/"+id+"/ffffffffffff/x.mp4", ""); rec.Code != 404 {
		t.Errorf("unknown id: %d", rec.Code)
	}
	pl, _ := io.ReadAll(e.do("GET", "/api/playlist.m3u8?lib="+id+"&hide=mkv", "").Body)
	if !strings.HasPrefix(string(pl), "#EXTM3U\n#EXTINF:12,clip one.mp4\nhttp://127.0.0.1:8766/media/clips/"+it.ID+"/clip%20one.mp4") {
		t.Errorf("playlist:\n%s", pl)
	}
	if rec := e.do("GET", "/api/playlist.m3u8?lib="+id+"&hide=mp4", ""); strings.Contains(rec.Body.String(), "clip one") {
		t.Errorf("the type filter should hide mp4:\n%s", rec.Body.String())
	}
	if rec := e.do("POST", "/api/libraries/remove", `{"id":"videos"}`); rec.Code != 200 {
		t.Errorf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/libraries/remove", `{"id":"clips"}`); rec.Code != 200 {
		t.Errorf("the last library can go too: %d", rec.Code)
	}
	if got := decode(t, e.do("GET", "/api/libraries", "")); len(got["libraries"].([]any)) != 0 || got["active"] != "" {
		t.Errorf("no libraries: %v", got)
	}
}

func TestThumbnailNamesAreValidated(t *testing.T) {
	e := setup(t, true)
	lib := e.cfg.Libraries()[0]
	dir := filepath.Join(e.cfg.LibDir(lib), "thumbs")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "0123456789ab-0123456789-0.jpg"), []byte{0xFF, 0xD8}, 0o644)
	_ = os.WriteFile(filepath.Join(e.home, "x.jpg"), []byte("x"), 0o644)
	if rec := e.do("GET", "/thumbs/videos/0123456789ab-0123456789-0.jpg", ""); rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("%d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/thumbs/videos/..%2F..%2Fx.jpg", "/thumbs/..%2F/x.jpg", "/thumbs/videos/notathumb.jpg", "/thumbs/nolib/0123456789ab-0123456789-0.jpg"} {
		if rec := e.do("GET", p, ""); rec.Code != 404 {
			t.Errorf("%s -> %d", p, rec.Code)
		}
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestThumbnailFormats(t *testing.T) {
	e := setup(t, true)
	lib := e.cfg.Libraries()[0]
	dir := filepath.Join(e.cfg.LibDir(lib), "thumbs")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "0123456789ab-0123456789-0.jpg"), []byte{0xFF, 0xD8}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "0123456789ab-0123456789-1.webp"), []byte("RIFFxxxxWEBP"), 0o644)
	// the page asks for .avif; whatever older format exists under that name answers it
	if rec := e.do("GET", "/thumbs/"+lib.ID+"/0123456789ab-0123456789-0.avif", ""); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("jpg behind a .webp URL: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := e.do("GET", "/thumbs/"+lib.ID+"/0123456789ab-0123456789-1.avif", ""); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/webp" {
		t.Errorf("webp: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSearchFindsMediaByTextPinyinAndTypos(t *testing.T) {
	e := setup(t, true)
	folder := t.TempDir()
	add := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(folder)+`, "name": "Clips"}`))
	lib, _ := e.cfg.Library(add["id"].(string))
	recs := map[string]media.Item{}
	for _, f := range [][2]string{{"3D区DOA动画整合计划.mp4", "3D"}, {"holiday photos.mp4", "travel/iceland"}, {"天平キツネ.mkv", "天平キツネ"}} {
		key := f[1] + "/" + f[0]
		it := media.NewItem(key, f[0], f[1], "video", 10, "2026-01-01T00:00:00Z")
		it.Indexed, it.Height, it.Codec = true, 1080, "avc1"
		recs[it.ID] = it
	}
	if _, err := media.SaveLibrary(e.cfg, lib, recs, nil); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		"动画": "3D区DOA动画整合计划.mp4", "donghua": "3D区DOA动画整合计划.mp4", "dhzh": "3D区DOA动画整合计划.mp4",
		"きつね": "天平キツネ.mkv", "holidya": "holiday photos.mp4", "iceland": "holiday photos.mp4",
	} {
		got := decode(t, e.do("GET", "/api/search?q="+url.QueryEscape(q), ""))
		res, _ := got["results"].([]any)
		if len(res) == 0 || res[0].(map[string]any)["name"] != want {
			t.Errorf("%q: %v", q, got)
		}
	}
	got := decode(t, e.do("GET", "/api/search?lib="+lib.ID+"&ids=1&limit=0&q=1080p", ""))
	if ids, _ := got["ids"].([]any); len(ids) != 3 {
		t.Errorf("by tag: %v", got)
	}
	if got := decode(t, e.do("GET", "/api/search?q=", "")); got["total"] != float64(0) {
		t.Errorf("empty query: %v", got)
	}
	// filters on their own list what passes them, unranked; with words they narrow the matches
	if got := decode(t, e.do("GET", "/api/search?lib="+lib.ID+"&ids=1&limit=0&q="+url.QueryEscape("res>=1080 size<1k"), "")); got["total"] != float64(3) || got["ranked"] != false {
		t.Errorf("filters only: %v", got)
	}
	if got := decode(t, e.do("GET", "/api/search?q="+url.QueryEscape("holiday res>=1080"), "")); got["total"] != float64(1) || got["ranked"] != true {
		t.Errorf("words and filters: %v", got)
	}
	if got := decode(t, e.do("GET", "/api/search?q="+url.QueryEscape("res>=4k"), "")); got["total"] != float64(0) {
		t.Errorf("4k: %v", got)
	}
}

func TestAutoIndexKeepsReachableLibrariesUpToDate(t *testing.T) {
	e := setup(t, true)
	here, gone := t.TempDir(), t.TempDir()
	add := func(path, name string) string {
		d := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(path)+`, "name": `+quote(name)+`}`))
		id, _ := d["id"].(string)
		if id == "" {
			t.Fatalf("%v", d)
		}
		return id
	}
	a, b := add(here, "Here"), add(gone, "Gone")
	if err := e.app.RemoveLibrary("videos"); err != nil { // only the two libraries of this test
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil { // an unplugged disk
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.app.AutoIndex(ctx, 30*time.Millisecond)

	finished := func(after float64) float64 {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if j, ok := e.app.Jobs()[a]; ok && j.State == "done" && j.Started > after {
				return j.Started
			}
		}
		t.Fatalf("no indexing pass finished: %v", e.app.Jobs())
		return 0
	}
	first := finished(0)
	if lib := decode(t, e.do("GET", "/api/library?lib="+a, "")); lib["updated"] == nil {
		t.Errorf("the pass wrote no index: %v", lib)
	}
	finished(first) // and passes keep coming
	if j, ok := e.app.Jobs()[b]; ok {
		t.Errorf("an unreachable library was indexed (and would show as failed): %v", j)
	}
	if info := decode(t, e.do("GET", "/api/system", "")); info["auto_index"] != float64(0) {
		t.Errorf("auto_index %v", info["auto_index"])
	}
}

func TestRevealShowsALocalFileOnThisComputerOnly(t *testing.T) {
	e := setup(t, false)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(folder)+`, "name": "Clips"}`))
	lib, _ := e.cfg.Library(add["id"].(string))
	it := media.NewItem("clip.mp4", "clip.mp4", "", "video", 1, "2026-01-01T00:00:00Z")
	gone := media.NewItem("gone.mp4", "gone.mp4", "", "video", 1, "2026-01-01T00:00:00Z")
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{it.ID: it, gone.ID: gone}, nil); err != nil {
		t.Fatal(err)
	}
	var shown []string
	defer func(old func(string) error) { revealFile = old }(revealFile)
	revealFile = func(p string) error { shown = append(shown, p); return nil }

	body := func(id string) string { return `{"lib": ` + quote(lib.ID) + `, "id": ` + quote(id) + `}` }
	if rec := e.do("POST", "/api/reveal", body(it.ID)); rec.Code != 200 || len(shown) != 1 || shown[0] != filepath.Join(folder, "clip.mp4") {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), shown)
	}
	for name, c := range map[string]struct {
		body   string
		mod    []func(*http.Request)
		status int
	}{
		"another computer": {body(it.ID), []func(*http.Request){remote}, 403},
		"missing file":     {body(gone.ID), nil, 404},
		"unknown id":       {body("000000000000"), nil, 404},
	} {
		if rec := e.do("POST", "/api/reveal", c.body, c.mod...); rec.Code != c.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(shown) != 1 {
		t.Errorf("revealed %v", shown)
	}
}

func TestLibraryAnswersWithWhatChanged(t *testing.T) {
	e := setup(t, false)
	lib, _ := e.cfg.Library("videos")
	mk := func(name string) media.Item {
		return media.NewItem(name, name, "", "video", 1, "2026-01-01T00:00:00Z")
	}
	a, b, c := mk("a.mp4"), mk("b.mp4"), mk("c.mp4")
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{a.ID: a, b.ID: b}, nil); err != nil {
		t.Fatal(err)
	}
	first := decode(t, e.do("GET", "/api/library?lib=videos", ""))
	v1, _ := first["version"].(string)
	if v1 == "" || len(first["items"].([]any)) != 2 {
		t.Fatalf("full answer: %v", first)
	}
	// a gets its covers, b goes, c arrives
	a.Frames, a.Indexed = 5, true
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{a.ID: a, c.ID: c}, nil); err != nil {
		t.Fatal(err)
	}
	delta := decode(t, e.do("GET", "/api/library?lib=videos&since="+url.QueryEscape(v1), ""))
	changed, removed := delta["changed"].([]any), delta["removed"].([]any)
	names := map[any]bool{}
	for _, x := range changed {
		names[x.(map[string]any)["name"]] = true
	}
	if delta["items"] != nil || len(changed) != 2 || !names["a.mp4"] || !names["c.mp4"] || len(removed) != 1 || removed[0] != b.ID || delta["version"] == v1 {
		t.Fatalf("delta: %v", delta)
	}
	// Nothing new since then: an empty answer, not the library.
	again := decode(t, e.do("GET", "/api/library?lib=videos&since="+url.QueryEscape(delta["version"].(string)), ""))
	if len(again["changed"].([]any)) != 0 || len(again["removed"].([]any)) != 0 {
		t.Errorf("no change: %v", again)
	}
	// A version this server never gave out (it restarted): everything.
	if full := decode(t, e.do("GET", "/api/library?lib=videos&since=old.3", "")); len(full["items"].([]any)) != 2 || full["changed"] != nil {
		t.Errorf("unknown version: %v", full)
	}
}

func TestSubtitlesAreServedAndFetchedForThePlayer(t *testing.T) {
	e := setup(t, false)
	lib, _ := e.cfg.Library("videos")
	root := config.LocalRoot(lib)
	if err := os.WriteFile(filepath.Join(root, "Film.en.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := media.NewItem("Film.mkv", "Film.mkv", "", "video", 1, "2026-01-01T00:00:00Z")
	it.Subs = []string{"Film.en.srt"}
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{it.ID: it}, nil); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/subs/videos/"+it.ID+"/Film.en.srt", "", remote) // a phone on the network may load it
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Hi") || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("local subtitle: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := e.do("GET", "/subs/videos/"+it.ID+"/..%2Fsecret.srt", ""); rec.Code != 404 {
		t.Errorf("a name that is not one of its subtitles: %d", rec.Code)
	}

	// A bucket library: the player gets a copy on this computer, since it would not look next to a URL.
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media/films/Film.zh.srt" {
			_, _ = io.WriteString(w, "zh subtitles")
			return
		}
		http.NotFound(w, r)
	}))
	defer bucket.Close()
	conn, err := e.cfg.AddConnection(config.ConnectionForm{Name: strPtr("Store"), Provider: strPtr("minio"), Endpoint: strPtr(bucket.URL),
		AccessKey: strPtr("AKIAEXAMPLE"), SecretKey: strPtr("secret")})
	if err != nil {
		t.Fatal(err)
	}
	s3lib, err := e.cfg.AddS3Library(conn.ID, "media", "films/", "Films")
	if err != nil {
		t.Fatal(err)
	}
	obj := media.NewItem("films/Film.mkv", "Film.mkv", "", "video", 1, "2026-01-01T00:00:00Z")
	obj.Subs = []string{"Film.zh.srt", "Film.gone.srt"}
	paths := e.app.fetchSubs(s3lib, &obj)
	if len(paths) != 1 || filepath.Base(paths[0]) != "Film.zh.srt" {
		t.Fatalf("fetched: %v", paths)
	}
	if b, _ := os.ReadFile(paths[0]); string(b) != "zh subtitles" {
		t.Errorf("content: %q", b)
	}
}
