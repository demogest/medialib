package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// UserError is a problem with what the user asked for (bad input, a name already taken ...): the API answers 400.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Errorf makes a UserError.
func Errorf(format string, a ...any) error { return &UserError{fmt.Sprintf(format, a...)} }

// Player is an extra media player from config.json.
type Player struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	TitleArg *string `json:"title_arg,omitempty"`
}

// Library is a local folder, a bucket folder read through the S3 API, or (older) an rclone remote folder.
type Library struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // local | s3 | rclone
	Path       string `json:"path,omitempty"`
	Connection string `json:"connection,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
}

// Connection is one object-storage account.
type Connection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	AccessKey     string `json:"access_key"`
	SecretKey     string `json:"secret_key,omitempty"`
	SecretKeyEnv  string `json:"secret_key_env,omitempty"`
	SessionToken  string `json:"session_token,omitempty"`
	Addressing    string `json:"addressing"`
	VerifyTLS     bool   `json:"verify_tls"`
	DefaultBucket string `json:"default_bucket"`
}

func (c *Connection) UnmarshalJSON(b []byte) error {
	type plain Connection
	p := plain{VerifyTLS: true}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Connection(p)
	return nil
}

// Settings are the plain top-level options.
type Settings struct {
	Rclone        string
	FFmpeg        string
	FFprobe       string
	Port          int
	DefaultPlayer string
	Players       []Player
	Workers       int
	Password      string
}

type data struct {
	Rclone        string       `json:"rclone"`
	FFmpeg        string       `json:"ffmpeg"`
	FFprobe       string       `json:"ffprobe"`
	Port          int          `json:"port"`
	DefaultPlayer string       `json:"default_player"`
	Players       []Player     `json:"players"`
	Active        string       `json:"active"`
	Connections   []Connection `json:"connections"`
	Libraries     []Library    `json:"libraries"`
	Workers       int          `json:"workers,omitempty"`
	Password      string       `json:"password,omitempty"`
}

// Config is config.json held in memory. It is safe for concurrent use; read it through the accessors.
type Config struct {
	mu    sync.RWMutex
	d     data
	extra map[string]json.RawMessage // keys this version does not know, kept when saving
	home  string
}

func defaultData() data {
	videos := "~/Videos"
	if runtime.GOOS == "windows" {
		videos = `%USERPROFILE%\Videos`
	}
	return data{
		Rclone: "rclone", FFmpeg: "ffmpeg", FFprobe: "ffprobe", Port: 8766, DefaultPlayer: "mpv",
		Players: []Player{}, Active: "videos", Connections: []Connection{},
		Libraries: []Library{{ID: "videos", Name: "Videos", Type: "local", Path: videos}},
	}
}

// New is an empty-default config living in home (nothing is written until a change is made).
func New(home string) *Config { return &Config{d: defaultData(), home: home} }

// Home is the folder holding config.json and cache/.
func (c *Config) Home() string { return c.home }

// CacheDir is where indexes and thumbnails live.
func (c *Config) CacheDir() string { return filepath.Join(c.home, "cache") }

// Path is the config file.
func (c *Config) Path() string { return filepath.Join(c.home, "config.json") }

// LibDir is the folder of one library's index and thumbnails.
func (c *Config) LibDir(lib Library) string { return filepath.Join(c.CacheDir(), lib.ID) }

// Load reads config.json from home, writing the defaults the first time.
func Load(home string) (*Config, error) {
	c := New(home)
	raw, err := os.ReadFile(c.Path())
	var rawMap map[string]json.RawMessage
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", c.Path(), err)
		}
	case os.IsNotExist(err):
		rawMap = map[string]json.RawMessage{}
	default:
		return nil, err
	}
	if err := c.fromMap(rawMap); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Path(), err)
	}
	_, hasLibs := rawMap["libraries"]
	_, hasBucket := rawMap["bucket"]
	if !hasLibs && hasBucket { // config from before libraries existed: a single rclone folder
		var remote, bucket, prefix string
		_ = json.Unmarshal(rawMap["remote"], &remote)
		_ = json.Unmarshal(rawMap["bucket"], &bucket)
		_ = json.Unmarshal(rawMap["prefix"], &prefix)
		lib := Library{ID: Slug(bucket + "-" + prefix), Name: strings.TrimRight(bucket+"/"+strings.Trim(prefix, "/"), "/"),
			Type: "rclone", Remote: remote, Bucket: bucket, Prefix: prefix}
		c.d.Libraries, c.d.Active = []Library{lib}, lib.ID
	}
	legacy := false
	for _, k := range []string{"remote", "bucket", "prefix"} {
		if _, ok := c.extra[k]; ok {
			delete(c.extra, k)
			legacy = true
		}
	}
	if len(c.d.Libraries) == 0 {
		c.d.Libraries = defaultData().Libraries
	}
	if legacy || !hasLibs {
		if err := c.save(); err != nil {
			return nil, err
		}
	}
	c.adoptOldCache()
	return c, nil
}

// adoptOldCache moves the cache layout from before libraries existed (cache/library.json + cache/thumbs).
func (c *Config) adoptOldCache() {
	cache := c.CacheDir()
	first := c.LibDir(c.d.Libraries[0])
	old := filepath.Join(cache, "library.json")
	if _, err := os.Stat(old); err != nil {
		return
	}
	if _, err := os.Stat(first); err == nil {
		return
	}
	if os.MkdirAll(first, 0o755) != nil {
		return
	}
	_ = os.Rename(old, filepath.Join(first, "library.json"))
	if _, err := os.Stat(filepath.Join(cache, "thumbs")); err == nil {
		_ = os.Rename(filepath.Join(cache, "thumbs"), filepath.Join(first, "thumbs"))
	}
}

func (c *Config) fromMap(m map[string]json.RawMessage) error {
	d := defaultData()
	c.extra = map[string]json.RawMessage{}
	fields := map[string]any{
		"rclone": &d.Rclone, "ffmpeg": &d.FFmpeg, "ffprobe": &d.FFprobe, "port": &d.Port, "default_player": &d.DefaultPlayer,
		"players": &d.Players, "active": &d.Active, "connections": &d.Connections, "libraries": &d.Libraries,
		"workers": &d.Workers, "password": &d.Password,
	}
	if _, ok := m["libraries"]; ok {
		d.Libraries = nil
	}
	for k, v := range m {
		dst, known := fields[k]
		if !known {
			c.extra[k] = v
			continue
		}
		if string(v) == "null" {
			continue
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return fmt.Errorf("%q: %w", k, err)
		}
	}
	if d.Players == nil {
		d.Players = []Player{}
	}
	if d.Connections == nil {
		d.Connections = []Connection{}
	}
	for i := range d.Libraries {
		if d.Libraries[i].Type == "" {
			d.Libraries[i].Type = "rclone"
		}
	}
	c.d = d
	return nil
}

// save writes config.json. The caller holds the write lock (or owns the Config exclusively).
func (c *Config) save() error {
	out := map[string]json.RawMessage{}
	for k, v := range c.extra {
		out[k] = v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c.d); err != nil {
		return err
	}
	b := buf.Bytes()
	var known map[string]json.RawMessage
	_ = json.Unmarshal(b, &known)
	for k, v := range known {
		out[k] = v
	}
	text, err := marshalOrdered(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.home, 0o755); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(c.Path(), ".json") + ".tmp"
	if err := os.WriteFile(tmp, append(text, '\n'), 0o600); err != nil { // credentials live here
		return err
	}
	return os.Rename(tmp, c.Path())
}

var keyOrder = []string{"rclone", "ffmpeg", "ffprobe", "port", "default_player", "players", "active", "connections", "libraries", "workers", "password"}

func marshalOrdered(m map[string]json.RawMessage) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("{\n")
	first := true
	emit := func(k string, v json.RawMessage) {
		if !first {
			sb.WriteString(",\n")
		}
		first = false
		kb, _ := json.Marshal(k)
		var pretty strings.Builder
		_ = indentJSON(&pretty, v)
		sb.WriteString("  " + string(kb) + ": " + strings.ReplaceAll(pretty.String(), "\n", "\n  "))
	}
	done := map[string]bool{}
	for _, k := range keyOrder {
		if v, ok := m[k]; ok {
			emit(k, v)
			done[k] = true
		}
	}
	var rest []string
	for k := range m {
		if !done[k] {
			rest = append(rest, k)
		}
	}
	sortStrings(rest)
	for _, k := range rest {
		emit(k, m[k])
	}
	sb.WriteString("\n}")
	return []byte(sb.String()), nil
}

// ---------------------------------------------------------------- accessors

// Settings returns the plain options.
func (c *Config) Settings() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pw := c.d.Password
	if env := os.Getenv("MEDIALIB_PASSWORD"); env != "" {
		pw = env
	}
	return Settings{Rclone: c.d.Rclone, FFmpeg: c.d.FFmpeg, FFprobe: c.d.FFprobe, Port: c.d.Port, DefaultPlayer: c.d.DefaultPlayer,
		Players: append([]Player(nil), c.d.Players...), Workers: c.d.Workers, Password: pw}
}

// Libraries returns a copy of the library list.
func (c *Config) Libraries() []Library {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Library(nil), c.d.Libraries...)
}

// Active is the id of the library shown by default.
func (c *Config) Active() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeLocked()
}

func (c *Config) activeLocked() string {
	if l, ok := c.findLocked(c.d.Active); ok {
		return l.ID
	}
	return c.d.Libraries[0].ID
}

func (c *Config) findLocked(id string) (Library, bool) {
	for _, l := range c.d.Libraries {
		if l.ID == id {
			return l, true
		}
	}
	return Library{}, false
}

// Library finds a library by id; an empty id means the active one.
func (c *Config) Library(id string) (Library, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id == "" {
		id = c.activeLocked()
	}
	return c.findLocked(id)
}

// Connections returns a copy of the connection list.
func (c *Config) Connections() []Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Connection(nil), c.d.Connections...)
}

// Connection finds one by id.
func (c *Config) Connection(id string) (Connection, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connLocked(id)
}

func (c *Config) connLocked(id string) (Connection, bool) {
	for _, x := range c.d.Connections {
		if x.ID == id {
			return x, true
		}
	}
	return Connection{}, false
}

// ---------------------------------------------------------------- libraries

var (
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
	hex12    = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// Slug turns a name into an id.
func Slug(text string) string {
	s := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "library"
	}
	if hex12.MatchString(s) {
		return "lib-" + s // 12 hex characters would read as a media id
	}
	return s
}

func uniqueID(base string, taken func(string) bool) string {
	id := base
	for n := 2; taken(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// LocalRoot is a local library's folder, with environment variables and ~ expanded.
func LocalRoot(lib Library) string { return ExpandVars(lib.Path) }

// Location is a library's address as text.
func Location(lib Library) string {
	switch lib.Type {
	case "local":
		return LocalRoot(lib)
	case "s3":
		return "s3://" + lib.Bucket + "/" + lib.Prefix
	}
	return lib.Remote + lib.Bucket + "/" + lib.Prefix
}

func cleanPath(p string) (string, error) {
	p = filepath.Clean(ExpandVars(strings.Trim(strings.TrimSpace(p), `"`)))
	if st, err := os.Stat(p); !filepath.IsAbs(p) || err != nil || !st.IsDir() {
		return "", Errorf("Not a reachable folder: %s", p)
	}
	return p, nil
}

