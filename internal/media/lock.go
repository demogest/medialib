package media

import (
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Lock is a mutex shared by every goroutine of every medialib process: one per library and purpose.
//
//	"io"     held for the instant library.json is read or replaced
//	"index"  held for a whole indexing run, so two runs never work on one library at once
//
// It is a lock file the OS releases if the holder dies, so it cannot go stale. Waiting blocks in the kernel and the
// waiter is woken the moment the holder releases. Make a new Lock for every use: one value is one holder.
type Lock struct{ fl *flock.Flock }

func newLock(libDir, purpose string) *Lock {
	_ = os.MkdirAll(libDir, 0o755)
	return &Lock{fl: flock.New(filepath.Join(libDir, "."+purpose+".lock"))}
}

// Acquire takes the lock. With wait false it returns false at once if someone else holds it.
func (l *Lock) Acquire(wait bool) (bool, error) {
	if wait {
		return true, l.fl.Lock()
	}
	return l.fl.TryLock()
}

// Release lets go.
func (l *Lock) Release() { _ = l.fl.Unlock() }
