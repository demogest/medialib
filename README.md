# Media Library

**Your video folders, as a wall of real covers. Click one, and it plays in the player you already use.**

Point it at a folder, a NAS share or an S3 bucket. Media Library finds every video, makes covers from real keyframes, finds anything as you type (in any language, pinyin and typos included), and opens it in mpv, VLC, PotPlayer, IINA or whatever you like. No account, no cloud, no transcoding: your files stay where they are.

<p align="center"><img src="docs/screenshots/home-dark.jpg" alt="Home: recently played, recently added and your libraries" width="880"></p>

**[Download for Windows](https://github.com/demogest/medialib/releases/latest)** (installer or portable) · **[macOS](https://github.com/demogest/medialib/releases/latest)** (Apple silicon) · **[Linux](https://github.com/demogest/medialib/releases/latest)** · or run it as a **server** ([Docker](#web-server)) and open it from any browser.

- **Set up in a minute.** The first run shows the folders on your computer that hold videos; one click adds one and makes its covers.
- **Browse like a streaming app.** Home shows what you played and what is new; folders are cards with their own covers; hover a video to scrub through its keyframes.
- **Find anything.** `Ctrl K` searches every library by name, folder, codec or resolution, with filters like `dur>1h` or `res>=4k`.
- **Your player, your files.** Plays in your own player, or copies the real path or link. It updates itself, and works the same on Windows, macOS and Linux.
- **Cloud storage too.** Browse and manage buckets on Amazon S3, Cloudflare R2, Backblaze B2, MinIO, RustFS and others, and make any bucket folder a library.

<table>
<tr>
<td width="50%"><img src="docs/screenshots/library-light.jpg" alt="A library: its folders as cards"><br><sub>Folders as cards, light theme</sub></td>
<td width="50%"><img src="docs/screenshots/folder-dark.jpg" alt="A folder of videos"><br><sub>A folder: hover a cover to scrub its keyframes</sub></td>
</tr>
<tr>
<td><img src="docs/screenshots/search-dark.jpg" alt="Search everything with Ctrl K"><br><sub>Search every library at once</sub></td>
<td><img src="docs/screenshots/details-dark.jpg" alt="Details of a video"><br><sub>Every keyframe, codec, size and where the file is</sub></td>
</tr>
<tr>
<td><img src="docs/screenshots/settings-dark.jpg" alt="Settings"><br><sub>Players, scanning, tools and updates, all in Settings</sub></td>
<td align="center"><img src="docs/screenshots/phone-home.jpg" alt="On a phone" width="220"><br><sub>On a phone, from the server</sub></td>
</tr>
</table>

---

A self-hosted media library and object-storage browser. It runs as a **desktop app** on your own machine, or as a plain **web server** you deploy and open in any browser. The backend is a single Go binary with the UI inside it and a hand-written S3 client (no runtime to install); the frontend is plain ES modules with no build step.

## What's in it

**Home**: what you played lately and what is new in every library, one click from playing, and each library as a tile of its newest covers. On a first run it lists the folders on the computer that hold videos, to add with one click.

**Library**
- Folders as cards with their own covers, or every video grouped by folder under a floating full-path title; a folder tree; search; sort by name, date, size or length.
- **Search** (the box in the library, and `Ctrl+K` for every library at once): by name, folder, extension, codec or resolution (`hevc`, `4k`, `1080p`); Chinese, Japanese and any other script; Chinese by **pinyin**, full (`donghua`) or initials (`dh`); hiragana and katakana alike; fuzzy, so letters in order (`hldy`) and a slip of the keys (`holidya`) still find the file. Several words must all match. **Filters** narrow the results down, or list files on their own: `dur>1h`, `dur<20m` (a bare number is minutes), `size<2g`, `size>500mb` (a bare number is megabytes), `date>=2024-05`, `date=2024` (a year, month or day of the file's date), `res>=1080`, `res>=4k`, with `<`, `<=`, `>`, `>=` or `=`. Names, folders and their pinyin are stored in the index as soon as files are listed, so files an indexing run has not reached yet are searchable already. Results are ranked by relevance (the sort box offers *Best match* while you search); `Enter` in the palette plays the file in your player, `Ctrl+Enter` shows it in its folder.
- Five real keyframes per video; hover a cover to scrub through them. The indexer reads an MP4's own sample index and fetches only the bytes of the chosen keyframes (about 1 to 4 MB per video, even over the network). H.264, HEVC and AV1; other formats fall back to ffmpeg seeking. Indexing is incremental and multithreaded.
- Libraries can be a local folder, a NAS share, or a bucket folder read straight through the S3 API.
- Click a cover to open it in mpv, mpv.net, VLC, PotPlayer, MPC-HC, MPC-BE, IINA, SMPlayer, Celluloid, Haruna or the system default, whichever are installed (or any other you add in Settings). Play a whole folder as a playlist, or shuffled, or save it as a playlist file.
- **Copy link** gives the file's real location: its path on this computer for a local library, a presigned link that works anywhere for a week for a bucket. **Save playlist…** writes an `.m3u8` of the same links that any player opens without medialib.
- Click a file's name (or right-click → **Details**) for all its keyframes (arrow keys step through them), codec, frame rate, sound and full path. In a local library, **Show in Explorer** (Finder, or the file manager on Linux) opens its folder with the file selected.

**Storage** (new in 2.0)
- Connect to **RustFS, MinIO, Amazon S3, Cloudflare R2, Backblaze B2, Wasabi, Alibaba OSS, Tencent COS, DigitalOcean Spaces, Google Cloud Storage** (interoperability mode) or anything else that speaks S3.
- Browse buckets and folders as a list or a grid, with filter and recursive search, sortable columns, multi-select, and keyboard shortcuts.
- Preview images, video (with seeking), audio and text in place; see size, ETag, content type and metadata; edit content type, `Cache-Control` and custom metadata.
- Upload files and whole folders by button or drag and drop (large files go up as parallel multipart uploads, with a progress panel). Download, rename, copy and move between folders, buckets and even connections, and delete folders with everything in them.
- Copy presigned links (1 hour, 24 hours, 7 days), `s3://` addresses or object keys. Play objects in your external player.
- Create and delete buckets, measure folder sizes, find and discard unfinished multipart uploads, and turn any folder into a library with one click.

**Connections**
- Presets for the stores above, a connection test, and one-click import of credentials you already have: rclone remotes, `~/.aws` profiles and `AWS_*` variables.
- Secrets stay on your computer, in the git-ignored `config.json`, or in an environment variable you name. The API never sends a secret back to the browser.

**Activity**: copies, moves, deletes, size scans and indexing runs, with progress, cancel and a per-item error list.

**Settings**: theme; automatic indexing, cover quality and files at once; the players (add one, remove one, pick the default, look again); your own ffmpeg, ffprobe or rclone; where the index and covers live (moved for you, with progress); and updates.

**Updates**: medialib looks for a new release every few hours and says so in the sidebar and in Settings, with what's new. The desktop app installs it with **Update now** (or by itself as it closes, with *Install automatically*): a downloaded file is used only if it matches the release's `SHA256SUMS`, a Windows install runs the new setup and comes back, a portable copy replaces its own program. A server only tells; replace its program or image. Set Settings → Automatic updates to *Off* (`"updates": "off"`) to never ask GitHub.

## Requirements

- `ffmpeg` and `ffprobe` on `PATH` (for covers)
- To build from source: Go 1.26+. To build the desktop app also a C/C++ compiler (see below)
- Optional: `rclone`, only for older libraries that still read through an rclone remote

## Three ways to run it

One program, one UI. Build it (`make server`, `make desktop`), or take one from the [releases page](https://github.com/demogest/medialib/releases): `medialib-<version>-<os>-<arch>` is the desktop app (`-setup.exe` and `-portable.zip` on Windows), `…-server` the server and command line, each archive holding one program named `medialib`. Every release lists its files and has `SHA256SUMS`.

### Desktop app

```bash
medialib desktop              # or double-click medialib-desktop
```

The UI in its own window, talking to a server that lives and dies with the window. Settings and indexes live in your user config folder (`%AppData%\medialib`, `~/.config/medialib`).

**Windows** gets a proper application: the system's WebView2 runtime driven from pure Go (no C compiler, no extra DLL), one window per user (starting it again raises the running one), the window comes back where you left it, the title bar follows the page's light or dark theme, links to other sites open in your browser, and there is an icon, DPI-aware manifest and version info. Without WebView2 it falls back to an Edge or Chrome app window. Take `medialib-<version>-windows-x64-setup.exe` from the releases (per-user install, Start menu entry, optional desktop shortcut, optional ffmpeg via winget) or the portable `medialib-<version>-windows-x64-portable.zip`. To build them yourself:

```powershell
powershell -File scripts\package-windows.ps1            # needs Go; Inno Setup 6 for the installer
```

macOS and Linux use the platform web view (WKWebView, WebKitGTK) and need cgo: `make desktop` (`scripts/build-desktop.sh`).

| Platform | Needs |
|---|---|
| Windows | Go, and the WebView2 runtime (included in Windows 11) |
| macOS | Xcode command line tools |
| Linux | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` (or `-4.0-dev`) |

The ordinary build (`make server`) has no native window; there `medialib desktop` borrows an app-mode window from Edge, Chrome or Chromium instead.

**Interface**: a collapsible sidebar (`Ctrl+B`) with your libraries and stores, a command palette (`Ctrl+K`) to jump anywhere or run a command, `Alt+1…5` for the sections, light/dark themes, and a welcome screen on first run.

### Web server

```bash
medialib serve                                   # http://127.0.0.1:8766, this computer only
medialib serve --host 0.0.0.0 --port 8766        # reachable by other computers
```

or as a container: `docker compose up -d` (see `docker-compose.yml`; the image is ffmpeg plus the one binary).

Nothing about the page changes between the desktop app and the server: it is the same files, served from inside the binary.

- Set **`MEDIALIB_PASSWORD`** (or `"password"` in `config.json`) before opening the server to other computers. Every request then needs it (HTTP Basic: any user name). Without a password, other computers can browse libraries and thumbnails but cannot touch storage, connections or anything that changes state.
- Serve it over HTTPS, for example behind Caddy or nginx: Basic authentication is sent with every request.
- Playing in an external player (mpv, VLC ...) and the folder picker act on the computer running the server, so only a browser on that same computer can use them. From another computer a cover opens the stream in the browser, and **Copy playlist URL** hands the library to your own player.
- New files on the disk, share or bucket show up after the next indexing run. Set **`MEDIALIB_AUTO_INDEX`** (or `"auto_index"` in `config.json`) to a number of minutes and the server runs one by itself: soon after it starts, then that long after each pass ends, over every library in turn. Libraries that cannot be reached at the time (a disk that is not plugged in) are skipped, not marked as failed. Without a server running, `medialib index --all` from cron or a scheduled task does the same once.
- `MEDIALIB_HOST`, `MEDIALIB_PORT`, `MEDIALIB_HOME` and `MEDIALIB_LOG=1` (one log line per request) are read from the environment, which is how the container is configured. `GET /healthz` answers `ok` without a password.

### Command line

```bash
medialib add "D:\Videos" --name "My videos"            # a local folder or NAS share
medialib import                                        # credentials found on this computer
medialib import rclone:myremote                        # adopt one
medialib connect --provider minio --endpoint http://nas:9000 \
       --access-key KEY --secret-env MY_SECRET_VARIABLE          # prefer --secret-env to --secret-key
medialib connections --test
medialib ls nas                                        # buckets
medialib ls nas:media/videos/ -r                       # objects
medialib add-s3 nas media/videos --name "NAS videos"   # a bucket folder as a library
medialib index --library nas-videos
medialib index --all                                   # every library, one after another (for cron)
```

Connect a store from the **Connections** page (or the command line), then browse it under **Storage**, or add a bucket folder as a library with **Library → Add a library → Bucket (S3)**.

A library that was set up through rclone keeps working. **Library → Manage → ⋯ → Read directly over S3** switches it to the S3 API without re-indexing, and removes the rclone process from every request.

## Configuration

`config.json` and the `cache/` folder (indexes, thumbnails) live in the first of these that applies: `$MEDIALIB_HOME`; the working directory or the executable's folder, if a `config.json` is there (portable use, and installs from the Python version); your user config folder. See [`config.example.json`](config.example.json). The file is written with owner-only permissions; unknown keys are kept when medialib saves it.

| Key | Meaning |
|---|---|
| `connections` | `{"id", "name", "provider", "endpoint", "region", "access_key", "secret_key" or "secret_key_env", "addressing": "path"/"virtual"/"auto", "verify_tls", "default_bucket"}` |
| `libraries` | `{"type": "local", "path"}`, `{"type": "s3", "connection", "bucket", "prefix"}`, or the older `{"type": "rclone", "remote", "bucket", "prefix"}` |
| `players` | Players added by hand (Settings → Players → Add a player), listed before the auto-detected ones; one with the id of a detected player takes its place. `title_arg` passes the file name as the window title; `args` is a list of extra command-line arguments for every launch (for mpv, `["--demuxer-max-bytes=256MiB"]` keeps its read-ahead small). |
| `hidden_players` | Ids of detected players removed from the list (Settings brings them back). |
| `default_player` | Player id used until you pick one in the UI |
| `rclone`, `ffmpeg`, `ffprobe` | Executable paths, if they aren't on `PATH`. Besides `PATH`, medialib looks where package managers put them (Homebrew, winget, Scoop, Chocolatey). |
| `cache_dir` | Where indexes and thumbnails live, if not in `cache/` beside `config.json`. Change it in Settings, which moves them. |
| `updates` | `"off"`, `"notify"` (the default: say when there is a new version) or `"auto"` (the desktop app installs it as it closes). |
| `workers` | Files indexed at once. The default depends on the CPU count and the library type. |
| `thumb_quality` | Quality (1 to 100) of new thumbnails, which are AVIF when ffmpeg can write it, else WebP. Default 65; lower is smaller. `medialib compact` converts older thumbnails. |
| `auto_index` | Minutes between automatic indexing passes over every library while medialib runs. Default 0 (off); `MEDIALIB_AUTO_INDEX` takes precedence. |
| `password` | Password for a server other computers can reach; `MEDIALIB_PASSWORD` takes precedence. |

`default_bucket` is for keys that are limited to one bucket and so cannot list all of them.

## Layout

```
cmd/medialib/       the program: serve, desktop and the command line
internal/
  s3/               S3 client: Signature V4, retries, keep-alive pool, multipart, presigned URLs (standard library only)
  config/           config.json, libraries, connections, credential import, client cache
  storage/          browse / search / upload / copy / move / delete on top of the client
  tasks/            background tasks with progress and cancel
  media/            library sources (local, S3, rclone), MP4 sample-index parsing, keyframes, indexing, index store
  players/          finding players, opening media in them, bringing the window to the front (Windows)
  update/           new releases: check, download, verify against SHA256SUMS, install
  server/           HTTP server and JSON API, access rules
web/                the UI (ES modules; css/, js/lib/, js/views/), embedded into the binary by web/embed.go
tests/e2e/          the whole API against a mock S3, and a disposable demo
```

Set `MEDIALIB_WEB=web` to serve the UI from disk instead of the embedded copy while working on it.

## Performance

Measured against the Python version this replaced (same machine, 4 cores):

| | Python | Go |
|---|---|---|
| status polling (`/api/tasks`, 32 clients) | 800 requests/s | 34,000 requests/s |
| static files (32 clients) | 830 requests/s | 25,000 requests/s |
| a 50,000-item library, 8 clients | 3 requests/s | 17 requests/s |
| indexing 24 short MP4s | 2.4 s | 0.75 s |
| streaming a 300 MB local file | 2.0 GB/s | 1.8 GB/s (the loopback is the limit) |

Searching 50,000 files takes 3 to 15 ms per query on a 4-core machine (typo-tolerant and pinyin matching included), and allocates nothing, so typing in the search box stays smooth in big libraries. Picking each video's cover reads the decoded frames' luma plane directly, about 20 times faster than going through every pixel's colour. While a library is being indexed, each saved index goes to the server in memory instead of being read back from disk; reading one from disk (50,000 files) takes about 0.3 s.

The index is serialized once per change instead of once per request, answers to other computers are gzip-compressed, and all of a video's keyframes are decoded by a single ffmpeg process (byte-identical to decoding them one by one; it falls back to that if anything looks off), which matters most on Windows where starting a program is slow.

## Tests

```bash
make test                                       # go vet and the unit tests (-race): signing vectors, connections, MP4 parsing, indexing, access rules
pip install -r requirements-dev.txt
make e2e                                        # the whole API against a mock S3 (moto)
python -m tests.e2e.demo_env                    # a disposable demo: mock S3 with sample videos, images and text
```

The signing tests use the examples published in the AWS documentation. The unit tests that need ffmpeg skip themselves without it.

## Releasing

Bug fixes are released by themselves. New features are released when you say so: push a tag, or run the release workflow by hand.

- **A bug fix reaching `main`** releases the next patch version (3.3.0 → 3.3.1) by itself. A fix is a commit whose title starts with *Fix* (`Fix the cover cache…`, `fix: …`, `fix(ui): …`), or whose pull request has the **bug** label. Everything since the last release counts, so a fix is not missed when more lands before its release starts. Other pushes release nothing.
- **Push a tag**: `git tag -a v3.4.0 -m "medialib 3.4.0" && git push origin v3.4.0`. A tag message that says more than a title leads the notes.
- **Actions → release → Run workflow** on `main`, with **publish** ticked. It releases the next version (choose **patch**, **minor** or **major**), or exactly the **version** you type, and tags the commit it built. It refuses a version that is already tagged, or anything but `vX.Y.Z`. Without **publish** it is a dry run: it builds everything and writes the notes and checksums (shown on the run's page and kept as artifacts) but publishes nothing.

"The next version" counts on from the newest of `VERSION` and the versions already released. Whichever way, the workflow:

1. builds every download (the version goes into the program, the Windows file properties and the installer);
2. packs them under their release names (`scripts/package-release.sh`);
3. once all of them have built, publishes the release with `SHA256SUMS` and generated notes: every change since the previous version, from the commit messages (a squash-merged pull request brings its list of changes);
4. writes the version back into `main`: `VERSION` and the Windows version files, with `scripts/set-version.sh`. `VERSION` always says what was released last, and the next run counts on from it. A pre-release, or a fix release of an older version, leaves them as they are.

`scripts/release-notes.sh v3.4.0` shows the notes beforehand. **Actions → release-notes → Run workflow** writes the notes into a release that already exists, leaving its files alone.

## Security

The server listens on `127.0.0.1` only and refuses cross-site requests and foreign `Host` headers. Everything that changes anything, and everything that touches your object stores (browsing included), is accepted only from the local machine, even if you start the server with `--host 0.0.0.0` to stream your libraries to other devices, unless a password is set and the request carries it. A request that arrives through a reverse proxy (`X-Forwarded-For` and friends) never counts as local. Changing requests also need a custom `X-Medialib` header, which web pages on other sites cannot send.

Files from a bucket or a folder are served with `Content-Security-Policy: sandbox` and `nosniff`: an HTML or SVG file opened in a tab renders in a sandbox of its own, never as part of medialib with access to its API. No page of medialib can be framed by another site. A browser on another computer that may only watch is not told local paths (the settings and index folders, where players and tools are).

Credentials live in `config.json`. Anyone who can read that file can use your store, so keep it out of backups you share, or use `secret_key_env`. The API never sends a secret back to the browser.
