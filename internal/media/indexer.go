package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
)

// Reporter receives progress from an indexing run.
type Reporter interface {
	State(state, line string)    // listing | waiting | indexing | done
	Plan(total int, line string) // indexing starts: how many files will be done
	Progress(done, errors int, line string)
	Line(line string)
}

// JobState is what the UI polls while a library is indexed.
type JobState struct {
	State    string   `json:"state"`
	Total    int      `json:"total"`
	Done     int      `json:"done"`
	Errors   int      `json:"errors"`
	Line     string   `json:"line"`
	Started  float64  `json:"started"`
	Finished *float64 `json:"finished,omitempty"`
}

// Running reports whether a state means "not finished".
func Running(state string) bool {
	return state == "waiting" || state == "listing" || state == "indexing"
}

// Job is the live state of one indexing run; it is a Reporter.
type Job struct {
	mu   sync.Mutex
	st   JobState
	done chan struct{}
	once sync.Once
}

// NewJob starts a job in the "listing" state.
func NewJob() *Job {
	return &Job{st: JobState{State: "listing", Started: float64(time.Now().UnixNano()) / 1e9}, done: make(chan struct{})}
}

func (j *Job) State(state, line string) {
	j.mu.Lock()
	j.st.State, j.st.Line = state, line
	j.mu.Unlock()
}
func (j *Job) Plan(total int, line string) {
	j.mu.Lock()
	j.st.State, j.st.Total, j.st.Done, j.st.Errors, j.st.Line = "indexing", total, 0, 0, line
	j.mu.Unlock()
}
func (j *Job) Progress(done, errors int, line string) {
	j.mu.Lock()
	j.st.Done, j.st.Errors, j.st.Line = done, errors, line
	j.mu.Unlock()
}
func (j *Job) Line(line string) { j.mu.Lock(); j.st.Line = line; j.mu.Unlock() }

// Fail ends the job with an error.
func (j *Job) Fail(err error) {
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	j.mu.Lock()
	j.st.State, j.st.Line = "error", msg
	j.mu.Unlock()
}

// Finish stamps the end time.
func (j *Job) Finish() {
	f := float64(time.Now().UnixNano()) / 1e9
	j.mu.Lock()
	j.st.Finished = &f
	j.mu.Unlock()
	j.once.Do(func() { close(j.done) })
}

// Done is closed when the job has finished.
func (j *Job) Done() <-chan struct{} { return j.done }

// Snapshot copies the state.
func (j *Job) Snapshot() JobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

// Options tune a run.
type Options struct {
	Workers int
	Limit   int
	Force   bool
}

// Indexer brings libraries up to date.
type Indexer struct {
	Cfg     *config.Config
	Clients *config.Clients
}

// Run brings a library's index up to date. One run per library at a time, in any process: a second run says so
// and sleeps on the index lock until the first releases it, then does its own (by then mostly empty) pass.
func (ix *Indexer) Run(ctx context.Context, lib config.Library, opt Options, rep Reporter) error {
	lock := newLock(ix.Cfg.LibDir(lib), "index")
	got, err := lock.Acquire(false)
	if err != nil {
		return err
	}
	if !got {
		rep.State("waiting", fmt.Sprintf("“%s” is already being indexed by another medialib process. Waiting for it to finish ...", lib.Name))
		if _, err := lock.Acquire(true); err != nil {
			return err
		}
	}
	defer lock.Release()
	return ix.run(ctx, lib, opt, rep)
}

