package main

// Windows resources (icon, version, DPI-aware manifest) go into resource_windows_*.syso, which the linker picks up
// by itself. They are committed, so a plain `go build` has them; regenerate after changing winres/ with
// `go generate ./cmd/medialib` on any system (draw the icon first with `go run ./scripts/genicon`). Release builds
// stamp the tag's version into them (`make winres`, scripts/package-windows.ps1), so the version in
// winres/versioninfo.json only shows in other builds.

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo -64 -o resource_windows_amd64.syso winres/versioninfo.json
//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo -arm -64 -o resource_windows_arm64.syso winres/versioninfo.json
