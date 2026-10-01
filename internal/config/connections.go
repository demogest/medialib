package config

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/demogest/medialib/internal/proc"
	"github.com/demogest/medialib/internal/s3"
)

// SecretOf is a connection's secret: stored in config.json, or read from the environment variable it names.
func SecretOf(c Connection) string {
	if c.SecretKey != "" {
		return c.SecretKey
	}
	if c.SecretKeyEnv != "" {
		return os.Getenv(c.SecretKeyEnv)
	}
	return ""
}

// PublicConnection is a connection as the browser may see it: everything but the secret.
type PublicConnection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	AccessKey     string `json:"access_key"`
	SecretKeyEnv  string `json:"secret_key_env"`
	Addressing    string `json:"addressing"`
	DefaultBucket string `json:"default_bucket"`
	VerifyTLS     bool   `json:"verify_tls"`
	HasSecret     bool   `json:"has_secret"`
	HasToken      bool   `json:"has_token"`
	SecretSource  string `json:"secret_source"`
}

// Public strips the secret from a connection.
func Public(c Connection) PublicConnection {
	src := ""
	switch {
	case c.SecretKey != "":
		src = "config"
	case c.SecretKeyEnv != "":
		src = "env"
	}
	return PublicConnection{ID: c.ID, Name: c.Name, Provider: c.Provider, Endpoint: c.Endpoint, Region: c.Region, AccessKey: c.AccessKey,
		SecretKeyEnv: c.SecretKeyEnv, Addressing: c.Addressing, DefaultBucket: c.DefaultBucket, VerifyTLS: c.VerifyTLS,
		HasSecret: SecretOf(c) != "", HasToken: c.SessionToken != "", SecretSource: src}
}

// ToS3 is what the S3 client needs to reach the connection.
func ToS3(c Connection) s3.Connection {
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	addr := c.Addressing
	if addr == "" {
		addr = "path"
	}
	return s3.Connection{Endpoint: c.Endpoint, AccessKey: c.AccessKey, SecretKey: SecretOf(c), Region: region,
		SessionToken: c.SessionToken, Addressing: addr, VerifyTLS: c.VerifyTLS}
}

// ConnectionForm is what the connection form (or the CLI) submits. Nil/absent fields keep the stored value.
type ConnectionForm struct {
	ID            string
	Name          *string
	Provider      *string
	Endpoint      *string
	Region        *string
	AccessKey     *string
	SecretKey     *string
	SecretKeyEnv  *string
	SessionToken  *string
	Addressing    *string
	VerifyTLS     *bool
	DefaultBucket *string
	ClearSecret   bool
}

func pick(v *string, old string) string {
	if v != nil {
		return strings.TrimSpace(*v)
	}
	return strings.TrimSpace(old)
}

