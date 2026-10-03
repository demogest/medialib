package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/proc"
	"github.com/demogest/medialib/internal/s3"
)

// Library sources: how a library lists its media and reads bytes from it.

var (
	videoExt = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".wmv": true, ".flv": true, ".ts": true, ".m2ts": true}
	audioExt = map[string]bool{".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".wav": true, ".ogg": true, ".opus": true}
	mp4Ext   = []string{".mp4", ".m4v", ".mov"}
	skipDirs = map[string]bool{"system volume information": true, "$recycle.bin": true, "@eadir": true, "#recycle": true}
)

func extOf(name string) string { return strings.ToLower(filepath.Ext(name)) }

// MediaKind is "video", "audio" or "" for a file name.
func MediaKind(name string) string {
	switch ext := extOf(name); {
	case videoExt[ext]:
		return "video"
	case audioExt[ext]:
		return "audio"
	}
	return ""
}

func isMP4(name string) bool {
	e := extOf(name)
	for _, x := range mp4Ext {
		if e == x {
			return true
		}
	}
	return false
}

// Reader gives random access to one item's bytes, and a Target ffmpeg/ffprobe can open themselves (a URL or a file
// path) for the fallback path.
type Reader interface {
	ReadRange(start, end int64) ([]byte, error)
	Target() string
	Close()
}

// Source lists a library's media and opens a reader per item.
type Source interface {
	List() ([]Item, error)
	Reader(Item) (Reader, error)
}

// Warner is implemented by sources that notice problems while listing.
type Warner interface{ Warnings() []string }

// ---------------------------------------------------------------- readers

// FileReader reads a local file; several goroutines may read it at once.
type FileReader struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func (r *FileReader) Target() string { return r.path }

func (r *FileReader) ReadRange(start, end int64) ([]byte, error) {
	r.mu.Lock()
	if r.f == nil {
		f, err := os.Open(r.path)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		r.f = f
	}
	f := r.f
	r.mu.Unlock()
	buf := make([]byte, end-start+1)
	n, err := f.ReadAt(buf, start)
	if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
		return nil, err
	}
	countBytes(n)
	return buf[:n], nil
}

func (r *FileReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
}

// S3Reader reads an object through the S3 API; its target is a presigned URL.
type S3Reader struct {
	client      *s3.Client
	bucket, key string
	url         string
}

func (r *S3Reader) Target() string { return r.url }

func (r *S3Reader) ReadRange(start, end int64) ([]byte, error) {
	data, err := r.client.GetRange(r.bucket, r.key, start, end)
	if err == nil {
		countBytes(len(data))
	}
	return data, err
}

func (r *S3Reader) Close() {}

// pool keeps connections alive per host, shared by every goroutine, so a ranged read rarely pays for a new
// TCP + TLS handshake (all presigned URLs of a library point at the same host).
var pool = &http.Client{Transport: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns:        128,
	MaxIdleConnsPerHost: 48,
	IdleConnTimeout:     90 * time.Second,
}, Timeout: 60 * time.Second}

// HTTPReader reads ranges of a URL (an rclone link).
type HTTPReader struct{ url string }

func (r *HTTPReader) Target() string { return r.url }
func (r *HTTPReader) Close()         {}

func (r *HTTPReader) ReadRange(start, end int64) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
		}
		data, err := fetchRange(r.url, start, end)
		if err == nil {
			countBytes(len(data))
			return data, nil
		}
		last = err
	}
	return nil, last
}

func fetchRange(url string, start, end int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := pool.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	want := end - start + 1
	if resp.StatusCode != 206 && !(resp.StatusCode == 200 && start == 0 && resp.ContentLength >= 0 && resp.ContentLength <= want) {
		// never drain a whole object because a server ignored Range
		if resp.StatusCode == 200 {
			return nil, errors.New("server ignored the Range header")
		}
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return io.ReadAll(io.LimitReader(resp.Body, want))
}

// ---------------------------------------------------------------- sources

// LocalSource lists a folder tree.
type LocalSource struct {
	root     string
	warnings []string
}

func (s *LocalSource) Warnings() []string { return s.warnings }

func (s *LocalSource) List() ([]Item, error) {
	if st, err := os.Stat(s.root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("Folder not reachable: %s", s.root)
	}
	s.warnings = nil
	var items []Item
	type frame struct{ folder, rel string }
	stack := []frame{{s.root, ""}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(cur.folder)
		if err != nil {
			continue
		}
		seen := map[string]string{}
		for _, e := range entries {
			// A NAS keeps "Clips" and "clips" apart, but Windows (and a share mounted without case) does not: both names
			// open whichever one the server picks, so indexing both would list one folder twice and the other not at
			// all. Keep the first. Where the two names do open two different things (a Linux disk), both are real.
			lower := strings.ToLower(e.Name())
			twin, ok := seen[lower]
			if ok && sameFile(filepath.Join(cur.folder, twin), filepath.Join(cur.folder, e.Name())) {
				where := ""
				if cur.rel != "" {
					where = cur.rel + "/"
				}
				s.warnings = append(s.warnings, fmt.Sprintf("“%s%s” and “%s%s” differ only by letter case. This computer can open only one of them, so the second is skipped. Rename one on the NAS itself to fix this.", where, twin, where, e.Name()))
				continue
			}
			if !ok {
				seen[lower] = e.Name()
			}
			rel := e.Name()
			if cur.rel != "" {
				rel = cur.rel + "/" + e.Name()
			}
			if e.IsDir() {
				if !strings.HasPrefix(e.Name(), ".") && !skipDirs[lower] {
					stack = append(stack, frame{filepath.Join(cur.folder, e.Name()), rel})
				}
				continue
			}
			kind := MediaKind(e.Name())
			if kind == "" {
				continue
			}
			var info os.FileInfo
			if e.Type()&os.ModeSymlink != 0 { // a linked file is followed, a linked folder is not
				info, err = os.Stat(filepath.Join(cur.folder, e.Name()))
			} else {
				info, err = e.Info()
			}
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			items = append(items, NewItem(rel, e.Name(), cur.rel, kind, info.Size(), info.ModTime().UTC().Format("2006-01-02T15:04:05Z")))
		}
	}
	return items, nil
}

// sameFile reports whether two names lead to one file or folder. When that cannot be told, it says yes: skipping a
// twin is the safe side, listing one thing twice is not.
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return true
	}
	sb, err := os.Stat(b)
	if err != nil {
		return true
	}
	return os.SameFile(sa, sb)
}

