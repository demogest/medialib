//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// Hide keeps a child process from opening a console window. It is for console programs (ffmpeg, rclone): HideWindow
// also makes the child's first window start hidden, which is wrong for anything with a window of its own.
func Hide(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// NoConsole is for programs whose window the person is meant to see (players, browsers): they get no console, and
// their own window is shown as usual.
func NoConsole(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
