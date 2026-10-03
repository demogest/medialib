package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// pixelsOnly hides an image's type, so imageScore has to ask for every pixel.
type pixelsOnly struct{ image.Image }

// frame draws a 480x270 picture: a base brightness with some amount of detail on it, decoded from JPEG as the real
// frames are.
func frame(t testing.TB, base, detail int) image.Image {
	r := rand.New(rand.NewSource(int64(base*1000 + detail)))
	m := image.NewRGBA(image.Rect(0, 0, 480, 270))
	for y := 0; y < 270; y++ {
		for x := 0; x < 480; x++ {
			v := base + (x/24+y/18)%2*detail + r.Intn(detail+1) - detail/2
			v = max(0, min(255, v))
			m.Set(x, y, color.RGBA{uint8(v), uint8(max(0, v-40)), uint8(min(255, v+30)), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestCoverScoreFastPathAgrees(t *testing.T) {
	frames := []image.Image{frame(t, 4, 2), frame(t, 250, 3), frame(t, 110, 10), frame(t, 110, 60), frame(t, 60, 120), frame(t, 170, 90)}
	if _, ok := frames[0].(*image.YCbCr); !ok {
		t.Fatalf("JPEG decodes to %T, not the YCbCr the fast path is for", frames[0])
	}
	best, bestSlow := 0, 0
	for i, f := range frames {
		fast, slow := imageScore(f), imageScore(pixelsOnly{f})
		if d := fast - slow; d > 0.5+slow*0.02 || -d > 0.5+slow*0.02 {
			t.Errorf("frame %d: fast %.3f, per pixel %.3f", i, fast, slow)
		}
		if fast > imageScore(frames[best]) {
			best = i
		}
		if slow > imageScore(pixelsOnly{frames[bestSlow]}) {
			bestSlow = i
		}
	}
	if best != bestSlow || best < 3 {
		t.Errorf("cover %d (fast) vs %d (per pixel)", best, bestSlow)
	}
	// a cropped image (its planes start away from the origin) reads the right pixels
	sub := frames[4].(*image.YCbCr).SubImage(image.Rect(100, 50, 400, 250))
	if fast, slow := imageScore(sub), imageScore(pixelsOnly{sub}); fast-slow > 0.5+slow*0.02 || slow-fast > 0.5+slow*0.02 {
		t.Errorf("sub-image: fast %.3f, per pixel %.3f", fast, slow)
	}
}

func BenchmarkCoverScore(b *testing.B) {
	p := filepath.Join(b.TempDir(), "f.jpg")
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, frame(b, 110, 60), &jpeg.Options{Quality: 85})
	_ = os.WriteFile(p, buf.Bytes(), 0o644)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		coverScore(p)
	}
}
