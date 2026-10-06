// Package players finds media players and opens media in them.
package players

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	TitleArg string   // {title} is replaced by the file name when a single item is opened
	SubArgs  []string // how to hand it a subtitle file: {sub} is replaced by its path
	SubOnce  bool     // it takes only one subtitle file
	Args     []string // extra arguments, put before the file (from "args" in config.json)
	Custom   bool     // added by hand ("players" in config.json) rather than found on this computer
	Hidden   bool     // found on this computer, but removed from the list ("hidden_players")
	Missing  bool     // added by hand, but its program is not there (any more)
}

// Playable is whether the player is offered for playing.
func (p Player) Playable() bool { return !p.Hidden && !p.Missing }

// regValue is a registry value that holds a player's path on Windows: key under HKEY_CURRENT_USER or
// HKEY_LOCAL_MACHINE, and the value's name ("" for the key's default value).
type regValue struct{ key, value string }

type known struct {
	id, name string
	paths    []string   // absolute (with %VARS% and ~), or a program name looked up like any other (see config.FindProgram)
	exes     []string   // Windows: program names registered under App Paths by their installers
	reg      []regValue // Windows: other registry values that hold the program's path
	titleArg string
	subArgs  []string
	subOnce  bool
}

// knownPlayers are looked for on every computer, in this order (the first one found is the default until the user
// picks one).
var knownPlayers = []known{
	{id: "mpv", name: "mpv", titleArg: "--force-media-title={title}", subArgs: []string{"--sub-file={sub}"}, exes: []string{"mpv.exe"}, paths: []string{
		`%USERPROFILE%\scoop\apps\mpv\current\mpv.exe`, `%ProgramFiles%\mpv\mpv.exe`, `%ProgramData%\chocolatey\bin\mpv.exe`,
		"mpv", "/Applications/mpv.app/Contents/MacOS/mpv"}},
	{id: "mpvnet", name: "mpv.net", titleArg: "--force-media-title={title}", subArgs: []string{"--sub-file={sub}"}, exes: []string{"mpvnet.exe"}, paths: []string{
		`%ProgramFiles%\mpv.net\mpvnet.exe`, `%LOCALAPPDATA%\Programs\mpv.net\mpvnet.exe`, "mpvnet"}},
	{id: "iina", name: "IINA", titleArg: "--mpv-force-media-title={title}", subArgs: []string{"--mpv-sub-file={sub}"}, paths: []string{"/Applications/IINA.app/Contents/MacOS/iina-cli"}},
	{id: "potplayer", name: "PotPlayer", subArgs: []string{"/sub={sub}"}, subOnce: true, exes: []string{"PotPlayerMini64.exe", "PotPlayerMini.exe"},
		reg: []regValue{{`Software\DAUM\PotPlayer64`, "ProgramPath"}, {`Software\DAUM\PotPlayer`, "ProgramPath"}},
		paths: []string{`%ProgramFiles%\DAUM\PotPlayer\PotPlayerMini64.exe`, `%ProgramFiles(x86)%\DAUM\PotPlayer\PotPlayerMini.exe`,
			`%USERPROFILE%\scoop\apps\potplayer\current\PotPlayerMini64.exe`}},
	{id: "vlc", name: "VLC", titleArg: "--meta-title={title}", subArgs: []string{"--sub-file={sub}"}, subOnce: true, exes: []string{"vlc.exe"}, reg: []regValue{{`SOFTWARE\VideoLAN\VLC`, ""}},
		paths: []string{`%ProgramFiles%\VideoLAN\VLC\vlc.exe`, `%ProgramFiles(x86)%\VideoLAN\VLC\vlc.exe`,
			`%USERPROFILE%\scoop\apps\vlc\current\vlc.exe`, "vlc", "/Applications/VLC.app/Contents/MacOS/VLC"}},
	{id: "mpc-hc", name: "MPC-HC", subArgs: []string{"/sub", "{sub}"}, exes: []string{"mpc-hc64.exe", "mpc-hc.exe"}, reg: []regValue{{`Software\MPC-HC\MPC-HC`, "ExePath"}},
		paths: []string{`%ProgramFiles%\MPC-HC\mpc-hc64.exe`, `%ProgramFiles(x86)%\MPC-HC\mpc-hc.exe`,
			`%ProgramFiles%\K-Lite Codec Pack\MPC-HC64\mpc-hc64.exe`, `%ProgramFiles(x86)%\K-Lite Codec Pack\MPC-HC64\mpc-hc64.exe`}},
	{id: "mpc-be", name: "MPC-BE", subArgs: []string{"/sub", "{sub}"}, exes: []string{"mpc-be64.exe", "mpc-be.exe"}, reg: []regValue{{`Software\MPC-BE`, "ExePath"}},
		paths: []string{`%ProgramFiles%\MPC-BE x64\mpc-be64.exe`, `%ProgramFiles%\MPC-BE\mpc-be64.exe`, `%ProgramFiles(x86)%\MPC-BE\mpc-be.exe`}},
	{id: "smplayer", name: "SMPlayer", subArgs: []string{"-sub", "{sub}"}, subOnce: true, exes: []string{"smplayer.exe"},
		paths: []string{`%ProgramFiles%\SMPlayer\smplayer.exe`, "smplayer", "/Applications/SMPlayer.app/Contents/MacOS/SMPlayer"}},
	{id: "celluloid", name: "Celluloid", paths: []string{"celluloid"}},
	{id: "haruna", name: "Haruna", paths: []string{"haruna"}},
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
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

// find is where a known player is on this computer, or "".
func (k known) find() string {
	for _, raw := range k.paths {
		if exp := config.ExpandVars(raw); filepath.IsAbs(exp) {
			if exists(exp) {
				return exp
			}
		} else if p := config.FindProgram(raw); p != "" {
			return p
		}
	}
	for _, p := range registryPaths(k.exes, k.reg) {
		if p = strings.Trim(p, `"`); filepath.IsAbs(p) && exists(p) {
			return p
		}
	}
	return ""
}

// Detect lists every player: the ones from config.json first, then the ones found on this computer (those the user
// removed are marked Hidden), then the system's default handler. Only the Playable ones are offered for playing.
func Detect(extra []config.Player, hidden []string) []Player {
	var out []Player
	seen := map[string]bool{}
	for _, p := range extra {
		pl := Player{ID: p.ID, Name: p.Name, Path: p.Path, Args: p.Args, Custom: true}
		if p.TitleArg != nil {
			pl.TitleArg = *p.TitleArg
		}
		if path := config.FindProgram(p.Path); path != "" {
			pl.Path = path
			seen[norm(path)] = true
		} else {
			pl.Missing = true
		}
		out = append(out, pl)
	}
	for _, k := range knownPlayers {
		if slices.ContainsFunc(out, func(p Player) bool { return p.ID == k.id }) {
			continue // one of the user's own takes its place
		}
		if path := k.find(); path != "" && !seen[norm(path)] {
			seen[norm(path)] = true
			out = append(out, Player{ID: k.id, Name: k.name, Path: path, TitleArg: k.titleArg, SubArgs: k.subArgs, SubOnce: k.subOnce,
				Hidden: slices.Contains(hidden, k.id)})
		}
	}
	return append(out, Player{ID: "system", Name: "System default"})
}

// Playable keeps the players that are offered for playing.
func Playable(list []Player) []Player {
	var out []Player
	for _, p := range list {
		if p.Playable() {
			out = append(out, p)
		}
	}
	return out
}

// Entry is something to play: a file path or URL.
type Entry struct {
	Target   string
	Name     string
	Duration float64
	Subs     []string // subtitle files on this computer, for a player that is handed one entry
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
		return "", errors.New("No media player found. Add one in Settings, under Players.")
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
		if len(entries) == 1 {
			args = append(args, SubArgs(player, entries[0].Subs)...)
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

// SubArgs are the arguments that hand a player subtitle files (none for a player that cannot be told).
func SubArgs(player Player, subs []string) []string {
	if player.SubOnce && len(subs) > 1 {
		subs = subs[:1]
	}
	var args []string
	for _, s := range subs {
		for _, a := range player.SubArgs {
			args = append(args, strings.ReplaceAll(a, "{sub}", s))
		}
	}
	return args
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

// Open opens a folder, file or URL the way a double click would (a folder in Explorer, Finder or the file manager).
func Open(target string) error {
	cmd := openDefault(target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
