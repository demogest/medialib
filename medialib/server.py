"""HTTP server: the web UI, the library API, and the connection / storage API."""
import json
import mimetypes
import os
import platform
import re
import shutil
import subprocess
import sys
import threading
import time
import urllib.parse
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import __version__
from . import connections as conns
from .config import _config_lock, add_local_library, add_s3_library, find_library, local_root, location, save_config
from .indexer import load_library, run_index
from .players import detect_players
from .s3 import PROVIDERS, S3Error
from .sources import _clients_of, presign
from .storage import Storage, guess_type
from .tasks import Tasks
from .util import CACHE, NO_WINDOW, WEB, lib_dir
from .winfocus import _top_windows, bring_to_front, focus_titled

# A native folder dialog, run in a child process because Tk wants its own main thread.
PICK_TITLE = "Choose a media folder"
PICK_FOLDER = f"""
import tkinter, tkinter.filedialog as fd
root = tkinter.Tk(); root.withdraw(); root.attributes('-topmost', True)
print(fd.askdirectory(parent=root, title={PICK_TITLE!r}, mustexist=True))
"""

RUNNING = ("waiting", "listing", "indexing")  # job states that mean "not finished"
LOOPBACK = ("127.0.0.1", "::1", "::ffff:127.0.0.1")


class ApiError(Exception):
    def __init__(self, status, message, **extra):
        super().__init__(message)
        self.status, self.message, self.extra = status, message, extra


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
        self.clients = _clients_of(cfg)
        self.tasks = Tasks()
        self.storage = Storage(self.clients, self.tasks)

    # ---- libraries
    def store(self, lib):
        with self.lock:
            if lib["id"] not in self.stores:
                self.stores[lib["id"]] = Store(lib)
            return self.stores[lib["id"]]

    def describe(self, lib):
        data, _ = self.store(lib).get()
        out = {"id": lib["id"], "name": lib["name"], "type": lib["type"], "location": location(lib),
               "items": len(data["items"]), "updated": data.get("updated"),
               "reachable": os.path.isdir(local_root(lib)) if lib["type"] == "local" else True}
        if lib["type"] == "s3":
            conn = conns.find(self.cfg, lib["connection"])
            out.update(connection=lib["connection"], connection_name=conn["name"] if conn else None, bucket=lib["bucket"],
                       prefix=lib["prefix"], reachable=bool(conn))
        elif lib["type"] == "rclone":
            out["convertible"] = True
        return out

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

    def convert_to_s3(self, lib_id):
        """Switch an rclone library to reading through the S3 API directly. Its index stays valid: ids and versions
        only depend on the object key, size and modification time, which are the same either way."""
        with self.lock, _config_lock:
            lib = find_library(self.cfg, lib_id)
            if not lib or lib["type"] != "rclone":
                raise ValueError("That library is not an rclone library.")
            if self.jobs.get(lib_id, {}).get("state") in RUNNING:
                raise ValueError("That library is being indexed; wait for it to finish.")
            remote = lib["remote"].rstrip(":")
            draft = next((d for d in conns.rclone_remotes(self.cfg) if d["name"] == remote), None)
            if not draft:
                raise ValueError(f"rclone has no S3 remote named “{remote}”, so its credentials can't be reused.")
            conn = conns.adopt(self.cfg, draft["source"])
            lib.pop("remote", None)
            lib.update(type="s3", connection=conn["id"])
            save_config(self.cfg)
            self.stores.pop(lib_id, None)
        return lib

    def rename(self, lib_id, name):
        with _config_lock:
            lib = find_library(self.cfg, lib_id)
            name = (name or "").strip()
            if not lib or not name:
                raise ValueError("Enter a name.")
            lib["name"] = name
            save_config(self.cfg)

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
            url = presign(self.cfg, lib, rec["key"], "24h", self.clients)
            with self.lock:
                self.links[key] = (url, time.time() + 12 * 3600)
        return url

    def local_path(self, lib, rec):
        return os.path.join(local_root(lib), *rec["key"].split("/"))

    def media_url(self, host, lib, rec):
        return f"http://{host}/media/{lib['id']}/{rec['id']}/{urllib.parse.quote(rec['name'])}"

    def playlist(self, lib, recs, host=None):
        """M3U with stable URLs; with host=None a local library lists its file paths instead."""
        entries = [{"target": self.local_path(lib, r) if host is None and lib["type"] == "local" else self.media_url(host or f"127.0.0.1:{self.port}", lib, r),
                    "name": r["name"], "duration": r.get("duration")} for r in recs]
        return m3u(entries)

    def play(self, lib, player_id, recs):
        entries = []
        for r in recs:
            if lib["type"] == "local":
                path = self.local_path(lib, r)
                if len(recs) == 1 and not os.path.exists(path):
                    raise RuntimeError(f"File not reachable: {path}")  # the file itself: no HTTP hop, sidecar subtitles load
                entries.append({"target": path, "name": r["name"], "duration": r.get("duration")})
            else:
                entries.append({"target": self.media_url(f"127.0.0.1:{self.port}", lib, r), "name": r["name"], "duration": r.get("duration")})
        return self.launch(player_id, entries)

    def launch(self, player_id, entries):
        """Open entries [{"target": path or URL, "name", "duration"}] in a player: one directly, several as a playlist."""
        if not self.players:
            raise RuntimeError("No media player found. Add one under \"players\" in config.json.")
        player = next((p for p in self.players if p["id"] == player_id), None) or self.players[0]
        first = entries[0]["target"]
        if len(entries) == 1 and (os.path.isabs(first) or player["id"] != "system"):
            target = first
        else:
            pl = CACHE / "playlists" / "now-playing.m3u8"
            pl.parent.mkdir(parents=True, exist_ok=True)
            pl.write_text(m3u(entries), encoding="utf-8")
            target = str(pl)
        before = {h for h, _ in _top_windows()}
        if player["id"] == "system":
            os.startfile(target)
            proc = None
        else:
            cmd = [player["path"]]
            if len(entries) == 1 and player.get("title_arg"):
                cmd.append(player["title_arg"].format(title=entries[0]["name"]))
            proc = subprocess.Popen(cmd + [target], close_fds=True)
        threading.Thread(target=bring_to_front, args=(proc, player.get("path"), before), daemon=True).start()
        return player["name"]

    def system_info(self):
        def where(name):
            return shutil.which(self.cfg.get(name, name))
        try:
            import PIL
            pillow = PIL.__version__
        except ImportError:
            pillow = None
        return {"version": __version__, "python": platform.python_version(), "platform": platform.platform(),
                "ffmpeg": where("ffmpeg"), "ffprobe": where("ffprobe"), "rclone": where("rclone"), "pillow": pillow,
                "config_dir": str(CACHE.parent), "cache_dir": str(CACHE), "port": self.port,
                "players": [{"id": p["id"], "name": p["name"], "path": p.get("path")} for p in self.players]}


