# Contributing to Media Library

This covers how the project is put together, how to build and test it, the conventions to follow and how releases are made. Bugs and ideas go in [an issue](https://github.com/demogest/medialib/issues/new/choose) (each has a short form); security problems are reported privately, as [SECURITY.md](SECURITY.md) explains. Contributions are under the [MIT license](LICENSE), like the rest of the project. For what the app does, see the [README](README.md). Before adding a screen, a button or a setting, read [docs/DESIGN.md](docs/DESIGN.md): who the app is for, how its features are ranked and the principles the interface follows.

## Set up

| You need | For |
|---|---|
| Go 1.26 or later | Everything |
| `ffmpeg` and `ffprobe` on `PATH` | Covers, and the tests that make them (they skip themselves without it) |
| Node 22 or later | `make lint`: it checks that every script of the UI parses |
| Python 3 and `pip install -r requirements-dev.txt` | `make e2e` and the demo (a mock S3, moto) |
| The desktop app on macOS or Linux | A C/C++ compiler (cgo): Xcode command line tools on macOS; `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` (or `-4.0-dev`) on Linux |
| The desktop app on Windows | Only Go; [Inno Setup 6](https://jrsoftware.org/isinfo.php) for the installer |

```bash
make server                                     # dist/medialib: the server and command line, the UI inside
MEDIALIB_WEB=web dist/medialib serve            # serve the UI from web/ instead: edit, reload, no rebuild
python -m tests.e2e.demo_env                    # a disposable demo: a mock S3 with sample videos, images and text
```

The demo never touches your real `config.json`, cache or storage. Set `MEDIALIB_HOME` to a scratch folder to do the same with a plain `medialib serve`.

## Build

| Command | Makes |
|---|---|
| `make server` | `dist/medialib`: one static binary (no cgo), the UI embedded |
| `make desktop` | `dist/medialib-desktop`: the same with a native window (`scripts/build-desktop.sh`); built on the system it is for |
| `make cross` | The server for Linux, macOS and Windows, x64 and ARM64 |
| `make docker` | The container image (`Dockerfile`: ffmpeg plus the one binary) |
| `powershell -File scripts\package-windows.ps1` | The Windows desktop app, its portable zip and, with Inno Setup, the installer |

`VERSION=v3.4.0 make server` sets the version the program reports; it defaults to `git describe`.

The desktop window comes from build tags: `desktop && windows` drives WebView2 from pure Go (`desktop_windows.go`), `desktop && !windows` uses webview with cgo (`desktop_webview.go`), and the plain build borrows an app-mode window from Edge, Chrome or Chromium (`desktop_plain.go`, `desktop_appmode.go`).

## Layout

```
cmd/medialib/       the program: serve, desktop and the command line; Windows resources (winres/, *.syso)
internal/
  config/           config.json, settings, libraries, connections, credential import, where files live
  s3/               S3 client: Signature V4, retries, keep-alive pool, multipart, presigned links (standard library only)
  storage/          browse, search, upload, copy, move and delete on top of the client
  media/            library sources (local, S3, rclone), MP4 sample-index parsing, keyframes, scanning, the index files
  search/           matching (pinyin, kana, typos) and filters (dur>1h, res>=4k), allocation-free
  players/          finding players, opening media in them, showing a file in its folder, bringing a window forward
  server/           HTTP server, JSON API, access rules, Home's feed, settings
  tasks/            background tasks with progress and cancel
  update/           new releases: check, download, verify against SHA256SUMS, install
  proc/             starting programs without a console window, waiting for a process to end
  version/          the version, stamped in at build time
web/                the UI: index.html, css/, js/ (lib/, shell/, views/), embedded by web/embed.go
tests/e2e/          the whole API against a mock S3, and the demo
scripts/            building, packaging, release notes and versions, the icon (genicon)
installer/          the Inno Setup script of the Windows installer
docs/               design notes and the README's screenshots
```

## How it works

- **One program.** The server, the command line and the desktop app are one Go binary. The UI is plain ES modules with no build step and no npm dependencies, embedded with `go:embed`. Dependencies are few, and the S3 client uses the standard library only; think twice before adding one.
- **The API** is JSON under `/api/`. Every route is declared in `internal/server/routes.go` with an access class, which `guard` (`internal/server/http.go`) enforces:

  | Class | Who may use it | For |
  |---|---|---|
  | `open` | Anyone who can reach the server | Library views, covers, streams |
  | `private` | This computer, or anyone signed in with the password | The storage browser, connections, every change |
  | `machine` | This computer only | Acting on the computer itself: players, the folder dialog |

  Give a new route the narrowest class that works. Requests that change something must carry `X-Medialib: 1` (`web/js/lib/api.js` adds it), which pages on other sites cannot send. Never send a secret to the browser. Files from a library or a bucket are served through `inert()`, so an HTML or SVG file cannot run as part of medialib.
- **Scanning** reads an MP4's own sample index to fetch only the bytes of five keyframes per video (other formats go through ffmpeg), decodes them with one ffmpeg process, picks the cover from the luma plane, and writes the covers as AVIF or WebP. A library's index is a file in the cache folder, saved after each batch and served from memory.
- **The UI** builds the DOM with `h()` and `fill()` from `web/js/lib/dom.js`, talks to the server with `get`/`post`/`del` from `lib/api.js`, keeps shared state in `lib/state.js` (with events to subscribe to), and routes with `lib/router.js`. Each screen is a view in `js/views/` that the router mounts and destroys.

## Tests and checks

```bash
make lint                                       # gofmt, every UI script parses (node --check), shell scripts parse, VERSION
make test                                       # go vet and the unit tests, with -race
make e2e                                        # the whole API against a mock S3 (needs requirements-dev.txt and ffmpeg)
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12    # after changing .github/workflows/
```

CI runs `lint` (with actionlint), `test` (with `make e2e`), `cross`, the desktop build on Linux, macOS and Windows, and the Docker image, on every pull request. The S3 signing tests use the examples published in the AWS documentation.

The **security** workflow runs on every pull request, on `main`, and every Monday:
- **CodeQL** reads the Go code, the UI's JavaScript and the workflows for known kinds of vulnerability.
- **govulncheck** reports the known vulnerabilities, in Go itself and in the modules, that the code actually reaches.

Their findings are code scanning alerts in the Security tab, and GitHub marks a pull request that brings a new serious one. Treat an alert as a bug: fix it, or dismiss it in the Security tab saying why it does not apply.

**Dependabot** opens one pull request a week per kind of dependency, and one at once for a dependency with a known vulnerability. Go modules come as `fix(deps): …`, so once merged they go out in the next bug-fix release; GitHub Actions and the Docker base images come as `ci: …` and `build: …`.

Unit tests sit beside the code (`*_test.go`); a change to the API wants a case in `tests/e2e/test_api.py`. Code for one system goes in `*_windows.go` with an `*_other.go` or `*_unix.go` beside it, so every build still compiles; the desktop CI jobs and `make cross` catch it if not.

## Conventions

- **Go**: `gofmt`. Comments say why, in full sentences. Errors a user will see are written for them, not for a developer.
- **Interface text** is plain language, as [docs/DESIGN.md](docs/DESIGN.md) sets out: *scan* and *covers*, not *index* and *keyframes*; KB, MB and GB; *Added 3 d ago*; sentence-case headings. Every empty or broken state says what happened and offers the one action that fixes it. Never show `127.0.0.1` or a port.
- **Settings**: a new `config.json` key goes in `internal/config`, the README's configuration table and `config.example.json` if it belongs there. Keys medialib does not know are kept when it saves the file.
- **Pull requests** are squash-merged: the PR's title becomes the commit's title, a heading in the release notes, and its description the text under it. So write the title for someone reading what changed, and the description as the list of changes.
- **Titles starting with "Fix"** (`Fix …`, `fix: …`, `fix(ui): …`), and pull requests labelled **bug**, go out in the next automatic bug-fix release (see below). Use them for bug fixes only.
- **Versions** are not edited by hand: `VERSION`, `winres/versioninfo.json`, the `.syso` files and the installer's `AppVersion` are set by `scripts/set-version.sh`, which the release workflow runs after each release.
- **Generated files**: the Windows resources (`resource_windows_*.syso`) come from `winres/` with `go generate ./cmd/medialib`; the icon from `go run ./scripts/genicon`. Both are committed, so a plain `go build` has them.
- Never commit `config.json`, `cache/` or `dist/` (they are in `.gitignore`).

## Performance

Numbers to keep, measured against the Python version this replaced (same machine, 4 cores):

| | Python | Go |
|---|---|---|
| status polling (`/api/tasks`, 32 clients) | 800 requests/s | 34,000 requests/s |
| static files (32 clients) | 830 requests/s | 25,000 requests/s |
| a 50,000-item library, 8 clients | 3 requests/s | 17 requests/s |
| indexing 24 short MP4s | 2.4 s | 0.75 s |
| streaming a 300 MB local file | 2.0 GB/s | 1.8 GB/s (the loopback is the limit) |

Searching 50,000 files takes 3 to 15 ms per query (typos and pinyin included) and allocates nothing, so typing stays smooth in big libraries. Picking a video's cover reads the decoded frames' luma plane directly, about 20 times faster than going through every pixel's colour. While a library is being scanned, each saved index goes to the server in memory instead of being read back from disk; reading one from disk (50,000 files) takes about 0.3 s. The index is serialized once per change instead of once per request, answers to other computers are gzip-compressed, and all of a video's keyframes are decoded by a single ffmpeg process, which matters most on Windows, where starting a program is slow.

## Releasing

Bug fixes are released by themselves, every other day, all together. New features are released when a maintainer says so: by pushing a tag, or by running the release workflow by hand.

- **Bug fixes, every other day** (03:17 UTC, on the odd days of the month): if `main` has any since the last release, they all go out as one new patch version, the last digit (3.3.0 → 3.3.1). While a version is in alpha or beta, they go out as the next one of those instead (3.4.0-beta.1 → 3.4.0-beta.2). Pushing fixes one by one makes no versions of its own, and two days without a fix release nothing. A fix is a commit whose title starts with *Fix*, or whose pull request has the **bug** label. A fix that cannot wait: run the workflow by hand with **patch**. The schedule is one line in `.github/workflows/release.yml`.
- **Push a tag**: `git tag -a v3.4.0 -m "medialib 3.4.0" && git push origin v3.4.0`. A tag message that says more than a title leads the notes.
- **Actions → release → Run workflow** on `main`, with **publish** ticked. It releases the next version (choose **patch**, **minor** or **major**, and **alpha** or **beta** for a pre-release of it), or exactly the **version** you type, and tags the commit it built. It refuses a version that is already tagged, or anything but `vX.Y.Z`, `vX.Y.Z-alpha.N` and `vX.Y.Z-beta.N`. Without **publish** it is a dry run: it builds everything and writes the notes and checksums (shown on the run's page and kept as artifacts) but publishes nothing.

"The next version" counts on from the newest of `VERSION` and the versions already released; `scripts/next-version.sh minor beta` prints it. **Alpha and beta versions** (anything with a `-`, like `v3.4.0-beta.1`) are published as GitHub pre-releases: the app does not offer them as updates, and the notes of the full release that follows list everything since the last full release. Whichever way, the workflow:

1. builds every download (the version goes into the program, the Windows file properties and the installer);
2. packs them under their release names (`scripts/package-release.sh`);
3. once all of them have built, publishes the release with `SHA256SUMS` and generated notes: every change since the previous version, from the commit messages;
4. writes the version back into `main`: `VERSION` and the Windows version files, with `scripts/set-version.sh`, in a commit of its own (*Set the version to X.Y.Z, as released*). `VERSION` always says which full release came last. A pre-release, or a fix release of an older version, leaves them as they are.

`scripts/release-notes.sh v3.4.0` shows the notes beforehand. **Actions → release-notes → Run workflow** writes the notes into a release that already exists, leaving its files alone.
