package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/proc"
)

// runAppMode shows the UI in an app-mode window of Edge, Chrome or Chromium (no tabs or address bar, its own
// profile). It is the desktop window of the plain build, and the fallback of the native one.
func runAppMode(url string) error {
	browser := findAppBrowser()
	if browser == "" {
		fmt.Println("No Edge, Chrome or Chromium found for an app window; opening your browser instead. Press Ctrl+C to stop.")
		openBrowser(url)
		done := make(chan struct{})
		var once sync.Once
		closeWindow = func() { once.Do(func() { close(done) }) }
		waitForInterrupt(done)
		return nil
	}
	profile := filepath.Join(config.Home(), "window-profile")
	cmd := exec.Command(browser, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1360,860")
	proc.NoConsole(cmd) // the app window must be shown
	if err := cmd.Start(); err != nil {
		return err
	}
	closeWindow = func() { _ = cmd.Process.Kill() }
	// The window is closed: the server goes with it. (If Chromium handed the window to a running instance this
	// returns early; --user-data-dir keeps medialib's window in a process of its own.)
	return cmd.Wait()
}

func findAppBrowser() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	case "darwin":
		candidates = []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		for _, n := range []string{"microsoft-edge", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				candidates = append(candidates, p)
			}
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}
