//go:build desktop && !windows

package main

import (
	webview "github.com/webview/webview_go"
)

// A desktop build opens its window when it is started without arguments (a double click).
const defaultCommand = "desktop"

// desktopBuild: this build updates from the desktop downloads.
const desktopBuild = true

func desktopPreflight() bool { return true }

// runDesktop shows the UI in a native window: WKWebView on macOS, WebKitGTK on Linux (cgo). It returns when the
// window is closed. Windows has its own pure-Go host, in desktop_windows.go.
func runDesktop(url string) error {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Media Library")
	w.SetSize(1360, 860, webview.HintNone)
	closeWindow = func() { w.Dispatch(w.Terminate) }
	w.Navigate(url)
	w.Run()
	return nil
}
