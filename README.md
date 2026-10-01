# medialib

A self-hosted media library and object-storage browser. Index your video folders (local disks, NAS shares, or buckets on any S3-compatible store) into a browsable grid of keyframe covers that opens in the player you already use, and manage the buckets themselves: browse, preview, upload, move, rename and delete.

It runs on your own machine. The backend is Python's standard library plus a hand-written S3 client; the frontend is plain ES modules with no build step.

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

- Python 3.9+ (Windows 11 is the tested platform)
- `ffmpeg` and `ffprobe` on `PATH` (for covers)
- Optional: [Pillow](https://pypi.org/project/pillow/) for better cover selection, and `rclone` only for older libraries that still read through an rclone remote

## Quick start

```bash
python -m medialib serve        # or: python run.py serve
```

This opens the UI at http://127.0.0.1:8766. On first run `config.json` is created with your `Videos` folder as a library.

Connect a store from the **Connect** page (or the command line), then browse it under **Storage**, or add a bucket folder as a library with **Library → Add a library → Bucket (S3)**.

```bash
# command line
python -m medialib add "D:\Videos" --name "My videos"            # a local folder or NAS share
python -m medialib import                                        # credentials found on this computer
python -m medialib import rclone:myremote                        # adopt one
python -m medialib connect --provider minio --endpoint http://nas:9000 \
       --access-key KEY --secret-env MY_SECRET_VARIABLE          # prefer --secret-env to --secret-key
python -m medialib connections --test
python -m medialib ls nas                                        # buckets
python -m medialib ls nas:media/videos/ -r                       # objects
python -m medialib add-s3 nas media/videos --name "NAS videos"   # a bucket folder as a library
python -m medialib index --library nas-videos
```

A library that was set up through rclone keeps working. **Library → Manage → ⋯ → Read directly over S3** switches it to the S3 API without re-indexing, and removes the rclone process from every request.

## Configuration

`config.json` sits next to the package and is git-ignored (set `MEDIALIB_HOME` to keep it, and the `cache/` folder, elsewhere). See [`config.example.json`](config.example.json).

| Key | Meaning |
|---|---|
| `connections` | `{"id", "name", "provider", "endpoint", "region", "access_key", "secret_key" or "secret_key_env", "addressing": "path"/"virtual"/"auto", "verify_tls", "default_bucket"}` |
| `libraries` | `{"type": "local", "path"}`, `{"type": "s3", "connection", "bucket", "prefix"}`, or the older `{"type": "rclone", "remote", "bucket", "prefix"}` |
| `players` | Extra players, listed before the auto-detected ones. `title_arg` passes the file name as the window title. |
| `default_player` | Player id used until you pick one in the UI |
| `rclone`, `ffmpeg`, `ffprobe` | Executable paths, if they aren't on `PATH` |
| `workers` | Files indexed at once. The default depends on the CPU count and the library type. |

`default_bucket` is for keys that are limited to one bucket and so cannot list all of them.

## Layout

```
medialib/
  s3.py           S3 client: Signature V4, retries, keep-alive pool, multipart, presigned URLs (no dependencies)
  connections.py  connection records, credential import, client cache
  storage.py      browse / search / upload / copy / move / delete on top of the client
  tasks.py        background tasks with progress and cancel
  sources.py      library sources: local, S3, rclone
  mp4.py          MP4 sample-index parsing and keyframe extraction
  indexer.py      incremental indexing and the cross-process locks
  server.py       HTTP server and JSON API
  cli.py          command line
web/              the UI (ES modules; css/, js/lib/, js/views/)
tests/            unit tests, API tests against a mock S3, and a demo environment
```

## Tests

```bash
pip install -r requirements-dev.txt
python -m unittest discover -s tests -t .       # signing vectors, connections, and the whole API against a mock S3 (moto)
python -m tests.demo_env                        # a disposable demo: mock S3 with sample videos, images and text
```

The signing tests use the examples published in the AWS documentation.

## Security

The server listens on `127.0.0.1` only and refuses cross-site requests and foreign `Host` headers. Everything that changes anything, and everything that touches your object stores (browsing included), is accepted only from the local machine, even if you start the server with `--host 0.0.0.0` to stream your libraries to other devices. Changing requests also need a custom `X-Medialib` header, which web pages on other sites cannot send.

Credentials live in `config.json`. Anyone who can read that file can use your store, so keep it out of backups you share, or use `secret_key_env`.
