// medialib: a self-hosted media library and object-storage browser.
//
// One binary, three ways to use it:
//
//	medialib serve     the web UI for a browser, on this computer or deployed on a server
//	medialib desktop   the same UI in its own window
//	medialib index ... the command line (index, add, connect, ls ...)
package main

import (
	"fmt"
	"os"

	"github.com/demogest/medialib/internal/version"
)

const usage = `Media library and object-storage browser.

    medialib serve [--port 8766] [--host 127.0.0.1] [--no-browser]
    medialib desktop                                              the UI in its own window
    medialib index [--library ID | --all] [--limit N] [--workers 8] [--force]
    medialib compact [--library ID]                               convert older thumbnails to AVIF or WebP (smaller)
    medialib add PATH [--name NAME]                               add a local folder (disk or NAS share)
    medialib libraries                                            list the libraries
    medialib connect --endpoint URL --access-key K ...            add an S3 / RustFS / MinIO / R2 ... connection
    medialib connections [--test]                                 list connections
    medialib import [SOURCE]                                      list or adopt credentials from rclone / AWS / env
    medialib ls CONNECTION[:BUCKET[/PREFIX]] [-r]                 browse a store
    medialib add-s3 CONNECTION BUCKET[/PREFIX] [--name NAME]      add a bucket folder as a library
    medialib version

A library is a local folder, a NAS share, or a folder of an S3-compatible bucket read straight through the S3 API.
index    Reads each MP4's own sample index and fetches only the bytes of a few real keyframes, which ffmpeg decodes
         into thumbnails. Incremental: unchanged files are skipped, removed files are dropped.
serve    Runs the UI: library, storage browser, connections. Settings live in config.json (see MEDIALIB_HOME).
         Set MEDIALIB_PASSWORD to protect a server that other computers can reach, and MEDIALIB_AUTO_INDEX=60
         (or "auto_index": 60) to bring every library up to date every hour by itself.
`

func main() {
	if len(os.Args) < 2 && defaultCommand != "" {
		os.Args = append(os.Args, defaultCommand)
	}
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	waitForPredecessor()
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("medialib", version.Version)
		return
	}
	if err := run(cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
