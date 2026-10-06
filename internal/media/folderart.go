package media

import (
	"path"
	"strings"
)

// Folder pictures: a poster.jpg or folder.jpg that stands for the folder it is in, the way Kodi, Jellyfin and Plex
// read them. A folder that has one shows it as its cover instead of a mosaic of its videos.

var (
	artNames = []string{"poster", "folder", "cover", "fanart"} // best first
	artExt   = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
)

// artRank is how good a file name is as a folder picture: 1 for the best, 0 for a file that is not one.
func artRank(name string) int {
	ext := extOf(name)
	if !artExt[ext] {
		return 0
	}
	base := strings.ToLower(strings.TrimSuffix(name, name[len(name)-len(ext):]))
	for i, n := range artNames {
		if base == n {
			return i + 1
		}
	}
	return 0
}

// IsFolderArt reports whether a file name is one medialib takes as its folder's picture.
func IsFolderArt(name string) bool { return artRank(name) > 0 }

// artSet collects the best picture of each folder while a source lists: folder (as items have it) -> key.
type artSet struct {
	keys map[string]string
	rank map[string]int
}

func (s *artSet) reset() { s.keys, s.rank = nil, nil }

func (s *artSet) add(dir, name, key string) {
	r := artRank(name)
	if r == 0 {
		return
	}
	if s.keys == nil {
		s.keys, s.rank = map[string]string{}, map[string]int{}
	}
	if old, ok := s.rank[dir]; ok && (old < r || old == r && s.keys[dir] <= key) {
		return
	}
	s.keys[dir], s.rank[dir] = key, r
}

// FolderArt is what was found: folder -> the key of its picture.
func (s *artSet) FolderArt() map[string]string { return s.keys }

// Arter is implemented by sources that note the folder pictures they pass while listing.
type Arter interface{ FolderArt() map[string]string }

// relDir is the folder of a library-relative path, as items have it ("" at the top).
func relDir(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}