def m3u(entries):
    lines = ["#EXTM3U"]
    for e in entries:
        lines += [f"#EXTINF:{int(e.get('duration') or -1)},{e['name']}", e["target"]]
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------- routing

ROUTES = []  # (method, compiled pattern, function, local_only)


def route(method, pattern, local=False):
    def register(fn):
        ROUTES.append((method, re.compile(pattern), fn, local or method not in ("GET", "HEAD")))
        return fn
    return register


class Ctx:
    """What a route function gets: the app, the request handler, the matched path parts, query and body."""

    def __init__(self, app, handler, match, query):
        self.app, self.h, self.query = app, handler, query
        self.p = {k: urllib.parse.unquote(v) if v is not None else None for k, v in match.groupdict().items()}
        self._body = None

    def arg(self, name, default=None):
        return self.query.get(name, [default])[0]

    @property
    def body(self):
        if self._body is None:
            n = int(self.h.headers.get("Content-Length") or 0)
            self._body = json.loads(self.h.rfile.read(n) or b"{}") if n else {}
        return self._body

    @property
    def cfg(self):
        return self.app.cfg

    def library(self, lib_id=None):
        lib = find_library(self.cfg, lib_id)
        if not lib:
            raise ApiError(404, "unknown library")
        return lib

    def client(self):
        try:
            return self.app.clients.get(self.p["conn"])
        except ValueError as e:
            raise ApiError(404, str(e)) from None


