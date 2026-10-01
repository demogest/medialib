// Package tasks runs background jobs (copy, move, delete, size of a folder ...) with progress that the UI polls.
package tasks

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCancelled is what Check returns once the user asked to stop.
var ErrCancelled = errors.New("cancelled")

// Task is one background job. The job function updates it through the methods; the UI reads Snapshot.
type Task struct {
	id, kind, title string
	mu              sync.Mutex
	state           string // running | done | error | cancelled
	done, total     int64
	bytes           int64
	line            string
	errors          []string
	errorCount      int
	result          any
	started         time.Time
	finished        *time.Time
	cancel          chan struct{}
	cancelOnce      sync.Once
	end             chan struct{}
}

// Snapshot is the JSON form of a task.
type Snapshot struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	State      string   `json:"state"`
	Done       int64    `json:"done"`
	Total      int64    `json:"total"`
	Bytes      int64    `json:"bytes"`
	Line       string   `json:"line"`
	Errors     []string `json:"errors"`
	ErrorCount int      `json:"error_count"`
	Result     any      `json:"result"`
	Started    float64  `json:"started"`
	Finished   *float64 `json:"finished"`
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Snapshot copies the task's state.
func (t *Task) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Snapshot{ID: t.id, Kind: t.kind, Title: t.title, State: t.state, Done: t.done, Total: t.total, Bytes: t.bytes, Line: t.line,
		Errors: append([]string{}, t.errors...), ErrorCount: t.errorCount, Result: t.result, Started: unix(t.started)}
	if t.finished != nil {
		f := unix(*t.finished)
		s.Finished = &f
	}
	return s
}

// Check stops the job (returns ErrCancelled) if the user asked for that. Call it between steps.
func (t *Task) Check() error {
	select {
	case <-t.cancel:
		return ErrCancelled
	default:
		return nil
	}
}

// Fail records a message about one item that failed (the first 50 are kept; all are counted).
func (t *Task) Fail(format string, a ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errorCount++
	if len(t.errors) < 50 {
		t.errors = append(t.errors, fmt.Sprintf(format, a...))
	}
}

func (t *Task) Line(s string)    { t.mu.Lock(); t.line = s; t.mu.Unlock() }
func (t *Task) SetTotal(n int64) { t.mu.Lock(); t.total = n; t.mu.Unlock() }
func (t *Task) AddTotal(n int64) { t.mu.Lock(); t.total += n; t.mu.Unlock() }
func (t *Task) AddDone(n int64)  { t.mu.Lock(); t.done += n; t.mu.Unlock() }
func (t *Task) AddBytes(n int64) { t.mu.Lock(); t.bytes += n; t.mu.Unlock() }

// Progress sets done and bytes together.
func (t *Task) Progress(done, bytes int64) {
	t.mu.Lock()
	t.done, t.bytes = done, bytes
	t.mu.Unlock()
}

// Done reports how many items are finished.
func (t *Task) Done() int64 { t.mu.Lock(); defer t.mu.Unlock(); return t.done }

// SetResult stores what the job found (for a size scan: objects and bytes).
func (t *Task) SetResult(v any) { t.mu.Lock(); t.result = v; t.mu.Unlock() }

// Wait blocks until the task ends or the time is up; it reports whether it ended.
func (t *Task) Wait(d time.Duration) bool {
	select {
	case <-t.end:
		return true
	case <-time.After(d):
		return false
	}
}

// Runner starts jobs and remembers them for the Activity list.
type Runner struct {
	mu    sync.Mutex
	tasks map[string]*Task
	next  atomic.Int64
}

const keep = 40 // finished tasks kept for the Activity list

// NewRunner makes an empty runner.
func NewRunner() *Runner { return &Runner{tasks: map[string]*Task{}} }

// Start runs fn on a goroutine and returns the task at once.
func (r *Runner) Start(kind, title string, fn func(*Task) error) *Task {
	t := &Task{id: fmt.Sprintf("t%d", r.next.Add(1)), kind: kind, title: title, state: "running", started: time.Now(),
		cancel: make(chan struct{}), end: make(chan struct{})}
	r.mu.Lock()
	r.tasks[t.id] = t
	var finished []*Task
	for _, o := range r.tasks {
		o.mu.Lock()
		if o.finished != nil {
			finished = append(finished, o)
		}
		o.mu.Unlock()
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].finished.Before(*finished[j].finished) })
	if len(finished) > keep {
		for _, o := range finished[:len(finished)-keep] {
			delete(r.tasks, o.id)
		}
	}
	r.mu.Unlock()
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			return fn(t)
		}()
		t.mu.Lock()
		switch {
		case errors.Is(err, ErrCancelled):
			t.state = "cancelled"
		case err != nil:
			msg := err.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
			t.state, t.line = "error", msg
			t.errorCount++
			if len(t.errors) < 50 {
				t.errors = append(t.errors, msg)
			}
		case t.errorCount > 0 && t.done == 0:
			t.state = "error"
		default:
			t.state = "done"
		}
		now := time.Now()
		t.finished = &now
		t.mu.Unlock()
		close(t.end)
	}()
	return t
}

// List returns every remembered task, newest first.
func (r *Runner) List() []Snapshot {
	r.mu.Lock()
	all := make([]*Task, 0, len(r.tasks))
	for _, t := range r.tasks {
		all = append(all, t)
	}
	r.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].started.After(all[j].started) })
	out := make([]Snapshot, len(all))
	for i, t := range all {
		out[i] = t.Snapshot()
	}
	return out
}

// Cancel asks a task to stop. It reports whether the task exists.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	t := r.tasks[id]
	r.mu.Unlock()
	if t == nil {
		return false
	}
	t.cancelOnce.Do(func() { close(t.cancel) })
	return true
}

// Dismiss forgets one finished task, or (id "") all of them.
func (r *Runner) Dismiss(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, t := range r.tasks {
		t.mu.Lock()
		fin := t.finished != nil
		t.mu.Unlock()
		if fin && (id == "" || id == k) {
			delete(r.tasks, k)
		}
	}
}
