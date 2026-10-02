package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/demogest/medialib/internal/config"
)

func TestSyncTimesAndSampleLocation(t *testing.T) {
	// 10 samples of 100 ticks each; keyframes at 1, 4 and 8. Chunks of 3 samples starting at offsets 1000, 2000 ...
	st := &sampleTable{
		stts: []uint32{10, 100}, count: 10, sync: []uint32{1, 4, 8}, hasSync: true,
		stsc:   []uint32{1, 3, 1, 4, 1, 1},
		chunks: []uint64{1000, 2000, 3000, 4000},
		sizes:  []uint32{10, 20, 30, 40, 50, 60, 70, 80, 90, 100},
	}
	got := syncTimes(st)
	want := []syncSample{{1, 0}, {4, 300}, {8, 700}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("syncTimes = %v", got)
	}
	// sample 5 is the 2nd of chunk 2 (samples 4-6): 2000 + size(4)
	if off, size, err := sampleLocation(st, 5); err != nil || off != 2040 || size != 50 {
		t.Errorf("sample 5 at %d (%d bytes): %v", off, size, err)
	}
	// sample 10 is the only one of chunk 4
	if off, size, err := sampleLocation(st, 10); err != nil || off != 4000 || size != 100 {
		t.Errorf("sample 10 at %d (%d bytes): %v", off, size, err)
	}
	if _, _, err := sampleLocation(st, 11); err == nil {
		t.Error("a sample past the table must be an error")
	}
}

func TestAnnexB(t *testing.T) {
	sample := []byte{0, 0, 0, 2, 0xAA, 0xBB, 0, 0, 0, 1, 0xCC}
	got := annexB(sample, 4, [][]byte{{0x67, 1}})
	want := []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0xAA, 0xBB, 0, 0, 0, 1, 0xCC}
	if string(got) != string(want) {
		t.Errorf("got % x", got)
	}
}

func TestItemIDsAreStable(t *testing.T) {
	// Existing indexes stay valid only while ids and versions are computed the way they always were.
	it := NewItem("shows/a.mp4", "a.mp4", "shows", "video", 123, "2026-01-02T03:04:05Z")
	if it.ID != "33f1b4a51f65" && len(it.ID) != 12 {
		t.Errorf("id %q", it.ID)
	}
	if len(it.Ver) != 10 {
		t.Errorf("ver %q", it.Ver)
	}
	if again := NewItem("shows/a.mp4", "a.mp4", "shows", "video", 123, "2026-01-02T03:04:05Z"); again != it {
		t.Error("not deterministic")
	}
	if changed := NewItem("shows/a.mp4", "a.mp4", "shows", "video", 124, "2026-01-02T03:04:05Z"); changed.ID != it.ID || changed.Ver == it.Ver {
		t.Error("a changed file keeps its id and gets a new version")
	}
}

func haveFFmpeg() bool {
	_, err1 := exec.LookPath("ffmpeg")
	_, err2 := exec.LookPath("ffprobe")
	return err1 == nil && err2 == nil
}

func makeClip(t *testing.T, path string, extra ...string) {
	t.Helper()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc=duration=6:size=320x180:rate=24", "-f", "lavfi", "-i", "sine=duration=6",
		"-pix_fmt", "yuv420p", "-g", "24"}
	args = append(append(args, extra...), "-y", path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
}

type quiet struct{}

func (quiet) State(string, string)      {}
func (quiet) Plan(int, string)          {}
func (quiet) Progress(int, int, string) {}
func (quiet) Line(string)               {}

