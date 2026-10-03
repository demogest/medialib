//go:build !windows

package players

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/demogest/medialib/internal/proc"
)

// Reveal opens the folder that holds path in the file manager, with the file selected where the file manager can do
// that: Finder on macOS; on Linux any that implements org.freedesktop.FileManager1 (Files, Dolphin, Nemo, Caja ...),
// else the folder alone through xdg-open.
func Reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return startDetached(exec.Command("open", "-R", path))
	}
	uri := strings.ReplaceAll((&url.URL{Scheme: "file", Path: path}).String(), ",", "%2C") // dbus-send splits arrays at commas
	if r, err := proc.Run(5*time.Second, nil, "dbus-send", "--session", "--print-reply", "--dest=org.freedesktop.FileManager1",
		"--type=method_call", "/org/freedesktop/FileManager1", "org.freedesktop.FileManager1.ShowItems",
		"array:string:"+uri, "string:"); err == nil && r.ExitCode == 0 {
		return nil
	}
	return startDetached(exec.Command("xdg-open", filepath.Dir(path)))
}

func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
