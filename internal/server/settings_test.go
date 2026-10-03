package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/update"
)

func TestSettingsAreChangedFromTheUI(t *testing.T) {
	t.Setenv("MEDIALIB_AUTO_INDEX", "")
	e := setup(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.app.RunAutoIndex(ctx)
	running := func() bool { e.app.autoMu.Lock(); defer e.app.autoMu.Unlock(); return e.app.autoStop != nil }
	if running() {
		t.Fatal("automatic indexing runs while it is off")
	}

	info := decode(t, e.do("PUT", "/api/settings", `{"auto_index": 60, "workers": 4, "thumb_quality": 85, "updates": "auto"}`))
	if info["auto_index"] != float64(60) || info["workers"] != float64(4) || info["thumb_quality"] != float64(85) || info["updates"] != "auto" {
		t.Errorf("not saved: %v", info)
	}
	if !running() {
		t.Error("turning automatic indexing on did not start it")
	}
	reloaded, err := config.Load(e.home)
	if err != nil || reloaded.Settings().AutoIndex != 60 || reloaded.Settings().Updates != "auto" {
		t.Errorf("not in config.json: %+v %v", reloaded.Settings(), err)
	}
	decode(t, e.do("PUT", "/api/settings", `{"auto_index": 0}`))
	if running() {
		t.Error("turning automatic indexing off did not stop it")
	}

	for body, want := range map[string]string{
		`{"workers": 100}`:                  "64",
		`{"auto_index": -5}`:                "once a week",
		`{"updates": "sometimes"}`:          "sometimes",
		`{"ffmpeg": "/no/such/dir/ffmpeg"}`: "No program found",
	} {
		if rec := e.do("PUT", "/api/settings", body); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	prog := fakeProgram(t, "my-ffmpeg")
	if info := decode(t, e.do("PUT", "/api/settings", `{"ffmpeg": `+quote(prog)+`}`)); info["ffmpeg"] != prog {
		t.Errorf("ffmpeg of one's own: %v", info["ffmpeg"])
	}
	if info := decode(t, e.do("PUT", "/api/settings", `{"ffmpeg": ""}`)); info["tools"].(map[string]any)["ffmpeg"] != "ffmpeg" {
		t.Errorf("back to the default: %v", info["tools"])
	}

	// Another computer may read, not change; MEDIALIB_AUTO_INDEX locks the interval.
	if rec := e.do("PUT", "/api/settings", `{"workers": 2}`, remote); rec.Code != 403 {
		t.Errorf("remote change: %d", rec.Code)
	}
	if info := decode(t, e.do("GET", "/api/system", "", remote)); info["can_edit"] != false || info["on_machine"] != false {
		t.Errorf("remote rights: %v %v", info["can_edit"], info["on_machine"])
	}
	if info := decode(t, e.do("GET", "/api/system", "")); info["can_edit"] != true || info["on_machine"] != true {
		t.Errorf("local rights: %v %v", info["can_edit"], info["on_machine"])
	}
	t.Setenv("MEDIALIB_AUTO_INDEX", "5")
	if rec := e.do("PUT", "/api/settings", `{"auto_index": 60}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "MEDIALIB_AUTO_INDEX") {
		t.Errorf("env lock: %d %s", rec.Code, rec.Body.String())
	}
	if info := decode(t, e.do("GET", "/api/system", "")); info["auto_index"] != float64(5) || info["auto_index_env"] != true {
		t.Errorf("env value: %v %v", info["auto_index"], info["auto_index_env"])
	}
}

// fakeProgram is an executable file in a folder of its own.
func fakeProgram(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlayersAreEditedFromTheUI(t *testing.T) {
	e := setup(t, false)
	prog := fakeProgram(t, "myplayer")
	list := decode(t, e.do("POST", "/api/players", `{"name": "My Player", "path": `+quote(prog)+`, "args": "--fs  --volume=50"}`))
	find := func(m map[string]any, key, id string) map[string]any {
		for _, p := range m[key].([]any) {
			if p := p.(map[string]any); p["id"] == id {
				return p
			}
		}
		return nil
	}
	if p := find(list, "all", "my-player"); p == nil || p["custom"] != true || p["path"] != prog || find(list, "players", "my-player") == nil {
		t.Fatalf("added: %v", list)
	}
	if s := e.cfg.Settings(); len(s.Players) != 1 || strings.Join(s.Players[0].Args, " ") != "--fs --volume=50" {
		t.Errorf("saved: %+v", s.Players)
	}
	decode(t, e.do("PUT", "/api/settings", `{"default_player": "my-player"}`))
	if got := decode(t, e.do("GET", "/api/players", "")); got["default"] != "my-player" {
		t.Errorf("default: %v", got["default"])
	}

	if rec := e.do("POST", "/api/players", `{"path": "/no/such/player"}`); rec.Code != 400 {
		t.Errorf("missing program: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/players", `{"path": `+quote(prog)+`}`, remote); rec.Code != 403 {
		t.Errorf("from another computer: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/api/players/system", ""); rec.Code != 400 {
		t.Errorf("the system player was removed: %d", rec.Code)
	}

	list = decode(t, e.do("DELETE", "/api/players/my-player", ""))
	if find(list, "all", "my-player") != nil || e.cfg.Settings().DefaultPlayer != "" {
		t.Errorf("removed: %v / default %q", list, e.cfg.Settings().DefaultPlayer)
	}
	// A player found on this computer is hidden rather than forgotten, and comes back on request.
	decode(t, e.do("DELETE", "/api/players/vlc", ""))
	if h := e.cfg.Settings().HiddenPlayers; len(h) != 1 || h[0] != "vlc" {
		t.Errorf("hidden: %v", h)
	}
	decode(t, e.do("POST", "/api/players/restore", ""))
	if h := e.cfg.Settings().HiddenPlayers; len(h) != 0 {
		t.Errorf("restored: %v", h)
	}
}

func TestIndexAndCoversMoveToAnotherFolder(t *testing.T) {
	for _, across := range []bool{false, true} {
		e := setup(t, true)
		if across { // another disk: rename cannot, so every file is copied
			defer func(old func(string, string) error) { renameDir = old }(renameDir)
			renameDir = func(string, string) error { return errors.New("cross-device link") }
		}
		folder := t.TempDir()
		add := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(folder)+`, "name": "Clips"}`))
		lib, _ := e.cfg.Library(add["id"].(string))
		it := media.NewItem("clip.mp4", "clip.mp4", "", "video", 1, "2026-01-01T00:00:00Z")
		if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{it.ID: it}, nil); err != nil {
			t.Fatal(err)
		}
		thumb := filepath.Join(e.cfg.LibDir(lib), "thumbs", it.ID+"-"+it.Ver+"-0.webp")
		if err := os.MkdirAll(filepath.Dir(thumb), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(thumb, []byte("img"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := e.cfg.CacheDir()

		empty, full := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(full, "notes.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if plan := decode(t, e.do("POST", "/api/settings/cache-dir", `{"path": `+quote(empty)+`, "check": true}`)); plan["to"] != empty || plan["files"].(float64) < 2 {
			t.Errorf("plan: %v", plan)
		}
		if plan := decode(t, e.do("POST", "/api/settings/cache-dir", `{"path": `+quote(full)+`, "check": true}`)); plan["to"] != filepath.Join(full, "medialib") {
			t.Errorf("a folder with files in it gets a medialib folder inside: %v", plan)
		}
		for _, bad := range []string{filepath.Join(old, "sub"), "relative/path", old} {
			if rec := e.do("POST", "/api/settings/cache-dir", `{"path": `+quote(bad)+`, "check": true}`); rec.Code != 400 {
				t.Errorf("%s: %d %s", bad, rec.Code, rec.Body.String())
			}
		}
		if rec := e.do("POST", "/api/settings/cache-dir", `{"path": `+quote(empty)+`}`, remote); rec.Code != 403 {
			t.Errorf("from another computer: %d", rec.Code)
		}

		start := decode(t, e.do("POST", "/api/settings/cache-dir", `{"path": `+quote(empty)+`}`))
		task := start["task"].(map[string]any)
		waitTask(t, e, task["id"].(string))
		if got := e.cfg.CacheDir(); got != empty {
			t.Fatalf("across %v: cache dir %s", across, got)
		}
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("across %v: the old folder stays: %v", across, err)
		}
		if b, err := os.ReadFile(filepath.Join(empty, lib.ID, "thumbs", filepath.Base(thumb))); err != nil || string(b) != "img" {
			t.Errorf("across %v: thumbnail %q %v", across, b, err)
		}
		if got := decode(t, e.do("GET", "/api/library?lib="+lib.ID, "")); len(got["items"].([]any)) != 1 {
			t.Errorf("across %v: the index is not read from its new place: %v", across, got)
		}
		if rec := e.do("GET", "/thumbs/"+lib.ID+"/"+filepath.Base(thumb), ""); rec.Code != 200 || rec.Body.String() != "img" {
			t.Errorf("across %v: thumbnail served: %d", across, rec.Code)
		}

		back := decode(t, e.do("POST", "/api/settings/cache-dir", `{"default": true}`))
		waitTask(t, e, back["task"].(map[string]any)["id"].(string))
		if e.cfg.CacheDir() != e.cfg.DefaultCacheDir() {
			t.Errorf("across %v: not back: %s", across, e.cfg.CacheDir())
		}
		if raw, _ := os.ReadFile(e.cfg.Path()); strings.Contains(string(raw), "cache_dir") {
			t.Errorf("across %v: the default is written out: %s", across, raw)
		}
	}
}

func waitTask(t *testing.T, e *env, id string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, s := range e.app.Tasks.List() {
			if s.ID == id && s.State != "running" {
				if s.State != "done" {
					t.Fatalf("task %s: %s %v", id, s.State, s.Errors)
				}
				return
			}
		}
	}
	t.Fatalf("task %s did not finish", id)
}

func TestNoIndexingWhileTheIndexMoves(t *testing.T) {
	e := setup(t, true)
	lib, _ := e.cfg.Library("")
	e.app.mu.Lock()
	e.app.moving = true
	e.app.mu.Unlock()
	j := e.app.StartIndex(lib, false)
	select {
	case <-j.Done():
	case <-time.After(time.Second):
		t.Fatal("the refused run never finished")
	}
	if st := j.Snapshot(); st.State != "error" || !strings.Contains(st.Line, "moved") {
		t.Errorf("%+v", st)
	}
}

func TestCopiedLinksPointAtTheMediaItself(t *testing.T) {
	e := setup(t, false)
	folder := t.TempDir()
	add := decode(t, e.do("POST", "/api/libraries", `{"path": `+quote(folder)+`, "name": "Clips"}`))
	lib, _ := e.cfg.Library(add["id"].(string))
	it := media.NewItem("a b.mp4", "a b.mp4", "", "video", 1, "2026-01-01T00:00:00Z")
	if _, err := media.SaveLibrary(e.cfg, lib, map[string]media.Item{it.ID: it}, nil); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, e.do("GET", "/api/link?lib="+lib.ID+"&id="+it.ID, "")); got["url"] != filepath.Join(folder, "a b.mp4") || got["kind"] != "path" {
		t.Errorf("local: %v", got)
	}

	conn, err := e.cfg.AddConnection(config.ConnectionForm{Name: strPtr("Store"), Provider: strPtr("minio"), Endpoint: strPtr("http://127.0.0.1:9000"),
		AccessKey: strPtr("AKIAEXAMPLE"), SecretKey: strPtr("secret")})
	if err != nil {
		t.Fatal(err)
	}
	s3lib, err := e.cfg.AddS3Library(conn.ID, "media", "films/", "Films")
	if err != nil {
		t.Fatal(err)
	}
	obj := media.NewItem("films/x.mkv", "x.mkv", "", "video", 1, "2026-01-01T00:00:00Z")
	if _, err := media.SaveLibrary(e.cfg, s3lib, map[string]media.Item{obj.ID: obj}, nil); err != nil {
		t.Fatal(err)
	}
	got := decode(t, e.do("GET", "/api/link?lib="+s3lib.ID+"&id="+obj.ID, "", remote)) // anyone who may stream it
	u, _ := got["url"].(string)
	if !strings.HasPrefix(u, "http://127.0.0.1:9000/media/films/x.mkv?") || !strings.Contains(u, "X-Amz-Expires=604800") || got["expires"] == nil {
		t.Errorf("bucket: %v", got)
	}
	if rec := e.do("GET", "/api/link?lib="+s3lib.ID+"&id=000000000000", ""); rec.Code != 404 {
		t.Errorf("unknown id: %d", rec.Code)
	}
}

func strPtr(s string) *string { return &s }

func TestUpdatesAreCheckedAndOnlyInstalledHere(t *testing.T) {
	e := setup(t, false)
	if rec := e.do("GET", "/api/update", ""); rec.Code != 404 {
		t.Errorf("no updater: %d", rec.Code)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v99.0.0", "html_url": "https://example.com", "assets": []any{}})
	}))
	defer gh.Close()
	e.app.Updates = &update.Updater{Current: "3.2.0", Install: true, Exe: fakeProgram(t, "medialib"), API: gh.URL}
	if st := decode(t, e.do("POST", "/api/update", `{"action": "check"}`)); st["available"] != true || st["latest"] != "99.0.0" || st["can_install"] != false {
		t.Errorf("check: %v", st)
	}
	if st := decode(t, e.do("GET", "/api/update", "")); st["latest"] != "99.0.0" {
		t.Errorf("status: %v", st)
	}
	if rec := e.do("POST", "/api/update", `{"action": "install"}`); rec.Code != 400 {
		t.Errorf("nothing to install: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ method, body string }{{"GET", ""}, {"POST", `{"action": "check"}`}} {
		if rec := e.do(c.method, "/api/update", c.body, remote); rec.Code != 403 {
			t.Errorf("%s from another computer: %d", c.method, rec.Code)
		}
	}
}
