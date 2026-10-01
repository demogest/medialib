//go:build windows

package players

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pEnumWindows         = user32.NewProc("EnumWindows")
	pIsWindowVisible     = user32.NewProc("IsWindowVisible")
	pGetWindow           = user32.NewProc("GetWindow")
	pGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	pGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	pIsIconic            = user32.NewProc("IsIconic")
	pShowWindow          = user32.NewProc("ShowWindow")
	pAttachThreadInput   = user32.NewProc("AttachThreadInput")
	pBringWindowToTop    = user32.NewProc("BringWindowToTop")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pKeybdEvent          = user32.NewProc("keybd_event")
	pFindWindow          = user32.NewProc("FindWindowW")

	pOpenProcess        = kernel32.NewProc("OpenProcess")
	pQueryImageName     = kernel32.NewProc("QueryFullProcessImageNameW")
	pCloseHandle        = kernel32.NewProc("CloseHandle")
	pGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

type window struct {
	hwnd uintptr
	pid  uint32
}

var enumTarget *[]window

// The callback is created once: Go can only make a limited number of them.
var enumCallback = syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
	vis, _, _ := pIsWindowVisible.Call(hwnd)
	owner, _, _ := pGetWindow.Call(hwnd, 4) // GW_OWNER
	n, _, _ := pGetWindowTextLength.Call(hwnd)
	if vis != 0 && owner == 0 && n != 0 && enumTarget != nil {
		var pid uint32
		pGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		*enumTarget = append(*enumTarget, window{hwnd, pid})
	}
	return 1
})

// windows lists visible, unowned, titled top-level windows.
func windows() []window {
	var out []window
	enumTarget = &out
	pEnumWindows.Call(enumCallback, 0)
	enumTarget = nil
	return out
}

func topWindows() map[uintptr]bool {
	m := map[uintptr]bool{}
	for _, w := range windows() {
		m[w.hwnd] = true
	}
	return m
}

var exeDirs = map[uint32]string{}

func exeDir(pid uint32) string {
	if d, ok := exeDirs[pid]; ok {
		return d
	}
	dir := ""
	if h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid)); h != 0 { // PROCESS_QUERY_LIMITED_INFORMATION
		buf := make([]uint16, 1024)
		size := uint32(len(buf))
		if r, _, _ := pQueryImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r != 0 {
			dir = strings.ToLower(filepath.Dir(syscall.UTF16ToString(buf[:size])))
		}
		pCloseHandle.Call(h)
	}
	exeDirs[pid] = dir
	return dir
}

func threadOf(hwnd uintptr) uintptr {
	t, _, _ := pGetWindowThreadPID.Call(hwnd, 0)
	return t
}

func foreground() uintptr {
	h, _, _ := pGetForegroundWindow.Call()
	return h
}

func focus(hwnd uintptr) bool {
	if foreground() == hwnd {
		return true // it activated itself; touching it now would only knock it back out
	}
	if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
		pShowWindow.Call(hwnd, 9) // SW_RESTORE
	}
	fg := threadOf(foreground())
	me, _, _ := pGetCurrentThreadID.Call()
	// Borrow the foreground thread's input state so SetForegroundWindow is allowed; never the player's own.
	attached := false
	if fg != 0 && fg != me && fg != threadOf(hwnd) {
		r, _, _ := pAttachThreadInput.Call(me, fg, 1)
		attached = r != 0
	}
	pBringWindowToTop.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)
	if attached {
		pAttachThreadInput.Call(me, fg, 0)
	}
	if foreground() != hwnd {
		// Last resort: a synthetic Alt tap makes this process the last-input owner, which lifts the lock.
		pKeybdEvent.Call(0x12, 0, 0, 0)
		pKeybdEvent.Call(0x12, 0, 2, 0)
		pSetForegroundWindow.Call(hwnd)
	}
	return foreground() == hwnd
}

// bringToFront focuses the window a just-launched player opens: one owned by its process, else a new window from
// the player's folder (child processes, mpv.com -> mpv.exe), else, if it handed off to an already running instance
// and exited, that instance's window. With no process id (the system handler), any new window.
func bringToFront(cmd *exec.Cmd, pid int, exe string, before map[uintptr]bool, timeout time.Duration) {
	folder := ""
	if exe != "" {
		if abs, err := filepath.Abs(exe); err == nil {
			folder = strings.ToLower(filepath.Dir(abs))
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		wins := windows()
		var target uintptr
		isNew := func(w window) bool { return !before[w.hwnd] }
		if pid == 0 {
			for _, w := range wins {
				if isNew(w) {
					target = w.hwnd
					break
				}
			}
		} else {
			for _, w := range wins {
				if int(w.pid) == pid {
					target = w.hwnd
					break
				}
			}
			if target == 0 {
				for _, w := range wins {
					if isNew(w) && exeDir(w.pid) == folder {
						target = w.hwnd
						break
					}
				}
			}
			if target == 0 && cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				for _, w := range wins {
					if exeDir(w.pid) == folder {
						target = w.hwnd
						break
					}
				}
			}
		}
		if target != 0 {
			// The player may still be activating itself or restyling its window: check that the focus sticks.
			for i := 0; i < 4; i++ {
				time.Sleep(300 * time.Millisecond)
				if focus(target) {
					time.Sleep(400 * time.Millisecond)
					if foreground() == target {
						return
					}
				}
			}
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// FocusTitled focuses a window by its exact title once it appears (dialogs are owned windows, which bringToFront
// skips).
func FocusTitled(title string) bool {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if h, _, _ := pFindWindow.Call(0, uintptr(unsafe.Pointer(t))); h != 0 {
			time.Sleep(200 * time.Millisecond)
			return focus(h)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}
