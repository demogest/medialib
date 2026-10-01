// Package web carries the UI (index.html, css/, js/) inside the binary, so the server is one file.
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed index.html css js
var files embed.FS

// FS returns the UI's files. Set MEDIALIB_WEB to a folder to serve the UI from disk instead (for working on it).
func FS() fs.FS {
	if dir := os.Getenv("MEDIALIB_WEB"); dir != "" {
		return os.DirFS(dir)
	}
	return files
}
