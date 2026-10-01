//go:build desktop

package main

import (
	webview "github.com/webview/webview_go"
)

// runDesktop shows the UI in a native window: WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux.
// It returns when the window is closed.
func runDesktop(url string) error {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Media Library")
	w.SetSize(1360, 860, webview.HintNone)
	w.Navigate(url)
	w.Run()
	return nil
}
