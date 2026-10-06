package players

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/demogest/medialib/internal/config"
)

func program(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectFindsHidesAndOverridesPlayers(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	program(t, bin, "mpv")
	program(t, bin, "vlc")
	own := program(t, t.TempDir(), "myplayer")

	ids := func(list []Player) (out []string) {
		for _, p := range list {
			out = append(out, p.ID)
		}
		return out
	}
	all := Detect(nil, nil)
	if got := ids(Playable(all)); len(got) != 3 || got[0] != "mpv" || got[1] != "vlc" || got[2] != "system" {
		t.Fatalf("found %v", got)
	}

	all = Detect([]config.Player{{ID: "mine", Name: "Mine", Path: own}, {ID: "gone", Name: "Gone", Path: "/no/such/player"}}, []string{"vlc"})
	byID := map[string]Player{}
	for _, p := range all {
		byID[p.ID] = p
	}
	if p := byID["mine"]; !p.Custom || p.Path != own || !p.Playable() {
		t.Errorf("own player: %+v", p)
	}
	if p := byID["gone"]; !p.Missing || p.Playable() {
		t.Errorf("a player whose program is gone: %+v", p)
	}
	if p := byID["vlc"]; !p.Hidden || p.Playable() {
		t.Errorf("a removed player: %+v", p)
	}
	if got := ids(Playable(all)); len(got) != 3 || got[0] != "mine" || got[1] != "mpv" || got[2] != "system" {
		t.Errorf("playable %v", got)
	}

	// An "mpv" of one's own takes the place of the one found.
	all = Detect([]config.Player{{ID: "mpv", Name: "My mpv", Path: own}}, nil)
	if got := ids(all); len(got) != 3 || all[0].Name != "My mpv" || got[1] != "vlc" {
		t.Errorf("override: %v", got)
	}
}

func TestSubtitleArguments(t *testing.T) {
	byID := map[string]known{}
	for _, k := range knownPlayers {
		byID[k.id] = k
	}
	p := func(id string) Player { k := byID[id]; return Player{ID: id, SubArgs: k.subArgs, SubOnce: k.subOnce} }
	subs := []string{"/c/Film.en.srt", "/c/Film.zh.srt"}
	cases := map[string][]string{
		"mpv":    {"--sub-file=/c/Film.en.srt", "--sub-file=/c/Film.zh.srt"},
		"vlc":    {"--sub-file=/c/Film.en.srt"},
		"mpc-hc": {"/sub", "/c/Film.en.srt", "/sub", "/c/Film.zh.srt"},
		"iina":   {"--mpv-sub-file=/c/Film.en.srt", "--mpv-sub-file=/c/Film.zh.srt"},
	}
	for id, want := range cases {
		if got := SubArgs(p(id), subs); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if got := SubArgs(Player{ID: "system"}, subs); got != nil {
		t.Errorf("the system default cannot be told: %q", got)
	}
}
