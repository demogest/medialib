//go:build !windows

package proc

import "os/exec"

// Hide keeps a child process from opening a console window (Windows only).
func Hide(*exec.Cmd) {}
