package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/demogest/medialib/internal/config"
)

// CompactResult says what a compaction did.
type CompactResult struct {
	Converted int   // thumbnails replaced by the smaller format
	Kept      int   // left alone because the new file would not have been smaller, or could not be made
	Before    int64 // bytes of the converted files
	After     int64 // bytes of the files that replaced them
}

// Saved is the space won.
func (r CompactResult) Saved() int64 { return r.Before - r.After }

// CompactThumbs converts a library's older thumbnails (JPEG, WebP) to the best format this ffmpeg writes (AVIF, else
// WebP), without touching the media they came from, so nothing is read from the NAS or the bucket again. A file is
// replaced only after its replacement is complete and smaller. It takes the indexing lock, so it never runs
// alongside an indexing pass. progress is called now and then with the number of files done and the total.
func CompactThumbs(ctx context.Context, cfg *config.Config, lib config.Library, workers int, progress func(done, total int)) (CompactResult, error) {
	var res CompactResult
	tools := ToolsOf(cfg)
	target := tools.Ext()
	if target == extJPEG {
		return res, errors.New("this ffmpeg can write neither AVIF nor WebP (libaom-av1 and libwebp are missing); the thumbnails stay JPEG")
	}
	l := newLock(cfg.LibDir(lib), "index")
	if _, err := l.Acquire(true); err != nil {
		return res, err
	}
	defer l.Release()

	dir := filepath.Join(cfg.LibDir(lib), "thumbs")
	stale, _ := filepath.Glob(filepath.Join(dir, "*.part")) // left by an interrupted run
	for _, p := range stale {
		_ = os.Remove(p)
	}
	var files []string
	for i, ext := range thumbExts { // thumbExts runs from the best format to the worst: convert only downwards
		if ext == target || i < slices.Index(thumbExts, target) {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(dir, "*"+ext))
		files = append(files, m...)
	}
	if workers <= 0 {
		workers = max(2, runtime.NumCPU()/2)
	}
	var (
		wg                    sync.WaitGroup
		done, converted, kept atomic.Int64
		before, after         atomic.Int64
		lastReport            atomic.Int64
		jobs                  = make(chan string)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				b, a, ok := compactOne(tools, p, target)
				if ok {
					converted.Add(1)
					before.Add(b)
					after.Add(a)
				} else {
					kept.Add(1)
				}
				n := done.Add(1)
				if progress != nil && time.Now().UnixMilli()-lastReport.Load() > 500 {
					lastReport.Store(time.Now().UnixMilli())
					progress(int(n), len(files))
				}
			}
		}()
	}
feed:
	for _, p := range files {
		select {
		case <-ctx.Done():
			break feed
		case jobs <- p:
		}
	}
	close(jobs)
	wg.Wait()
	if progress != nil {
		progress(int(done.Load()), len(files))
	}
	res = CompactResult{Converted: int(converted.Load()), Kept: int(kept.Load()), Before: before.Load(), After: after.Load()}
	return res, ctx.Err()
}

// compactOne converts one thumbnail; it reports the sizes and whether the old file was replaced.
func compactOne(tools Tools, src, target string) (before, after int64, ok bool) {
	in, err := os.Stat(src)
	if err != nil {
		return 0, 0, false
	}
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + target
	if !tools.convertImage(src, dst) {
		return 0, 0, false
	}
	out, err := os.Stat(dst)
	if err != nil || out.Size() >= in.Size() {
		_ = os.Remove(dst)
		return 0, 0, false
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst) // keep exactly one of the two
		return 0, 0, false
	}
	return in.Size(), out.Size(), true
}

// FormatCompact is a one-line summary for the command line.
func FormatCompact(r CompactResult) string {
	return fmt.Sprintf("%d thumbnails converted (%s -> %s, %s saved), %d left as they were",
		r.Converted, Human(r.Before), Human(r.After), Human(r.Saved()), r.Kept)
}
