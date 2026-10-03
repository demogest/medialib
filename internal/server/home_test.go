package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
)

func TestHomeShowsWhatWasPlayedAndWhatIsNew(t *testing.T) {
	e := setup(t, false)
	lib, _ := e.cfg.Library("videos")
	recs := map[string]media.Item{}
	var ids []string
	for n := 1; n <= 30; n++ {
		it := media.NewItem(fmt.Sprintf("clip %02d.mp4", n), fmt.Sprintf("clip %02d.mp4", n), "", "video", 1, fmt.Sprintf("2026-01-%02dT00:00:00Z", n))
		if n%2 == 0 {
			it.Frames, it.Indexed = 5, true
		}
		recs[it.ID] = it
		ids = append(ids, it.ID)
	}
	if _, err := media.SaveLibrary(e.cfg, lib, recs, nil); err != nil {
		t.Fatal(err)
	}
	home := decode(t, e.do("GET", "/api/home", ""))
	added := home["added"].([]any)
	if len(added) != homeRows || added[0].(map[string]any)["name"] != "clip 30.mp4" || added[0].(map[string]any)["lib_name"] != "Videos" ||
		added[homeRows-1].(map[string]any)["name"] != "clip 07.mp4" || len(home["played"].([]any)) != 0 {
		t.Fatalf("home: %v", home)
	}

	snap, _ := e.app.store(lib).Get()
	e.app.recordPlay(lib, snap.ByID[ids[2]])
	e.app.recordPlay(lib, snap.ByID[ids[5]])
	e.app.recordPlay(lib, snap.ByID[ids[2]]) // again: moves to the front, once
	played := decode(t, e.do("GET", "/api/home", ""))["played"].([]any)
	if len(played) != 2 || played[0].(map[string]any)["id"] != ids[2] || played[1].(map[string]any)["id"] != ids[5] || played[0].(map[string]any)["played"] == nil {
		t.Errorf("played: %v", played)
	}
	if far := decode(t, e.do("GET", "/api/home", "", remote)); len(far["played"].([]any)) != 0 || len(far["added"].([]any)) != homeRows {
		t.Errorf("another computer sees the history: %v", far["played"])
	}
	// Kept across restarts, and cleared on request.
	again := NewApp(e.cfg, "server")
	if got := again.plays(); len(got) != 2 || got[0].ID != ids[2] {
		t.Errorf("history after a restart: %v", got)
	}
	if rec := e.do("DELETE", "/api/history", ""); rec.Code != 200 || len(e.app.plays()) != 0 {
		t.Errorf("clear: %d %v", rec.Code, e.app.plays())
	}

	// A library's tile shows the covers of its newest videos.
	libs := decode(t, e.do("GET", "/api/libraries", ""))["libraries"].([]any)
	covers := libs[0].(map[string]any)["covers"].([]any)
	if len(covers) != 4 || covers[0].(map[string]any)["id"] != recs[ids[29]].ID || covers[3].(map[string]any)["id"] != recs[ids[23]].ID {
		t.Errorf("covers: %v", covers)
	}
}

func TestFirstRunSuggestsFoldersWithVideos(t *testing.T) {
	home := t.TempDir()
	defer func(f func() []string) { diskRoots = f }(diskRoots)
	diskRoots = func() []string { return nil }
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	mk := func(p string, n int) {
		_ = os.MkdirAll(p, 0o755)
		for i := range n {
			_ = os.WriteFile(filepath.Join(p, fmt.Sprintf("v%d.mkv", i)), nil, 0o644)
		}
		_ = os.WriteFile(filepath.Join(p, "notes.txt"), nil, 0o644)
	}
	mk(filepath.Join(home, "Videos", "Trips"), 3)
	mk(filepath.Join(home, "Downloads"), 1)
	mk(filepath.Join(home, "Desktop"), 0)
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if libs := cfg.Libraries(); len(libs) != 0 {
		t.Fatalf("a fresh install starts with libraries: %v", libs)
	}
	e := &env{app: NewApp(cfg, "server"), cfg: cfg}
	e.app.Loopback = true
	e.h = e.app.Handler(nil)
	got := decode(t, e.do("GET", "/api/suggestions", ""))["folders"].([]any)
	if len(got) != 2 || got[0].(map[string]any)["name"] != "Videos" || got[0].(map[string]any)["videos"] != float64(3) ||
		got[1].(map[string]any)["name"] != "Downloads" {
		t.Fatalf("suggestions: %v", got)
	}
	// A folder that is a library already is not suggested again.
	if _, err := cfg.AddLocalLibrary(filepath.Join(home, "Videos"), ""); err != nil {
		t.Fatal(err)
	}
	got = decode(t, e.do("GET", "/api/suggestions", ""))["folders"].([]any)
	if len(got) != 1 || !strings.HasSuffix(got[0].(map[string]any)["path"].(string), "Downloads") {
		t.Errorf("after adding Videos: %v", got)
	}
	if rec := e.do("GET", "/api/suggestions", "", remote); rec.Code != 403 {
		t.Errorf("from another computer: %d", rec.Code)
	}
}
