#!/usr/bin/env python3
"""Media library: index S3 folders (via rclone) or local folders into a browsable, playable library.

    python medialib.py serve [--port 8766] [--host 127.0.0.1] [--no-browser]
    python medialib.py index [--library ID] [--limit N] [--workers 8] [--force]
    python medialib.py add PATH [--name NAME]        # add a local folder (disk or NAS share)
    python medialib.py libraries                     # list the configured libraries

A library is either an rclone remote folder (e.g. myremote:bucket/videos/) or a local folder (a disk or a NAS share).
index  Reads each MP4's own sample index and fetches only the bytes of a few real
       keyframes (ranged requests for S3, plain seeks for local files), which ffmpeg
       decodes into thumbnails. Incremental: unchanged files are skipped, removed
       files are dropped. Other formats fall back to ffmpeg seeking.
serve  Runs the library UI on localhost. Libraries can be added, indexed and switched
       in the UI. Every item has a stable URL, /media/<library>/<id>/<name>; clicking a
       cover opens the chosen player (S3 items by that URL, local items by file path).

Settings live in config.json next to this script (created on first run).
"""
import argparse
import bisect
import concurrent.futures as cf
import hashlib
import http.client
import json
import mimetypes
import os
import pathlib
import re
import shutil
import struct
import subprocess
import sys
import threading
import time
import urllib.parse
import webbrowser
from array import array
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = pathlib.Path(__file__).resolve().parent
WEB = HERE / "web"
CACHE = HERE / "cache"
CONFIG = HERE / "config.json"

DEFAULT_CONFIG = {
    "rclone": "rclone",
    "ffmpeg": "ffmpeg",
    "ffprobe": "ffprobe",
    "port": 8766,
    "default_player": "mpv",
    # Extra players, listed before the detected ones: {"id", "name", "path", "title_arg"}.
    # {title} in title_arg is replaced by the file name when a single item is opened.
    "players": [],
    "active": "videos",
    # type "local": path (environment variables allowed);  type "rclone": remote + bucket + prefix
    "libraries": [
        {"id": "videos", "name": "Videos", "type": "local", "path": "%USERPROFILE%\\Videos" if sys.platform == "win32" else "~/Videos"},
    ],
}

# Players picked up automatically when their executable exists.
KNOWN_PLAYERS = [
    ("mpv", "mpv", ["%USERPROFILE%\\scoop\\apps\\mpv\\current\\mpv.exe", "mpv"], "--force-media-title={title}"),
    ("potplayer", "PotPlayer", [r"%ProgramFiles%\DAUM\PotPlayer\PotPlayerMini64.exe"], None),
    ("vlc", "VLC", [r"%ProgramFiles%\VideoLAN\VLC\vlc.exe", r"%ProgramFiles(x86)%\VideoLAN\VLC\vlc.exe"], "--meta-title={title}"),
    ("mpc-hc", "MPC-HC", [r"%ProgramFiles%\MPC-HC\mpc-hc64.exe"], None),
    ("mpc-be", "MPC-BE", [r"%ProgramFiles%\MPC-BE x64\mpc-be64.exe", r"%ProgramFiles%\MPC-BE\mpc-be64.exe"], None),
]

VIDEO_EXT = {".mp4", ".m4v", ".mov", ".mkv", ".webm", ".avi", ".wmv", ".flv", ".ts", ".m2ts"}
AUDIO_EXT = {".mp3", ".flac", ".m4a", ".aac", ".wav", ".ogg", ".opus"}
MP4_EXT = (".mp4", ".m4v", ".mov")
SKIP_DIRS = {"system volume information", "$recycle.bin", "@eadir", "#recycle"}
FRACTIONS = (0.1, 0.3, 0.5, 0.7, 0.9)  # where the keyframes are taken, as a share of the duration
THUMB_BOX = 480
SCALE = f"scale={THUMB_BOX}:{THUMB_BOX}:force_original_aspect_ratio=decrease:force_divisible_by=2"
# Retry filter for files tagged with "reserved" colour primaries/transfer, which swscale refuses to convert.
SCALE_BT709 = "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709," + SCALE
NO_WINDOW = subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0

_fetched = 0
_fetched_lock = threading.Lock()

if sys.platform == "win32":
    import ctypes
    from ctypes import wintypes

    _k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _k32.CreateMutexW.argtypes = [wintypes.LPVOID, wintypes.BOOL, wintypes.LPCWSTR]
    _k32.CreateMutexW.restype = wintypes.HANDLE
    _k32.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    _k32.WaitForSingleObject.restype = wintypes.DWORD
    _k32.ReleaseMutex.argtypes = [wintypes.HANDLE]


class Mutex:
    """A mutex shared by every thread of every medialib process: one per library and purpose.

    Waiting blocks in the kernel and the waiter is woken the moment the holder releases, so nothing polls or
    retries. The OS also releases it if the holder dies, so it cannot go stale the way a lock file can.
    Windows: a named mutex. Elsewhere: flock on a lock file. Release from the thread that acquired it.

      "io"     held for the instant library.json is read or replaced
      "index"  held for a whole indexing run, so two runs never work on one library at once
    """

    _handles, _guard = {}, threading.Lock()

    def __init__(self, lib, purpose):
        folder = lib_dir(lib)
        digest = hashlib.sha1(os.path.normcase(str(folder.resolve())).encode("utf-8")).hexdigest()[:20]
        self.name, self.file, self.fd = f"Local\\medialib-{digest}-{purpose}", folder / f".{purpose}.lock", None

    def acquire(self, wait=True):
        """True once held. With wait=False, False straight away if someone else holds it."""
        if sys.platform == "win32":
            with Mutex._guard:
                handle = Mutex._handles.get(self.name)
                if not handle:
                    handle = Mutex._handles[self.name] = _k32.CreateMutexW(None, False, self.name)
                    if not handle:
                        raise ctypes.WinError(ctypes.get_last_error())
            result = _k32.WaitForSingleObject(handle, 0xFFFFFFFF if wait else 0)
            if result in (0x0, 0x80):  # WAIT_OBJECT_0, or WAIT_ABANDONED: the last holder died and it is ours now
                return True
            if result == 0x102:  # WAIT_TIMEOUT
                return False
            raise ctypes.WinError(ctypes.get_last_error())
        import fcntl
        self.file.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(self.file, os.O_CREAT | os.O_RDWR)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | (0 if wait else fcntl.LOCK_NB))
        except BlockingIOError:
            os.close(fd)
            return False
        self.fd = fd
        return True

    def release(self):
        if sys.platform == "win32":
            _k32.ReleaseMutex(Mutex._handles[self.name])
        else:
            os.close(self.fd)  # closing the descriptor drops the flock

    def __enter__(self):
        self.acquire()
        return self

    def __exit__(self, *exc):
        self.release()
_config_lock = threading.RLock()


# ---------------------------------------------------------------- config / libraries

def slug(text):
    s = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")[:40] or "library"
    return "lib-" + s if re.fullmatch(r"[0-9a-f]{12}", s) else s  # 12 hex chars would read as a media id


def lib_dir(lib):
    return CACHE / lib["id"]


def save_config(cfg):
    with _config_lock:
        tmp = CONFIG.with_suffix(".tmp")
        tmp.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        os.replace(tmp, CONFIG)


