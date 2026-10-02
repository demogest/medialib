package media

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// The index lives in library.json.gz: gzip-compressed JSON in a compact form (see encodeData). Indexes written by
// earlier versions are plain library.json; they are read as they are and replaced by the compact file on the next
// save.
const (
	indexFile  = "library.json.gz"
	legacyFile = "library.json"
	formatV2   = 2
)

func tmpFile(indexPath string) string { return filepath.Join(filepath.Dir(indexPath), "library.tmp") }

func legacyPath(indexPath string) string { return filepath.Join(filepath.Dir(indexPath), legacyFile) }

// diskItem is an Item as stored. The id and version follow from the key, size and modification time, the name and
// kind are usually implied, and a folder is stored once in diskData.Dirs and referred to by number.
type diskItem struct {
	Key      string  `json:"k"`
	Dir      int     `json:"d"`
	Name     string  `json:"n,omitempty"` // only when it is not the last part of the key
	Kind     string  `json:"t,omitempty"` // only when it is not "video"
	Size     int64   `json:"z"`
	MTime    string  `json:"m"`
	Duration float64 `json:"du,omitempty"`
	Width    int     `json:"w,omitempty"`
	Height   int     `json:"h,omitempty"`
	Codec    string  `json:"c,omitempty"`
	FPS      float64 `json:"f,omitempty"`
	Audio    bool    `json:"a,omitempty"`
	Frames   int     `json:"fr,omitempty"`
	Cover    *int    `json:"cv,omitempty"`
	Indexed  bool    `json:"ix,omitempty"`
	Note     string  `json:"no,omitempty"`
	Error    string  `json:"er,omitempty"`
	// search forms, only for names or folders with Chinese characters
	NameP string `json:"np,omitempty"`
	NameI string `json:"ni,omitempty"`
	DirP  string `json:"dp,omitempty"`
	DirI  string `json:"di,omitempty"`
}

type diskData struct {
	Format   int        `json:"format"`
	Library  string     `json:"library"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Location string     `json:"location"`
	Updated  string     `json:"updated"`
	Warnings []string   `json:"warnings"`
	Dirs     []string   `json:"dirs"`
	Items    []diskItem `json:"items"`
}

func baseName(key string) string { return path.Base(strings.ReplaceAll(key, `\`, "/")) }

// encodeData serializes an index in the compact, compressed form.
func encodeData(d Data) ([]byte, error) {
	dd := diskData{Format: formatV2, Library: d.Library, Name: d.Name, Type: d.Type, Location: d.Location, Updated: d.Updated,
		Warnings: d.Warnings, Dirs: []string{}, Items: make([]diskItem, 0, len(d.Items))}
	dirIdx := map[string]int{}
	for _, it := range d.Items {
		di, ok := dirIdx[it.Dir]
		if !ok {
			di = len(dd.Dirs)
			dirIdx[it.Dir] = di
			dd.Dirs = append(dd.Dirs, it.Dir)
		}
		x := diskItem{Key: it.Key, Dir: di, Size: it.Size, MTime: it.MTime, Duration: it.Duration, Width: it.Width, Height: it.Height,
			Codec: it.Codec, FPS: it.FPS, Audio: it.Audio, Frames: it.Frames, Cover: it.Cover, Indexed: it.Indexed, Note: it.Note, Error: it.Error,
			NameP: it.NamePinyin, NameI: it.NameInitials, DirP: it.DirPinyin, DirI: it.DirInitials}
		if it.Name != baseName(it.Key) {
			x.Name = it.Name
		}
		if it.Kind != "video" {
			x.Kind = it.Kind
		}
		dd.Items = append(dd.Items, x)
	}
	text, err := json.Marshal(dd)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write(text); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeData reads either form: gzip-compressed compact JSON, or the plain JSON of earlier versions.
func decodeData(raw []byte) (*Data, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		if raw, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	var probe struct {
		Format int `json:"format"`
	}
	_ = json.Unmarshal(raw, &probe)
	if probe.Format == 0 { // the plain library.json of version 1
		var d Data
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		if d.Items == nil {
			d.Items = []Item{}
		}
		if d.Warnings == nil {
			d.Warnings = []string{}
		}
		for i := range d.Items {
			d.Items[i].fillPinyin()
		}
		return &d, nil
	}
	var dd diskData
	if err := json.Unmarshal(raw, &dd); err != nil {
		return nil, err
	}
	d := &Data{Library: dd.Library, Name: dd.Name, Type: dd.Type, Location: dd.Location, Updated: dd.Updated, Warnings: dd.Warnings,
		Items: make([]Item, 0, len(dd.Items))}
	for _, x := range dd.Items {
		if x.Dir < 0 || x.Dir >= len(dd.Dirs) {
			return nil, fmt.Errorf("item %q refers to folder %d of %d", x.Key, x.Dir, len(dd.Dirs))
		}
		name, kind := x.Name, x.Kind
		if name == "" {
			name = baseName(x.Key)
		}
		if kind == "" {
			kind = "video"
		}
		it := newItem(x.Key, name, dd.Dirs[x.Dir], kind, x.Size, x.MTime)
		it.NamePinyin, it.NameInitials, it.DirPinyin, it.DirInitials = x.NameP, x.NameI, x.DirP, x.DirI
		it.fillPinyin() // an index from before pinyin was stored
		it.Duration, it.Width, it.Height, it.Codec, it.FPS, it.Audio = x.Duration, x.Width, x.Height, x.Codec, x.FPS, x.Audio
		it.Frames, it.Cover, it.Indexed, it.Note, it.Error = x.Frames, x.Cover, x.Indexed, x.Note, x.Error
		d.Items = append(d.Items, it)
	}
	if d.Warnings == nil {
		d.Warnings = []string{}
	}
	return d, nil
}
