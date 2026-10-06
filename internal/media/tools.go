package media

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // decoders for coverScore
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/demogest/medialib/internal/proc"
)

// Thumbnails are AVIF (about a third of the JPEGs of version 3.0 at the same look) when this ffmpeg can write it,
// else WebP (about half), else JPEG. The server answers any of the three extensions, so a library can hold a mix
// while it is converted (`medialib compact`).
//
// AVIF has no Go decoder, and the cover choice needs to look at the pixels, so frames are first written as JPEG
// (the "frame" format), scored in Go, and then packed into AVIF (the final format) by Tools.Finalize.
const (
	extAVIF = ".avif"
	extWebP = ".webp"
	extJPEG = ".jpg"
	// DefaultThumbQuality is the 0-100 setting. It maps to libwebp's -quality directly and to libaom's CRF as
	// 63 - q*0.38 (65 -> CRF 38). On real 480 px thumbnails: AVIF 30% of the old JPEG q5 at SSIM 0.944, WebP 41% at 0.947.
	DefaultThumbQuality = 65
)

var thumbExts = []string{extAVIF, extWebP, extJPEG}

var (
	thumbFormats   sync.Map   // ffmpeg path and quality -> thumbFormat
	thumbFormatsMu sync.Mutex // so the workers of a run that all start at once ask ffmpeg once, not once each
)

type thumbFormat struct {
	ext       string   // final thumbnails
	args      []string // encoder arguments that turn an image file into one
	muxer     string   // -f value for the final file ("" lets ffmpeg choose)
	frameExt  string   // what ffmpeg writes straight from the video
	frameArgs []string
}

// format reports which still-image formats this ffmpeg writes.
func (t Tools) format() thumbFormat {
	q := t.Quality
	if q < 1 || q > 100 {
		q = DefaultThumbQuality
	}
	key := t.FFmpeg + "|" + strconv.Itoa(q)
	if v, ok := thumbFormats.Load(key); ok {
		return v.(thumbFormat)
	}
	thumbFormatsMu.Lock()
	defer thumbFormatsMu.Unlock()
	if v, ok := thumbFormats.Load(key); ok {
		return v.(thumbFormat)
	}
	list := func(what string) string {
		if out, err := proc.Run(20*time.Second, nil, t.FFmpeg, "-hide_banner", what); err == nil && out != nil {
			return string(out.Stdout)
		}
		return ""
	}
	enc, mux := list("-encoders"), list("-muxers")
	jpg := []string{"-q:v", "5"}
	f := thumbFormat{ext: extJPEG, args: jpg, frameExt: extJPEG, frameArgs: jpg}
	switch {
	case strings.Contains(enc, "libaom-av1") && strings.Contains(mux, " avif"):
		crf := strconv.Itoa(int(math.Round(63 - float64(q)*0.38)))
		f = thumbFormat{ext: extAVIF, muxer: "avif", frameExt: extJPEG, frameArgs: []string{"-q:v", "3"},
			args: []string{"-c:v", "libaom-av1", "-crf", crf, "-b:v", "0", "-cpu-used", "6", "-threads", "2", "-still-picture", "1", "-pix_fmt", "yuv420p"}}
	case strings.Contains(enc, "libwebp"):
		w := []string{"-c:v", "libwebp", "-quality", strconv.Itoa(q), "-compression_level", "6"}
		f = thumbFormat{ext: extWebP, args: w, muxer: "webp", frameExt: extWebP, frameArgs: w}
	}
	thumbFormats.Store(key, f)
	return f
}

// Ext is the file extension of the thumbnails this program leaves in the cache.
func (t Tools) Ext() string { return t.format().ext }

// FrameExt is the extension of the frames ffmpeg writes before Finalize.
func (t Tools) FrameExt() string { return t.format().frameExt }

// convertImage re-encodes an image file into the final format. It leaves dst complete or absent, never partial.
func (t Tools) convertImage(src, dst string) bool {
	f := t.format()
	part := dst + ".part"
	a := append([]string{"-v", "error", "-i", src}, f.args...)
	if f.muxer != "" {
		a = append(a, "-f", f.muxer)
	}
	a = append(a, "-y", part)
	if r, err := proc.Run(120*time.Second, nil, t.FFmpeg, a...); err != nil || r.ExitCode != 0 || !nonEmpty(fileSize(part)) {
		_ = os.Remove(part)
		return false
	}
	if os.Rename(part, dst) != nil {
		_ = os.Remove(part)
		return false
	}
	return true
}

// convertImages re-encodes several image files into the final format in one ffmpeg process, which costs about a third
// less than one process each. It reports which ones landed; each dst is complete or absent, never partial.
func (t Tools) convertImages(srcs, dsts []string) []bool {
	f := t.format()
	ok := make([]bool, len(srcs))
	a := []string{"-v", "error"}
	for _, src := range srcs {
		a = append(a, "-i", src)
	}
	parts := make([]string, len(dsts))
	for i, dst := range dsts {
		parts[i] = dst + ".part"
		a = append(append(a, "-map", strconv.Itoa(i)+":v"), f.args...)
		if f.muxer != "" {
			a = append(a, "-f", f.muxer)
		}
		a = append(a, "-y", parts[i])
	}
	r, err := proc.Run(120*time.Second, nil, t.FFmpeg, a...)
	whole := err == nil && r.ExitCode == 0 // a failed run may have left some parts unfinished: keep none
	for i, part := range parts {
		if whole && nonEmpty(fileSize(part)) && os.Rename(part, dsts[i]) == nil {
			ok[i] = true
			continue
		}
		_ = os.Remove(part)
	}
	return ok
}