def load_config():
    with _config_lock:
        raw = json.loads(CONFIG.read_text(encoding="utf-8")) if CONFIG.exists() else {}
        cfg = {**DEFAULT_CONFIG, **raw}
        if "libraries" not in raw:
            if "bucket" in raw:  # config from before libraries existed: a single rclone folder
                prefix = raw.get("prefix", "")
                lib = {"id": slug(f"{raw['bucket']}-{prefix}"), "name": f"{raw['bucket']}/{prefix.strip('/')}".rstrip("/"),
                       "type": "rclone", "remote": raw["remote"], "bucket": raw["bucket"], "prefix": prefix}
                cfg["libraries"], cfg["active"] = [lib], lib["id"]
        legacy = [k for k in ("remote", "bucket", "prefix") if k in cfg]
        for k in legacy:
            del cfg[k]
        if legacy or "libraries" not in raw:
            save_config(cfg)
        # Adopt the cache layout from before libraries existed (cache/library.json + cache/thumbs).
        first = lib_dir(cfg["libraries"][0])
        if (CACHE / "library.json").exists() and not first.exists():
            first.mkdir(parents=True)
            shutil.move(str(CACHE / "library.json"), str(first / "library.json"))
            if (CACHE / "thumbs").exists():
                shutil.move(str(CACHE / "thumbs"), str(first / "thumbs"))
        return cfg


def find_library(cfg, lib_id=None):
    libs = cfg["libraries"]
    for wanted in (lib_id, cfg.get("active")) if lib_id is None else (lib_id,):
        for lib in libs:
            if lib["id"] == wanted:
                return lib
    return libs[0] if lib_id is None and libs else None


def add_local_library(cfg, path, name=None):
    path = os.path.normpath(os.path.expandvars(path.strip().strip('"')))
    if not os.path.isabs(path) or not os.path.isdir(path):
        raise ValueError(f"Not a reachable folder: {path}")
    with _config_lock:
        for lib in cfg["libraries"]:
            if lib["type"] == "local" and os.path.normcase(local_root(lib)) == os.path.normcase(path):
                raise ValueError(f"That folder is already the library “{lib['name']}”.")
        name = (name or "").strip() or os.path.basename(path.rstrip("\\/")) or path
        base = lib_id = slug(name)
        taken = {lib["id"] for lib in cfg["libraries"]}
        n = 2
        while lib_id in taken:
            lib_id, n = f"{base}-{n}", n + 1
        lib = {"id": lib_id, "name": name, "type": "local", "path": path}
        cfg["libraries"].append(lib)
        save_config(cfg)
    return lib


def location(lib):
    return local_root(lib) if lib["type"] == "local" else f"{lib['remote']}{lib['bucket']}/{lib['prefix']}"


def local_root(lib):
    """A local library's folder, with environment variables and ~ expanded."""
    return os.path.expanduser(os.path.expandvars(lib["path"]))


def run(cmd, timeout=120, data=None):
    try:
        return subprocess.run(cmd, input=data, capture_output=True, timeout=timeout, creationflags=NO_WINDOW)
    except subprocess.TimeoutExpired:
        # Not the default message: it quotes the whole command line, presigned URL included.
        raise RuntimeError(f"{os.path.basename(cmd[0])} timed out after {timeout}s") from None


def rclone(cfg, *args, timeout=120):
    r = run([cfg["rclone"], *args], timeout=timeout)
    if r.returncode:
        raise RuntimeError(r.stderr.decode("utf-8", "replace").strip()[-400:])
    return r.stdout.decode("utf-8")  # raw UTF-8: the console codec would mangle CJK names


def presign(cfg, lib, key, expire):
    return rclone(cfg, "link", "--expire", expire, f"{lib['remote']}{lib['bucket']}/{key}", timeout=60).strip()


def _count(n):
    global _fetched
    with _fetched_lock:
        _fetched += n


class ConnPool:
    """Keep-alive connections per host, shared by every thread, so a ranged read rarely pays for a new
    TCP + TLS handshake (all presigned URLs of a library point at the same host)."""

    def __init__(self, keep=48):
        self.keep, self.idle, self.lock = keep, {}, threading.Lock()

    def get(self, url, start, end):
        u = urllib.parse.urlsplit(url)
        key, target, want = (u.scheme, u.netloc), u.path + ("?" + u.query if u.query else ""), end - start + 1
        while True:
            with self.lock:
                conn = self.idle[key].pop() if self.idle.get(key) else None
            reused = conn is not None
            if not reused:
                conn = (http.client.HTTPSConnection if u.scheme == "https" else http.client.HTTPConnection)(u.netloc, timeout=60)
            try:
                conn.request("GET", target, headers={"Range": f"bytes={start}-{end}"})
                resp = conn.getresponse()
                length = int(resp.getheader("Content-Length") or -1)
                if resp.status == 206 or (resp.status == 200 and start == 0 and 0 <= length <= want):
                    data = resp.read()
                else:  # never drain a whole object because a server ignored Range
                    conn.close()
                    raise RuntimeError("server ignored the Range header" if resp.status == 200 else f"HTTP {resp.status} {resp.reason}")
            except (http.client.HTTPException, OSError):
                conn.close()
                if reused:
                    continue  # an idle connection the server had already dropped: go again on a fresh one
                raise
            if resp.will_close:
                conn.close()
            else:
                with self.lock:
                    idle = self.idle.setdefault(key, [])
                    idle.append(conn) if len(idle) < self.keep else conn.close()
            return data


_conns = ConnPool()
# Keyframes of one video are fetched and decoded side by side on this pool, shared by all files in progress.
_frame_pool = cf.ThreadPoolExecutor(max_workers=max(8, (os.cpu_count() or 4) * 2), thread_name_prefix="frame")


def default_workers(lib):
    """Files in flight at once: S3 waits mostly on the network, a local folder on decoding and the disk."""
    cpus = os.cpu_count() or 4
    return max(4, cpus // 2) if lib["type"] == "local" else min(32, max(8, cpus * 2))


def fetch(url, start, end, tries=3):
    """Bytes start..end (inclusive) of a presigned URL, never reading past end."""
    for attempt in range(tries):
        try:
            data = _conns.get(url, start, end)
            _count(len(data))
            return data
        except (OSError, http.client.HTTPException):
            if attempt == tries - 1:
                raise
            time.sleep(1.5 * (attempt + 1))


# ---------------------------------------------------------------- sources
# A source lists a library's media and opens a reader per item. A reader gives random access to the bytes
# (read) and a `target` ffmpeg/ffprobe can open themselves (URL or file path) for the fallback path.

class HttpReader:
    def __init__(self, url):
        self.target = url

    def read(self, start, end):
        return fetch(self.target, start, end)

    def close(self):
        pass


class FileReader:
    def __init__(self, path):
        self.target, self._f, self._lock = path, None, threading.Lock()

    def read(self, start, end):
        with self._lock:  # one handle, read from several frame threads
            if self._f is None:
                self._f = open(self.target, "rb")
            self._f.seek(start)
            data = self._f.read(end - start + 1)
        _count(len(data))
        return data

    def close(self):
        if self._f:
            self._f.close()


def media_kind(name):
    ext = os.path.splitext(name)[1].lower()
    return "video" if ext in VIDEO_EXT else "audio" if ext in AUDIO_EXT else None


def make_item(key, name, rel_dir, kind, size, mtime):
    return {
        "id": hashlib.sha1(key.encode("utf-8")).hexdigest()[:12], "key": key, "name": name, "dir": rel_dir, "kind": kind,
        "size": size, "mtime": mtime, "ver": hashlib.sha1(f"{key}|{size}|{mtime}".encode("utf-8")).hexdigest()[:10],
    }


class RcloneSource:
    def __init__(self, cfg, lib):
        self.cfg, self.lib = cfg, lib

    def list(self):
        rows = json.loads(rclone(self.cfg, "lsjson", "-R", "--files-only", "--fast-list", "--no-mimetype",
                                 "--use-server-modtime", location(self.lib), timeout=900))
        items = []
        for r in rows:
            kind = media_kind(r["Name"])
            if kind:
                items.append(make_item(self.lib["prefix"] + r["Path"], r["Name"], r["Path"].rpartition("/")[0], kind,
                                       r["Size"], r["ModTime"][:19] + "Z"))
        return items

    def reader(self, item):
        return HttpReader(presign(self.cfg, self.lib, item["key"], "2h"))


class LocalSource:
    def __init__(self, cfg, lib):
        self.root, self.warnings = local_root(lib), []

    def path_of(self, item):
        return os.path.join(self.root, *item["key"].split("/"))

    def list(self):
        if not os.path.isdir(self.root):
            raise RuntimeError(f"Folder not reachable: {self.root}")
        items, stack, self.warnings = [], [(self.root, "")], []
        while stack:
            folder, rel_dir = stack.pop()
            try:
                entries = list(os.scandir(folder))
            except OSError:
                continue
            seen = {}
            for e in entries:
                # A NAS keeps "Clips" and "clips" apart, Windows does not: both names open whichever one the server
                # picks, so indexing both would list one folder twice and the other not at all. Keep the first.
                twin = seen.setdefault(e.name.lower(), e.name)
                if twin != e.name:
                    where = f"{rel_dir}/" if rel_dir else ""
                    self.warnings.append(f"“{where}{twin}” and “{where}{e.name}” differ only by letter case. Windows can open "
                                         "only one of them, so the second is skipped. Rename one on the NAS itself to fix this.")
                    continue
                try:
                    if e.is_dir(follow_symlinks=False):
                        if not e.name.startswith(".") and e.name.lower() not in SKIP_DIRS:
                            stack.append((e.path, f"{rel_dir}/{e.name}" if rel_dir else e.name))
                        continue
                    kind = media_kind(e.name)
                    if not kind:
                        continue
                    st = e.stat()  # on Windows this comes with the directory listing: no extra round trip on a NAS
                except OSError:
                    continue
                mtime = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(st.st_mtime))
                items.append(make_item(f"{rel_dir}/{e.name}" if rel_dir else e.name, e.name, rel_dir, kind, st.st_size, mtime))
        return items

    def reader(self, item):
        return FileReader(self.path_of(item))


