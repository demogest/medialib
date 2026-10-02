package main

import (
	"os/exec"
	"runtime"

	"github.com/demogest/medialib/internal/proc"
)

// openBrowser opens a URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	proc.NoConsole(cmd)
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}
