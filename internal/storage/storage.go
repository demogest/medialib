// Package storage holds the object-store operations behind the Storage view: browse, search, upload, copy, move,
// delete, properties. Pure logic on top of the S3 client; the HTTP layer only translates requests into these calls.
package storage

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"sync"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/s3"
	"github.com/demogest/medialib/internal/tasks"
)

const (
	partSize      = 16 << 20 // upload part size: big enough to be efficient, small enough to hold a few in memory
	crossChunk    = 16 << 20
	maxScan       = 50_000 // keys a name search looks through before giving up
	inFlight      = 3      // parts uploaded at once
	crossInFlight = 4      // parts of one object travelling between two connections at once
)

// GuessType is the content type for a file name.
func GuessType(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(name))); t != "" {
		if i := strings.Index(t, ";"); i > 0 {
			t = t[:i]
		}
		return t
	}
	return "application/octet-stream"
}

func checkKey(key string) error {
	switch {
	case strings.Trim(key, "/") == "":
		return config.Errorf("The object name is empty.")
	case len(key) > 1024:
		return config.Errorf("Object names are limited to 1024 bytes.")
	}
	return nil
}

func leaf(key string) string {
	key = strings.TrimRight(key, "/")
	return key[strings.LastIndex(key, "/")+1:]
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Storage runs operations against the configured connections.
type Storage struct {
	Clients *config.Clients
	Tasks   *tasks.Runner
}

// ---------------------------------------------------------------- browsing

func (s *Storage) Buckets(conn string) ([]s3.Bucket, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	bs, err := c.ListBuckets()
	if err != nil {
		return nil, err
	}
	sortBuckets(bs)
	return bs, nil
}

func sortBuckets(bs []s3.Bucket) {
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && bs[j].Name < bs[j-1].Name; j-- {
			bs[j], bs[j-1] = bs[j-1], bs[j]
		}
	}
}

func (s *Storage) List(conn, bucket, prefix, token string, limit int, delimiter string) (*s3.Page, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	page, err := c.ListObjects(bucket, prefix, delimiter, token, max(1, min(limit, 1000)))
	if err != nil {
		return nil, err
	}
	// Zero-byte "folder marker" objects (key ends with "/") would otherwise show up as an empty file named "".
	kept := page.Objects[:0]
	for _, o := range page.Objects {
		if o.Key != prefix || !strings.HasSuffix(o.Key, "/") {
			kept = append(kept, o)
		}
	}
	page.Objects = kept
	return page, nil
}

// SearchResult is the answer of a name search.
type SearchResult struct {
	Objects   []s3.Object `json:"objects"`
	Scanned   int         `json:"scanned"`
	Truncated bool        `json:"truncated"`
}

var errStop = errors.New("stop")