def make_source(cfg, lib):
    return (LocalSource if lib["type"] == "local" else RcloneSource)(cfg, lib)


# ---------------------------------------------------------------- MP4 index parsing

def boxes(buf, pos, end):
    """(type, payload_start, box_end) for each box in buf[pos:end]."""
    while pos + 8 <= end:
        size, typ = struct.unpack_from(">I4s", buf, pos)
        hdr = 8
        if size == 1:
            size, hdr = struct.unpack_from(">Q", buf, pos + 8)[0], 16
        elif size == 0:
            size = end - pos
        if size < hdr:
            return
        yield typ.decode("latin-1"), pos + hdr, min(pos + size, end)
        pos += size


def child(buf, start, end, *path):
    for name in path:
        for typ, s, e in boxes(buf, start, end):
            if typ == name:
                start, end = s, e
                break
        else:
            return None
    return start, end


def read_moov(reader, size):
    """Walk the top-level boxes with small ranged reads and return the moov box payload."""
    head = reader.read(0, min(size, 65536) - 1)
    pos = 0
    while pos + 8 <= size:
        hdr = head[pos:pos + 16] if pos + 16 <= len(head) else reader.read(pos, min(pos + 15, size - 1))
        box_size, typ = struct.unpack_from(">I4s", hdr)
        hlen = 8
        if box_size == 1:
            box_size, hlen = struct.unpack_from(">Q", hdr, 8)[0], 16
        elif box_size == 0:
            box_size = size - pos
        if box_size < hlen:
            raise ValueError("corrupt top-level box")
        if typ == b"moov":
            end = pos + box_size
            box = head[pos:end] if end <= len(head) else reader.read(pos, end - 1)
            return box[hlen:]
        pos += box_size
    raise ValueError("no moov box")


def u32s(buf, pos, count, code="I"):
    a = array(code)
    a.frombytes(buf[pos:pos + a.itemsize * count])
    if sys.byteorder == "little":
        a.byteswap()
    return a


def parse_tracks(moov):
    tracks = []
    for typ, s, e in boxes(moov, 0, len(moov)):
        if typ != "trak":
            continue
        hd, md, tk = child(moov, s, e, "mdia", "hdlr"), child(moov, s, e, "mdia", "mdhd"), child(moov, s, e, "tkhd")
        if not (hd and md and tk):
            continue
        if moov[md[0]] == 1:
            timescale, duration = struct.unpack_from(">IQ", moov, md[0] + 20)
        else:
            timescale, duration = struct.unpack_from(">II", moov, md[0] + 12)
        w, h = struct.unpack_from(">II", moov, tk[1] - 8)
        tracks.append({
            "handler": moov[hd[0] + 8:hd[0] + 12].decode("latin-1"), "timescale": timescale, "duration": duration,
            "width": w >> 16, "height": h >> 16, "stbl": child(moov, s, e, "mdia", "minf", "stbl"),
        })
    return tracks


def read_stbl(buf, s, e):
    def box(name):
        return child(buf, s, e, name)

    st = {}
    stsd = box("stsd")
    entry = stsd[0] + 8
    esize, fourcc = struct.unpack_from(">I4s", buf, entry)
    st["fourcc"] = fourcc.decode("latin-1")
    st["width"], st["height"] = struct.unpack_from(">HH", buf, entry + 8 + 24)
    st["conf"] = None
    for typ, cs, ce in boxes(buf, entry + 8 + 78, entry + esize):
        if typ in ("avcC", "hvcC", "av1C"):
            st["conf"] = (typ, bytes(buf[cs:ce]))

    p = box("stts")[0]
    st["stts"] = u32s(buf, p + 8, 2 * struct.unpack_from(">I", buf, p + 4)[0])
    p = box("stsz")[0]
    st["const_size"], st["count"] = struct.unpack_from(">II", buf, p + 4)
    st["sizes"] = None if st["const_size"] else u32s(buf, p + 12, st["count"])
    p = box("stsc")[0]
    st["stsc"] = u32s(buf, p + 8, 3 * struct.unpack_from(">I", buf, p + 4)[0])
    co = box("stco") or box("co64")
    code = "I" if box("stco") else "Q"
    st["chunks"] = u32s(buf, co[0] + 8, struct.unpack_from(">I", buf, co[0] + 4)[0], code)
    ss = box("stss")
    st["sync"] = u32s(buf, ss[0] + 8, struct.unpack_from(">I", buf, ss[0] + 4)[0]) if ss else None
    return st


