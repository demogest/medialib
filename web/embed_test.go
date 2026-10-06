package web

import (
	"encoding/json"
	"io/fs"
	"testing"
)

// The UI loads its text from locales/ at start: without them every label would show as its key.
func TestLocalesAreEmbedded(t *testing.T) {
	for _, name := range []string{"locales/en.json", "locales/zh-CN.json", "js/lib/i18n.js", "js/lib/messageformat.js"} {
		b, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name[len(name)-5:] == ".json" {
			var m map[string]string
			if err := json.Unmarshal(b, &m); err != nil || len(m) == 0 {
				t.Errorf("%s: not a message file (%v)", name, err)
			}
		}
	}
}
