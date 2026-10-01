# medialib

A self-hosted media library and object-storage browser. Index your video folders (local disks, NAS shares, or buckets on any S3-compatible store) into a browsable grid of keyframe covers that opens in the player you already use, and manage the buckets themselves: browse, preview, upload, move, rename and delete.

It runs as a **desktop app** on your own machine, or as a plain **web server** you deploy and open in any browser. The backend is a single Go binary with the UI inside it and a hand-written S3 client (no runtime to install); the frontend is plain ES modules with no build step.

## What's in it

**Library**
- Folder tree, grouped cover grids with a floating full-path title, search, and sort by name, date, size or length.
- Five real keyframes per video; hover a cover to scrub through them. The indexer reads an MP4's own sample index and fetches only the bytes of the chosen keyframes (about 1 to 4 MB per video, even over the network). H.264, HEVC and AV1; other formats fall back to ffmpeg seeking. Indexing is incremental and multithreaded.
- Libraries can be a local folder, a NAS share, or a bucket folder read straight through the S3 API.
- Click a cover to open it in mpv, VLC, PotPlayer, MPC or the system default. Play a whole folder as a playlist, or copy its stream URL.

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

## Requirements

- `ffmpeg` and `ffprobe` on `PATH` (for covers)
- To build from source: Go 1.25+. To build the desktop app also a C/C++ compiler (see below)
- Optional: `rclone`, only for older libraries that still read through an rclone remote

## Three ways to run it

One program, one UI. Build it (`make server`, `make desktop`), or take a binary from the releases page once a version is tagged (`.github/workflows/release.yml`).

### Desktop app

```bash
medialib desktop              # or double-click medialib-desktop
```

The UI in its own window (WebView2 on Windows, WebKit on macOS, WebKitGTK on Linux), talking to a server that lives and dies with the window. Settings and indexes live in your user config folder (`%AppData%\medialib`, `~/.config/medialib`).

Build it with `make desktop` (`scripts/build-desktop.sh`). It needs cgo:

| Platform | Needs |
|---|---|
| Windows | MinGW-w64 (`gcc`, `g++`); WebView2 runtime (included in Windows 11) |
| macOS | Xcode command line tools |
| Linux | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` (or `-4.0-dev`) |

The ordinary build (`make server`) has no native window; there `medialib desktop` borrows an app-mode window from Edge, Chrome or Chromium instead. File downloads from the Storage view are handled by the web view of your platform, so on Linux the browser is the better place to download.

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
```

Connect a store from the **Connect** page (or the command line), then browse it under **Storage**, or add a bucket folder as a library with **Library → Add a library → Bucket (S3)**.

A library that was set up through rclone keeps working. **Library → Manage → ⋯ → Read directly over S3** switches it to the S3 API without re-indexing, and removes the rclone process from every request.

## Configuration

`config.json` and the `cache/` folder (indexes, thumbnails) live in the first of these that applies: `$MEDIALIB_HOME`; the working directory or the executable's folder, if a `config.json` is there (portable use, and installs from the Python version); your user config folder. See [`config.example.json`](config.example.json). The file is written with owner-only permissions; unknown keys are kept when medialib saves it.

| Key | Meaning |
|---|---|
| `connections` | `{"id", "name", "provider", "endpoint", "region", "access_key", "secret_key" or "secret_key_env", "addressing": "path"/"virtual"/"auto", "verify_tls", "default_bucket"}` |
| `libraries` | `{"type": "local", "path"}`, `{"type": "s3", "connection", "bucket", "prefix"}`, or the older `{"type": "rclone", "remote", "bucket", "prefix"}` |
| `players` | Extra players, listed before the auto-detected ones. `title_arg` passes the file name as the window title. |
| `default_player` | Player id used until you pick one in the UI |
| `rclone`, `ffmpeg`, `ffprobe` | Executable paths, if they aren't on `PATH` |
| `workers` | Files indexed at once. The default depends on the CPU count and the library type. |
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

The index is serialized once per change instead of once per request, answers to other computers are gzip-compressed, and all of a video's keyframes are decoded by a single ffmpeg process (byte-identical to decoding them one by one; it falls back to that if anything looks off), which matters most on Windows where starting a program is slow.

## Tests

```bash
make test                                       # go vet and the unit tests (-race): signing vectors, connections, MP4 parsing, indexing, access rules
pip install -r requirements-dev.txt
make e2e                                        # the whole API against a mock S3 (moto)
python -m tests.e2e.demo_env                    # a disposable demo: mock S3 with sample videos, images and text
```

The signing tests use the examples published in the AWS documentation. The unit tests that need ffmpeg skip themselves without it.

## Security

The server listens on `127.0.0.1` only and refuses cross-site requests and foreign `Host` headers. Everything that changes anything, and everything that touches your object stores (browsing included), is accepted only from the local machine, even if you start the server with `--host 0.0.0.0` to stream your libraries to other devices, unless a password is set and the request carries it. A request that arrives through a reverse proxy (`X-Forwarded-For` and friends) never counts as local. Changing requests also need a custom `X-Medialib` header, which web pages on other sites cannot send.

Credentials live in `config.json`. Anyone who can read that file can use your store, so keep it out of backups you share, or use `secret_key_env`. The API never sends a secret back to the browser.
