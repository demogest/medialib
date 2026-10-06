package media

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/demogest/medialib/internal/config"
)

func TestSubtitlesNamedAfterAVideoGoWithIt(t *testing.T) {
	items := []Item{NewItem("Shows/Film.mkv", "Film.mkv", "Shows", "video", 1, "x"), NewItem("Shows/Other.mp4", "Other.mp4", "Shows", "video", 1, "x"),
		NewItem("Film.mkv", "Film.mkv", "", "video", 1, "x")}
	attachSubs(items, []string{"Shows/film.EN.srt", "Shows/Film.ass", "Shows/Filmography.srt", "Shows/Other.zh-Hans.vtt", "Elsewhere/Film.srt"})
	if want := []string{"Film.ass", "film.EN.srt"}; !reflect.DeepEqual(items[0].Subs, want) {
		t.Errorf("Film: %v, want %v", items[0].Subs, want)
	}
	if want := []string{"Other.zh-Hans.vtt"}; !reflect.DeepEqual(items[1].Subs, want) {
		t.Errorf("Other: %v", items[1].Subs)
	}
	if items[2].Subs != nil {
		t.Errorf("another folder's subtitles: %v", items[2].Subs)
	}
	if k := items[0].SubKey("Film.ass"); k != "Shows/Film.ass" {
		t.Errorf("key %q", k)
	}
}

func TestScanNotesSubtitlesAndTheirChanges(t *testing.T) {
	dir := t.TempDir()
	broken(t, filepath.Join(dir, "Film.mkv"), testTime)
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := cfg.AddLocalLibrary(dir, "Test")
	if err != nil {
		t.Fatal(err)
	}
	ix := &Indexer{Cfg: cfg, Clients: config.NewClients(cfg)}
	scan := func() Item {
		t.Helper()
		if err := ix.Run(context.Background(), lib, Options{}, quiet{}); err != nil {
			t.Fatal(err)
		}
		d, _ := LoadLibrary(cfg, lib)
		return d.Items[0]
	}
	if it := scan(); it.Subs != nil {
		t.Fatalf("no subtitles yet: %v", it.Subs)
	}
	// Added later, next to a video that did not change (and is not scanned again): it is noted all the same.
	if err := os.WriteFile(filepath.Join(dir, "Film.en.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if it := scan(); !reflect.DeepEqual(it.Subs, []string{"Film.en.srt"}) {
		t.Fatalf("after adding one: %v", it.Subs)
	}
	_ = os.Remove(filepath.Join(dir, "Film.en.srt"))
	if it := scan(); it.Subs != nil {
		t.Errorf("after removing it: %v", it.Subs)
	}
}
