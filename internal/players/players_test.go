package players

import (
	"os"
	"path/filepath"
	"runtime"
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