def api_error(e):
    """Map S3 and library errors onto HTTP answers."""
    if isinstance(e, S3Error):
        status = e.status if 400 <= e.status < 500 else 502
        return status, {"error": str(e), "code": e.code, "status": e.status, "request_id": e.request_id}
    if isinstance(e, ApiError):
        return e.status, {"error": e.message, **e.extra}
    if isinstance(e, (ValueError, KeyError)):
        return 400, {"error": str(e)}
    return 500, {"error": str(e) or type(e).__name__}


def make_handler(app, loopback):
    class Handler(BaseHTTPRequestHandler):
        server_version = "medialib"
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):
            pass

        # ---- low level
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

        def send_json(self, obj, code=200, close=False):
            self.send(code, json.dumps(obj, ensure_ascii=False).encode("utf-8"),
                      headers=[("Cache-Control", "no-store")] + ([("Connection", "close")] if close else []))
            self.close_connection = self.close_connection or close

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
                    self.close_connection = True  # the player seeked or closed

        # ---- dispatch
        def dispatch(self, method):
            if not self.allowed():
                return self.send(403, b"forbidden", "text/plain")
            url = urllib.parse.urlsplit(self.path)
            query = urllib.parse.parse_qs(url.query, keep_blank_values=True)
            for m, rx, fn, local in ROUTES:
                if m != method and not (method == "HEAD" and m == "GET"):
                    continue
                match = rx.fullmatch(url.path)
                if not match:
                    continue
                if local and self.client_address[0] not in LOOPBACK:
                    return self.send_json({"error": "only available on the computer running the library"}, 403)
                if method not in ("GET", "HEAD") and self.headers.get("X-Medialib") != "1":
                    return self.send(403, b"forbidden", "text/plain")  # a custom header: web pages cannot send it cross-site
                ctx = Ctx(app, self, match, query)
                try:
                    result = fn(ctx)
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    self.close_connection = True
                    return
                except Exception as e:  # noqa: BLE001
                    status, body = api_error(e)
                    # The request body may be half read: this connection is no longer in step, so end it.
                    return self.send_json(body, status, close=method in ("PUT", "POST") and self.headers.get("Content-Length") not in (None, "0"))
                if result is not None:
                    self.send_json(result)
                return
            self.send(404, b"not found", "text/plain")

        def do_GET(self):
            self.dispatch("GET")

        do_HEAD = do_GET

        def do_POST(self):
            self.dispatch("POST")

        def do_PUT(self):
            self.dispatch("PUT")

        def do_DELETE(self):
            self.dispatch("DELETE")

        # ---- streaming objects out of a bucket
        def send_object(self, client, bucket, key, download=False):
            if self.command == "HEAD":
                info = client.head_object(bucket, key)
                return self.send(200, b"", info["content_type"] or guess_type(key), [("X-Object-Size", str(info["size"]))])
            stream = client.get_object(bucket, key, byte_range=self.headers.get("Range"), stream=True)
            with stream:
                h = stream.headers
                ctype = h.get("content-type") or ""
                if ctype in ("", "binary/octet-stream", "application/octet-stream"):
                    ctype = guess_type(key)
                self.send_response(stream.status)
                self.send_header("Content-Type", ctype)
                for name in ("content-length", "content-range", "etag", "last-modified"):
                    if h.get(name):
                        self.send_header(name.title(), h[name])
                self.send_header("Accept-Ranges", "bytes")
                if download:
                    self.send_header("Content-Disposition", "attachment; filename*=UTF-8''" + urllib.parse.quote(key.rpartition("/")[2]))
                self.end_headers()
                try:
                    while chunk := stream.read():
                        self.wfile.write(chunk)
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    self.close_connection = True  # the player seeked or closed

    return Handler