func (s *LocalSource) Reader(it Item) (Reader, error) {
	return &FileReader{path: filepath.Join(s.root, filepath.FromSlash(it.Key))}, nil
}

// S3Source reads a bucket (or a folder of one) straight through the S3 API: no rclone process, presigned URLs
// minted locally.
type S3Source struct {
	client *s3.Client
	lib    config.Library
}

func (s *S3Source) List() ([]Item, error) {
	var items []Item
	err := s.client.EachObject(s.lib.Bucket, s.lib.Prefix, "", func(o s3.Object) error {
		if strings.HasSuffix(o.Key, "/") {
			return nil
		}
		name := o.Key[strings.LastIndex(o.Key, "/")+1:]
		if kind := MediaKind(name); kind != "" {
			rel := o.Key[len(s.lib.Prefix):]
			dir := ""
			if i := strings.LastIndex(rel, "/"); i >= 0 {
				dir = rel[:i]
			}
			items = append(items, NewItem(o.Key, name, dir, kind, o.Size, o.MTime))
		}
		return nil
	})
	return items, err
}

func (s *S3Source) Reader(it Item) (Reader, error) {
	u, err := s.client.Presign("GET", s.lib.Bucket, it.Key, 7200, nil)
	if err != nil {
		return nil, err
	}
	return &S3Reader{client: s.client, bucket: s.lib.Bucket, key: it.Key, url: u}, nil
}

// RcloneSource is the older way: an rclone remote.
type RcloneSource struct {
	tools Tools
	lib   config.Library
}

func (s *RcloneSource) rclone(timeout time.Duration, args ...string) (string, error) {
	r, err := proc.Run(timeout, nil, s.tools.Rclone, args...)
	if err != nil {
		return "", err
	}
	if r.ExitCode != 0 {
		msg := strings.TrimSpace(string(r.Stderr))
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return "", errors.New(msg)
	}
	return string(r.Stdout), nil // raw UTF-8: the console codec would mangle CJK names
}

func (s *RcloneSource) List() ([]Item, error) {
	out, err := s.rclone(900*time.Second, "lsjson", "-R", "--files-only", "--fast-list", "--no-mimetype", "--use-server-modtime", config.Location(s.lib))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Path, Name string
		Size       int64
		ModTime    string
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, err
	}
	var items []Item
	for _, r := range rows {
		if kind := MediaKind(r.Name); kind != "" {
			dir := ""
			if i := strings.LastIndex(r.Path, "/"); i >= 0 {
				dir = r.Path[:i]
			}
			mt := r.ModTime
			if len(mt) > 19 {
				mt = mt[:19]
			}
			items = append(items, NewItem(s.lib.Prefix+r.Path, r.Name, dir, kind, r.Size, mt+"Z"))
		}
	}
	return items, nil
}

func (s *RcloneSource) Reader(it Item) (Reader, error) {
	u, err := s.link(it.Key, 2*time.Hour)
	if err != nil {
		return nil, err
	}
	return &HTTPReader{url: u}, nil
}

func (s *RcloneSource) link(key string, expire time.Duration) (string, error) {
	out, err := s.rclone(60*time.Second, "link", "--expire", fmt.Sprintf("%ds", int(expire.Seconds())), s.lib.Remote+s.lib.Bucket+"/"+key)
	return strings.TrimSpace(out), err
}

// MakeSource opens the right kind of source for a library.
func MakeSource(cfg *config.Config, clients *config.Clients, lib config.Library) (Source, error) {
	switch lib.Type {
	case "local":
		return &LocalSource{root: config.LocalRoot(lib)}, nil
	case "s3":
		c, err := clients.Get(lib.Connection)
		if err != nil {
			return nil, err
		}
		return &S3Source{client: c, lib: lib}, nil
	}
	return &RcloneSource{tools: ToolsOf(cfg), lib: lib}, nil
}

// ToolsOf reads the external program names from the config.
func ToolsOf(cfg *config.Config) Tools {
	s := cfg.Settings()
	return Tools{FFmpeg: orDefault(s.FFmpeg, "ffmpeg"), FFprobe: orDefault(s.FFprobe, "ffprobe"), Rclone: orDefault(s.Rclone, "rclone"), Quality: s.ThumbQuality}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// Presign makes a URL for one object of an rclone or s3 library, valid for the given time.
func Presign(cfg *config.Config, clients *config.Clients, lib config.Library, key string, expire time.Duration) (string, error) {
	if lib.Type == "s3" {
		c, err := clients.Get(lib.Connection)
		if err != nil {
			return "", err
		}
		return c.Presign("GET", lib.Bucket, key, int(expire.Seconds()), nil)
	}
	return (&RcloneSource{tools: ToolsOf(cfg), lib: lib}).link(key, expire)
}
