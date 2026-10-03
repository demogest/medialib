//go:build windows

package players

import (
	"golang.org/x/sys/windows/registry"

	"github.com/demogest/medialib/internal/config"
)

// registryPaths reads where installers recorded a player's program: App Paths (the list Windows itself uses to
// start programs by name) and the player's own settings, for the current user and for all users.
func registryPaths(exes []string, values []regValue) []string {
	var out []string
	read := func(root registry.Key, key, value string) {
		k, err := registry.OpenKey(root, key, registry.QUERY_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue(value); err == nil && v != "" {
			out = append(out, config.ExpandVars(v))
		}
	}
	roots := []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE}
	for _, exe := range exes {
		for _, root := range roots {
			read(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+exe, "")
		}
	}
	for _, v := range values {
		for _, root := range roots {
			read(root, v.key, v.value)
			read(root, `SOFTWARE\WOW6432Node\`+trimSoftware(v.key), v.value)
		}
	}
	return out
}

func trimSoftware(key string) string {
	if len(key) > 9 && (key[:9] == "SOFTWARE\\" || key[:9] == "Software\\") {
		return key[9:]
	}
	return key
}
