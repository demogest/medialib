package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// The desktop build is a GUI program (-H windowsgui) so a double click opens no console, but the same exe is also
// the command line. When it was started from a terminal it has no standard handles of its own; hook it up to the
// console it was started from so `medialib ls` and friends print.
func init() {
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return // already has output (a console build, or redirected to a file or pipe)
	}
	r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(^uintptr(0)) // ATTACH_PARENT_PROCESS
	if r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}