// Search looks through everything under a prefix for names containing text (case-insensitive).
func (s *Storage) Search(conn, bucket, prefix, text string, limit int) (*SearchResult, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(text)
	res := &SearchResult{Objects: []s3.Object{}}
	err = c.EachObject(bucket, prefix, "", func(o s3.Object) error {
		res.Scanned++
		if strings.Contains(strings.ToLower(o.Key[len(prefix):]), needle) && !strings.HasSuffix(o.Key, "/") {
			res.Objects = append(res.Objects, o)
			if len(res.Objects) >= limit {
				res.Truncated = true
				return errStop
			}
		}
		if res.Scanned >= maxScan {
			res.Truncated = true
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return nil, err
	}
	return res, nil
}

func (s *Storage) Head(conn, bucket, key string) (*s3.Info, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	info, err := c.HeadObject(bucket, key)
	if err != nil {
		return nil, err
	}
	switch info.ContentType {
	case "", "binary/octet-stream", "application/octet-stream":
		info.GuessedType = GuessType(key)
	}
	return info, nil
}

func (s *Storage) MakeFolder(conn, bucket, prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/") + "/"
	if prefix == "/" {
		return "", config.Errorf("Enter a folder name.")
	}
	c, err := s.Clients.Get(conn)
	if err != nil {
		return "", err
	}
	_, err = c.PutObject(bucket, prefix, nil, "application/x-directory", nil, nil)
	return prefix, err
}

// ---------------------------------------------------------------- properties

// SetProperties rewrites an object's headers and metadata in place (S3 can only do this by copying it onto itself).
func (s *Storage) SetProperties(conn, bucket, key string, contentType *string, metadata map[string]string, hasMeta bool, cacheControl, contentDisposition *string) (*s3.Info, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	cur, err := c.HeadObject(bucket, key)
	if err != nil {
		return nil, err
	}
	extra := map[string]string{}
	cc, cd := cur.CacheControl, cur.ContentDisposition
	if cacheControl != nil {
		cc = *cacheControl
	}
	if contentDisposition != nil {
		cd = *contentDisposition
	}
	if cc != "" {
		extra["Cache-Control"] = cc
	}
	if cd != "" {
		extra["Content-Disposition"] = cd
	}
	if cur.ContentEncoding != "" {
		extra["Content-Encoding"] = cur.ContentEncoding
	}
	ctype := cur.ContentType
	if contentType != nil && *contentType != "" {
		ctype = *contentType
	}
	if ctype == "" {
		ctype = GuessType(key)
	}
	meta := cur.Metadata
	if hasMeta {
		meta = metadata
	}
	if err := c.CopyObject(bucket, key, bucket, key, cur.Size, ctype, meta, true, extra); err != nil {
		return nil, err
	}
	return c.HeadObject(bucket, key)
}

// ---------------------------------------------------------------- upload

// Upload stores length bytes read from r. Large bodies go up as a multipart upload, a few parts at a time.
func (s *Storage) Upload(conn, bucket, key string, length int64, r io.Reader, contentType string, overwrite bool) (map[string]any, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if !overwrite {
		_, herr := c.HeadObject(bucket, key)
		switch {
		case herr == nil:
			return nil, config.Errorf("“%s” already exists.", key)
		case !s3.IsStatus(herr, 404):
			return nil, herr
		}
	}
	if contentType == "" {
		contentType = GuessType(key)
	}
	if length <= partSize {
		data, err := readExact(r, length)
		if err != nil {
			return nil, err
		}
		etag, err := c.PutObject(bucket, key, data, contentType, nil, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "size": length, "etag": etag}, nil
	}
	part, _ := s3.PlanParts(length, partSize)
	uploadID, err := c.CreateMultipart(bucket, key, contentType, nil, nil)
	if err != nil {
		return nil, err
	}
	var (
		mu       sync.Mutex
		parts    []s3.Part
		firstErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, inFlight) // backpressure: the browser is not read faster than the store takes parts
	)
	fail := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	failed := func() bool { mu.Lock(); defer mu.Unlock(); return firstErr != nil }
	var sent int64
	for number := 1; sent < length && !failed(); number++ {
		sem <- struct{}{}
		chunk, rerr := readExact(r, min(part, length-sent))
		if rerr != nil {
			<-sem
			fail(rerr)
			break
		}
		sent += int64(len(chunk))
		wg.Add(1)
		go func(n int, data []byte) {
			defer wg.Done()
			defer func() { <-sem }()
			etag, err := c.UploadPart(bucket, key, uploadID, n, data)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			parts = append(parts, s3.Part{Number: n, ETag: etag})
		}(number, chunk)
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = c.CompleteMultipart(bucket, key, uploadID, parts)
	}
	if firstErr != nil {
		c.AbortMultipart(bucket, key, uploadID)
		return nil, firstErr
	}
	return map[string]any{"key": key, "size": length, "etag": ""}, nil
}

func readExact(r io.Reader, n int64) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, errors.New("The upload ended before all bytes arrived.")
	}
	return buf, nil
}

// ---------------------------------------------------------------- tasks

func (s *Storage) Delete(conn, bucket string, keys, prefixes []string) (*tasks.Task, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	what := plural(len(keys), "object")
	if len(prefixes) > 0 {
		what += " and " + plural(len(prefixes), "folder")
	}
	return s.Tasks.Start("delete", fmt.Sprintf("Delete %s from %s", what, bucket), func(t *tasks.Task) error {
		t.SetTotal(int64(len(keys)))
		for i := 0; i < len(keys); i += 1000 {
			if err := t.Check(); err != nil {
				return err
			}
			batch := keys[i:min(i+1000, len(keys))]
			t.Line(fmt.Sprintf("Deleting %d objects", len(batch)))
			if err := flush(c, bucket, batch, t); err != nil {
				return err
			}
		}
		for _, prefix := range prefixes {
			t.Line("Deleting everything under " + prefix)
			var batch []string
			err := c.EachObject(bucket, prefix, "", func(o s3.Object) error {
				if err := t.Check(); err != nil {
					return err
				}
				batch = append(batch, o.Key)
				t.AddTotal(1)
				if len(batch) >= 1000 {
					err := flush(c, bucket, batch, t)
					batch = nil
					return err
				}
				return nil
			})
			if err != nil {
				return err
			}
			if len(batch) > 0 {
				if err := flush(c, bucket, batch, t); err != nil {
					return err
				}
			}
			_ = c.DeleteObject(bucket, prefix) // the folder's own marker, if there is one
		}
		t.Line(fmt.Sprintf("Deleted %d of %d", t.Done(), t.Snapshot().Total))
		return nil
	}), nil
}