# ---------------------------------------------------------------- routes: UI

@route("GET", r"/")
def r_index(c):
    c.h.send_file(WEB / "index.html", "text/html; charset=utf-8")


@route("GET", r"/static/(?P<path>[\w./-]+)")
def r_static(c):
    path = (WEB / c.p["path"]).resolve()
    if WEB.resolve() not in path.parents or not path.is_file():
        return c.h.send(404, b"not found", "text/plain")
    ctype = {".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".json": "application/json",
             ".html": "text/html"}.get(path.suffix) or mimetypes.guess_type(path.name)[0] or "application/octet-stream"
    c.h.send_file(path, ctype + ("; charset=utf-8" if ctype.startswith(("text/", "application/json", "image/svg")) else ""))


@route("GET", r"/thumbs/(?P<lib>[a-z0-9-]+)/(?P<name>[0-9a-f]{12}-[0-9a-f]{10}-\d+\.jpg)")
def r_thumb(c):
    lib = find_library(c.cfg, c.p["lib"])
    if not lib:
        return c.h.send(404, b"unknown library", "text/plain")
    c.h.send_file(lib_dir(lib) / "thumbs" / c.p["name"], "image/jpeg", "public, max-age=31536000, immutable")


@route("GET", r"/api/system")
def r_system(c):
    return c.app.system_info()


# ---------------------------------------------------------------- routes: libraries

@route("GET", r"/api/libraries")
def r_libraries(c):
    return {"libraries": [c.app.describe(lib) for lib in c.cfg["libraries"]], "active": find_library(c.cfg)["id"]}


@route("GET", r"/api/library")
def r_library(c):
    lib = c.library(c.arg("lib"))
    data, _ = c.app.store(lib).get()
    return {**c.app.describe(lib), "warnings": data.get("warnings", []), "items": data["items"]}


@route("GET", r"/api/index")
def r_jobs(c):
    return {"jobs": c.app.jobs}


@route("GET", r"/api/players")
def r_players(c):
    return {"players": [{"id": p["id"], "name": p["name"]} for p in c.app.players], "default": c.cfg.get("default_player")}


@route("GET", r"/api/playlist\.m3u8")
def r_playlist(c):
    lib = c.library(c.arg("lib"))
    data, by_id = c.app.store(lib).get()
    if c.arg("ids"):
        recs = [by_id[i] for i in c.arg("ids").split(",") if i in by_id]
    else:
        d = (c.arg("dir") or "").strip("/")
        recs = [r for r in data["items"] if not d or r["dir"] == d or r["dir"].startswith(d + "/")]
    hide = {e.strip(". ").lower() for e in (c.arg("hide") or "").split(",")} - {""}  # the UI's type filter
    if hide:
        recs = [r for r in recs if os.path.splitext(r["name"])[1][1:].lower() not in hide]
    c.h.send(200, c.app.playlist(lib, recs, c.h.headers.get("Host")).encode("utf-8"), "audio/x-mpegurl; charset=utf-8")


@route("GET", r"/media/(?:(?P<lib>[a-z0-9-]+)/)?(?P<id>[0-9a-f]{12})(?:/.*)?")
def r_media(c):
    # /media/<library>/<id>/<name>; the older /media/<id>/<name> is looked up in every library.
    libs = [find_library(c.cfg, c.p["lib"])] if c.p.get("lib") else c.cfg["libraries"]
    lib, rec = next(((lb, c.app.store(lb).get()[1].get(c.p["id"])) for lb in libs if lb and c.p["id"] in c.app.store(lb).get()[1]), (None, None))
    if not rec:
        return c.h.send(404, b"unknown media id", "text/plain")
    if lib["type"] == "local":
        return c.h.send_media(c.app.local_path(lib, rec))
    try:
        target = c.app.link(lib, rec)
    except Exception as e:
        return c.h.send(502, str(e).encode("utf-8"), "text/plain")
    c.h.send(302, b"", "text/plain", [("Location", target), ("Cache-Control", "no-store")])