def sync_times(stts, sync, count):
    """(sample_number, decode_time) for each sync sample; every ~sample if the track has no stss."""
    if sync is None:
        sync = range(1, count + 1, max(1, count // 400))
    out, it = [], iter(sync)
    nxt = next(it, None)
    sample, t = 1, 0
    for i in range(0, len(stts), 2):
        cnt, delta = stts[i], stts[i + 1]
        while nxt is not None and nxt < sample + cnt:
            out.append((nxt, t + (nxt - sample) * delta))
            nxt = next(it, None)
        sample += cnt
        t += cnt * delta
    return out


def sample_location(st, n):
    """File offset and size of 1-based sample n."""
    idx, acc, stsc = n - 1, 0, st["stsc"]
    for j in range(0, len(stsc), 3):
        first, per_chunk = stsc[j], stsc[j + 1]
        last = stsc[j + 3] if j + 3 < len(stsc) else len(st["chunks"]) + 1
        run_len = (last - first) * per_chunk
        if idx < acc + run_len:
            chunk = first + (idx - acc) // per_chunk
            first_in_chunk = acc + (chunk - first) * per_chunk
            off = st["chunks"][chunk - 1]
            if st["const_size"]:
                return off + (idx - first_in_chunk) * st["const_size"], st["const_size"]
            return off + sum(st["sizes"][first_in_chunk:idx]), st["sizes"][idx]
        acc += run_len
    raise ValueError(f"sample {n} outside the chunk table")


def param_sets(kind, c):
    """NAL length size and parameter-set NALs (SPS/PPS, plus VPS for HEVC) from avcC/hvcC."""
    params = []
    if kind == "avcC":
        nal_len, pos = (c[4] & 3) + 1, 6
        groups = [c[5] & 0x1F]
        for g in range(2):
            for _ in range(groups[g]):
                n = int.from_bytes(c[pos:pos + 2], "big")
                params.append(c[pos + 2:pos + 2 + n])
                pos += 2 + n
            if g == 0:
                groups.append(c[pos])
                pos += 1
    else:
        nal_len, pos = (c[21] & 3) + 1, 23
        for _ in range(c[22]):
            count = int.from_bytes(c[pos + 1:pos + 3], "big")
            pos += 3
            for _ in range(count):
                n = int.from_bytes(c[pos:pos + 2], "big")
                params.append(c[pos + 2:pos + 2 + n])
                pos += 2 + n
    return nal_len, params


def annexb(sample, nal_len, params):
    out = bytearray()
    for p in params:
        out += b"\0\0\0\1" + p
    pos = 0
    while pos + nal_len <= len(sample):
        n = int.from_bytes(sample[pos:pos + nal_len], "big")
        pos += nal_len
        out += b"\0\0\0\1" + sample[pos:pos + n]
        pos += n
    return bytes(out)


def decode_frame(cfg, fmt, data, out):
    cmd = [cfg["ffmpeg"], "-v", "error", "-f", fmt]
    if fmt == "h264":
        cmd += ["-flags2", "+showall"]  # also output lone non-IDR I-frames
    for vf in (SCALE, SCALE_BT709):
        run(cmd + ["-i", "pipe:0", "-frames:v", "1", "-vf", vf, "-q:v", "5", "-y", str(out)], timeout=60, data=data)
        if out.exists() and out.stat().st_size > 0:
            return True
    return False


def mp4_keyframes(cfg, reader, size, stem):
    """stem is the thumbnail path without its '-<n>.jpg' suffix."""
    moov = read_moov(reader, size)
    tracks = parse_tracks(moov)
    vt = next((t for t in tracks if t["handler"] == "vide" and t["stbl"]), None)
    if not vt or not vt["duration"]:
        raise ValueError("no indexed video track")
    st = read_stbl(moov, *vt["stbl"])
    if not st["conf"]:
        raise ValueError(f"no fast path for {st['fourcc']}")
    if st["conf"][0] == "av1C":
        # AV1 samples are already OBUs. A decodable stream is: temporal delimiter, the sequence header kept in
        # av1C (after its 4 fixed bytes), then the keyframe's own OBUs.
        fmt, head = "obu", b"\x12\x00" + st["conf"][1][4:]

        def bitstream(sample):
            return head + sample
    else:
        fmt = "h264" if st["conf"][0] == "avcC" else "hevc"
        nal_len, params = param_sets(*st["conf"])

        def bitstream(sample):
            return annexb(sample, nal_len, params)
    duration = vt["duration"] / vt["timescale"]
    meta = {
        "duration": round(duration, 2), "width": vt["width"] or st["width"], "height": vt["height"] or st["height"],
        "codec": st["fourcc"], "fps": round(st["count"] / duration, 2), "audio": any(t["handler"] == "soun" for t in tracks),
    }
    syncs = sync_times(st["stts"], st["sync"], st["count"])
    times = [t for _, t in syncs]
    picks = []
    for f in FRACTIONS:
        n = syncs[max(0, bisect.bisect_right(times, f * vt["duration"]) - 1)][0]
        if n not in picks:
            picks.append(n)
    def grab(job):
        i, n = job
        off, length = sample_location(st, n)
        out = pathlib.Path(f"{stem}-k{i}.jpg")
        return out if decode_frame(cfg, fmt, bitstream(reader.read(off, off + length - 1)), out) else None

    paths = number_frames(stem, _frame_pool.map(grab, enumerate(picks)))
    if not paths:
        raise ValueError("no keyframe decoded")
    return meta, paths


def number_frames(stem, outs):
    """Rename the frames that came out (in time order) to <stem>-0.jpg, -1.jpg, ... with no gaps."""
    paths = []
    for out in outs:
        if out:
            final = pathlib.Path(f"{stem}-{len(paths)}.jpg")
            os.replace(out, final)
            paths.append(final)
    return paths


# ---------------------------------------------------------------- fallback (non-MP4 / odd files)

def ffprobe_meta(cfg, target):
    r = run([cfg["ffprobe"], "-v", "error", "-show_entries", "format=duration:stream=codec_type,codec_name,width,height",
             "-of", "json", target], timeout=120)
    info = json.loads(r.stdout.decode("utf-8") or "{}")
    streams = info.get("streams", [])
    v = next((s for s in streams if s.get("codec_type") == "video"), {})
    a = next((s for s in streams if s.get("codec_type") == "audio"), {})
    return {
        "duration": round(float(info.get("format", {}).get("duration") or 0), 2), "width": v.get("width"),
        "height": v.get("height"), "codec": v.get("codec_name") or a.get("codec_name"), "audio": any(s.get("codec_type") == "audio" for s in streams),
    }


def grab_seek(cfg, target, t, out):
    for vf in (SCALE, SCALE_BT709):
        run([cfg["ffmpeg"], "-v", "error", "-threads", "1", "-noaccurate_seek", "-ss", f"{t:.2f}", "-i", target,
             "-map", "0:v:0", "-frames:v", "1", "-vf", vf, "-q:v", "5", "-y", str(out)], timeout=180)
        if out.exists() and out.stat().st_size > 0:
            return True
    return False


def cover_score(path):
    """Prefer detailed, mid-exposure frames over black, white or flat ones."""
    try:
        from PIL import Image, ImageStat
        with Image.open(path) as im:
            s = ImageStat.Stat(im.convert("L").resize((64, 36)))
        return s.stddev[0] * (1 - min(1.0, abs(s.mean[0] - 115) / 160))
    except ImportError:
        return path.stat().st_size


# ---------------------------------------------------------------- indexing

def index_item(cfg, source, thumbs, item):
    rec = dict(item)
    base = f"{item['id']}-{item['ver']}"
    for old in thumbs.glob(base + "-*.jpg"):
        old.unlink()
    stem = thumbs / base
    reader = source.reader(item)
    paths, note = [], None
    try:
        if item["kind"] == "video":
            if item["name"].lower().endswith(MP4_EXT):
                try:
                    meta, paths = mp4_keyframes(cfg, reader, item["size"], stem)
                except Exception as e:  # odd MP4 layouts: let ffmpeg handle them
                    note = f"fast path: {e}"
            if not paths:
                meta = ffprobe_meta(cfg, reader.target)

                def grab(job):
                    out = pathlib.Path(f"{stem}-k{job[0]}.jpg")
                    return out if grab_seek(cfg, reader.target, meta["duration"] * job[1], out) else None

                if meta["duration"]:
                    # Each ffmpeg opens the file itself. Side by side suits S3; on a disk or NAS it would multiply
                    # the concurrent streams fivefold and thrash it, so there they go one after another.
                    run_all = map if isinstance(reader, FileReader) else _frame_pool.map
                    paths = number_frames(stem, run_all(grab, enumerate(FRACTIONS)))
        else:
            meta = ffprobe_meta(cfg, reader.target)
            out = pathlib.Path(f"{stem}-0.jpg")
            run([cfg["ffmpeg"], "-v", "error", "-i", reader.target, "-map", "0:v:0?", "-frames:v", "1", "-vf", SCALE, "-q:v", "5", "-y", str(out)])
            if out.exists() and out.stat().st_size:
                paths.append(out)
    finally:
        reader.close()
    rec.update(meta)
    rec["frames"] = len(paths)
    rec["cover"] = max(range(len(paths)), key=lambda i: cover_score(paths[i])) if paths else None
    rec["indexed"] = True
    if note and item["name"].lower().endswith(MP4_EXT):
        rec["note"] = note
    if item["kind"] == "video" and not paths:
        rec["error"] = note or "no frame could be extracted"
    return rec


def save_library(lib, recs, warnings=()):
    path = lib_dir(lib) / "library.json"
    data = {"library": lib["id"], "name": lib["name"], "type": lib["type"], "location": location(lib),
            "updated": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "warnings": list(warnings),
            "items": sorted(recs.values(), key=lambda r: r["key"])}
    text = json.dumps(data, ensure_ascii=False, separators=(",", ":"))
    tmp = path.with_suffix(".tmp")
    # Windows refuses to replace a file that anyone has open. Every medialib reader and writer takes the io mutex,
    # so none of them can be holding library.json at this point.
    with Mutex(lib, "io"):
        tmp.write_text(text, encoding="utf-8")
        try:
            os.replace(tmp, path)
        except PermissionError:
            # A program that is not medialib (antivirus, backup, an editor) has library.json open. Nothing is lost
            # and nothing needs retrying: library.tmp is complete and newer, load_library reads it in preference,
            # and the next save or load promotes it.
            return False
    return True


def load_library(lib):
    path = lib_dir(lib) / "library.json"
    tmp = path.with_suffix(".tmp")
    with Mutex(lib, "io"):
        if tmp.exists():  # a save whose replace was blocked (see save_library), or a crash in the middle of a write
            try:
                data = json.loads(tmp.read_text(encoding="utf-8"))
            except ValueError:  # torn write: library.json is still the truth
                try:
                    tmp.unlink()
                except OSError:
                    pass
            else:
                try:
                    os.replace(tmp, path)
                except PermissionError:
                    pass
                return data
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return {"items": []}


def human(n):
    for unit in ("B", "KiB", "MiB", "GiB", "TiB"):
        if n < 1024 or unit == "TiB":
            return f"{n:.1f} {unit}" if unit != "B" else f"{n} B"
        n /= 1024


def run_index(cfg, lib, workers=None, limit=None, force=False, report=lambda **kw: None):
    """Bring a library's index up to date. report(**fields) receives progress: state, total, done, errors, line.

    One run per library at a time, in any process. A second run says so and sleeps on the index mutex until the
    first releases it, then does its own (by then mostly empty) incremental pass."""
    lock = Mutex(lib, "index")
    if not lock.acquire(wait=False):
        report(state="waiting", line=f"“{lib['name']}” is already being indexed by another medialib process. "
                                     "Waiting for it to finish ...")
        lock.acquire()
    try:
        _run_index(cfg, lib, workers, limit, force, report)
    finally:
        lock.release()


def _run_index(cfg, lib, workers, limit, force, report):
    thumbs = lib_dir(lib) / "thumbs"
    thumbs.mkdir(parents=True, exist_ok=True)
    source = make_source(cfg, lib)
    workers = workers or cfg.get("workers") or default_workers(lib)
    report(state="listing", line=f"Listing {location(lib)} ...")
    listed = source.list()
    warnings = getattr(source, "warnings", [])
    for w in warnings:
        report(line="Warning: " + w)
    old = {r["id"]: r for r in load_library(lib)["items"]}
    recs, todo = {}, []
    for it in listed:
        prev = old.get(it["id"])
        current = bool(prev and prev.get("ver") == it["ver"] and prev.get("indexed") and not prev.get("error"))
        # A still-valid record stays in place until its replacement lands, so --force (and --limit) never blank it.
        recs[it["id"]] = prev if current else {**it, "indexed": False}
        if force or not current:
            todo.append(it)
    fresh = len(listed) - len(todo)
    if limit:
        todo = todo[:limit]
    report(state="indexing", total=len(todo), done=0, errors=0,
           line=f"{len(listed)} media files; {fresh} up to date, indexing {len(todo)} with {workers} workers")
    save_library(lib, recs, warnings)

    started, errors, fetched0, saved = time.time(), 0, _fetched, time.time()
    pool = cf.ThreadPoolExecutor(workers)
    try:
        futures = {pool.submit(index_item, cfg, source, thumbs, it): it for it in todo}
        for done, fut in enumerate(cf.as_completed(futures), 1):
            it = futures[fut]
            try:
                rec = fut.result()
            except Exception as e:
                rec = {**it, "indexed": False, "error": str(e)[:300]}
            recs[it["id"]] = rec
            if rec.get("error"):
                errors += 1
            if time.time() - saved > 3:  # lets a running UI pick up new covers as they land
                try:
                    save_library(lib, recs, warnings)
                except OSError:
                    pass  # only a checkpoint: the next one, or the final save below, will land
                saved = time.time()
            flag = "ERR " + rec["error"] if rec.get("error") else f"{rec.get('frames', 0)} frames" + (" (ffmpeg fallback)" if rec.get("note") else "")
            report(done=done, errors=errors,
                   line=f"[{done}/{len(todo)}] {human(_fetched - fetched0)} read, {time.time() - started:.0f}s | {it['key']} -> {flag}")
    except BaseException:
        # Stop at once and keep what is recorded; never let workers grind on with nobody collecting results.
        pool.shutdown(wait=False, cancel_futures=True)
        try:
            save_library(lib, recs, warnings)
        except OSError:
            pass
        raise
    pool.shutdown()

    keep = {f"{r['id']}-{r['ver']}" for r in recs.values()}
    orphans = [p for p in thumbs.glob("*.jpg") if p.stem.rsplit("-", 1)[0] not in keep]
    for p in orphans:
        p.unlink()
    save_library(lib, recs, warnings)
    report(state="done", line=f"Done: {len(todo)} indexed in {time.time() - started:.0f}s, {errors} errors, "
                              f"{human(_fetched - fetched0)} read by the MP4 fast path, {len(orphans)} stale thumbnails removed.")


# ---------------------------------------------------------------- player focus (Windows)
# Windows only lets a new window take the foreground if the foreground app (here the browser) started it.
# Players are started by this background server instead, so their window would open behind the browser.
# After launching, wait for the player's window and hand it the foreground explicitly.

if sys.platform == "win32":
    import ctypes
    from ctypes import wintypes

    _user32 = ctypes.WinDLL("user32", use_last_error=True)
    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _WNDENUMPROC = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
    _user32.EnumWindows.argtypes = [_WNDENUMPROC, wintypes.LPARAM]
    _user32.GetWindowThreadProcessId.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)]
    _user32.GetWindowThreadProcessId.restype = wintypes.DWORD
    _user32.GetWindow.argtypes = [wintypes.HWND, wintypes.UINT]
    _user32.GetWindow.restype = wintypes.HWND
    _user32.GetForegroundWindow.restype = wintypes.HWND
    for _fn in ("IsWindowVisible", "GetWindowTextLengthW", "IsIconic", "BringWindowToTop", "SetForegroundWindow"):
        getattr(_user32, _fn).argtypes = [wintypes.HWND]
    _user32.ShowWindow.argtypes = [wintypes.HWND, ctypes.c_int]
    _user32.AttachThreadInput.argtypes = [wintypes.DWORD, wintypes.DWORD, wintypes.BOOL]
    _kernel32.OpenProcess.restype = wintypes.HANDLE
    _kernel32.QueryFullProcessImageNameW.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD)]

    def _top_windows():
        """(hwnd, pid) of visible, unowned, titled top-level windows."""
        out = []

        def cb(hwnd, _):
            if _user32.IsWindowVisible(hwnd) and not _user32.GetWindow(hwnd, 4) and _user32.GetWindowTextLengthW(hwnd):  # 4 = GW_OWNER
                pid = wintypes.DWORD()
                _user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
                out.append((hwnd, pid.value))
            return True

        _user32.EnumWindows(_WNDENUMPROC(cb), 0)
        return out

    def _exe_dir(pid, cache={}):
        if pid not in cache:
            path = ""
            h = _kernel32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
            if h:
                buf, size = ctypes.create_unicode_buffer(1024), wintypes.DWORD(1024)
                if _kernel32.QueryFullProcessImageNameW(h, 0, buf, ctypes.byref(size)):
                    path = os.path.normcase(os.path.dirname(buf.value))
                _kernel32.CloseHandle(h)
            cache[pid] = path
        return cache[pid]

    def _focus(hwnd):
        if _user32.GetForegroundWindow() == hwnd:
            return True  # it activated itself; touching it now would only knock it back out
        if _user32.IsIconic(hwnd):
            _user32.ShowWindow(hwnd, 9)  # SW_RESTORE
        fg_thread = _user32.GetWindowThreadProcessId(_user32.GetForegroundWindow(), None)
        me = _kernel32.GetCurrentThreadId()
        # Borrow the foreground thread's input state so SetForegroundWindow is allowed; never the player's own.
        attached = bool(fg_thread not in (0, me, _user32.GetWindowThreadProcessId(hwnd, None))
                        and _user32.AttachThreadInput(me, fg_thread, True))
        try:
            _user32.BringWindowToTop(hwnd)
            _user32.SetForegroundWindow(hwnd)
        finally:
            if attached:
                _user32.AttachThreadInput(me, fg_thread, False)
        if _user32.GetForegroundWindow() != hwnd:
            # Last resort: a synthetic Alt tap makes this process the last-input owner, which lifts the lock.
            _user32.keybd_event(0x12, 0, 0, 0)
            _user32.keybd_event(0x12, 0, 2, 0)
            _user32.SetForegroundWindow(hwnd)
        return _user32.GetForegroundWindow() == hwnd

    def bring_to_front(proc, exe, before, timeout=20.0):
        """Focus the window a just-launched player opens: one owned by its process, else a new window from the
        player's folder (child processes, mpv.com -> mpv.exe), else, if it handed off to an already running
        instance and exited, that instance's window. With no process (os.startfile), any new window."""
        folder = os.path.normcase(os.path.dirname(os.path.abspath(exe))) if exe else None
        deadline = time.time() + timeout
        while time.time() < deadline:
            wins = _top_windows()
            fresh = [h for h, p in wins if h not in before]
            if proc is None:
                target = fresh[:1]
            else:
                target = ([h for h, p in wins if p == proc.pid]
                          or [h for h, p in wins if h in fresh and _exe_dir(p) == folder])
                if not target and proc.poll() is not None:
                    target = [h for h, p in wins if _exe_dir(p) == folder]
            if target:
                # The player may still be activating itself or restyling its window: check that the focus sticks.
                for _ in range(4):
                    time.sleep(0.3)
                    if _focus(target[0]):
                        time.sleep(0.4)
                        if _user32.GetForegroundWindow() == target[0]:
                            return True
                return False
            time.sleep(0.15)
        return False

    _user32.FindWindowW.argtypes = [wintypes.LPCWSTR, wintypes.LPCWSTR]
    _user32.FindWindowW.restype = wintypes.HWND

    def focus_titled(title, timeout=15.0):
        """Focus a window by its exact title once it appears (dialogs are owned windows, which
        bring_to_front skips)."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            hwnd = _user32.FindWindowW(None, title)
            if hwnd:
                time.sleep(0.2)
                return _focus(hwnd)
            time.sleep(0.15)
        return False
else:
    def _top_windows():
        return []

    def bring_to_front(proc, exe, before, timeout=20.0):
        return False

    def focus_titled(title, timeout=15.0):
        return False


# ---------------------------------------------------------------- serving

def detect_players(cfg):
    players = [dict(p) for p in cfg.get("players", []) if pathlib.Path(p["path"]).exists() or shutil.which(p["path"])]
    seen = {os.path.normcase(os.path.abspath(shutil.which(p["path"]) or p["path"])) for p in players}
    for pid, name, paths, title_arg in KNOWN_PLAYERS:
        if any(p["id"] == pid for p in players):
            continue
        for raw in paths:
            path = shutil.which(raw) if not os.path.isabs(os.path.expandvars(raw)) else os.path.expandvars(raw)
            if path and os.path.exists(path) and os.path.normcase(os.path.abspath(path)) not in seen:
                seen.add(os.path.normcase(os.path.abspath(path)))
                players.append({"id": pid, "name": name, "path": path, "title_arg": title_arg})
                break
    if sys.platform == "win32":
        players.append({"id": "system", "name": "System default", "path": None})
    return players


# A native folder dialog, run in a child process because Tk wants its own main thread.
PICK_TITLE = "Choose a media folder"
PICK_FOLDER = f"""
import tkinter, tkinter.filedialog as fd
root = tkinter.Tk(); root.withdraw(); root.attributes('-topmost', True)
print(fd.askdirectory(parent=root, title={PICK_TITLE!r}, mustexist=True))
"""


RUNNING = ("waiting", "listing", "indexing")  # job states that mean "not finished"


class Store:
    """One library's library.json, reloaded whenever the indexer rewrites it."""

    def __init__(self, lib):
        self.lib, self.lock, self.mtime, self.data, self.by_id = lib, threading.Lock(), -1, {"items": []}, {}

    def get(self):
        path = lib_dir(self.lib) / "library.json"
        with self.lock:
            # library.tmp counts too: a save whose replace was blocked lives there until it is promoted
            m = tuple(f.stat().st_mtime if f.exists() else None for f in (path, path.with_suffix(".tmp")))
            if m != self.mtime:
                self.mtime, self.data = m, load_library(self.lib)
                self.by_id = {r["id"]: r for r in self.data["items"]}
            return self.data, self.by_id


