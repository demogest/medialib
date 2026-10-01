//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// Hide keeps a child process from opening a console window.
func Hide(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