@route("POST", r"/api/play")
def r_play(c):
    lib = c.library(c.body.get("lib"))
    _, by_id = c.app.store(lib).get()
    recs = [by_id[i] for i in c.body.get("ids", [])[:500] if i in by_id]
    if not recs:
        raise ApiError(400, "nothing to play")
    name = c.app.play(lib, c.body.get("player") or c.cfg.get("default_player"), recs)
    return {"ok": True, "player": name, "count": len(recs)}


@route("POST", r"/api/libraries")
def r_add_library(c):
    b = c.body
    if b.get("type") == "s3":
        lib = add_s3_library(c.cfg, b.get("connection"), b.get("bucket"), b.get("prefix", ""), b.get("name"))
    else:
        lib = add_local_library(c.cfg, b.get("path") or "", b.get("name"))
    return c.app.describe(lib)


@route("POST", r"/api/libraries/remove")
def r_remove_library(c):
    c.app.remove(c.body.get("id"))
    return {"ok": True, "active": find_library(c.cfg)["id"]}


@route("POST", r"/api/libraries/active")
def r_active(c):
    lib = c.library(c.body.get("id") or "")
    c.cfg["active"] = lib["id"]
    save_config(c.cfg)
    return {"ok": True}


@route("POST", r"/api/libraries/rename")
def r_rename(c):
    c.app.rename(c.body.get("id"), c.body.get("name"))
    return {"ok": True}


@route("POST", r"/api/libraries/convert")
def r_convert(c):
    return c.app.describe(c.app.convert_to_s3(c.body.get("id")))


@route("POST", r"/api/index")
def r_start_index(c):
    lib = c.library(c.body.get("id") or "")
    return c.app.start_index(lib, bool(c.body.get("force")))


@route("POST", r"/api/pick-folder")
def r_pick_folder(c):
    threading.Thread(target=focus_titled, args=(PICK_TITLE,), daemon=True).start()
    r = subprocess.run([sys.executable, "-c", PICK_FOLDER], capture_output=True, timeout=900,
                       creationflags=NO_WINDOW, env={**os.environ, "PYTHONIOENCODING": "utf-8"})
    if r.returncode:
        raise ApiError(500, "No folder dialog available here; type the path instead.")
    picked = r.stdout.decode("utf-8", "replace").strip()
    return {"path": os.path.normpath(picked) if picked else ""}


# ---------------------------------------------------------------- routes: connections

@route("GET", r"/api/providers", local=True)
def r_providers(c):
    return {"providers": [{"id": k, **v} for k, v in PROVIDERS.items()]}


@route("GET", r"/api/connections", local=True)
def r_connections(c):
    return {"connections": [conns.public(x) for x in c.cfg["connections"]]}


@route("POST", r"/api/connections")
def r_add_connection(c):
    rec = conns.add(c.cfg, c.body)
    return conns.public(rec)


@route("PUT", r"/api/connections/(?P<id>[^/]+)")
def r_update_connection(c):
    rec = conns.update(c.cfg, c.p["id"], c.body)
    c.app.clients.drop(c.p["id"])
    return conns.public(rec)


@route("DELETE", r"/api/connections/(?P<id>[^/]+)")
def r_remove_connection(c):
    conns.remove(c.cfg, c.p["id"])
    c.app.clients.drop(c.p["id"])
    return {"ok": True}


@route("POST", r"/api/connections/test")
def r_test_draft(c):
    """Try a connection form before saving it. A blank secret falls back to the stored one of `id` (when editing)."""
    existing = conns.find(c.cfg, c.body.get("id") or "")
    rec = conns.clean(c.body, existing)
    from .s3 import S3Client
    client = S3Client(conns.to_connection(rec))
    try:
        return conns.test(client, c.body.get("bucket") or rec.get("default_bucket") or None)
    finally:
        client.close()


@route("POST", r"/api/connections/(?P<conn>[^/]+)/test")
def r_test_saved(c):
    conn = conns.find(c.cfg, c.p["conn"])
    if not conn:
        raise ApiError(404, "unknown connection")
    return conns.test(c.client(), conn.get("default_bucket") or None)