// CleanConnection validates form data into a connection record. A blank secret keeps the stored one when editing.
func CleanConnection(f ConnectionForm, existing *Connection) (Connection, error) {
	var old Connection
	if existing != nil {
		old = *existing
	} else {
		old.VerifyTLS = true
	}
	out := Connection{
		ID: old.ID, Name: pick(f.Name, old.Name), Provider: pick(f.Provider, old.Provider), Endpoint: pick(f.Endpoint, old.Endpoint),
		Region: pick(f.Region, old.Region), AccessKey: pick(f.AccessKey, old.AccessKey), SecretKey: pick(f.SecretKey, old.SecretKey),
		SecretKeyEnv: pick(f.SecretKeyEnv, old.SecretKeyEnv), SessionToken: pick(f.SessionToken, old.SessionToken),
		Addressing: pick(f.Addressing, old.Addressing), DefaultBucket: pick(f.DefaultBucket, old.DefaultBucket), VerifyTLS: old.VerifyTLS,
	}
	if f.VerifyTLS != nil {
		out.VerifyTLS = *f.VerifyTLS
	}
	// A blank secret field means "keep what is stored" unless the form asked to clear it.
	if f.SecretKey != nil && *f.SecretKey == "" && !f.ClearSecret {
		out.SecretKey = old.SecretKey
	}
	if f.SessionToken != nil && *f.SessionToken == "" && !f.ClearSecret {
		out.SessionToken = old.SessionToken
	}
	preset, ok := s3.ProviderByID(out.Provider)
	if !ok {
		out.Provider = "other"
		preset, _ = s3.ProviderByID("other")
	}
	if out.Region == "" {
		out.Region = preset.Region
	}
	switch out.Addressing {
	case "path", "virtual", "auto":
	default:
		out.Addressing = preset.Addressing
	}
	out.Endpoint = strings.ReplaceAll(out.Endpoint, "{region}", out.Region)
	if out.Endpoint == "" {
		out.Endpoint = strings.ReplaceAll(preset.Endpoint, "{region}", out.Region)
	}
	switch {
	case strings.Contains(out.Endpoint, "<"):
		return out, Errorf("Replace the placeholder in the endpoint with your account's own.")
	case out.Endpoint == "":
		return out, Errorf("Enter the endpoint URL of the store.")
	}
	if _, _, _, err := s3.ParseEndpoint(out.Endpoint, out.Region); err != nil {
		return out, &UserError{err.Error()}
	}
	switch {
	case out.AccessKey == "":
		return out, Errorf("Enter an access key.")
	case out.SecretKey == "" && out.SecretKeyEnv == "":
		return out, Errorf("Enter the secret key (or the name of an environment variable that holds it).")
	}
	if out.Name == "" {
		out.Name = preset.Label
	}
	return out, nil
}