func (ix *Indexer) run(ctx context.Context, lib config.Library, opt Options, rep Reporter) error {
	thumbs := filepath.Join(ix.Cfg.LibDir(lib), "thumbs")
	if err := os.MkdirAll(thumbs, 0o755); err != nil {
		return err
	}
	source, err := MakeSource(ix.Cfg, ix.Clients, lib)
	if err != nil {
		return err
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = ix.Cfg.Settings().Workers
	}
	if workers <= 0 {
		workers = config.DefaultWorkers(lib)
	}
	tools := ToolsOf(ix.Cfg)
	rep.State("listing", "Listing "+config.Location(lib)+" ...")
	listed, err := source.List()
	if err != nil {
		return err
	}
	var warnings []string
	if w, ok := source.(Warner); ok {
		warnings = w.Warnings()
	}
	for _, w := range warnings {
		rep.Line("Warning: " + w)
	}
	prevData, err := LoadLibrary(ix.Cfg, lib)
	if err != nil {
		return err
	}
	old := make(map[string]Item, len(prevData.Items))
	for _, r := range prevData.Items {
		old[r.ID] = r
	}
	recs := make(map[string]Item, len(listed))
	var todo []Item
	for _, it := range listed {
		prev, had := old[it.ID]
		current := had && prev.Ver == it.Ver && prev.Indexed && prev.Error == ""
		// A still-valid record stays in place until its replacement lands, so Force (and Limit) never blank it.
		if current {
			recs[it.ID] = prev
		} else {
			recs[it.ID] = it
		}
		if opt.Force || !current {
			todo = append(todo, it)
		}
	}
	fresh := len(listed) - len(todo)
	if opt.Limit > 0 && len(todo) > opt.Limit {
		todo = todo[:opt.Limit]
	}
	rep.Plan(len(todo), fmt.Sprintf("%d media files; %d up to date, indexing %d with %d workers", len(listed), fresh, len(todo), workers))
	if _, err := SaveLibrary(ix.Cfg, lib, recs, warnings); err != nil {
		return err
	}

	started, fetched0, saved := time.Now(), FetchedTotal(), time.Now()
	type result struct {
		item Item
		rec  Item
	}
	jobs := make(chan Item)
	results := make(chan result)
	var wg sync.WaitGroup
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				if runCtx.Err() != nil {
					return
				}
				rec, err := safeIndexItem(tools, source, thumbs, it)
				if err != nil {
					msg := err.Error()
					if len(msg) > 300 {
						msg = msg[:300]
					}
					rec = it
					rec.Indexed, rec.Error = false, msg
				}
				select {
				case results <- result{it, rec}:
				case <-runCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, it := range todo {
			select {
			case jobs <- it:
			case <-runCtx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	errorsN, done := 0, 0
	for res := range results {
		done++
		recs[res.item.ID] = res.rec
		if res.rec.Error != "" {
			errorsN++
		}
		if time.Since(saved) > 3*time.Second { // lets a running UI pick up new covers as they land
			_, _ = SaveLibrary(ix.Cfg, lib, recs, warnings) // only a checkpoint: the next one, or the final save, will land
			saved = time.Now()
		}
		flag := "ERR " + res.rec.Error
		if res.rec.Error == "" {
			flag = fmt.Sprintf("%d frames", res.rec.Frames)
			if res.rec.Note != "" {
				flag += " (ffmpeg fallback)"
			}
		}
		rep.Progress(done, errorsN, fmt.Sprintf("[%d/%d] %s read, %.0fs | %s -> %s", done, len(todo), Human(FetchedTotal()-fetched0), time.Since(started).Seconds(), res.item.Key, flag))
		if ctx.Err() != nil {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		// Stop at once and keep what is recorded; never let workers grind on with nobody collecting results.
		cancel()
		_, _ = SaveLibrary(ix.Cfg, lib, recs, warnings)
		return err
	}

	keep := make(map[string]bool, len(recs))
	for _, r := range recs {
		keep[r.ID+"-"+r.Ver] = true
	}
	orphans := 0
	for _, ext := range thumbExts {
		files, _ := filepath.Glob(filepath.Join(thumbs, "*"+ext))
		for _, p := range files {
			stem := strings.TrimSuffix(filepath.Base(p), ext)
			if i := strings.LastIndex(stem, "-"); i >= 0 && !keep[stem[:i]] {
				if os.Remove(p) == nil {
					orphans++
				}
			}
		}
	}
	if _, err := SaveLibrary(ix.Cfg, lib, recs, warnings); err != nil {
		return err
	}
	rep.State("done", fmt.Sprintf("Done: %d indexed in %.0fs, %d errors, %s read by the MP4 fast path, %d stale thumbnails removed.",
		len(todo), time.Since(started).Seconds(), errorsN, Human(FetchedTotal()-fetched0), orphans))
	return nil
}

// indexItem makes the thumbnails and metadata of one file.
func indexItem(tools Tools, source Source, thumbs string, item Item) (Item, error) {
	rec := item
	base := item.ID + "-" + item.Ver
	for _, ext := range thumbExts {
		old, _ := filepath.Glob(filepath.Join(thumbs, base+"-*"+ext))
		for _, p := range old {
			_ = os.Remove(p)
		}
	}
	ext := tools.FrameExt()
	stem := filepath.Join(thumbs, base)
	reader, err := source.Reader(item)
	if err != nil {
		return rec, err
	}
	defer reader.Close()
	var (
		paths []string
		meta  Meta
		note  string
	)
	if item.Kind == "video" {
		if isMP4(item.Name) {
			m, p, err := mp4Keyframes(tools, reader, item.Size, stem)
			if err != nil { // odd MP4 layouts: let ffmpeg handle them
				note = "fast path: " + err.Error()
			} else {
				meta, paths = m, p
			}
		}
		if len(paths) == 0 {
			meta, err = tools.probe(reader.Target())
			if err != nil {
				return rec, err
			}
			if meta.Duration > 0 {
				// Each ffmpeg opens the file itself. Side by side suits S3; on a disk or NAS it would multiply the
				// concurrent streams fivefold and thrash it, so there they go one after another.
				_, sequential := reader.(*FileReader)
				outs := make([]string, len(Fractions))
				var wg sync.WaitGroup
				for i, f := range Fractions {
					grab := func(i int, f float64) {
						frameSem <- struct{}{}
						defer func() { <-frameSem }()
						out := fmt.Sprintf("%s-k%d%s", stem, i, ext)
						if tools.grabSeek(reader.Target(), meta.Duration*f, out) {
							outs[i] = out
						}
					}
					if sequential {
						grab(i, f)
						continue
					}
					wg.Add(1)
					go func(i int, f float64) { defer wg.Done(); grab(i, f) }(i, f)
				}
				wg.Wait()
				paths = numberFrames(stem, outs, ext)
			}
		}
	} else {
		meta, err = tools.probe(reader.Target())
		if err != nil {
			return rec, err
		}
		out := stem + "-0" + ext
		if tools.firstFrame(reader.Target(), out) {
			paths = append(paths, out)
		}
	}
	rec.Duration, rec.Width, rec.Height, rec.Codec, rec.FPS, rec.Audio = meta.Duration, meta.Width, meta.Height, meta.Codec, meta.FPS, meta.Audio
	rec.Frames = len(paths)
	rec.Cover = nil
	if len(paths) > 0 {
		best, bestScore := 0, -1.0
		for i, p := range paths {
			if s := coverScore(p); s > bestScore {
				best, bestScore = i, s
			}
		}
		rec.Cover = &best
	}
	tools.Finalize(paths) // frames written as JPEG become AVIF once the cover is chosen
	rec.Indexed = true
	if note != "" && isMP4(item.Name) {
		rec.Note = note
	}
	if item.Kind == "video" && len(paths) == 0 {
		rec.Error = note
		if rec.Error == "" {
			rec.Error = "no frame could be extracted"
		}
	}
	return rec, nil
}

// safeIndexItem is indexItem, with a panic in odd media turned into that file's error rather than a crash.
func safeIndexItem(tools Tools, source Source, thumbs string, item Item) (rec Item, err error) {
	defer func() {
		if p := recover(); p != nil {
			rec, err = item, fmt.Errorf("internal error: %v", p)
		}
	}()
	return indexItem(tools, source, thumbs, item)
}
