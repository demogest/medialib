# medialib

A small, self-hosted media library for your own video folders: local disks, NAS shares, or S3-compatible object storage through [rclone](https://rclone.org). It indexes your files, shows them as a browsable grid of keyframe covers organised by folder, and opens them in the player you already use (mpv, VLC, PotPlayer, MPC, …).

It is a single Python script plus a static web UI, and runs entirely on your own machine.

## Features

- **Folder-organised library.** A folder tree, grouped cover grids with a floating full-path title, search, and sort by name, date, size or length.
- **Real keyframe covers.** Five keyframes per video, with the most detailed one used as the cover. Hover over a cover to scrub through the other four.
- **Cheap indexing, even over the network.** For MP4/MOV files the indexer reads the file's own sample index and fetches only the bytes of the chosen keyframes, typically 1 to 4 MB per video. H.264, HEVC and AV1 are supported. Other formats fall back to ffmpeg seeking. Indexing is incremental and multithreaded.
- **Several libraries.** Mix local folders, NAS shares and rclone remotes (S3, RustFS, MinIO, …), and add or switch them from the UI with a native folder picker.
- **Plays in your player.** Click a cover to open it in the chosen player, with focus handed to the player window on Windows. Each folder can be played as a playlist.
- **Stable stream URLs.** Every item has `/media/<library>/<id>/<name>`. Local files are served with Range support, and S3 items redirect to a fresh presigned link, so the URLs work in any player and never expire.
- **File type filter.** Common video formats are shown by default. Hide or show any extension.
- **Safe with concurrent use.** A cross-process mutex per library guards the index file and the indexing run. A second indexer waits for the first one instead of colliding with it.

## Requirements

- Python 3.9+ (Windows 11 is the tested platform)
- `ffmpeg` and `ffprobe` on `PATH`
- Optional: [Pillow](https://pypi.org/project/pillow/) for better cover selection, and `rclone` for S3 libraries

## Quick start

```bash
python medialib.py serve
```

This opens the UI at http://127.0.0.1:8766. Use the folder-plus button next to the library picker to add a folder, and it is indexed right away. On first run, `config.json` is created with your `Videos` folder as the first library.

From the command line:

```bash
python medialib.py add "D:\Videos" --name "My videos"   # add a local folder or NAS share
python medialib.py index --library my-videos            # bring its index up to date
python medialib.py libraries                            # list libraries
```

## Configuration

`config.json` sits next to the script and is git-ignored. See [`config.example.json`](config.example.json).

| Key | Meaning |
|---|---|
| `libraries` | `{"type": "local", "path": ...}` (environment variables and `~` allowed), or `{"type": "rclone", "remote": "myremote:", "bucket": ..., "prefix": ...}` |
| `players` | Extra players, listed before the auto-detected ones. `title_arg` passes the file name as the window title. |
| `default_player` | Player id used until you pick one in the UI |
| `rclone`, `ffmpeg`, `ffprobe` | Executable paths, if they aren't on `PATH` |
| `workers` | Files indexed at once. The default depends on the CPU count and the library type. |

Indexes and thumbnails live in `cache/<library-id>/`.

## Security

The server listens on `127.0.0.1` only. It refuses cross-site requests and foreign `Host` headers. Anything that launches programs or changes libraries is accepted only from the local machine, even if you start it with `--host 0.0.0.0` to stream to other devices on your network.
