package media

import (
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/proc"
)

// Fractions say where the keyframes are taken, as a share of the duration.
var Fractions = []float64{0.1, 0.3, 0.5, 0.7, 0.9}

const (
	thumbBox = 480
	scale    = "scale=480:480:force_original_aspect_ratio=decrease:force_divisible_by=2"
	// Retry filter for files tagged with "reserved" colour primaries/transfer, which swscale refuses to convert.
	scaleBT709 = "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709," + scale
)

// Tools names the external programs.
type Tools struct {
	FFmpeg  string
	FFprobe string
	Rclone  string
}

func nonEmpty(sz int64, err error) bool { return err == nil && sz > 0 }

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// decodeFrame turns one keyframe bitstream (piped into ffmpeg) into a JPEG.
func (t Tools) decodeFrame(format string, data []byte, out string) bool {
	args := []string{"-v", "error", "-f", format}
	if format == "h264" {
		args = append(args, "-flags2", "+showall") // also output lone non-IDR I-frames
	}
	for _, vf := range []string{scale, scaleBT709} {
		a := append(append([]string{}, args...), "-i", "pipe:0", "-frames:v", "1", "-vf", vf, "-q:v", "5", "-y", out)
		if _, err := proc.Run(60*time.Second, data, t.FFmpeg, a...); err != nil {
			return false
		}
		if nonEmpty(fileSize(out)) {
			return true
		}
	}
	return false
}

// decodeBatch decodes several keyframe bitstreams of one video in a single ffmpeg process: they are concatenated into
// one stream (each starts with its own parameter sets, so each decodes on its own) and every picture is written as
// <stem>-k<i>.jpg. One process instead of one per keyframe matters most on Windows, where starting a program is slow.
// It returns the file names, or false if the number of pictures that came out is not the number that went in (the
// caller then decodes them one by one).
func (t Tools) decodeBatch(format string, streams [][]byte, stem string) ([]string, bool) {
	args := []string{"-v", "error", "-f", format}
	if format == "h264" {
		args = append(args, "-flags2", "+showall") // also output lone non-IDR I-frames
	}
	var all []byte
	for _, s := range streams {
		all = append(all, s...)
	}
	for _, vf := range []string{scale, scaleBT709} {
		a := append(append([]string{}, args...), "-i", "pipe:0", "-vf", vf, "-fps_mode", "passthrough", "-q:v", "5", "-start_number", "0", "-y", stem+"-k%d.jpg")
		_, err := proc.Run(120*time.Second, all, t.FFmpeg, a...)
		outs := make([]string, len(streams))
		complete := err == nil
		for i := range outs {
			outs[i] = fmt.Sprintf("%s-k%d.jpg", stem, i)
			if !nonEmpty(fileSize(outs[i])) {
				complete = false
			}
		}
		// A stray extra picture means the stream was split somewhere unexpected: do not trust the pairing.
		if _, serr := os.Stat(fmt.Sprintf("%s-k%d.jpg", stem, len(streams))); serr == nil {
			complete = false
		}
		if complete {
			return outs, true
		}
		cleanFrames(stem, len(streams)+1)
	}
	return nil, false
}

func cleanFrames(stem string, n int) {
	for i := 0; i < n; i++ {
		_ = os.Remove(fmt.Sprintf("%s-k%d.jpg", stem, i))
	}
}

// grabSeek seeks to t seconds in a file or URL and writes one frame.
func (t Tools) grabSeek(target string, at float64, out string) bool {
	for _, vf := range []string{scale, scaleBT709} {
		_, err := proc.Run(180*time.Second, nil, t.FFmpeg, "-v", "error", "-threads", "1", "-noaccurate_seek", "-ss", fmt.Sprintf("%.2f", at),
			"-i", target, "-map", "0:v:0", "-frames:v", "1", "-vf", vf, "-q:v", "5", "-y", out)
		if err != nil {
			return false
		}
		if nonEmpty(fileSize(out)) {
			return true
		}
	}
	return false
}

// firstFrame writes the first video frame (or cover art) of a file, for audio files and images.
func (t Tools) firstFrame(target, out string) bool {
	_, err := proc.Run(120*time.Second, nil, t.FFmpeg, "-v", "error", "-i", target, "-map", "0:v:0?", "-frames:v", "1", "-vf", scale, "-q:v", "5", "-y", out)
	return err == nil && nonEmpty(fileSize(out))
}

// probe reads duration, resolution and codec with ffprobe.
func (t Tools) probe(target string) (Meta, error) {
	r, err := proc.Run(120*time.Second, nil, t.FFprobe, "-v", "error", "-show_entries", "format=duration:stream=codec_type,codec_name,width,height", "-of", "json", target)
	if err != nil {
		return Meta{}, err
	}
	var info struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(r.Stdout, &info)
	var m Meta
	d, _ := strconv.ParseFloat(info.Format.Duration, 64)
	m.Duration = round2(d)
	var vcodec, acodec string
	haveV := false
	for _, s := range info.Streams {
		switch s.CodecType {
		case "video":
			if !haveV {
				haveV, m.Width, m.Height, vcodec = true, s.Width, s.Height, s.CodecName
			}
		case "audio":
			m.Audio = true
			if acodec == "" {
				acodec = s.CodecName
			}
		}
	}
	m.Codec = vcodec
	if m.Codec == "" {
		m.Codec = acodec
	}
	return m, nil
}

// coverScore prefers detailed, mid-exposure frames over black, white or flat ones.
func coverScore(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		if st, serr := f.Stat(); serr == nil {
			return float64(st.Size())
		}
		return 0
	}
	// Mean and standard deviation of luma over a 64x36 box-filtered thumbnail.
	const gw, gh = 64, 36
	b := img.Bounds()
	var sum, sumSq float64
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			x0, x1 := b.Min.X+gx*b.Dx()/gw, b.Min.X+(gx+1)*b.Dx()/gw
			y0, y1 := b.Min.Y+gy*b.Dy()/gh, b.Min.Y+(gy+1)*b.Dy()/gh
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var acc float64
			var n int
			for y := y0; y < y1 && y < b.Max.Y; y++ {
				for x := x0; x < x1 && x < b.Max.X; x++ {
					acc += luma(img, x, y)
					n++
				}
			}
			if n > 0 {
				acc /= float64(n)
			}
			sum += acc
			sumSq += acc * acc
		}
	}
	cnt := float64(gw * gh)
	mean := sum / cnt
	std := math.Sqrt(math.Max(0, sumSq/cnt-mean*mean))
	return std * (1 - math.Min(1, math.Abs(mean-115)/160))
}

func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257
}

// ---------------------------------------------------------------- bytes read (for progress lines)

var (
	fetchedMu sync.Mutex
	fetched   int64
)

func countBytes(n int) {
	fetchedMu.Lock()
	fetched += int64(n)
	fetchedMu.Unlock()
}

// FetchedTotal is the number of media bytes read so far by the MP4 fast path.
func FetchedTotal() int64 {
	fetchedMu.Lock()
	defer fetchedMu.Unlock()
	return fetched
}

// Human formats a byte count.
func Human(n int64) string {
	f := float64(n)
	for _, unit := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if f < 1024 || unit == "TiB" {
			if unit == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, unit)
		}
		f /= 1024
	}
	return ""
}
