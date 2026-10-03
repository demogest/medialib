//go:build !windows

package proc

import (
	"os"
	"syscall"
	"time"
)

// WaitExit waits (at most timeout) until process pid has ended. A process started by pid also stops waiting when
// pid ends without being reaped yet: it is then no longer its parent.
func WaitExit(pid int, timeout time.Duration) {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil || (os.Getppid() != pid && isParentOnce(pid)) {
			return
		}
	}
}

var startParent = os.Getppid()

// isParentOnce: pid was this process's parent when it started, and is not any more.
func isParentOnce(pid int) bool { return startParent == pid }
