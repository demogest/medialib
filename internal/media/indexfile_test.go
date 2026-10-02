package media

import (
	"path/filepath"

	"encoding/json"
	"github.com/demogest/medialib/internal/config"
	"os"
	"reflect"
	"testing"
)

func sampleData() Data {
	zero := 0
	two := 2
	a := NewItem("SS/a/b/one.mp4", "one.mp4", "a/b", "video", 1234, "2026-01-02T03:04:05Z")
	a.Duration, a.Width, a.Height, a.Codec, a.FPS, a.Audio, a.Frames, a.Cover, a.Indexed = 12.5, 1920, 1080, "avc1", 29.97, true, 5, &two, true
	b := NewItem("SS/a/b/two.mp3", "two.mp3", "a/b", "audio", 99, "2026-01-02T03:04:06Z")
	b.Indexed, b.Cover = true, &zero
	c := NewItem("SS/top.mkv", "Different Name.mkv", "", "video", 5, "2026-01-02T03:04:07Z")
	c.Error = "boom"
	return Data{Library: "x", Name: "X", Type: "s3", Location: "s3://b/SS/", Updated: "2026-01-01T00:00:00Z", Warnings: []string{"w"}, Items: []Item{a, b, c}}
}

func TestIndexRoundTrip(t *testing.T) {
	d := sampleData()
	raw, err := encodeData(d)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatal("not gzip")
	}
	got, err := decodeData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, d) {
		t.Fatalf("round trip changed the index:\n got %+v\nwant %+v", *got, d)
	}
}

func TestDecodeLegacyPlainJSON(t *testing.T) {
	d := sampleData()
	raw, _ := json.Marshal(d)
	got, err := decodeData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, d) {
		t.Fatal("legacy index read differently")
	}
}

// MEDIALIB_REAL_CACHE=<path to a plain library.json> checks the compact form against a real index.
func TestIndexRealFile(t *testing.T) {
	p := os.Getenv("MEDIALIB_REAL_CACHE")
	if p == "" {
		t.Skip("set MEDIALIB_REAL_CACHE to a library.json to run")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	old, err := decodeData(raw)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := encodeData(*old)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeData(packed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, back) {
		for i := range old.Items {
			if !reflect.DeepEqual(old.Items[i], back.Items[i]) {
				t.Fatalf("item %d differs:\n old %+v\n new %+v", i, old.Items[i], back.Items[i])
			}
		}
		t.Fatal("headers differ")
	}
	t.Logf("%d items: %d bytes -> %d bytes", len(old.Items), len(raw), len(packed))
}

func TestLegacyIndexIsReadAndReplaced(t *testing.T) {
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib := config.Library{ID: "x", Name: "X", Type: "local", Path: t.TempDir()}
	dir := cfg.LibDir(lib)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := sampleData()
	plain, _ := json.Marshal(d)
	legacy := filepath.Join(dir, "library.json")
	if err := os.WriteFile(legacy, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLibrary(cfg, lib)
	if err != nil || !reflect.DeepEqual(*got, d) {
		t.Fatalf("legacy load: %v", err)
	}
	recs := map[string]Item{}
	for _, it := range d.Items {
		recs[it.ID] = it
	}
	if ok, err := SaveLibrary(cfg, lib, recs, nil); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatal("the plain library.json should be gone after the first save")
	}
	if _, err := os.Stat(filepath.Join(dir, "library.json.gz")); err != nil {
		t.Fatal(err)
	}
	again, err := LoadLibrary(cfg, lib)
	if err != nil || len(again.Items) != len(d.Items) {
		t.Fatalf("reload: %v", err)
	}
}