func flush(c *s3.Client, bucket string, batch []string, t *tasks.Task) error {
	deleted, failed, err := c.DeleteObjects(bucket, batch)
	t.AddDone(int64(len(deleted)))
	for _, f := range failed {
		msg := f.Message
		if msg == "" {
			msg = f.Code
		}
		t.Fail("%s: %s", f.Key, msg)
	}
	return err
}

// Measure counts the objects and bytes under a prefix.
func (s *Storage) Measure(conn, bucket, prefix string) (*tasks.Task, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	return s.Tasks.Start("size", fmt.Sprintf("Size of %s/%s", bucket, prefix), func(t *tasks.Task) error {
		var n, size int64
		err := c.EachObject(bucket, prefix, "", func(o s3.Object) error {
			if err := t.Check(); err != nil {
				return err
			}
			n++
			size += o.Size
			if n%500 == 0 {
				t.Progress(n, size)
				t.Line(fmt.Sprintf("%d objects so far", n))
			}
			return nil
		})
		if err != nil {
			return err
		}
		t.Progress(n, size)
		t.SetTotal(n)
		t.SetResult(map[string]int64{"objects": n, "bytes": size})
		t.Line(fmt.Sprintf("%d objects", n))
		return nil
	}), nil
}

// Item is one thing to copy or move: a key, or a folder when both ends are "/"-terminated.
type Item struct{ From, To string }

type pair struct {
	from, to string
	size     int64
	known    bool
}

// Transfer copies or moves objects and folders. Within one connection the store copies server side; between
// connections the bytes pass through this machine.
func (s *Storage) Transfer(conn, bucket string, items []Item, toConn, toBucket string, move, skipExisting bool) (*tasks.Task, error) {
	if toConn == "" {
		toConn = conn
	}
	if toBucket == "" {
		toBucket = bucket
	}
	src, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	dst, err := s.Clients.Get(toConn)
	if err != nil {
		return nil, err
	}
	same := src == dst
	verb := "Copy"
	if move {
		verb = "Move"
	}
	title := fmt.Sprintf("%s %s to %s", verb, plural(len(items), "item"), toBucket)
	return s.Tasks.Start(strings.ToLower(verb), title, func(t *tasks.Task) error {
		var pairs []pair
		for _, it := range items {
			if err := t.Check(); err != nil {
				return err
			}
			a, b := it.From, it.To
			if strings.HasSuffix(a, "/") {
				if same && bucket == toBucket && (b == a || strings.HasPrefix(b, a)) {
					return config.Errorf("Can't %s “%s” into itself.", strings.ToLower(verb), a)
				}
				t.Line("Listing " + a)
				err := src.EachObject(bucket, a, "", func(o s3.Object) error {
					pairs = append(pairs, pair{o.Key, b + o.Key[len(a):], o.Size, true})
					return t.Check()
				})
				if err != nil {
					return err
				}
			} else {
				pairs = append(pairs, pair{from: a, to: b})
			}
		}
		t.SetTotal(int64(len(pairs)))
		// A moved object is removed from where it was once its copy is in place. One that cannot be is reported: it is
		// still there, and now in both places.
		removeMoved := func(keys []string) error {
			_, failed, err := src.DeleteObjects(bucket, keys)
			for _, f := range failed {
				msg := f.Message
				if msg == "" {
					msg = f.Code
				}
				t.Fail("%s: copied, but the original could not be removed: %s", f.Key, msg)
			}
			return err
		}
		var doneKeys []string
		for _, p := range pairs {
			if err := t.Check(); err != nil {
				return err
			}
			if l := leaf(p.from); l != "" {
				t.Line(l)
			} else {
				t.Line(p.from)
			}
			if same && bucket == toBucket && p.from == p.to {
				t.AddDone(1)
				continue
			}
			if skipExisting {
				_, herr := dst.HeadObject(toBucket, p.to)
				if herr == nil {
					t.Fail("%s: already exists, skipped", p.to)
					continue
				}
				if !s3.IsStatus(herr, 404) {
					if _, ok := s3.AsError(herr); ok {
						t.Fail("%s: %s", p.from, herr)
						continue
					}
					return herr
				}
			}
			cerr := copyOne(src, bucket, p, dst, toBucket, t)
			if cerr != nil {
				if errors.Is(cerr, tasks.ErrCancelled) {
					return cerr
				}
				if _, ok := s3.AsError(cerr); ok {
					t.Fail("%s: %s", p.from, cerr)
					continue
				}
				return cerr
			}
			t.AddDone(1)
			doneKeys = append(doneKeys, p.from)
			if move && len(doneKeys) >= 500 {
				if err := removeMoved(doneKeys); err != nil {
					return err
				}
				doneKeys = nil
			}
		}
		if move && len(doneKeys) > 0 {
			if err := removeMoved(doneKeys); err != nil {
				return err
			}
		}
		t.Line(fmt.Sprintf("%s finished: %d of %d", verb, t.Done(), len(pairs)))
		return nil
	}), nil
}

