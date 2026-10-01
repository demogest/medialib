"""Library sources: how a library lists its media and reads bytes from it."""
import hashlib
import http.client
import json
import os
import threading
import time
import urllib.parse

from .config import local_root, location
from .util import AUDIO_EXT, SKIP_DIRS, VIDEO_EXT, _count, run

def rclone(cfg, *args, timeout=120):
    r = run([cfg["rclone"], *args], timeout=timeout)
    if r.returncode:
        raise RuntimeError(r.stderr.decode("utf-8", "replace").strip()[-400:])
    return r.stdout.decode("utf-8")  # raw UTF-8: the console codec would mangle CJK names


def presign(cfg, lib, key, expire, clients=None):
    """A URL for one object of an rclone or s3 library. expire: "2h" style, or seconds."""
    seconds = expire if isinstance(expire, int) else int(expire[:-1]) * {"h": 3600, "m": 60, "s": 1, "d": 86400}[expire[-1]]
    if lib["type"] == "s3":
        return (clients or _clients_of(cfg)).get(lib["connection"]).presign("GET", lib["bucket"], key, seconds)
    return rclone(cfg, "link", "--expire", expire if isinstance(expire, str) else f"{seconds}s", f"{lib['remote']}{lib['bucket']}/{key}", timeout=60).strip()


_client_pools = {}


def _clients_of(cfg):
    """The shared S3 clients for a config (one pool per config object, so CLI runs and the server both work)."""
    from .connections import Clients
    return _client_pools.setdefault(id(cfg), Clients(cfg))


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


class S3Source:
    """A bucket (or a folder of one) read straight through the S3 API: no rclone process, presigned URLs minted locally."""

    def __init__(self, cfg, lib):
        self.cfg, self.lib = cfg, lib
        self.client = _clients_of(cfg).get(lib["connection"])

    def list(self):
        items, prefix = [], self.lib["prefix"]
        for o in self.client.iter_objects(self.lib["bucket"], prefix):
            key = o["key"]
            if key.endswith("/"):
                continue
            name = key.rpartition("/")[2]
            kind = media_kind(name)
            if kind:
                rel = key[len(prefix):]
                items.append(make_item(key, name, rel.rpartition("/")[0], kind, o["size"], o["mtime"]))
        return items

    def reader(self, item):
        return HttpReader(self.client.presign("GET", self.lib["bucket"], item["key"], 7200))


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
    return {"local": LocalSource, "s3": S3Source}.get(lib["type"], RcloneSource)(cfg, lib)