// Finalize turns the frames ffmpeg wrote into the final format (a no-op unless that is AVIF). A frame that cannot be
// converted stays as it is: the server finds it under any extension.
func (t Tools) Finalize(paths []string) []string {
	f := t.format()
	if f.ext == f.frameExt {
		return paths
	}
	out := make([]string, len(paths))
	dsts := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		dsts[i] = strings.TrimSuffix(p, filepath.Ext(p)) + f.ext
	}
	// All of a file's frames in one process; any that does not come out that way is tried again on its own.
	var done []bool
	if len(paths) > 1 {
		frameSem <- struct{}{}
		done = t.convertImages(paths, dsts)
		<-frameSem
	}
	var wg sync.WaitGroup
	for i, p := range paths {
		if done != nil && done[i] {
			_ = os.Remove(p)
			out[i] = dsts[i]
			continue
		}
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			frameSem <- struct{}{}
			defer func() { <-frameSem }()
			if t.convertImage(p, dsts[i]) {
				_ = os.Remove(p)
				out[i] = dsts[i]
			}
		}(i, p)
	}
	wg.Wait()
	return out
}

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
	Quality int // thumbnail quality 1-100 (0: DefaultThumbQuality)
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
		a := append(append([]string{}, args...), "-i", "pipe:0", "-frames:v", "1", "-vf", vf)
		a = append(append(a, t.format().frameArgs...), "-y", out)
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
// <stem>-k<i><ext>. One process instead of one per keyframe matters most on Windows, where starting a program is slow.
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
		ext := t.FrameExt()
		a := append(append([]string{}, args...), "-i", "pipe:0", "-vf", vf, "-fps_mode", "passthrough")
		a = append(append(a, t.format().frameArgs...), "-start_number", "0", "-y", stem+"-k%d"+ext)
		_, err := proc.Run(120*time.Second, all, t.FFmpeg, a...)
		outs := make([]string, len(streams))
		complete := err == nil
		for i := range outs {
			outs[i] = fmt.Sprintf("%s-k%d%s", stem, i, ext)
			if !nonEmpty(fileSize(outs[i])) {
				complete = false
			}
		}
		// A stray extra picture means the stream was split somewhere unexpected: do not trust the pairing.
		if _, serr := os.Stat(fmt.Sprintf("%s-k%d%s", stem, len(streams), ext)); serr == nil {
			complete = false
		}
		if complete {
			return outs, true
		}
		cleanFrames(stem, len(streams)+1, ext)
	}
	return nil, false
}

func cleanFrames(stem string, n int, ext string) {
	for i := 0; i < n; i++ {
		_ = os.Remove(fmt.Sprintf("%s-k%d%s", stem, i, ext))
	}
}

// grabSeek seeks to t seconds in a file or URL and writes one frame: the keyframe at or before t, as the MP4 fast
// path picks. That is the first frame decoded, but it is stamped before t, and ffmpeg's default frame rate handling
// drops such frames: without -fps_mode passthrough it decodes and throws away every frame up to t (up to a whole GOP,
// seconds per cover at a 10 s keyframe interval, and on S3 all of its bytes). Where the seek lands short of a keyframe
// (MPEG-TS), -skip_frame nokey skips decoding up to it. A stream whose keyframes are not flagged gives nothing that
// way, so it is tried again decoding every frame.
func (t Tools) grabSeek(target string, at float64, out string) bool {
	for _, skip := range [][]string{{"-skip_frame", "nokey"}, nil} {
		for _, vf := range []string{scale, scaleBT709} {
			a := append(append([]string{"-v", "error", "-threads", "1"}, skip...), "-noaccurate_seek", "-ss", fmt.Sprintf("%.2f", at), "-i", target,
				"-map", "0:v:0", "-frames:v", "1", "-fps_mode", "passthrough", "-vf", vf)
			_, err := proc.Run(180*time.Second, nil, t.FFmpeg, append(append(a, t.format().frameArgs...), "-y", out)...)
			if err != nil {
				return false
			}
			if nonEmpty(fileSize(out)) {
				return true
			}
		}
	}
	return false
}

// firstFrame writes the first video frame (or cover art) of a file, for audio files and images.
func (t Tools) firstFrame(target, out string) bool {
	a := []string{"-v", "error", "-i", target, "-map", "0:v:0?", "-frames:v", "1", "-vf", scale}
	_, err := proc.Run(120*time.Second, nil, t.FFmpeg, append(append(a, t.format().frameArgs...), "-y", out)...)
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
	img, _, err := image.Decode(f)
	if err != nil {
		if st, serr := f.Stat(); serr == nil {
			return float64(st.Size())
		}
		return 0
	}
	return imageScore(img)
}

func imageScore(img image.Image) float64 {
	// Mean and standard deviation of luma over a 64x36 box-filtered thumbnail.
	const gw, gh = 64, 36
	b := img.Bounds()
	plane, stride := lumaPlane(img)
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
				if plane != nil {
					row := plane[(y-b.Min.Y)*stride:]
					var sum int
					for x := x0; x < x1 && x < b.Max.X; x++ {
						sum += int(row[x-b.Min.X])
						n++
					}
					acc += float64(sum)
					continue
				}
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

// lumaPlane returns an image's 8-bit luma samples and their row stride, when it keeps them: JPEG and lossy WebP decode
// to YCbCr, whose Y plane is the luma. Reading it directly is several times faster than converting every pixel to RGB
// and back (and allocates nothing). nil means "ask the pixels one by one".
func lumaPlane(img image.Image) ([]uint8, int) {
	switch m := img.(type) {
	case *image.YCbCr:
		return m.Y[m.YOffset(m.Rect.Min.X, m.Rect.Min.Y):], m.YStride
	case *image.Gray:
		return m.Pix[m.PixOffset(m.Rect.Min.X, m.Rect.Min.Y):], m.Stride
	}
	return nil, 0
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