class App:
    def __init__(self, cfg, port):
        self.cfg, self.port = cfg, port
        self.players = detect_players(cfg)
        self.lock = threading.RLock()
        self.stores, self.links, self.jobs = {}, {}, {}

    # ---- libraries
    def store(self, lib):
        with self.lock:
            if lib["id"] not in self.stores:
                self.stores[lib["id"]] = Store(lib)
            return self.stores[lib["id"]]

    def describe(self, lib):
        data, _ = self.store(lib).get()
        return {"id": lib["id"], "name": lib["name"], "type": lib["type"], "location": location(lib),
                "items": len(data["items"]), "updated": data.get("updated"),
                "reachable": os.path.isdir(local_root(lib)) if lib["type"] == "local" else True}

    def remove(self, lib_id):
        with self.lock, _config_lock:
            lib = find_library(self.cfg, lib_id)
            if not lib:
                raise ValueError("unknown library")
            if len(self.cfg["libraries"]) == 1:
                raise ValueError("The last library can't be removed.")
            if self.jobs.get(lib_id, {}).get("state") in RUNNING:
                raise ValueError("That library is being indexed; wait for it to finish.")
            self.cfg["libraries"].remove(lib)
            if self.cfg.get("active") == lib_id:
                self.cfg["active"] = self.cfg["libraries"][0]["id"]
            save_config(self.cfg)
            self.stores.pop(lib_id, None)
            self.jobs.pop(lib_id, None)
        target = lib_dir(lib).resolve()
        if target.parent == CACHE.resolve():  # only ever the generated index and thumbnails, never media
            shutil.rmtree(target, ignore_errors=True)

    # ---- indexing jobs
    def start_index(self, lib, force=False):
        with self.lock:
            job = self.jobs.get(lib["id"])
            if job and job["state"] in RUNNING:
                return job
            job = self.jobs[lib["id"]] = {"state": "listing", "total": 0, "done": 0, "errors": 0, "line": "", "started": time.time()}

        def work():
            try:
                run_index(self.cfg, lib, force=force, report=lambda **kw: job.update(kw))
            except Exception as e:
                job.update(state="error", line=str(e)[:300])
            job["finished"] = time.time()

        threading.Thread(target=work, daemon=True).start()
        return job

    # ---- media
    def link(self, lib, rec):
        """Presigned URL for an S3 item, reused while it has more than 12 h left."""
        key = (lib["id"], rec["id"])
        with self.lock:
            url, until = self.links.get(key, (None, 0))
        if time.time() > until:
            url = presign(self.cfg, lib, rec["key"], "24h")
            with self.lock:
                self.links[key] = (url, time.time() + 12 * 3600)
        return url

    def local_path(self, lib, rec):
        return os.path.join(local_root(lib), *rec["key"].split("/"))

    def media_url(self, host, lib, rec):
        return f"http://{host}/media/{lib['id']}/{rec['id']}/{urllib.parse.quote(rec['name'])}"

    def playlist(self, lib, recs, host=None):
        """M3U with stable URLs; with host=None a local library lists its file paths instead."""
        lines = ["#EXTM3U"]
        for r in recs:
            entry = self.local_path(lib, r) if host is None and lib["type"] == "local" else self.media_url(host or f"127.0.0.1:{self.port}", lib, r)
            lines += [f"#EXTINF:{int(r.get('duration') or -1)},{r['name']}", entry]
        return "\n".join(lines) + "\n"

    def play(self, lib, player_id, recs):
        if not self.players:
            raise RuntimeError("No media player found. Add one under \"players\" in config.json.")
        player = next((p for p in self.players if p["id"] == player_id), None) or self.players[0]
        if len(recs) == 1 and lib["type"] == "local":
            target = self.local_path(lib, recs[0])  # the file itself: no HTTP hop, subtitles next to it still load
            if not os.path.exists(target):
                raise RuntimeError(f"File not reachable: {target}")
        elif len(recs) == 1 and player["id"] != "system":
            target = self.media_url(f"127.0.0.1:{self.port}", lib, recs[0])
        else:
            pl = CACHE / "playlists" / "now-playing.m3u8"
            pl.parent.mkdir(parents=True, exist_ok=True)
            pl.write_text(self.playlist(lib, recs), encoding="utf-8")
            target = str(pl)
        before = {h for h, _ in _top_windows()}
        if player["id"] == "system":
            os.startfile(target)
            proc = None
        else:
            cmd = [player["path"]]
            if len(recs) == 1 and player.get("title_arg"):
                cmd.append(player["title_arg"].format(title=recs[0]["name"]))
            proc = subprocess.Popen(cmd + [target], close_fds=True)
        threading.Thread(target=bring_to_front, args=(proc, player.get("path"), before), daemon=True).start()
        return player["name"]


