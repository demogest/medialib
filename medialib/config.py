"""config.json: libraries, players and settings."""
import json, os, shutil, sys, threading

from .util import CACHE, CONFIG, lib_dir, slug

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
    # Object-storage accounts: {"id", "name", "provider", "endpoint", "region", "access_key", "secret_key", "addressing", ...}.
    # A secret can instead be kept out of this file with "secret_key_env": "NAME_OF_ENVIRONMENT_VARIABLE".
    "connections": [],
    # type "local": path (environment variables allowed)
    # type "s3":    connection (an id from "connections") + bucket + prefix, read through the S3 API directly
    # type "rclone": remote + bucket + prefix, read through an rclone remote (older; "s3" is faster and needs no rclone)
    "libraries": [
        {"id": "videos", "name": "Videos", "type": "local", "path": "%USERPROFILE%\\Videos" if sys.platform == "win32" else "~/Videos"},
    ],
}


_config_lock = threading.RLock()



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
        lib_id = unique_id(slug(name), {lib["id"] for lib in cfg["libraries"]})
        lib = {"id": lib_id, "name": name, "type": "local", "path": path}
        cfg["libraries"].append(lib)
        save_config(cfg)
    return lib


def update_library(cfg, lib_id, data):
    """Edit a library in place. Returns (library, moved): moved is True when its location changed, which makes the
    stored index stale (the next indexing run drops what is no longer there and adds what is new)."""
    with _config_lock:
        lib = next((x for x in cfg["libraries"] if x["id"] == lib_id), None)
        if not lib:
            raise ValueError("Unknown library.")
        name = (data.get("name") or "").strip() or lib["name"]
        new = dict(lib, name=name)
        if lib["type"] == "local" and data.get("path") is not None:
            path = os.path.normpath(os.path.expandvars(data["path"].strip().strip('"')))
            if not os.path.isabs(path) or not os.path.isdir(path):
                raise ValueError(f"Not a reachable folder: {path}")
            new["path"] = path
        elif lib["type"] == "s3" and data.get("bucket") is not None:
            prefix = (data.get("prefix") or "").lstrip("/")
            conn = data.get("connection") or lib["connection"]
            if not any(c["id"] == conn for c in cfg.get("connections", [])):
                raise ValueError("Unknown connection.")
            if not data["bucket"]:
                raise ValueError("Pick a bucket.")
            new.update(connection=conn, bucket=data["bucket"], prefix=prefix + "/" if prefix and not prefix.endswith("/") else prefix)
        for other in cfg["libraries"]:
            if other is not lib and other["type"] == new["type"] and location(other) == location(new) and (other.get("connection") == new.get("connection")):
                raise ValueError(f"That location is already the library “{other['name']}”.")
        moved = location(new) != location(lib) or new.get("connection") != lib.get("connection")
        lib.update(new)
        save_config(cfg)
    return lib, moved


def location(lib):
    if lib["type"] == "local":
        return local_root(lib)
    if lib["type"] == "s3":
        return f"s3://{lib['bucket']}/{lib['prefix']}"
    return f"{lib['remote']}{lib['bucket']}/{lib['prefix']}"


def unique_id(base, taken):
    lib_id, n = base, 2
    while lib_id in taken:
        lib_id, n = f"{base}-{n}", n + 1
    return lib_id


def add_s3_library(cfg, connection, bucket, prefix="", name=None):
    """A library on a bucket (or a folder inside one) of a configured connection."""
    prefix = (prefix or "").lstrip("/")
    if prefix and not prefix.endswith("/"):
        prefix += "/"
    with _config_lock:
        if not any(c["id"] == connection for c in cfg.get("connections", [])):
            raise ValueError("Unknown connection.")
        if not bucket:
            raise ValueError("Pick a bucket.")
        for lib in cfg["libraries"]:
            if lib["type"] == "s3" and (lib["connection"], lib["bucket"], lib["prefix"]) == (connection, bucket, prefix):
                raise ValueError(f"That location is already the library “{lib['name']}”.")
        name = (name or "").strip() or f"{bucket}/{prefix.rstrip('/')}".rstrip("/")
        lib = {"id": unique_id(slug(name), {x["id"] for x in cfg["libraries"]}), "name": name, "type": "s3",
               "connection": connection, "bucket": bucket, "prefix": prefix}
        cfg["libraries"].append(lib)
        save_config(cfg)
    return lib


def local_root(lib):
    """A local library's folder, with environment variables and ~ expanded."""
    return os.path.expanduser(os.path.expandvars(lib["path"]))



def default_workers(lib):
    """Files in flight at once: S3 waits mostly on the network, a local folder on decoding and the disk."""
    cpus = os.cpu_count() or 4
    return max(4, cpus // 2) if lib["type"] == "local" else min(32, max(8, cpus * 2))
