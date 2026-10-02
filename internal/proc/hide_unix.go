//go:build !windows

package proc

import "os/exec"

// Hide keeps a child process from opening a console window (Windows only).
func Hide(*exec.Cmd) {}

// NoConsole keeps a child process from getting a console window of its own (Windows only), and nothing else.
func NoConsole(*exec.Cmd) {}
