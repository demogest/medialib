//go:build !desktop

package main

// The plain build has no web view of its own: `medialib desktop` borrows an app-mode window from Edge, Chrome or
// Chromium. Build with `-tags desktop` for the native window (see README).

const defaultCommand = ""

func desktopPreflight() bool { return true }

func runDesktop(url string) error { return runAppMode(url) }
