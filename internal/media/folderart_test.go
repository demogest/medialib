package media

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/demogest/medialib/internal/config"
)

func TestFolderArtNames(t *testing.T) {
	for name, want := range map[string]int{"poster.jpg": 1, "Folder.JPG": 2, "cover.webp": 3, "fanart.png": 4, "poster.gif": 0, "my poster.jpg": 0, "poster": 0, "Film.jpg": 0} {
		if got := artRank(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	var s artSet
	s.add("A", "cover.jpg", "A/cover.jpg")
	s.add("A", "poster.png", "A/poster.png")
	s.add("A", "folder.jpg", "A/folder.jpg")
	s.add("", "Folder.jpg", "Folder.jpg")
	s.add("", "folder.jpg", "folder.jpg") // a tie keeps the same one whichever comes first
	s.add("B", "notes.jpg", "B/notes.jpg")
	if want := map[string]string{"A": "A/poster.png", "": "Folder.jpg"}; !reflect.DeepEqual(s.FolderArt(), want) {
		t.Errorf("%v, want %v", s.FolderArt(), want)
	}
}

func TestScanNotesFolderPictures(t *testing.T) {
	dir := t.TempDir()
	broken(t, filepath.Join(dir, "Show", "Episode 1.mkv"), testTime)
	if err := os.WriteFile(filepath.Join(dir, "Show", "poster.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := cfg.AddLocalLibrary(dir, "Test")
	if err != nil {
		t.Fatal(err)
	}
	ix := &Indexer{Cfg: cfg, Clients: config.NewClients(cfg)}
	if err := ix.Run(context.Background(), lib, Options{}, quiet{}); err != nil {
		t.Fatal(err)
	}
	d, err := LoadLibrary(cfg, lib)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"Show": "Show/poster.jpg"}; !reflect.DeepEqual(d.Art, want) {
		t.Errorf("art %v, want %v", d.Art, want)
	}
}
