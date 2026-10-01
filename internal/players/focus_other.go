//go:build !windows

package players

import (
	"os/exec"
	"time"
)

func topWindows() map[uintptr]bool { return nil }

func bringToFront(*exec.Cmd, int, string, map[uintptr]bool, time.Duration) {}

// FocusTitled brings a window with this exact title to the front (Windows only).
func FocusTitled(string) bool { return false }
