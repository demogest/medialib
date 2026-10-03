package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func str(s string) *string { return &s }

func draft() ConnectionForm {
	return ConnectionForm{Provider: str("minio"), Endpoint: str("http://nas:9000"), AccessKey: str("AK"), SecretKey: str("SK")}
}

func isUser(err error) bool { var u *UserError; return errors.As(err, &u) }

func TestCleanFillsThePresetAndRequiresCredentials(t *testing.T) {
	rec, err := CleanConnection(draft(), nil)
	if err != nil || rec.Addressing != "path" || rec.Region != "us-east-1" || rec.Name != "MinIO" || !rec.VerifyTLS {
		t.Fatalf("%+v %v", rec, err)
	}
	for name, mod := range map[string]func(*ConnectionForm){
		"secret":   func(f *ConnectionForm) { f.SecretKey = str("") },
		"access":   func(f *ConnectionForm) { f.AccessKey = str("") },
		"endpoint": func(f *ConnectionForm) { f.Endpoint = str("") },
	} {
		f := draft()
		mod(&f)
		if _, err := CleanConnection(f, nil); !isUser(err) {
			t.Errorf("%s: want a user error, got %v", name, err)
		}
	}
}

func TestAWSPresetBuildsTheRegionalEndpoint(t *testing.T) {
	rec, err := CleanConnection(ConnectionForm{Provider: str("aws"), Region: str("eu-west-1"), AccessKey: str("AK"), SecretKey: str("SK")}, nil)
	if err != nil || rec.Endpoint != "https://s3.eu-west-1.amazonaws.com" || rec.Addressing != "virtual" {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestPlaceholderEndpointsAreRefused(t *testing.T) {
	if _, err := CleanConnection(ConnectionForm{Provider: str("r2"), AccessKey: str("AK"), SecretKey: str("SK")}, nil); !isUser(err) {
		t.Fatalf("got %v", err)
	}
}

func TestBlankSecretKeepsTheStoredOneWhenEditing(t *testing.T) {
	existing, _ := CleanConnection(draft(), nil)
	f := draft()
	f.SecretKey, f.Name = str(""), str("Renamed")
	rec, err := CleanConnection(f, &existing)
	if err != nil || rec.SecretKey != "SK" || rec.Name != "Renamed" {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestPublicViewNeverContainsTheSecret(t *testing.T) {
	f := draft()
	f.SessionToken = str("TOKEN")
	rec, _ := CleanConnection(f, nil)
	pub := Public(rec)
	if flat := strings.Join([]string{pub.AccessKey, pub.SecretKeyEnv, pub.SecretSource}, "|"); strings.Contains(flat, "SK") || strings.Contains(flat, "TOKEN") {
		t.Error("secret leaked")
	}
	if !pub.HasSecret || !pub.HasToken || pub.SecretSource != "config" {
		t.Errorf("%+v", pub)
	}
}

func TestSecretCanComeFromTheEnvironment(t *testing.T) {
	t.Setenv("MEDIALIB_TEST_SECRET", "from-env")
	f := draft()
	f.SecretKey, f.SecretKeyEnv = str(""), str("MEDIALIB_TEST_SECRET")
	rec, err := CleanConnection(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ToS3(rec).SecretKey != "from-env" || Public(rec).SecretSource != "env" {
		t.Errorf("%+v", rec)
	}
}

func newConfig(t *testing.T) *Config {
	t.Helper()
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAConnectionInUseCannotBeRemoved(t *testing.T) {
	c := newConfig(t)
	conn, err := c.AddConnection(draft())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddS3Library(conn.ID, "bucket", "x", "Lib"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveConnection(conn.ID); !isUser(err) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.RemoveLibrary("lib"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveConnection(conn.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLibrariesAreValidatedAndDeduplicated(t *testing.T) {
	c := newConfig(t)
	dir := t.TempDir()
	lib, err := c.AddLocalLibrary(dir, "")
	if err != nil || lib.Name != filepath.Base(dir) {
		t.Fatalf("%+v %v", lib, err)
	}
	if _, err := c.AddLocalLibrary(dir, "again"); !isUser(err) {
		t.Errorf("a folder can be a library once: %v", err)
	}
	if _, err := c.AddLocalLibrary(filepath.Join(dir, "missing"), ""); !isUser(err) {
		t.Errorf("a missing folder is refused: %v", err)
	}
	if _, _, err := c.UpdateLibrary("nope", LibraryEdit{}); !isUser(err) {
		t.Errorf("%v", err)
	}
	conn, _ := c.AddConnection(draft())
	s, err := c.AddS3Library(conn.ID, "media", "videos", "")
	if err != nil || s.Prefix != "videos/" || s.Name != "media/videos" {
		t.Fatalf("%+v %v", s, err)
	}
	moved, name := true, "Renamed"
	out, m, err := c.UpdateLibrary(s.ID, LibraryEdit{Name: &name, Bucket: str("media"), Prefix: str("videos")})
	if err != nil || m != !moved || out.Name != "Renamed" {
		t.Errorf("same location is not a move: %+v %v %v", out, m, err)
	}
	if _, m, _ := c.UpdateLibrary(s.ID, LibraryEdit{Bucket: str("media"), Prefix: str("other")}); !m {
		t.Error("a new prefix is a move")
	}
}

func TestConfigRoundTripKeepsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 9000, "custom_thing": {"a": [1, 2]}, "libraries": [{"id": "x", "name": "X", "type": "local", "path": "` + filepath.ToSlash(dir) + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings().Port != 9000 || c.Active() != "x" {
		t.Fatalf("%+v", c.Settings())
	}
	if err := c.RenameLibrary("x", "Renamed"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !strings.Contains(string(b), `"custom_thing"`) || !strings.Contains(string(b), `"Renamed"`) {
		t.Errorf("config lost data:\n%s", b)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"My Videos!": "my-videos", "日本語": "library", "0123456789ab": "lib-0123456789ab"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseINI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials")
	_ = os.WriteFile(p, []byte("[default]\naws_access_key_id = AK\n# comment\naws_secret_access_key=SK\n\n[profile x]\nregion = eu\n"), 0o600)
	m := parseINI(p)
	if m["default"]["aws_access_key_id"] != "AK" || m["default"]["aws_secret_access_key"] != "SK" || m["profile x"]["region"] != "eu" {
		t.Errorf("%v", m)
	}
}

func TestAutoIndexFromTheFileOrTheEnvironment(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"auto_index": 90, "libraries": [{"id": "a", "type": "local", "path": "/x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Settings().AutoIndex; got != 90 {
		t.Errorf("auto_index %d", got)
	}
	t.Setenv("MEDIALIB_AUTO_INDEX", "15")
	if got := cfg.Settings().AutoIndex; got != 15 {
		t.Errorf("MEDIALIB_AUTO_INDEX gave %d", got)
	}
	t.Setenv("MEDIALIB_AUTO_INDEX", "-5")
	if got := cfg.Settings().AutoIndex; got != 0 {
		t.Errorf("a negative interval must mean off, got %d", got)
	}
	// it survives a save
	if err := cfg.SetActive("a"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.json"))
	if !strings.Contains(string(raw), `"auto_index": 90`) {
		t.Errorf("not kept: %s", raw)
	}
}

func TestExpandVarsKeepsDollarSignsThatAreNotVariables(t *testing.T) {
	t.Setenv("MEDIALIB_TEST_ROOT", "/data")
	t.Setenv("ales", "")
	os.Unsetenv("ales")
	for in, want := range map[string]string{
		"$MEDIALIB_TEST_ROOT/videos":   "/data/videos",
		"${MEDIALIB_TEST_ROOT}/videos": "/data/videos",
		"/srv/$ales/videos":            "/srv/$ales/videos",
		"/mnt/video$/x":                "/mnt/video$/x",
		"/a/$1/$$":                     "/a/$1/$$",
	} {
		if got := ExpandVars(in); got != want {
			t.Errorf("ExpandVars(%q) = %q, want %q", in, got, want)
		}
	}
}