// copyOne copies one object and counts its bytes on the task as they arrive.
func copyOne(src *s3.Client, sb string, p pair, dst *s3.Client, db string, t *tasks.Task) error {
	if src == dst {
		size := p.size
		if !p.known {
			info, err := src.HeadObject(sb, p.from)
			if err != nil {
				return err
			}
			size = info.Size
		}
		if err := src.CopyObject(sb, p.from, db, p.to, size, "", nil, false, nil); err != nil {
			return err
		}
		t.AddBytes(size)
		return nil
	}
	info, err := src.HeadObject(sb, p.from)
	if err != nil {
		return err
	}
	ctype := info.ContentType
	if ctype == "" {
		ctype = GuessType(p.from)
	}
	headers := map[string]string{}
	for k, v := range map[string]string{"Cache-Control": info.CacheControl, "Content-Disposition": info.ContentDisposition,
		"Content-Encoding": info.ContentEncoding} {
		if v != "" {
			headers[k] = v
		}
	}
	if info.Size <= crossChunk {
		var data []byte
		if info.Size > 0 {
			if data, err = src.GetObject(sb, p.from, ""); err != nil {
				return err
			}
		}
		if _, err = dst.PutObject(db, p.to, data, ctype, info.Metadata, headers); err != nil {
			return err
		}
		t.AddBytes(info.Size)
		return nil
	}
	uploadID, err := dst.CreateMultipart(db, p.to, ctype, info.Metadata, headers)
	if err != nil {
		return err
	}
	part, _ := s3.PlanParts(info.Size, crossChunk)
	parts, err := copyParts(src, sb, p.from, dst, db, p.to, uploadID, info.Size, part, t)
	if err == nil {
		err = dst.CompleteMultipart(db, p.to, uploadID, parts)
	}
	if err != nil {
		dst.AbortMultipart(db, p.to, uploadID)
		return err
	}
	return nil
}

// copyParts moves an object's bytes into a multipart upload on another connection, a few parts at a time: one part
// is being read while others are being written, instead of the two connections taking turns.
func copyParts(src *s3.Client, sb, from string, dst *s3.Client, db, to, uploadID string, size, part int64, t *tasks.Task) ([]s3.Part, error) {
	count := int((size + part - 1) / part)
	parts := make([]s3.Part, count)
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
		next     = make(chan int)
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	failed := func() bool { mu.Lock(); defer mu.Unlock(); return firstErr != nil }
	for w := 0; w < min(crossInFlight, count); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				start := int64(i) * part
				end := min(size, start+part) - 1
				data, err := src.GetRange(sb, from, start, end)
				if err == nil && int64(len(data)) != end-start+1 {
					err = &s3.Error{Code: "ShortRead", Message: fmt.Sprintf("got %d of the %d bytes at offset %d", len(data), end-start+1, start)}
				}
				var etag string
				if err == nil {
					etag, err = dst.UploadPart(db, to, uploadID, i+1, data)
				}
				if err != nil {
					fail(err)
					continue
				}
				parts[i] = s3.Part{Number: i + 1, ETag: etag}
				t.AddBytes(int64(len(data)))
			}
		}()
	}
	for i := 0; i < count && !failed(); i++ {
		if err := t.Check(); err != nil {
			fail(err)
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	return parts, firstErr
}

// ---------------------------------------------------------------- housekeeping

func (s *Storage) IncompleteUploads(conn, bucket, prefix string) ([]s3.Upload, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return nil, err
	}
	return c.ListUploads(bucket, prefix)
}

func (s *Storage) AbortUploads(conn, bucket string, items []s3.Upload) (int, error) {
	c, err := s.Clients.Get(conn)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		c.AbortMultipart(bucket, it.Key, it.UploadID)
	}
	return len(items), nil
}
