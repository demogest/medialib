// Package config holds config.json (libraries, connections, players, settings) and the paths medialib works in.
package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Home returns the folder that holds config.json and cache/.
//
// In order: $MEDIALIB_HOME; a config.json in the working directory or next to the executable (portable use, and
// installs from before the Go rewrite); otherwise the user's config folder (%AppData%\medialib, ~/.config/medialib).
func Home() string {
	if h := os.Getenv("MEDIALIB_HOME"); h != "" {
		abs, err := filepath.Abs(h)
		if err == nil {
			return abs
		}
		return h
	}
	if wd, err := os.Getwd(); err == nil && fileExists(filepath.Join(wd, "config.json")) {
		return wd
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if fileExists(filepath.Join(dir, "config.json")) || fileExists(filepath.Join(dir, "portable")) {
			return dir
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "medialib")
	}
	wd, _ := os.Getwd()
	return wd
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

var winVar = regexp.MustCompile(`%([^%]+)%`)

// ExpandVars expands environment variables ($NAME everywhere, %NAME% on Windows) and a leading ~.
func ExpandVars(s string) string {
	if runtime.GOOS == "windows" {
		s = winVar.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
				return v
			}
			return m
		})
	}
	s = os.ExpandEnv(s)
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	return s
}

// SameFolder compares two folder paths the way the platform's file system would.
func SameFolder(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