def make_handler(app, loopback):
    class Handler(BaseHTTPRequestHandler):
        server_version = "medialib"

        def log_message(self, fmt, *args):
            pass

        def allowed(self):
            # Refuse cross-site requests from web pages, and foreign Host names (DNS rebinding) on loopback.
            if self.headers.get("Sec-Fetch-Site") == "cross-site":
                return False
            host = (self.headers.get("Host") or "").rsplit(":", 1)[0].strip("[]")
            return not loopback or host in ("127.0.0.1", "localhost", "::1")

        def send(self, code, body=b"", ctype="application/json; charset=utf-8", headers=()):
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            for k, v in headers:
                self.send_header(k, v)
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)

        def send_json(self, obj, code=200):
            self.send(code, json.dumps(obj, ensure_ascii=False).encode("utf-8"))

        def send_file(self, path, ctype, cache="no-cache"):
            try:
                body = path.read_bytes()
            except OSError:
                return self.send(404, b"not found", "text/plain")
            self.send(200, body, ctype, [("Cache-Control", cache)])

        def send_media(self, path):
            """A local media file with Range support, so players can seek."""
            try:
                size = os.path.getsize(path)
                f = open(path, "rb")
            except OSError:
                return self.send(404, b"file not reachable", "text/plain")
            with f:
                start, end, code = 0, size - 1, 200
                if m := re.fullmatch(r"bytes=(\d*)-(\d*)", (self.headers.get("Range") or "").strip()):
                    if m[1]:
                        start, end = int(m[1]), min(int(m[2]), size - 1) if m[2] else size - 1
                    elif m[2]:
                        start = max(0, size - int(m[2]))
                    if start > end or start >= size:
                        return self.send(416, b"", "text/plain", [("Content-Range", f"bytes */{size}")])
                    code = 206
                self.send_response(code)
                self.send_header("Content-Type", mimetypes.guess_type(path)[0] or "application/octet-stream")
                self.send_header("Content-Length", str(end - start + 1))
                self.send_header("Accept-Ranges", "bytes")
                if code == 206:
                    self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
                self.end_headers()
                if self.command == "HEAD":
                    return
                f.seek(start)
                left = end - start + 1
                try:
                    while left > 0:
                        chunk = f.read(min(1 << 18, left))
                        if not chunk:
                            break
                        self.wfile.write(chunk)
                        left -= len(chunk)
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass  # the player seeked or closed

        def library(self, query):
            """The library named by ?lib=, else the active one."""
            return find_library(app.cfg, query.get("lib", [None])[0])

        def do_HEAD(self):
            self.do_GET()

        def do_GET(self):
            if not self.allowed():
                return self.send(403, b"forbidden", "text/plain")
            url = urllib.parse.urlsplit(self.path)
            path = urllib.parse.unquote(url.path)
            q = urllib.parse.parse_qs(url.query)
            if path == "/":
                return self.send_file(WEB / "index.html", "text/html; charset=utf-8")
            if m := re.fullmatch(r"/static/([\w.-]+)", path):
                ctype = {"css": "text/css", "js": "text/javascript", "svg": "image/svg+xml"}.get(m[1].rsplit(".", 1)[-1], "application/octet-stream")
                return self.send_file(WEB / m[1], ctype + "; charset=utf-8")
            if m := re.fullmatch(r"/thumbs/([a-z0-9-]+)/([0-9a-f]{12}-[0-9a-f]{10}-\d+\.jpg)", path):
                lib = find_library(app.cfg, m[1])
                if not lib:
                    return self.send(404, b"unknown library", "text/plain")
                return self.send_file(lib_dir(lib) / "thumbs" / m[2], "image/jpeg", "public, max-age=31536000, immutable")
            if path == "/api/libraries":
                return self.send_json({"libraries": [app.describe(lib) for lib in app.cfg["libraries"]],
                                       "active": find_library(app.cfg)["id"]})
            if path == "/api/library":
                lib = self.library(q)
                if not lib:
                    return self.send_json({"error": "unknown library"}, 404)
                data, _ = app.store(lib).get()
                return self.send_json({**app.describe(lib), "warnings": data.get("warnings", []), "items": data["items"]})
            if path == "/api/index":
                return self.send_json({"jobs": app.jobs})
            if path == "/api/players":
                return self.send_json({"players": [{"id": p["id"], "name": p["name"]} for p in app.players],
                                       "default": app.cfg.get("default_player")})
            if path == "/api/playlist.m3u8":
                lib = self.library(q)
                if not lib:
                    return self.send(404, b"unknown library", "text/plain")
                data, by_id = app.store(lib).get()
                if "ids" in q:
                    recs = [by_id[i] for i in q["ids"][0].split(",") if i in by_id]
                else:
                    d = q.get("dir", [""])[0].strip("/")
                    recs = [r for r in data["items"] if not d or r["dir"] == d or r["dir"].startswith(d + "/")]
                hide = {e.strip(". ").lower() for e in q.get("hide", [""])[0].split(",")} - {""}  # the UI's type filter
                if hide:
                    recs = [r for r in recs if os.path.splitext(r["name"])[1][1:].lower() not in hide]
                return self.send(200, app.playlist(lib, recs, self.headers.get("Host")).encode("utf-8"), "audio/x-mpegurl; charset=utf-8")
            if m := re.fullmatch(r"/media/(?:([a-z0-9-]+)/)?([0-9a-f]{12})(?:/.*)?", path):
                # /media/<library>/<id>/<name>; the older /media/<id>/<name> is looked up in every library.
                libs = [find_library(app.cfg, m[1])] if m[1] else app.cfg["libraries"]
                lib, rec = next(((lb, app.store(lb).get()[1].get(m[2])) for lb in libs if lb and m[2] in app.store(lb).get()[1]), (None, None))
                if not rec:
                    return self.send(404, b"unknown media id", "text/plain")
                if lib["type"] == "local":
                    return self.send_media(app.local_path(lib, rec))
                try:
                    target = app.link(lib, rec)
                except Exception as e:
                    return self.send(502, str(e).encode("utf-8"), "text/plain")
                return self.send(302, b"", "text/plain", [("Location", target), ("Cache-Control", "no-store")])
            self.send(404, b"not found", "text/plain")

        def do_POST(self):
            if not self.allowed() or self.headers.get("Content-Type", "").split(";")[0] != "application/json":
                return self.send(403, b"forbidden", "text/plain")
            # Everything here starts programs or changes what is served: this machine only, even with --host.
            if self.client_address[0] not in ("127.0.0.1", "::1"):
                return self.send_json({"error": "only available on the computer running the library"}, 403)
            path = urllib.parse.urlsplit(self.path).path
            try:
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
                if path == "/api/play":
                    lib = find_library(app.cfg, body.get("lib"))
                    _, by_id = app.store(lib).get() if lib else (None, {})
                    recs = [by_id[i] for i in body.get("ids", [])[:500] if i in by_id]
                    if not recs:
                        return self.send_json({"error": "nothing to play"}, 400)
                    name = app.play(lib, body.get("player") or app.cfg.get("default_player"), recs)
                    return self.send_json({"ok": True, "player": name, "count": len(recs)})
                if path == "/api/libraries":
                    lib = add_local_library(app.cfg, body.get("path") or "", body.get("name"))
                    return self.send_json(app.describe(lib))
                if path == "/api/libraries/remove":
                    app.remove(body.get("id"))
                    return self.send_json({"ok": True, "active": find_library(app.cfg)["id"]})
                if path == "/api/libraries/active":
                    lib = find_library(app.cfg, body.get("id") or "")
                    if not lib:
                        return self.send_json({"error": "unknown library"}, 404)
                    app.cfg["active"] = lib["id"]
                    save_config(app.cfg)
                    return self.send_json({"ok": True})
                if path == "/api/index":
                    lib = find_library(app.cfg, body.get("id") or "")
                    if not lib:
                        return self.send_json({"error": "unknown library"}, 404)
                    return self.send_json(app.start_index(lib, bool(body.get("force"))))
                if path == "/api/pick-folder":
                    threading.Thread(target=focus_titled, args=(PICK_TITLE,), daemon=True).start()
                    r = subprocess.run([sys.executable, "-c", PICK_FOLDER], capture_output=True, timeout=900,
                                       creationflags=NO_WINDOW, env={**os.environ, "PYTHONIOENCODING": "utf-8"})
                    if r.returncode:
                        return self.send_json({"error": "No folder dialog available here; type the path instead."}, 500)
                    picked = r.stdout.decode("utf-8", "replace").strip()
                    return self.send_json({"path": os.path.normpath(picked) if picked else ""})
                self.send(404, b"not found", "text/plain")
            except ValueError as e:
                self.send_json({"error": str(e)}, 400)
            except Exception as e:
                self.send_json({"error": str(e)}, 500)

    return Handler