// AddConnection validates and stores a new connection.
func (c *Config) AddConnection(f ConnectionForm) (Connection, error) {
	rec, err := CleanConnection(f, nil)
	if err != nil {
		return Connection{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	base := f.ID
	if base == "" {
		base = rec.Name
	}
	rec.ID = uniqueID(Slug(base), func(id string) bool { _, ok := c.connLocked(id); return ok })
	c.d.Connections = append(c.d.Connections, rec)
	return rec, c.save()
}

// UpdateConnection edits a connection.
func (c *Config) UpdateConnection(id string, f ConnectionForm) (Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.d.Connections {
		if x.ID == id {
			rec, err := CleanConnection(f, &x)
			if err != nil {
				return Connection{}, err
			}
			rec.ID = id
			c.d.Connections[i] = rec
			return rec, c.save()
		}
	}
	return Connection{}, Errorf("Unknown connection.")
}

// RemoveConnection deletes a connection no library uses.
func (c *Config) RemoveConnection(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var users []string
	for _, l := range c.d.Libraries {
		if l.Connection == id {
			users = append(users, l.Name)
		}
	}
	if len(users) > 0 {
		return Errorf("Libraries still use this connection: %s", strings.Join(users, ", "))
	}
	kept := make([]Connection, 0, len(c.d.Connections))
	for _, x := range c.d.Connections {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	c.d.Connections = kept
	return c.save()
}

// ---------------------------------------------------------------- client cache

// Clients keeps one S3 client per connection, rebuilt when its settings change.
type Clients struct {
	cfg     *Config
	mu      sync.Mutex
	clients map[string]cached
}

type cached struct {
	conn   s3.Connection
	client *s3.Client
}

// NewClients makes an empty cache over cfg.
func NewClients(cfg *Config) *Clients { return &Clients{cfg: cfg, clients: map[string]cached{}} }

// Get returns the client for a connection id.
func (cl *Clients) Get(id string) (*s3.Client, error) {
	rec, ok := cl.cfg.Connection(id)
	if !ok {
		return nil, Errorf("Unknown connection “%s”.", id)
	}
	conn := ToS3(rec)
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if c, ok := cl.clients[id]; ok {
		if c.conn == conn {
			return c.client, nil
		}
		c.client.Close()
	}
	client, err := s3.New(conn)
	if err != nil {
		return nil, &UserError{err.Error()}
	}
	cl.clients[id] = cached{conn, client}
	return client, nil
}

// Drop forgets a connection's client (after an edit or removal).
func (cl *Clients) Drop(id string) {
	cl.mu.Lock()
	c, ok := cl.clients[id]
	delete(cl.clients, id)
	cl.mu.Unlock()
	if ok {
		c.client.Close()
	}
}

// ---------------------------------------------------------------- testing a connection

// TestReport is the answer of a connection test.
type TestReport struct {
	OK        bool     `json:"ok"`
	Buckets   []string `json:"buckets"`
	LatencyMS *int64   `json:"latency_ms"`
	Message   string   `json:"message"`
	Code      string   `json:"code,omitempty"`
	Status    *int     `json:"status,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// TestConnection tries the connection: list buckets (or, when the key may not list them, look at one).
func TestConnection(client *s3.Client, bucket string) TestReport {
	started := time.Now()
	rep := TestReport{}
	var err error
	if bucket != "" {
		_, err = client.ListObjects(bucket, "", "/", "", 1)
	} else {
		var bs []s3.Bucket
		if bs, err = client.ListBuckets(); err == nil {
			rep.Buckets = []string{}
			for _, b := range bs {
				rep.Buckets = append(rep.Buckets, b.Name)
			}
		}
	}
	if err == nil {
		ms := time.Since(started).Milliseconds()
		rep.OK, rep.LatencyMS, rep.Message = true, &ms, "Connected."
		return rep
	}
	rep.Message = err.Error()
	if e, ok := s3.AsError(err); ok {
		rep.Code, rep.Status = e.Code, &e.Status
		if (e.Code == "AccessDenied" || e.Code == "AllAccessDisabled") && bucket == "" {
			rep.Hint = "The key is valid but may not list buckets. Set a default bucket on the connection."
			rep.OK = e.Status == 403
		}
	}
	return rep
}

// ---------------------------------------------------------------- finding credentials you already have

// Draft is a connection that could be created from credentials found on this computer.
type Draft struct {
	Source       string `json:"source"`
	Label        string `json:"label"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	AccessKey    string `json:"access_key"`
	Addressing   string `json:"addressing"`
	secretKey    string
	sessionToken string
}

func (d Draft) form() ConnectionForm {
	return ConnectionForm{Name: &d.Name, Provider: &d.Provider, Endpoint: &d.Endpoint, Region: &d.Region, AccessKey: &d.AccessKey,
		SecretKey: &d.secretKey, SessionToken: &d.sessionToken, Addressing: &d.Addressing}
}

func rcloneRemotes(s Settings) []Draft {
	bin := s.Rclone
	if bin == "" {
		bin = "rclone"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "config", "dump")
	proc.Hide(cmd)
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	var dump map[string]map[string]string
	if json.Unmarshal(raw, &dump) != nil {
		return nil
	}
	names := make([]string, 0, len(dump))
	for n := range dump {
		names = append(names, n)
	}
	sortStrings(names)
	providers := map[string]string{"AWS": "aws", "Minio": "minio", "Cloudflare": "r2", "Wasabi": "wasabi", "Alibaba": "oss",
		"TencentCOS": "cos", "DigitalOcean": "spaces", "Backblaze": "b2", "GCS": "gcs"}
	var out []Draft
	for _, name := range names {
		c := dump[name]
		if c["type"] != "s3" {
			continue
		}
		provider := providers[c["provider"]]
		if provider == "" {
			provider = "other"
		}
		region := c["region"]
		if region == "" {
			region = "us-east-1"
		}
		addr := "virtual"
		if c["force_path_style"] != "false" && provider != "aws" {
			addr = "path"
		}
		out = append(out, Draft{Source: "rclone:" + name, Label: "rclone remote " + name, Name: name, Provider: provider,
			Endpoint: c["endpoint"], Region: region, AccessKey: c["access_key_id"], secretKey: c["secret_access_key"],
			sessionToken: c["session_token"], Addressing: addr})
	}
	return out
}

// parseINI reads the small INI dialect of ~/.aws files.
func parseINI(path string) map[string]map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]map[string]string{}
	var cur map[string]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			name := strings.TrimSpace(line[1 : len(line)-1])
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			cur = out[name]
		case cur != nil:
			if k, v, ok := strings.Cut(line, "="); ok {
				cur[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

func awsProfiles() []Draft {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".aws")
	credsPath, confPath := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), os.Getenv("AWS_CONFIG_FILE")
	if credsPath == "" {
		credsPath = filepath.Join(dir, "credentials")
	}
	if confPath == "" {
		confPath = filepath.Join(dir, "config")
	}
	creds, conf := parseINI(credsPath), parseINI(confPath)
	names := make([]string, 0, len(creds))
	for n := range creds {
		names = append(names, n)
	}
	sortStrings(names)
	var out []Draft
	for _, name := range names {
		c := creds[name]
		if c["aws_access_key_id"] == "" {
			continue
		}
		cs := conf[name]
		if cs == nil {
			cs = conf["profile "+name]
		}
		endpoint := cs["endpoint_url"]
		region := cs["region"]
		if region == "" {
			region = "us-east-1"
		}
		provider, addr := "aws", "virtual"
		if endpoint != "" {
			provider, addr = "other", "path"
		}
		out = append(out, Draft{Source: "aws:" + name, Label: "AWS profile " + name, Name: name, Provider: provider, Endpoint: endpoint,
			Region: region, AccessKey: c["aws_access_key_id"], secretKey: c["aws_secret_access_key"], sessionToken: c["aws_session_token"], Addressing: addr})
	}
	return out
}

func environmentDraft() []Draft {
	key, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if key == "" || secret == "" {
		return nil
	}
	endpoint := os.Getenv("AWS_ENDPOINT_URL_S3")
	if endpoint == "" {
		endpoint = os.Getenv("AWS_ENDPOINT_URL")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}
	provider, addr := "aws", "virtual"
	if endpoint != "" {
		provider, addr = "other", "path"
	}
	return []Draft{{Source: "env", Label: "Environment variables (AWS_ACCESS_KEY_ID …)", Name: "Environment", Provider: provider,
		Endpoint: endpoint, Region: region, AccessKey: key, secretKey: secret, sessionToken: os.Getenv("AWS_SESSION_TOKEN"), Addressing: addr}}
}

// Importable lists everything on this computer that could become a connection. Secrets stay inside the drafts.
func (c *Config) Importable() []Draft {
	out := rcloneRemotes(c.Settings())
	out = append(out, awsProfiles()...)
	return append(out, environmentDraft()...)
}

// RcloneRemote finds an rclone S3 remote by name, for converting an older library.
func (c *Config) RcloneRemote(name string) (Draft, bool) {
	for _, d := range rcloneRemotes(c.Settings()) {
		if d.Name == name && strings.HasPrefix(d.Source, "rclone:") {
			return d, true
		}
	}
	return Draft{}, false
}

// Adopt makes a connection out of one of Importable's drafts (or reuses the one already made from it).
func (c *Config) Adopt(source string) (Connection, error) {
	return c.adoptDraft(func() (Draft, bool) {
		for _, d := range c.Importable() {
			if d.Source == source {
				return d, true
			}
		}
		return Draft{}, false
	})
}

// AdoptDraft is Adopt for a draft already in hand.
func (c *Config) AdoptDraft(d Draft) (Connection, error) {
	return c.adoptDraft(func() (Draft, bool) { return d, true })
}

func (c *Config) adoptDraft(find func() (Draft, bool)) (Connection, error) {
	d, ok := find()
	if !ok {
		return Connection{}, Errorf("That credential source is no longer available.")
	}
	for _, x := range c.Connections() {
		if x.Endpoint == d.Endpoint && x.AccessKey == d.AccessKey {
			return x, nil
		}
	}
	return c.AddConnection(d.form())
}