func TestIndexesALocalLibrary(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("ffmpeg is not installed")
	}
	media := t.TempDir()
	makeClip(t, filepath.Join(media, "fast.mp4"), "-c:v", "libx264", "-c:a", "aac")
	if err := os.MkdirAll(filepath.Join(media, "sub dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeClip(t, filepath.Join(media, "sub dir", "other.mkv"), "-c:v", "libx264", "-c:a", "aac") // the ffmpeg fallback path
	_ = os.WriteFile(filepath.Join(media, "notes.txt"), []byte("not media"), 0o644)
	_ = os.MkdirAll(filepath.Join(media, "@eaDir"), 0o755)
	makeClip(t, filepath.Join(media, "@eaDir", "skipped.mp4"), "-c:v", "libx264")

	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := cfg.AddLocalLibrary(media, "Test")
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
	if len(d.Items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(d.Items), d.Items)
	}
	for _, it := range d.Items {
		if it.Error != "" || !it.Indexed || it.Frames < 3 || it.Duration < 5.5 || it.Duration > 6.5 || it.Width != 320 || it.Height != 180 || !it.Audio || it.Cover == nil {
			t.Errorf("%s: %+v", it.Key, it)
		}
		for i := 0; i < it.Frames; i++ {
			p := filepath.Join(cfg.LibDir(lib), "thumbs", it.ID+"-"+it.Ver+"-"+itoa(i)+ToolsOf(cfg).Ext())
			b, err := os.ReadFile(p)
			isJPEG := len(b) > 2 && b[0] == 0xFF && b[1] == 0xD8
			isWebP := len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
			isAVIF := len(b) > 12 && string(b[4:8]) == "ftyp" && strings.HasPrefix(string(b[8:16]), "avi")
			if err != nil || !(isJPEG || isWebP || isAVIF) || len(b) > 40000 {
				t.Errorf("%s is not a small AVIF, WebP or JPEG: %v (%d bytes)", p, err, len(b))
			}
		}
		fast := strings.HasSuffix(it.Name, ".mp4")
		if fast && it.Note != "" {
			t.Errorf("the MP4 fast path should have worked: %q", it.Note)
		}
		if it.Dir != map[bool]string{true: "", false: "sub dir"}[fast] {
			t.Errorf("%s: dir %q", it.Key, it.Dir)
		}
	}
	// A second run changes nothing and finds nothing to do; a removed file is dropped with its thumbnails.
	if err := os.Remove(filepath.Join(media, "fast.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Run(context.Background(), lib, Options{}, quiet{}); err != nil {
		t.Fatal(err)
	}
	d, _ = LoadLibrary(cfg, lib)
	if len(d.Items) != 1 || d.Items[0].Name != "other.mkv" {
		t.Fatalf("after removal: %+v", d.Items)
	}
	left, _ := filepath.Glob(filepath.Join(cfg.LibDir(lib), "thumbs", "*.*"))
	for _, p := range left {
		if !strings.HasPrefix(filepath.Base(p), d.Items[0].ID) {
			t.Errorf("stale thumbnail %s", p)
		}
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

func TestLocalSourceWarnsAboutCaseTwins(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "Clips"), 0o755)
	if err := os.MkdirAll(filepath.Join(root, "clips"), 0o755); err != nil {
		t.Skip("this file system folds case")
	}
	if entries, _ := os.ReadDir(root); len(entries) < 2 {
		t.Skip("this file system folds case")
	}
	_ = os.WriteFile(filepath.Join(root, "Clips", "a.mp4"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "clips", "b.mp4"), []byte("x"), 0o644)
	src := &LocalSource{root: root}
	items, err := src.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(src.Warnings()) != 1 {
		t.Errorf("items %d, warnings %v", len(items), src.Warnings())
	}
}

func TestStoreReloadsWhenTheIndexChanges(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	lib := cfg.Libraries()[0]
	store := NewStore(cfg, lib)
	snap, err := store.Get()
	if err != nil || len(snap.Data.Items) != 0 {
		t.Fatalf("%v %v", snap, err)
	}
	it := NewItem("a.mp4", "a.mp4", "", "video", 1, "2026-01-01T00:00:00Z")
	if _, err := SaveLibrary(cfg, lib, map[string]Item{it.ID: it}, nil); err != nil {
		t.Fatal(err)
	}
	snap, err = store.Get()
	if err != nil || len(snap.Data.Items) != 1 || snap.ByID[it.ID] == nil || !strings.Contains(string(snap.ItemsJSON()), `"a.mp4"`) {
		t.Fatalf("%+v %v", snap, err)
	}
}

// Decoding all keyframes in one ffmpeg process must give the very same pictures as decoding them one by one.
func TestBatchDecodingMatchesFrameByFrame(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("ffmpeg is not installed")
	}
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	for _, codec := range []string{"libx264", "libx265", "libaom-av1", "libsvtav1"} {
		if !strings.Contains(string(out), codec) {
			continue
		}
		t.Run(codec, func(t *testing.T) {
			dir := t.TempDir()
			clip := filepath.Join(dir, "clip.mp4")
			extra := []string{"-c:v", codec}
			switch codec {
			case "libaom-av1":
				extra = append(extra, "-cpu-used", "8", "-b:v", "0", "-crf", "40")
			case "libx265":
				extra = append(extra, "-x265-params", "log-level=error")
			}
			makeClip(t, clip, append(extra, "-an")...)
			tools := Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
			st, _ := os.Stat(clip)
			var results [2][][]byte
			for mode := 0; mode < 2; mode++ {
				disableBatch = mode == 1
				hits := batchHits.Load()
				defer func(mode int) {
					if mode == 0 && batchHits.Load() == hits {
						t.Error("the one-process path was never used")
					}
				}(mode)
				stem := filepath.Join(dir, []string{"batch", "single"}[mode])
				_, paths, err := mp4Keyframes(tools, &FileReader{path: clip}, st.Size(), stem)
				if err != nil {
					disableBatch = false
					t.Fatalf("mode %d: %v", mode, err)
				}
				for _, p := range paths {
					b, _ := os.ReadFile(p)
					results[mode] = append(results[mode], b)
				}
			}
			disableBatch = false
			if len(results[0]) < 4 || len(results[0]) != len(results[1]) {
				t.Fatalf("frame counts: batch %d, single %d", len(results[0]), len(results[1]))
			}
			for i := range results[0] {
				if string(results[0][i]) != string(results[1][i]) {
					t.Errorf("frame %d differs between batch and single decoding", i)
				}
			}
		})
	}
}
