package config

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

func sortStrings(s []string) { sort.Strings(s) }

func indentJSON(w io.Writer, v json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, v, "", "  "); err != nil {
		_, err = w.Write(v)
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