@route("GET", r"/api/connections/importable", local=True)
def r_importable(c):
    return {"sources": [conns.describe_draft(d) for d in conns.importable(c.cfg)]}


@route("POST", r"/api/connections/import")
def r_import(c):
    return conns.public(conns.adopt(c.cfg, c.body.get("source")))


# ---------------------------------------------------------------- routes: storage

B = r"/api/s3/(?P<conn>[^/]+)/b/(?P<bucket>[^/]+)"


@route("GET", r"/api/s3/(?P<conn>[^/]+)/buckets", local=True)
def r_buckets(c):
    conn = conns.find(c.cfg, c.p["conn"])
    try:
        buckets = c.app.storage.buckets(c.p["conn"])
    except S3Error as e:
        # A key limited to one bucket cannot list buckets: fall back to the default bucket set on the connection.
        if e.code in ("AccessDenied", "AllAccessDisabled") and conn and conn.get("default_bucket"):
            return {"buckets": [{"name": conn["default_bucket"], "created": ""}], "limited": True}
        raise
    return {"buckets": buckets, "limited": False}


@route("POST", r"/api/s3/(?P<conn>[^/]+)/buckets")
def r_create_bucket(c):
    name = (c.body.get("name") or "").strip()
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", name):
        raise ApiError(400, "Bucket names are 3 to 63 characters: lowercase letters, digits, dots and hyphens.")
    c.client().create_bucket(name)
    return {"ok": True, "name": name}


@route("DELETE", r"/api/s3/(?P<conn>[^/]+)/buckets/(?P<bucket>[^/]+)")
def r_delete_bucket(c):
    c.client().delete_bucket(c.p["bucket"])
    return {"ok": True}


@route("GET", B + r"/list", local=True)
def r_list(c):
    return c.app.storage.list(c.p["conn"], c.p["bucket"], c.arg("prefix", ""), c.arg("token"), int(c.arg("limit", 500)),
                              c.arg("delimiter", "/"))


@route("GET", B + r"/search", local=True)
def r_search(c):
    return c.app.storage.search(c.p["conn"], c.p["bucket"], c.arg("prefix", ""), c.arg("q", ""), int(c.arg("limit", 300)))


@route("GET", B + r"/head", local=True)
def r_head(c):
    return c.app.storage.head(c.p["conn"], c.p["bucket"], c.arg("key", ""))


@route("GET", B + r"/presign", local=True)
def r_presign(c):
    key, expires = c.arg("key", ""), int(c.arg("expires", 3600))
    extra = {"response-content-disposition": "attachment; filename*=UTF-8''" + urllib.parse.quote(key.rpartition("/")[2])} if c.arg("download") else None
    return {"url": c.client().presign("GET", c.p["bucket"], key, expires, response_headers=extra), "expires": expires}


@route("POST", B + r"/folder")
def r_folder(c):
    return {"prefix": c.app.storage.make_folder(c.p["conn"], c.p["bucket"], c.body.get("prefix", ""))}


@route("POST", B + r"/delete")
def r_delete(c):
    keys, prefixes = list(c.body.get("keys") or []), list(c.body.get("prefixes") or [])
    if not keys and not prefixes:
        raise ApiError(400, "Nothing selected.")
    task = c.app.storage.delete(c.p["conn"], c.p["bucket"], keys, prefixes)
    task.wait(1.5)
    return {"task": task.to_dict()}


@route("POST", B + r"/transfer")
def r_transfer(c):
    b = c.body
    items = [{"from": i["from"], "to": i["to"]} for i in b.get("items", [])]
    if not items:
        raise ApiError(400, "Nothing selected.")
    if b.get("to_conn") and not conns.find(c.cfg, b["to_conn"]):
        raise ApiError(404, "Unknown destination connection.")
    task = c.app.storage.transfer(c.p["conn"], c.p["bucket"], items, b.get("to_conn"), b.get("to_bucket"), bool(b.get("move")),
                                  b.get("skip_existing", True))
    task.wait(1.5)
    return {"task": task.to_dict()}


