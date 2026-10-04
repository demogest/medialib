package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Options carries what the Settings page may change; nil means "leave as is".
type Options struct {
	AutoIndex     *int    // minutes between indexing passes, 0: off
	Workers       *int    // files in flight while indexing, 0: automatic
	ThumbQuality  *int    // 1-100, 0: the default
	FFmpeg        *string // a program name or path, "": the default name
	FFprobe       *string
	Rclone        *string
	Updates       *string // off | notify | auto
	DefaultPlayer *string
}

// SetOptions validates and saves a change made on the Settings page.
func (c *Config) SetOptions(o Options) error {
	if o.AutoIndex != nil && (*o.AutoIndex < 0 || *o.AutoIndex > 7*24*60) {
		return Errorf("Automatic indexing runs at most once a minute and at least once a week.")
	}
	if o.Workers != nil && (*o.Workers < 0 || *o.Workers > 64) {
		return Errorf("Files at once: 1 to 64, or automatic.")
	}
	if o.ThumbQuality != nil && (*o.ThumbQuality < 0 || *o.ThumbQuality > 100) {
		return Errorf("Cover quality is 1 to 100.")
	}
	if o.Updates != nil && !slices.Contains(UpdateModes, *o.Updates) {
		return Errorf("Unknown update setting %q.", *o.Updates)
	}
	tool := func(p *string, name string) (string, error) {
		v := strings.Trim(strings.TrimSpace(*p), `"`)
		if v == "" || v == name {
			return name, nil
		}
		if FindProgram(v) == "" {
			return "", Errorf("No program found at %s.", v)
		}
		return v, nil
	}
	var ffmpeg, ffprobe, rclone string
	var err error
	if o.FFmpeg != nil {
		if ffmpeg, err = tool(o.FFmpeg, "ffmpeg"); err != nil {
			return err
		}
	}
	if o.FFprobe != nil {
		if ffprobe, err = tool(o.FFprobe, "ffprobe"); err != nil {
			return err
		}
	}
	if o.Rclone != nil {
		if rclone, err = tool(o.Rclone, "rclone"); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if o.AutoIndex != nil {
		c.d.AutoIndex = *o.AutoIndex
	}
	if o.Workers != nil {
		c.d.Workers = *o.Workers
	}
	if o.ThumbQuality != nil {
		c.d.ThumbQuality = *o.ThumbQuality
	}
	if o.FFmpeg != nil {
		c.d.FFmpeg = ffmpeg
	}
	if o.FFprobe != nil {
		c.d.FFprobe = ffprobe
	}
	if o.Rclone != nil {
		c.d.Rclone = rclone
	}
	if o.Updates != nil {
		c.d.Updates = *o.Updates
	}
	if o.DefaultPlayer != nil {
		c.d.DefaultPlayer = *o.DefaultPlayer
	}
	return c.save()
}

// SetCacheDir records where the index and covers now are ("" or the default folder: cache/ beside config.json).
func (c *Config) SetCacheDir(dir string) error {
	if dir != "" && SameFolder(filepath.Clean(dir), c.DefaultCacheDir()) {
		dir = ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d.CacheDir = dir
	return c.save()
}

// AddPlayer adds a player program. One with the id of a detected player (an "mpv" of one's own) takes its place.
func (c *Config) AddPlayer(name, path string, args []string) (Player, error) {
	name = strings.TrimSpace(name)
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return Player{}, Errorf("Choose the player's program.")
	}
	found := FindProgram(path)
	if found == "" {
		return Player{}, Errorf("No program found at %s.", path)
	}
	if strings.ContainsAny(path, `/\`) {
		path = found // an application bundle chosen on macOS becomes the program inside it
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	var kept []string
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" {
			kept = append(kept, a)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := uniqueID(Slug(name), func(id string) bool {
		return id == "system" || slices.ContainsFunc(c.d.Players, func(p Player) bool { return p.ID == id })
	})
	p := Player{ID: id, Name: name, Path: path, Args: kept}
	c.d.Players = append(c.d.Players, p)
	c.d.HiddenPlayers = slices.DeleteFunc(c.d.HiddenPlayers, func(h string) bool { return h == id })
	return p, c.save()
}

// RemovePlayer takes a player off the list: one added by hand is forgotten, a detected one stays hidden until
// RestorePlayers (detection would find it again otherwise).
func (c *Config) RemovePlayer(id string) error {
	if id == "" || id == "system" {
		return Errorf("The system default player can't be removed.")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	before := len(c.d.Players)
	c.d.Players = slices.DeleteFunc(c.d.Players, func(p Player) bool { return p.ID == id })
	if len(c.d.Players) == before && !slices.Contains(c.d.HiddenPlayers, id) {
		c.d.HiddenPlayers = append(c.d.HiddenPlayers, id)
	}
	if c.d.DefaultPlayer == id {
		c.d.DefaultPlayer = ""
	}
	return c.save()
}

// RestorePlayers brings back every detected player that was removed.
func (c *Config) RestorePlayers() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d.HiddenPlayers = nil
	return c.save()
}

// FindProgram finds a program by path or name: on PATH, or in the places package managers put programs that a
// desktop app does not always have on its PATH (Homebrew on macOS, winget, Scoop and Chocolatey on Windows: an app
// started by its installer, or from the Finder, inherits an older PATH). It returns "" when there is none.
func FindProgram(name string) string {
	name = ExpandVars(name)
	if strings.ContainsAny(name, `/\`) {
		st, err := os.Stat(name)
		switch {
		case err != nil:
			return ""
		case !st.IsDir():
			return name
		case goos == "darwin" && strings.HasSuffix(name, ".app"): // an application bundle: the program inside it
			inner := filepath.Join(name, "Contents", "MacOS")
			if p := filepath.Join(inner, strings.TrimSuffix(filepath.Base(name), ".app")); fileExists(p) {
				return p
			}
			if list, err := os.ReadDir(inner); err == nil {
				for _, e := range list {
					if !e.IsDir() {
						return filepath.Join(inner, e.Name())
					}
				}
			}
		}
		return ""
	}
	if p := lookPath(name); p != "" {
		return p
	}
	for _, dir := range programDirs() {
		for _, ext := range programExts() {
			p := filepath.Join(ExpandVars(dir), name+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// programDirs are the folders FindProgram looks in besides PATH (a variable for tests).
var programDirs = func() []string {
	switch goos {
	case "windows":
		return []string{`%LOCALAPPDATA%\Microsoft\WinGet\Links`, `%USERPROFILE%\scoop\shims`, `%ProgramData%\chocolatey\bin`}
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	}
	return []string{"/usr/local/bin", "/snap/bin", "~/.local/bin"}
}

func programExts() []string {
	if goos == "windows" {
		return []string{".exe", ""}
	}
	return []string{""}
}
