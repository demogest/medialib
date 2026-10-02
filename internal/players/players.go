// Package players finds media players and opens media in them.
package players

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/proc"
)

// Player is a way to open media. An empty Path means the system's default handler.
type Player struct {
	ID       string
	Name     string
	Path     string
	TitleArg string // {title} is replaced by the file name when a single item is opened
	Args     []string // extra arguments, put before the file (from "args" in config.json)
}

type known struct {
	id, name string
	paths    []string
	titleArg string
}

var knownPlayers = []known{
	{"mpv", "mpv", []string{`%USERPROFILE%\scoop\apps\mpv\current\mpv.exe`, "mpv", "/Applications/mpv.app/Contents/MacOS/mpv"}, "--force-media-title={title}"},
	{"potplayer", "PotPlayer", []string{`%ProgramFiles%\DAUM\PotPlayer\PotPlayerMini64.exe`}, ""},
	{"vlc", "VLC", []string{`%ProgramFiles%\VideoLAN\VLC\vlc.exe`, `%ProgramFiles(x86)%\VideoLAN\VLC\vlc.exe`, "vlc", "/Applications/VLC.app/Contents/MacOS/VLC"}, "--meta-title={title}"},
	{"mpc-hc", "MPC-HC", []string{`%ProgramFiles%\MPC-HC\mpc-hc64.exe`}, ""},
	{"mpc-be", "MPC-BE", []string{`%ProgramFiles%\MPC-BE x64\mpc-be64.exe`, `%ProgramFiles%\MPC-BE\mpc-be64.exe`}, ""},
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func which(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func norm(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// Detect lists the players: the ones from config.json first, then the ones found on this computer.
func Detect(extra []config.Player) []Player {
	var out []Player
	seen := map[string]bool{}
	for _, p := range extra {
		path := p.Path
		if !exists(path) {
			if w := which(path); w != "" {
				path = w
			} else {
				continue
			}
		} else if w := which(path); w != "" {
			path = w
		}
		pl := Player{ID: p.ID, Name: p.Name, Path: p.Path, Args: p.Args}
		if p.TitleArg != nil {
			pl.TitleArg = *p.TitleArg
		}
		out = append(out, pl)
		seen[norm(path)] = true
	}
	for _, k := range knownPlayers {
		dup := false
		for _, p := range out {
			dup = dup || p.ID == k.id
		}
		if dup {
			continue
		}
		for _, raw := range k.paths {
			var path string
			if exp := config.ExpandVars(raw); filepath.IsAbs(exp) {
				path = exp
			} else {
				path = which(raw)
			}
			if path != "" && exists(path) && !seen[norm(path)] {
				seen[norm(path)] = true
				out = append(out, Player{ID: k.id, Name: k.name, Path: path, TitleArg: k.titleArg})
				break
			}
		}
	}
	return append(out, Player{ID: "system", Name: "System default"})
}

// Entry is something to play: a file path or URL.
type Entry struct {
	Target   string
	Name     string
	Duration float64
}

// M3U writes entries as an extended M3U playlist.
func M3U(entries []Entry) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, e := range entries {
		d := -1
		if e.Duration > 0 {
			d = int(e.Duration)
		}
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", d, e.Name, e.Target)
	}
	return b.String()
}

// Launch opens entries in a player: one directly, several as a playlist written to playlistFile. It returns the
// name of the player used.
func Launch(list []Player, id string, entries []Entry, playlistFile string) (string, error) {
	if len(list) == 0 {
		return "", errors.New(`No media player found. Add one under "players" in config.json.`)
	}
	player := list[0]
	for _, p := range list {
		if p.ID == id {
			player = p
			break
		}
	}
	first := entries[0].Target
	target := first
	if !(len(entries) == 1 && (filepath.IsAbs(first) || player.ID != "system")) {
		if err := os.MkdirAll(filepath.Dir(playlistFile), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(playlistFile, []byte(M3U(entries)), 0o644); err != nil {
			return "", err
		}
		target = playlistFile
	}
	before := topWindows()
	var cmd *exec.Cmd
	if player.ID == "system" {
		cmd = openDefault(target)
	} else {
		args := append([]string{}, player.Args...)
		if len(entries) == 1 && player.TitleArg != "" {
			args = append(args, strings.ReplaceAll(player.TitleArg, "{title}", entries[0].Name))
		}
		cmd = exec.Command(player.Path, append(args, target)...)
		proc.NoConsole(cmd) // not Hide: that would start the player's own window hidden
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("Could not start %s: %w", player.Name, err)
	}
	startedCmd := cmd
	go func() {
		_ = startedCmd.Wait()
	}()
	var pid int
	if player.ID != "system" {
		pid = cmd.Process.Pid
	}
	go bringToFront(cmd, pid, player.Path, before, 20*time.Second)
	return player.Name, nil
}

// openDefault opens a file or URL with the system's handler.
func openDefault(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "windows":
		c := exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
		proc.NoConsole(c)
		return c
	case "darwin":
		return exec.Command("open", target)
	}
	return exec.Command("xdg-open", target)
}

// Reachable reports whether the file exists.
func Reachable(path string) bool { return exists(path) }