@route("POST", B + r"/properties")
def r_properties(c):
    b = c.body
    return c.app.storage.set_properties(c.p["conn"], c.p["bucket"], b["key"], b.get("content_type"), b.get("metadata"),
                                        b.get("cache_control"), b.get("content_disposition"))


@route("POST", B + r"/measure")
def r_measure(c):
    task = c.app.storage.measure(c.p["conn"], c.p["bucket"], c.body.get("prefix", ""))
    task.wait(1.0)
    return {"task": task.to_dict()}


@route("PUT", B + r"/object")
def r_upload(c):
    key = c.arg("key", "")
    length = int(c.h.headers.get("Content-Length") or 0)
    ctype = (c.h.headers.get("Content-Type") or "").split(";")[0]
    if ctype in ("application/octet-stream", "application/x-www-form-urlencoded", ""):
        ctype = None
    result = c.app.storage.upload(c.p["conn"], c.p["bucket"], key, length, c.h.rfile.read, ctype, c.arg("overwrite", "1") != "0")
    return result


@route("GET", B + r"/uploads", local=True)
def r_uploads(c):
    return {"uploads": c.app.storage.incomplete_uploads(c.p["conn"], c.p["bucket"], c.arg("prefix", ""))}


@route("POST", B + r"/uploads/abort")
def r_abort_uploads(c):
    return {"aborted": c.app.storage.abort_uploads(c.p["conn"], c.p["bucket"], c.body.get("items", []))}


@route("POST", B + r"/play")
def r_play_objects(c):
    client, bucket = c.client(), c.p["bucket"]
    entries = [{"target": client.presign("GET", bucket, k, 24 * 3600), "name": k.rpartition("/")[2], "duration": None}
               for k in (c.body.get("keys") or [])[:500]]
    if not entries:
        raise ApiError(400, "nothing to play")
    return {"ok": True, "player": c.app.launch(c.body.get("player") or c.cfg.get("default_player"), entries), "count": len(entries)}


@route("POST", B + r"/library")
def r_make_library(c):
    lib = add_s3_library(c.cfg, c.p["conn"], c.p["bucket"], c.body.get("prefix", ""), c.body.get("name"))
    c.app.start_index(lib)
    return c.app.describe(lib)


@route("GET", r"/s3/(?P<conn>[^/]+)/(?P<bucket>[^/]+)/(?P<key>.+)", local=True)
def r_object(c):
    c.h.send_object(c.client(), c.p["bucket"], c.p["key"], bool(c.arg("download")))


# ---------------------------------------------------------------- routes: tasks

@route("GET", r"/api/tasks", local=True)
def r_tasks(c):
    return {"tasks": c.app.tasks.list()}


@route("POST", r"/api/tasks/(?P<id>[^/]+)/cancel")
def r_cancel_task(c):
    if not c.app.tasks.cancel(c.p["id"]):
        raise ApiError(404, "unknown task")
    return {"ok": True}


@route("DELETE", r"/api/tasks(?:/(?P<id>[^/]+))?")
def r_dismiss_tasks(c):
    c.app.tasks.dismiss(c.p.get("id"))
    return {"ok": True}


# ---------------------------------------------------------------- run

def cmd_serve(cfg, args):
    port = args.port or cfg["port"]
    app = App(cfg, port)
    loopback = args.host in ("127.0.0.1", "localhost", "::1")
    server = ThreadingHTTPServer((args.host, port), make_handler(app, loopback))
    url = f"http://127.0.0.1:{port}/"
    print(f"Media library on {url}")
    print(f"  libraries:   {', '.join(lib['name'] for lib in cfg['libraries'])}")
    print(f"  connections: {', '.join(c['name'] for c in cfg['connections']) or '(none)'}")
    print(f"  players:     {', '.join(p['name'] for p in app.players)}")
    if not loopback:
        print("Warning: listening beyond this machine. Libraries and thumbnails are open to anyone on the network;\n"
              "         the storage browser and every change stay limited to this computer.")
    if not args.no_browser:
        threading.Timer(0.6, webbrowser.open, [url]).start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
