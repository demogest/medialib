package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/demogest/medialib/internal/config"
)

// planned records how many files each run set out to do.
type planned struct {
	quiet
	totals *[]int
}

func (p planned) Plan(total int, _ string) { *p.totals = append(*p.totals, total) }

var testTime = time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

// broken writes a file that no ffmpeg can read, stamped with the given time.
func broken(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func byName(d *Data) map[string]Item {
	m := map[string]Item{}
	for _, it := range d.Items {
		m[it.Name] = it
	}
	return m
}

func TestScanOrderFailuresAndFirstSeen(t *testing.T) {
	dir := t.TempDir()
	old, older := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2019, 1, 2, 0, 0, 0, 0, time.UTC)
	broken(t, filepath.Join(dir, "a old.mp4"), older)
	broken(t, filepath.Join(dir, "b new.mkv"), old)
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := cfg.AddLocalLibrary(dir, "Test")
	if err != nil {
		t.Fatal(err)
	}
	ix := &Indexer{Cfg: cfg, Clients: config.NewClients(cfg)}
	var totals []int
	rep := planned{totals: &totals}
	run := func(opt Options) map[string]Item {
		t.Helper()
		if err := ix.Run(context.Background(), lib, opt, rep); err != nil {
			t.Fatal(err)
		}
		d, err := LoadLibrary(cfg, lib)
		if err != nil {
			t.Fatal(err)
		}
		return byName(d)
	}

	// Newest first: with room for one file, the newer one is done.
	got := run(Options{Limit: 1})
	if got["b new.mkv"].Error == "" || got["a old.mp4"].Error != "" {
		t.Fatalf("the newest file should go first: %+v", got)
	}
	// The first scan takes the files' own times as when they were added.
	if got["a old.mp4"].Added != "2019-01-02T00:00:00Z" || got["b new.mkv"].Added != "2020-01-02T00:00:00Z" {
		t.Errorf("first scan: added %q, %q", got["a old.mp4"].Added, got["b new.mkv"].Added)
	}

	// A failure with this ffmpeg is remembered: the next scan does only the file not tried yet, the one after nothing.
	got = run(Options{})
	if got["b new.mkv"].FailedWith == "" || got["a old.mp4"].FailedWith == "" {
		t.Fatalf("failures not marked: %+v", got)
	}
	run(Options{})
	if want := []int{1, 1, 0}; len(totals) != 3 || totals[1] != want[1] || totals[2] != want[2] {
		t.Fatalf("files done per run: %v, want %v", totals, want)
	}
	// Asked to, it tries them again; Force does too.
	run(Options{Retry: true})
	run(Options{Force: true})
	if totals[3] != 2 || totals[4] != 2 {
		t.Errorf("retry and force: %v", totals)
	}
	// So does a change to the file.
	broken(t, filepath.Join(dir, "a old.mp4"), older.Add(time.Hour))
	run(Options{})
	if totals[5] != 1 {
		t.Errorf("a changed file is not tried again: %v", totals)
	}
	// And so does another ffmpeg.
	idx, _ := LoadLibrary(cfg, lib)
	recs := map[string]Item{}
	for _, it := range idx.Items {
		it.FailedWith = "another"
		recs[it.ID] = it
	}
	if _, err := SaveLibrary(cfg, lib, recs, nil); err != nil {
		t.Fatal(err)
	}
	run(Options{})
	if totals[6] != 2 {
		t.Errorf("a new ffmpeg does not retry: %v", totals)
	}

	// After the first scan, a new file counts as added now, whatever its own date; a moved file keeps its time.
	before := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z")
	broken(t, filepath.Join(dir, "c copied.mp4"), older)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "b new.mkv"), filepath.Join(dir, "sub", "b new.mkv")); err != nil {
		t.Fatal(err)
	}
	got = run(Options{})
	if got["c copied.mp4"].Added < before {
		t.Errorf("a copied file is not new: %q", got["c copied.mp4"].Added)
	}
	if got["b new.mkv"].Added != "2020-01-02T00:00:00Z" || got["b new.mkv"].Dir != "sub" {
		t.Errorf("a moved file: %+v", got["b new.mkv"])
	}
}