def cmd_serve(cfg, args):
    port = args.port or cfg["port"]
    app = App(cfg, port)
    loopback = args.host in ("127.0.0.1", "localhost", "::1")
    server = ThreadingHTTPServer((args.host, port), make_handler(app, loopback))
    url = f"http://127.0.0.1:{port}/"
    print(f"Media library on {url}")
    print(f"  libraries: {', '.join(lib['name'] for lib in cfg['libraries'])}")
    print(f"  players:   {', '.join(p['name'] for p in app.players)}")
    if not loopback:
        print("Warning: listening beyond this machine; anyone on the network can browse and stream.")
    if not args.no_browser:
        threading.Timer(0.6, webbrowser.open, [url]).start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


def cmd_index(cfg, args):
    lib = find_library(cfg, args.library)
    if not lib:
        sys.exit(f"Unknown library {args.library!r}. Known: {', '.join(x['id'] for x in cfg['libraries'])}")
    print(f"Library: {lib['name']} [{lib['id']}]", flush=True)
    run_index(cfg, lib, args.workers, args.limit, args.force, report=lambda **kw: print(kw["line"], flush=True))


def cmd_add(cfg, args):
    try:
        lib = add_local_library(cfg, args.path, args.name)
    except ValueError as e:
        sys.exit(str(e))
    print(f"Added “{lib['name']}” [{lib['id']}] -> {lib['path']}\nIndex it with: python medialib.py index --library {lib['id']}")


