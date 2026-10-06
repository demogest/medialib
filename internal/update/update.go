// Package update finds out whether a newer release of medialib is out, and installs it: a program file replaces
// itself with the one from the new release (a portable copy on Windows, macOS, Linux), a copy that Windows setup
// installed runs the new setup. Every download is checked against the release's SHA256SUMS before it is used.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where medialib is released.
const Repo = "demogest/medialib"

// WaitEnv names the process a restarted copy waits for: the one it replaces, which must be gone first (it holds
// the port, the window and its single-instance lock).
const WaitEnv = "MEDIALIB_WAIT_PID"

const maxDownload = 512 << 20

// Kind is how a copy of medialib is updated.
type Kind string

const (
	Installer Kind = "installer" // installed by Windows setup: the new setup updates it
	Program   Kind = "program"   // a program file: replaced by the one in the new release's archive
)

// Asset is a file of a release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>" (GitHub fills it in for new uploads)
}

type release struct {
	Tag        string  `json:"tag_name"`
	Page       string  `json:"html_url"`
	Body       string  `json:"body"`
	Published  string  `json:"published_at"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Status is what the Settings page shows about updates.
type Status struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`           // Latest is newer than Current
	Page       string    `json:"page,omitempty"`      // the release's page
	Notes      string    `json:"notes,omitempty"`     // its notes (Markdown)
	Published  string    `json:"published,omitempty"` // when it came out
	Releases   []Release `json:"releases,omitempty"`  // what changed: every release after Current up to Latest, newest first; Latest alone when there is no update
	Asset      string    `json:"asset,omitempty"`     // the download that updates this copy
	Size       int64     `json:"size,omitempty"`
	CanInstall bool      `json:"can_install"`   // this copy can update itself to Latest
	Why        string    `json:"why,omitempty"` // why it cannot
	State      string    `json:"state"`         // idle | downloading | ready | error
	Done       int64     `json:"done,omitempty"`
	Total      int64     `json:"total,omitempty"`
	Ready      bool      `json:"ready"` // the update is in place, or its setup is downloaded: a restart finishes it
	Checked    string    `json:"checked,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Release is a published version and its notes.
type Release struct {
	Version   string `json:"version"`
	Published string `json:"published,omitempty"`
	Page      string `json:"page,omitempty"`
	Notes     string `json:"notes,omitempty"` // Markdown
}

// maxReleases bounds the notes an update shows: a copy many versions behind gets the latest ones.
const maxReleases = 20

// Updater checks for and installs updates of the program it runs in. It is safe for concurrent use.
type Updater struct {
	Current string       // this program's version
	Desktop bool         // a desktop build: it updates from the desktop downloads, a plain build from the server ones
	Install bool         // it may install updates (the desktop app); otherwise it only tells there is one
	Exe     string       // this program's file
	Dir     string       // where downloads wait
	API     string       // GitHub's API; tests point it at their own server
	Client  *http.Client // nil: http.DefaultClient
	GOOS    string       // the system to update for; tests set it
	GOARCH  string

	mu      sync.Mutex
	st      Status
	checked time.Time
	asset   *Asset
	sums    *Asset
	kind    Kind
	staged  string // a downloaded setup program, waiting to run
	busy    bool
}

// New is an updater for the running program. dir is where downloads wait.
func New(current string, desktop, install bool, dir string) *Updater {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return &Updater{Current: current, Desktop: desktop, Install: install, Exe: exe, Dir: dir}
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

func (u *Updater) goos() string   { return or(u.GOOS, runtime.GOOS) }
func (u *Updater) goarch() string { return or(u.GOARCH, runtime.GOARCH) }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Status is the last known state.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.st
	st.Current = u.Current
	if st.State == "" {
		st.State = "idle"
	}
	return st
}

// Check asks GitHub for the latest release, unless the last answer is younger than maxAge, or an update is being
// downloaded or waits for a restart (there is nothing to check then).
func (u *Updater) Check(ctx context.Context, maxAge time.Duration) Status {
	u.mu.Lock()
	if u.busy || u.st.Ready || (maxAge > 0 && !u.checked.IsZero() && time.Since(u.checked) < maxAge) {
		u.mu.Unlock()
		return u.Status()
	}
	u.mu.Unlock()

	rel, err := u.latest(ctx)
	var releases []Release
	if err == nil {
		releases = u.releases(ctx, rel)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	u.checked = time.Now()
	if u.busy || u.st.Ready {
		return u.statusLocked()
	}
	if err != nil {
		u.st = Status{State: "error", Error: err.Error(), Checked: u.checked.UTC().Format(time.RFC3339)}
		return u.statusLocked()
	}
	st := Status{State: "idle", Latest: strings.TrimPrefix(rel.Tag, "v"), Page: rel.Page, Notes: rel.Body, Published: rel.Published,
		Releases: releases, Checked: u.checked.UTC().Format(time.RFC3339)}
	st.Available = Newer(rel.Tag, u.Current)
	u.asset, u.sums, u.kind = nil, nil, ""
	name, kind := u.assetName(rel.Tag)
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case name:
			u.asset = &rel.Assets[i]
		case "SHA256SUMS":
			u.sums = &rel.Assets[i]
		}
	}
	u.kind = kind
	if u.asset != nil {
		st.Asset, st.Size = u.asset.Name, u.asset.Size
	}
	switch {
	case !st.Available:
	case !u.Install:
		st.Why = "The desktop app updates itself. For a server, replace the program (or pull the new image) and restart it."
	case u.asset == nil:
		st.Why = fmt.Sprintf("This release has no download for %s.", platformName(u.goos(), u.goarch()))
	case u.sums == nil:
		st.Why = "This release has no checksums (SHA256SUMS) to check a download against."
	default:
		st.CanInstall = true
	}
	u.st = st
	return u.statusLocked()
}

func (u *Updater) statusLocked() Status {
	st := u.st
	st.Current = u.Current
	if st.State == "" {
		st.State = "idle"
	}
	return st
}

func (u *Updater) latest(ctx context.Context) (*release, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", or(u.API, "https://api.github.com")+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "medialib/"+u.Current)
	res, err := u.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Could not reach GitHub to look for updates (%v).", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 404:
		return nil, errors.New("No release has been published yet.")
	case res.StatusCode == 403 || res.StatusCode == 429:
		return nil, errors.New("GitHub asks to wait before looking for updates again; try later.")
	case res.StatusCode != 200:
		return nil, fmt.Errorf("GitHub answered %s when asked for the latest release.", res.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("GitHub's answer about the latest release was not understood: %w", err)
	}
	if rel.Tag == "" {
		return nil, errors.New("GitHub's latest release has no version.")
	}
	return &rel, nil
}

// releases are the notes of every release after this copy's version up to latest, newest first. They are one more
// request, made only when latest is newer; if it fails, the latest release's notes are all there is.
func (u *Updater) releases(ctx context.Context, latest *release) []Release {
	out := []Release{noteOf(latest)}
	if !Newer(latest.Tag, u.Current) {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", or(u.API, "https://api.github.com")+"/repos/"+Repo+"/releases?per_page=50", nil)
	if err != nil {
		return out
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "medialib/"+u.Current)
	res, err := u.client().Do(req)
	if err != nil {
		return out
	}
	defer res.Body.Close()
	var all []release
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&all) != nil {
		return out
	}
	var between []release
	for _, r := range all {
		if !r.Draft && !r.Prerelease && r.Tag != latest.Tag && Newer(r.Tag, u.Current) && Newer(latest.Tag, r.Tag) {
			between = append(between, r)
		}
	}
	sort.SliceStable(between, func(i, j int) bool { return Newer(between[i].Tag, between[j].Tag) })
	for _, r := range between {
		if len(out) == maxReleases {
			break
		}
		out = append(out, noteOf(&r))
	}
	return out
}

func noteOf(r *release) Release {
	return Release{Version: strings.TrimPrefix(r.Tag, "v"), Published: r.Published, Page: r.Page, Notes: r.Body}
}

// assetName is the release file that updates this copy, and how it is applied.
func (u *Updater) assetName(tag string) (string, Kind) {
	v := strings.TrimPrefix(tag, "v")
	sys, arch := u.goos(), u.goarch()
	osName := map[string]string{"darwin": "macos"}[sys]
	if osName == "" {
		osName = sys
	}
	if arch == "amd64" {
		arch = "x64"
	}
	base := fmt.Sprintf("medialib-%s-%s-%s", v, osName, arch)
	switch {
	case u.Desktop && sys == "windows" && u.installed():
		return base + "-setup.exe", Installer
	case u.Desktop && sys == "windows":
		return base + "-portable.zip", Program
	case u.Desktop:
		return base + ".tar.gz", Program
	case sys == "windows":
		return base + "-server.zip", Program
	}
	return base + "-server.tar.gz", Program
}

// installed is whether Windows setup put this program where it is (its uninstaller is beside it).
func (u *Updater) installed() bool {
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(u.Exe), "unins*.exe"))
	return len(m) > 0
}

func platformName(sys, arch string) string {
	n := map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}[sys]
	if n == "" {
		n = sys
	}
	return n + " " + map[string]string{"amd64": "x64", "arm64": "ARM64"}[arch]
}

// Prepare downloads the update and checks it. A program file is replaced at once (the running program carries on
// from its old file; the next start is the new version); a setup program waits for Finish.
func (u *Updater) Prepare(ctx context.Context) error {
	u.mu.Lock()
	switch {
	case u.busy:
		u.mu.Unlock()
		return errors.New("The update is already being downloaded.")
	case u.st.Ready:
		u.mu.Unlock()
		return nil
	case !u.st.CanInstall || u.asset == nil || u.sums == nil:
		why := or(u.st.Why, "There is no update to install.")
		u.mu.Unlock()
		return errors.New(why)
	}
	asset, sums, kind := *u.asset, *u.sums, u.kind
	u.busy = true
	u.st.State, u.st.Done, u.st.Total, u.st.Error = "downloading", 0, asset.Size, ""
	u.mu.Unlock()

	staged, err := u.prepare(ctx, asset, sums, kind)

	u.mu.Lock()
	defer u.mu.Unlock()
	u.busy = false
	if err != nil {
		u.st.State, u.st.Error = "error", err.Error()
		return err
	}
	u.st.State, u.st.Ready, u.staged = "ready", true, staged
	return nil
}

func (u *Updater) prepare(ctx context.Context, asset, sums Asset, kind Kind) (string, error) {
	if err := os.MkdirAll(u.Dir, 0o755); err != nil {
		return "", err
	}
	want, err := u.checksum(ctx, sums, asset.Name)
	if err != nil {
		return "", err
	}
	if d, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && !strings.EqualFold(d, want) {
		return "", fmt.Errorf("%s: GitHub and SHA256SUMS disagree about its checksum; not installing it.", asset.Name)
	}
	file := filepath.Join(u.Dir, asset.Name)
	if err := u.download(ctx, asset, file, want); err != nil {
		return "", err
	}
	if kind == Installer {
		return file, nil
	}
	defer os.Remove(file)
	prog := filepath.Join(u.Dir, programName(u.goos()))
	if err := extract(file, prog, programName(u.goos())); err != nil {
		return "", fmt.Errorf("%s: %w", asset.Name, err)
	}
	defer os.Remove(prog)
	if err := replace(u.Exe, prog, u.goos()); err != nil {
		return "", fmt.Errorf("Could not put the new version in place of %s: %w", u.Exe, err)
	}
	return "", nil
}

func programName(sys string) string {
	if sys == "windows" {
		return "medialib.exe"
	}
	return "medialib"
}

// checksum reads the expected SHA-256 of one file from the release's SHA256SUMS.
func (u *Updater) checksum(ctx context.Context, sums Asset, name string) (string, error) {
	res, err := u.get(ctx, sums.URL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			if _, err := hex.DecodeString(f[0]); err == nil {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no checksum for %s; not installing it.", name)
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "medialib/"+u.Current)
	res, err := u.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Download failed: %w", err)
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("Download failed: %s answered %s.", url, res.Status)
	}
	return res, nil
}

// download saves an asset as file once its SHA-256 is want, reporting progress as it goes.
func (u *Updater) download(ctx context.Context, asset Asset, file, want string) error {
	res, err := u.get(ctx, asset.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	part := file + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h, progress{u})
	n, err := io.Copy(w, io.LimitReader(res.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("the download is far larger than any release; not installing it")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("%s does not match its checksum (a broken or altered download); not installing it", asset.Name)
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, file)
}

type progress struct{ u *Updater }

func (p progress) Write(b []byte) (int, error) {
	p.u.mu.Lock()
	p.u.st.Done += int64(len(b))
	p.u.mu.Unlock()
	return len(b), nil
}

// extract takes the program out of a release archive (.zip or .tar.gz) into dst.
func extract(archive, dst, name string) error {
	var r io.Reader
	var closer func()
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if path.Base(f.Name) == name && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				r, closer = rc, func() { rc.Close() }
				break
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		for {
			hd, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if path.Base(hd.Name) == name && hd.Typeflag == tar.TypeReg {
				r = tr
				break
			}
		}
	}
	if r == nil {
		return fmt.Errorf("no %s inside", name)
	}
	if closer != nil {
		defer closer()
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxDownload+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("the program in the archive is far too large")
	}
	return err
}

// replace puts the program bin in place of exe. The running program is not disturbed: Windows lets a running
// program's file be renamed (to exe.old, removed at the next start), other systems keep the replaced file open.
func replace(exe, bin, sys string) error {
	tmp := filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".new")
	if err := copyFile(bin, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if sys == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe)
			os.Remove(tmp)
			return err
		}
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

// Finish completes an update that is ready. relaunch starts medialib again afterwards: the new setup does that
// when it is done, a replaced program is started right away (it waits for this one to exit, see WaitEnv). Without
// relaunch (medialib is closing) a setup program runs on its own, with no window. The caller then exits.
func (u *Updater) Finish(relaunch bool) error {
	u.mu.Lock()
	ready, staged, kind := u.st.Ready, u.staged, u.kind
	if kind == Installer {
		u.staged = "" // a setup runs once
	}
	u.mu.Unlock()
	if !ready {
		return errors.New("No update is ready to install.")
	}
	var cmd *exec.Cmd
	switch {
	case kind == Installer && staged != "":
		args := []string{"/SUPPRESSMSGBOXES", "/NORESTART", "/CURRENTUSER"}
		if u.forAllUsers() {
			args[2] = "/ALLUSERS"
		}
		if relaunch {
			args = append(args, "/SILENT", "/relaunch=yes")
		} else {
			args = append(args, "/VERYSILENT")
		}
		cmd = exec.Command(staged, args...)
	case kind == Program && relaunch:
		cmd = exec.Command(u.Exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), WaitEnv+"="+strconv.Itoa(os.Getpid()))
	default:
		return nil
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// forAllUsers is whether setup installed medialib for everyone (in Program Files) rather than for this user only.
func (u *Updater) forAllUsers() bool {
	for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if dir := os.Getenv(v); dir != "" && strings.HasPrefix(strings.ToLower(u.Exe), strings.ToLower(dir)+`\`) {
			return true
		}
	}
	return false
}

// AtExit runs a setup program downloaded by an automatic update that nobody restarted for.
func (u *Updater) AtExit() {
	u.mu.Lock()
	pending := u.st.Ready && u.kind == Installer && u.staged != ""
	u.mu.Unlock()
	if pending {
		_ = u.Finish(false)
	}
}

// Cleanup removes what an earlier update left behind: the replaced program (Windows) and old downloads.
func (u *Updater) Cleanup() {
	_ = os.Remove(u.Exe + ".old")
	_ = os.RemoveAll(u.Dir)
}

// ---------------------------------------------------------------- versions

var (
	versionRe  = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
	describeRe = regexp.MustCompile(`^(?:\d+-g[0-9a-f]+)?(?:-?dirty)?$`) // git describe: commits since the tag
)

type ver struct {
	n   [3]int
	pre string // a pre-release (3.2.0-rc1, 3.3.0-dev): before the release itself
}

func parseVersion(s string) (ver, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ver{}, false
	}
	var v ver
	for i := range 3 {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	if !describeRe.MatchString(m[4]) { // a build some commits after a release is that release, not one before it
		v.pre = m[4]
	}
	return v, true
}

// Newer reports whether version a is later than version b. Either one that is not a version (a "dev" build, a bare
// commit) makes it false: there is no telling.
func Newer(a, b string) bool {
	va, ok := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok || !ok2 {
		return false
	}
	for i := range 3 {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true // the release is later than its pre-releases
	case vb.pre == "":
		return false
	}
	return va.pre > vb.pre
}
