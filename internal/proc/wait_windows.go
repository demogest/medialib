//go:build windows

package proc

import (
	"time"

	"golang.org/x/sys/windows"
)

// WaitExit waits (at most timeout) until process pid has ended.
func WaitExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // gone already
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
}
