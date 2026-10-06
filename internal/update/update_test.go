package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v3.2.0", "3.1.0", true},
		{"v3.2.0", "v3.2.0", false},
		{"v3.1.9", "3.2.0", false},
		{"v3.10.0", "3.9.4", true},
		{"v4.0.0", "3.99.99", true},
		{"v3.2.0", "v3.2.0-dryrun", true},      // a release is later than its pre-releases
		{"v3.2.0", "3.2.0-dev", true},          //
		{"v3.2.0", "3.3.0-dev", false},         // a build of the next version
		{"v3.2.0", "v3.2.0-4-gabc1234", false}, // git describe: commits after 3.2.0
		{"v3.2.0", "v3.2.0-dirty", false},
		{"v3.2.0", "v3.1.0-12-gabc1234-dirty", true},
		{"v3.2.0-rc2", "v3.2.0-rc1", true},
		{"v3.2.0", "dev", false}, // no telling
		{"v3.2.0", "abc1234", false},
		{"nonsense", "3.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// fakeGitHub serves a latest release with the given files, plus SHA256SUMS of them unless sums is false.
type fakeGitHub struct {
	*httptest.Server
	files   map[string][]byte
	sums    bool
	sumsOf  map[string][]byte // SHA256SUMS describes these contents instead of files
	digests map[string]string
	tag     string
}

func newGitHub(t *testing.T, tag string, files map[string][]byte, sums bool) *fakeGitHub {
	g := &fakeGitHub{files: files, sums: sums, tag: tag, digests: map[string]string{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+Repo+"/releases/latest":
			var assets []Asset
			for name, data := range g.files {
				assets = append(assets, Asset{Name: name, URL: g.URL + "/dl/" + name, Size: int64(len(data)), Digest: g.digests[name]})
			}
			if g.sums {
				assets = append(assets, Asset{Name: "SHA256SUMS", URL: g.URL + "/dl/SHA256SUMS"})
			}
			_ = json.NewEncoder(w).Encode(release{Tag: g.tag, Page: "https://example.com/r", Body: "notes", Assets: assets})
		case r.URL.Path == "/dl/SHA256SUMS":
			described := g.files
			if g.sumsOf != nil {
				described = g.sumsOf
			}
			for name, data := range described {
				sum := sha256.Sum256(data)
				fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			}
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			data, ok := g.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func tarGz(t *testing.T, name string, data []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(data)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipped(t *testing.T, name string, data []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// installedCopy is a program file named exe in a folder of its own, holding "old".
func installedCopy(t *testing.T, exe string) string {
	dir := t.TempDir()
	p := filepath.Join(dir, exe)
	if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChecksAndPicksTheDownloadForThisCopy(t *testing.T) {
	files := map[string][]byte{}
	for _, n := range []string{"medialib-3.3.0-windows-x64-setup.exe", "medialib-3.3.0-windows-x64-portable.zip", "medialib-3.3.0-linux-x64.tar.gz",
		"medialib-3.3.0-macos-arm64.tar.gz", "medialib-3.3.0-linux-arm64-server.tar.gz", "medialib-3.3.0-windows-x64-server.zip"} {
		files[n] = []byte(n)
	}
	g := newGitHub(t, "v3.3.0", files, true)
	setup := installedCopy(t, "medialib.exe")
	_ = os.WriteFile(filepath.Join(filepath.Dir(setup), "unins000.exe"), nil, 0o644)
	for _, c := range []struct {
		name, goos, goarch string
		desktop            bool
		exe                string
		want               string
		kind               Kind
	}{
		{"installed Windows app", "windows", "amd64", true, setup, "medialib-3.3.0-windows-x64-setup.exe", Installer},
		{"portable Windows app", "windows", "amd64", true, installedCopy(t, "medialib.exe"), "medialib-3.3.0-windows-x64-portable.zip", Program},
		{"Linux app", "linux", "amd64", true, installedCopy(t, "medialib"), "medialib-3.3.0-linux-x64.tar.gz", Program},
		{"macOS app", "darwin", "arm64", true, installedCopy(t, "medialib"), "medialib-3.3.0-macos-arm64.tar.gz", Program},
		{"Linux server", "linux", "arm64", false, installedCopy(t, "medialib"), "medialib-3.3.0-linux-arm64-server.tar.gz", Program},
		{"Windows server", "windows", "amd64", false, installedCopy(t, "medialib.exe"), "medialib-3.3.0-windows-x64-server.zip", Program},
	} {
		u := &Updater{Current: "3.2.0", Desktop: c.desktop, Install: true, Exe: c.exe, API: g.URL, GOOS: c.goos, GOARCH: c.goarch}
		st := u.Check(context.Background(), 0)
		if !st.Available || st.Latest != "3.3.0" || st.Asset != c.want || !st.CanInstall || u.kind != c.kind || st.Error != "" {
			t.Errorf("%s: %+v (kind %s)", c.name, st, u.kind)
		}
	}

	// No download for this computer; up to date; a server that only tells.
	u := &Updater{Current: "3.2.0", Desktop: true, Install: true, Exe: installedCopy(t, "medialib"), API: g.URL, GOOS: "linux", GOARCH: "arm64"}
	if st := u.Check(context.Background(), 0); !st.Available || st.CanInstall || !strings.Contains(st.Why, "Linux ARM64") {
		t.Errorf("no download: %+v", st)
	}
	u = &Updater{Current: "3.3.0", Desktop: true, Install: true, Exe: installedCopy(t, "medialib"), API: g.URL, GOOS: "linux", GOARCH: "amd64"}
	if st := u.Check(context.Background(), 0); st.Available || st.CanInstall {
		t.Errorf("up to date: %+v", st)
	}
	u = &Updater{Current: "3.2.0", Install: false, Exe: installedCopy(t, "medialib"), API: g.URL, GOOS: "linux", GOARCH: "arm64"}
	if st := u.Check(context.Background(), 0); !st.Available || st.CanInstall || !strings.Contains(st.Why, "server") {
		t.Errorf("server: %+v", st)
	}
	if err := u.Prepare(context.Background()); err == nil {
		t.Error("a server installed an update")
	}
}

func TestReplacesTheProgram(t *testing.T) {
	for _, sys := range []string{"linux", "windows"} {
		name, asset, archive := "medialib", "medialib-3.3.0-linux-x64.tar.gz", tarGz(t, "medialib", []byte("new"))
		if sys == "windows" {
			name, asset, archive = "medialib.exe", "medialib-3.3.0-windows-x64-portable.zip", zipped(t, "medialib.exe", []byte("new"))
		}
		g := newGitHub(t, "v3.3.0", map[string][]byte{asset: archive}, true)
		exe := installedCopy(t, name)
		u := &Updater{Current: "3.2.0", Desktop: true, Install: true, Exe: exe, Dir: t.TempDir(), API: g.URL, GOOS: sys, GOARCH: "amd64"}
		if st := u.Check(context.Background(), 0); !st.CanInstall {
			t.Fatalf("%s: %+v", sys, st)
		}
		if err := u.Prepare(context.Background()); err != nil {
			t.Fatalf("%s: %v", sys, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "new" {
			t.Errorf("%s: program holds %q", sys, b)
		}
		if st, err := os.Stat(exe); err != nil || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s: not executable: %v %v", sys, st, err)
		}
		st := u.Status()
		if !st.Ready || st.State != "ready" || st.Done != int64(len(archive)) {
			t.Errorf("%s: status %+v", sys, st)
		}
		if b, err := os.ReadFile(exe + ".old"); (sys == "windows") != (err == nil) || (err == nil && string(b) != "old") {
			t.Errorf("%s: replaced file %q %v", sys, b, err)
		}
		if again := u.Check(context.Background(), 0); !again.Ready {
			t.Errorf("%s: a check forgot the update waiting for a restart: %+v", sys, again)
		}
		u.Cleanup()
		if _, err := os.Stat(exe + ".old"); err == nil {
			t.Errorf("%s: the replaced file stays", sys)
		}
	}
}

func TestRefusesWhatItCannotVerify(t *testing.T) {
	asset := "medialib-3.3.0-linux-x64.tar.gz"
	good := tarGz(t, "medialib", []byte("new"))

	// The file served is not the one SHA256SUMS describes.
	g := newGitHub(t, "v3.3.0", map[string][]byte{asset: good}, true)
	exe := installedCopy(t, "medialib")
	u := &Updater{Current: "3.2.0", Desktop: true, Install: true, Exe: exe, Dir: t.TempDir(), API: g.URL, GOOS: "linux", GOARCH: "amd64"}
	u.Check(context.Background(), 0)
	g.sumsOf = map[string][]byte{asset: good}
	g.files[asset] = tarGz(t, "medialib", []byte("evil"))
	if err := u.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("altered download: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Errorf("the program was replaced by an unverified download: %q", b)
	}
	if st := u.Status(); st.State != "error" || st.Ready {
		t.Errorf("status after a refused download: %+v", st)
	}

	// GitHub's own digest disagrees with SHA256SUMS.
	g = newGitHub(t, "v3.3.0", map[string][]byte{asset: good}, true)
	g.digests[asset] = "sha256:" + strings.Repeat("0", 64)
	u = &Updater{Current: "3.2.0", Desktop: true, Install: true, Exe: exe, Dir: t.TempDir(), API: g.URL, GOOS: "linux", GOARCH: "amd64"}
	u.Check(context.Background(), 0)
	if err := u.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("digest mismatch: %v", err)
	}

	// No SHA256SUMS at all: not installable.
	g = newGitHub(t, "v3.3.0", map[string][]byte{asset: good}, false)
	u = &Updater{Current: "3.2.0", Desktop: true, Install: true, Exe: exe, Dir: t.TempDir(), API: g.URL, GOOS: "linux", GOARCH: "amd64"}
	if st := u.Check(context.Background(), 0); st.CanInstall || !strings.Contains(st.Why, "SHA256SUMS") {
		t.Errorf("no checksums: %+v", st)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Errorf("program changed: %q", b)
	}
}

func TestCheckErrorsAndCaching(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	u := &Updater{Current: "3.2.0", API: srv.URL}
	if st := u.Check(context.Background(), 0); st.State != "error" || !strings.Contains(st.Error, "try later") {
		t.Errorf("rate limit: %+v", st)
	}
	u.Check(context.Background(), 1<<62)
	if calls != 1 {
		t.Errorf("a fresh answer was not reused: %d calls", calls)
	}
}

func TestNotesOfEveryReleaseSinceThisCopy(t *testing.T) {
	list := []release{
		{Tag: "v3.5.0-rc1", Body: "rc", Prerelease: true},
		{Tag: "v3.4.0", Body: "four"},
		{Tag: "v3.3.1", Body: "three one", Published: "2026-09-01T00:00:00Z"},
		{Tag: "v3.6.0", Body: "draft", Draft: true},
		{Tag: "v3.3.0", Body: "three"},
		{Tag: "v3.2.0", Body: "two"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			_ = json.NewEncoder(w).Encode(list[1])
		case "/repos/" + Repo + "/releases":
			_ = json.NewEncoder(w).Encode(list)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	versions := func(rs []Release) (v []string) {
		for _, r := range rs {
			v = append(v, r.Version+":"+r.Notes)
		}
		return v
	}
	u := &Updater{Current: "3.2.0", API: srv.URL, Exe: installedCopy(t, "medialib")}
	st := u.Check(context.Background(), 0)
	if got, want := strings.Join(versions(st.Releases), ","), "3.4.0:four,3.3.1:three one,3.3.0:three"; got != want {
		t.Errorf("releases = %s, want %s", got, want)
	}
	if st.Releases[1].Published == "" {
		t.Errorf("no release date: %+v", st.Releases[1])
	}
	u = &Updater{Current: "3.4.0", API: srv.URL, Exe: installedCopy(t, "medialib")}
	if st := u.Check(context.Background(), 0); len(st.Releases) != 0 || st.Notes != "" {
		t.Errorf("up to date: notes %q, releases %v", st.Notes, st.Releases)
	}

	// No list of releases (GitHub refused it): the latest release's notes.
	g := newGitHub(t, "v3.3.0", nil, false)
	u = &Updater{Current: "3.1.0", API: g.URL, Exe: installedCopy(t, "medialib")}
	if got := strings.Join(versions(u.Check(context.Background(), 0).Releases), ","); got != "3.3.0:notes" {
		t.Errorf("no list: releases = %s", got)
	}
}