func normPrefix(prefix string) string {
	prefix = strings.TrimLeft(prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

func (c *Config) libIDTaken(id string) bool {
	_, ok := c.findLocked(id)
	return ok
}

// AddLocalLibrary adds a folder (a disk or a NAS share).
func (c *Config) AddLocalLibrary(path, name string) (Library, error) {
	p, err := cleanPath(path)
	if err != nil {
		return Library{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.d.Libraries {
		if l.Type == "local" && SameFolder(filepath.Clean(LocalRoot(l)), p) {
			return Library{}, Errorf("That folder is already the library “%s”.", l.Name)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(strings.TrimRight(p, `\/`))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = p
		}
	}
	lib := Library{ID: uniqueID(Slug(name), c.libIDTaken), Name: name, Type: "local", Path: p}
	c.d.Libraries = append(c.d.Libraries, lib)
	return lib, c.save()
}

// AddS3Library adds a bucket, or a folder inside one, of a configured connection.
func (c *Config) AddS3Library(connection, bucket, prefix, name string) (Library, error) {
	prefix = normPrefix(prefix)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.connLocked(connection); !ok {
		return Library{}, Errorf("Unknown connection.")
	}
	if bucket == "" {
		return Library{}, Errorf("Pick a bucket.")
	}
	for _, l := range c.d.Libraries {
		if l.Type == "s3" && l.Connection == connection && l.Bucket == bucket && l.Prefix == prefix {
			return Library{}, Errorf("That location is already the library “%s”.", l.Name)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimRight(bucket+"/"+strings.TrimRight(prefix, "/"), "/")
	}
	lib := Library{ID: uniqueID(Slug(name), c.libIDTaken), Name: name, Type: "s3", Connection: connection, Bucket: bucket, Prefix: prefix}
	c.d.Libraries = append(c.d.Libraries, lib)
	return lib, c.save()
}

// LibraryEdit carries what the edit dialog may change; nil means "leave as is".
type LibraryEdit struct {
	Name       *string
	Path       *string
	Connection *string
	Bucket     *string
	Prefix     *string
}

// UpdateLibrary edits a library in place. moved is true when its location changed, which makes the stored index
// stale (the next indexing run drops what is no longer there and adds what is new).
func (c *Config) UpdateLibrary(id string, e LibraryEdit) (lib Library, moved bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := -1
	for i, l := range c.d.Libraries {
		if l.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return Library{}, false, Errorf("Unknown library.")
	}
	old := c.d.Libraries[idx]
	nw := old
	if e.Name != nil {
		if n := strings.TrimSpace(*e.Name); n != "" {
			nw.Name = n
		}
	}
	switch {
	case old.Type == "local" && e.Path != nil:
		p, err := cleanPath(*e.Path)
		if err != nil {
			return Library{}, false, err
		}
		nw.Path = p
	case old.Type == "s3" && e.Bucket != nil:
		conn := old.Connection
		if e.Connection != nil && *e.Connection != "" {
			conn = *e.Connection
		}
		if _, ok := c.connLocked(conn); !ok {
			return Library{}, false, Errorf("Unknown connection.")
		}
		if *e.Bucket == "" {
			return Library{}, false, Errorf("Pick a bucket.")
		}
		prefix := ""
		if e.Prefix != nil {
			prefix = *e.Prefix
		}
		nw.Connection, nw.Bucket, nw.Prefix = conn, *e.Bucket, normPrefix(prefix)
	}
	for i, o := range c.d.Libraries {
		if i != idx && o.Type == nw.Type && Location(o) == Location(nw) && o.Connection == nw.Connection {
			return Library{}, false, Errorf("That location is already the library “%s”.", o.Name)
		}
	}
	moved = Location(nw) != Location(old) || nw.Connection != old.Connection
	c.d.Libraries[idx] = nw
	return nw, moved, c.save()
}

// RemoveLibrary drops a library from the config (never touching media).
func (c *Config) RemoveLibrary(id string) (Library, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lib, ok := c.findLocked(id)
	if !ok {
		return Library{}, Errorf("unknown library")
	}
	if len(c.d.Libraries) == 1 {
		return Library{}, Errorf("The last library can't be removed.")
	}
	kept := c.d.Libraries[:0:0]
	for _, l := range c.d.Libraries {
		if l.ID != id {
			kept = append(kept, l)
		}
	}
	c.d.Libraries = kept
	if c.d.Active == id {
		c.d.Active = kept[0].ID
	}
	return lib, c.save()
}

// SetActive picks the library shown by default.
func (c *Config) SetActive(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.findLocked(id); !ok {
		return Errorf("unknown library")
	}
	c.d.Active = id
	return c.save()
}

// RenameLibrary renames a library.
func (c *Config) RenameLibrary(id, name string) error {
	name = strings.TrimSpace(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.d.Libraries {
		if c.d.Libraries[i].ID == id && name != "" {
			c.d.Libraries[i].Name = name
			return c.save()
		}
	}
	return Errorf("Enter a name.")
}

// ConvertToS3 turns an rclone library into one read through the S3 API, on the given connection.
func (c *Config) ConvertToS3(id, connection string) (Library, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.d.Libraries {
		if c.d.Libraries[i].ID == id {
			l := &c.d.Libraries[i]
			l.Remote, l.Type, l.Connection = "", "s3", connection
			return *l, c.save()
		}
	}
	return Library{}, Errorf("unknown library")
}

// DefaultWorkers is how many files are in flight at once: S3 waits mostly on the network, a local folder on
// decoding and the disk.
func DefaultWorkers(lib Library) int {
	cpus := runtime.NumCPU()
	if lib.Type == "local" {
		return max(4, cpus/2)
	}
	return min(32, max(8, cpus*2))
}