def cmd_libraries(cfg, args):
    active = find_library(cfg)["id"]
    for lib in cfg["libraries"]:
        n = len(load_library(lib)["items"])
        print(f"{'*' if lib['id'] == active else ' '} {lib['id']:24s} {lib['type']:7s} {n:6d} items  {location(lib)}  ({lib['name']})")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sv = sub.add_parser("serve", help="run the library UI")
    sv.add_argument("--port", type=int)
    sv.add_argument("--host", default="127.0.0.1")
    sv.add_argument("--no-browser", action="store_true")
    ix = sub.add_parser("index", help="bring a library's index up to date")
    ix.add_argument("--library", help="library id (default: the active one)")
    ix.add_argument("--workers", type=int, help="files indexed at once (default: chosen from the CPU count)")
    ix.add_argument("--limit", type=int, help="index at most N new/changed files (for testing)")
    ix.add_argument("--force", action="store_true", help="re-index everything")
    ad = sub.add_parser("add", help="add a local folder as a library")
    ad.add_argument("path")
    ad.add_argument("--name")
    sub.add_parser("libraries", help="list the configured libraries")
    args = ap.parse_args()
    cfg = load_config()
    {"serve": cmd_serve, "index": cmd_index, "add": cmd_add, "libraries": cmd_libraries}[args.cmd](cfg, args)


if __name__ == "__main__":
    main()
