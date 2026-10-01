"""Command line."""
import argparse
import sys

from . import __version__
from . import connections as conns
from .config import add_local_library, add_s3_library, find_library, load_config, location
from .indexer import load_library, run_index
from .s3 import PROVIDERS, S3Client, S3Error
from .sources import _clients_of
from .util import human

DOC = """Media library and object-storage browser.

    python -m medialib serve [--port 8766] [--host 127.0.0.1] [--no-browser]
    python -m medialib index [--library ID] [--limit N] [--workers 8] [--force]
    python -m medialib add PATH [--name NAME]                     add a local folder (disk or NAS share)
    python -m medialib libraries                                  list the libraries
    python -m medialib connect --endpoint URL --access-key K ...  add an S3 / RustFS / MinIO / R2 ... connection
    python -m medialib connections [--test]                       list connections
    python -m medialib import [SOURCE]                            list or adopt credentials from rclone / AWS / env
    python -m medialib ls CONNECTION[:BUCKET[/PREFIX]] [-r]       browse a store
    python -m medialib add-s3 CONNECTION BUCKET[/PREFIX]          add a bucket folder as a library

A library is a local folder, a NAS share, or a folder of an S3-compatible bucket read straight through the S3 API.
index  Reads each MP4's own sample index and fetches only the bytes of a few real keyframes, which ffmpeg decodes
       into thumbnails. Incremental: unchanged files are skipped, removed files are dropped.
serve  Runs the UI: library, storage browser, connections. Settings live in config.json next to the package.
"""


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
    print(f"Added '{lib['name']}' [{lib['id']}] -> {lib['path']}\nIndex it with: python -m medialib index --library {lib['id']}")


def cmd_libraries(cfg, args):
    active = find_library(cfg)["id"]
    for lib in cfg["libraries"]:
        n = len(load_library(lib)["items"])
        print(f"{'*' if lib['id'] == active else ' '} {lib['id']:24s} {lib['type']:7s} {n:6d} items  {location(lib)}  ({lib['name']})")


def cmd_connect(cfg, args):
    data = {"name": args.name or "", "provider": args.provider, "endpoint": args.endpoint, "region": args.region,
            "access_key": args.access_key, "secret_key": args.secret_key or "", "secret_key_env": args.secret_env or "",
            "addressing": args.addressing, "verify_tls": not args.insecure, "default_bucket": args.bucket or ""}
    try:
        rec = conns.add(cfg, data)
    except ValueError as e:
        sys.exit(str(e))
    print(f"Added connection '{rec['name']}' [{rec['id']}] -> {rec['endpoint']}")
    cmd_connections(cfg, argparse.Namespace(test=True, only=rec["id"]))


def cmd_connections(cfg, args):
    for c in cfg["connections"]:
        if getattr(args, "only", None) not in (None, c["id"]):
            continue
        label = PROVIDERS.get(c["provider"], {}).get("label", c["provider"])
        print(f"{c['id']:20s} {label:22s} {c['endpoint']}  ({c['name']})")
        if args.test:
            r = conns.test(S3Client(conns.to_connection(c)), c.get("default_bucket") or None)
            if r["ok"]:
                count = f", {len(r['buckets'])} buckets" if r["buckets"] is not None else ""
                print(f"    OK in {r['latency_ms']} ms{count}")
            else:
                print(f"    FAILED: {r['message']}")
    if not cfg["connections"]:
        print("No connections yet. Add one with `connect`, or see what can be imported with `import`.")


def cmd_import(cfg, args):
    drafts = conns.importable(cfg)
    if not args.source:
        for d in drafts:
            print(f"{d['source']:24s} {d['endpoint'] or '(AWS)':45s} {d['label']}")
        if not drafts:
            print("Nothing to import: no S3 remotes in rclone, no ~/.aws/credentials, no AWS_* variables.")
        return
    try:
        rec = conns.adopt(cfg, args.source)
    except ValueError as e:
        sys.exit(str(e))
    print(f"Connection '{rec['name']}' [{rec['id']}] is ready ({rec['endpoint']}).")


def _split(spec):
    conn, _, rest = spec.partition(":")
    bucket, _, prefix = rest.partition("/")
    return conn, bucket, prefix


def cmd_ls(cfg, args):
    conn, bucket, prefix = _split(args.target)
    try:
        client = _clients_of(cfg).get(conn)
        if not bucket:
            for b in client.list_buckets():
                print(f"{b['created'][:10]:12s} {b['name']}")
            return
        token = None
        while True:
            page = client.list_objects(bucket, prefix, "" if args.recursive else "/", token)
            for p in page["prefixes"]:
                print(f"{'':>10s}  {'':20s} {p}")
            for o in page["objects"]:
                print(f"{human(o['size']):>10s}  {o['mtime']:20s} {o['key']}")
            token = page["next"]
            if not token:
                return
    except (ValueError, S3Error) as e:
        sys.exit(str(e))


def cmd_add_s3(cfg, args):
    bucket, _, prefix = args.target.partition("/")
    try:
        lib = add_s3_library(cfg, args.connection, bucket, prefix, args.name)
    except ValueError as e:
        sys.exit(str(e))
    print(f"Added '{lib['name']}' [{lib['id']}] -> {location(lib)}\nIndex it with: python -m medialib index --library {lib['id']}")


def main():
    from .server import cmd_serve
    ap = argparse.ArgumentParser(prog="medialib", description=DOC, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--version", action="version", version=f"medialib {__version__}")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sv = sub.add_parser("serve", help="run the web UI")
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
    cn = sub.add_parser("connect", help="add an object-storage connection")
    cn.add_argument("--name")
    cn.add_argument("--provider", default="other", choices=sorted(PROVIDERS))
    cn.add_argument("--endpoint", default="")
    cn.add_argument("--region", default="")
    cn.add_argument("--access-key", required=True)
    cn.add_argument("--secret-key", help="prefer --secret-env: this ends up in your shell history")
    cn.add_argument("--secret-env", help="name of an environment variable holding the secret key")
    cn.add_argument("--addressing", default="", choices=["", "path", "virtual", "auto"])
    cn.add_argument("--bucket", help="default bucket, for keys that cannot list buckets")
    cn.add_argument("--insecure", action="store_true", help="do not verify the TLS certificate")
    cs = sub.add_parser("connections", help="list connections")
    cs.add_argument("--test", action="store_true")
    im = sub.add_parser("import", help="list or adopt credentials found in rclone, ~/.aws and the environment")
    im.add_argument("source", nargs="?", help="e.g. rclone:myremote (omit to list)")
    ls = sub.add_parser("ls", help="list buckets or objects")
    ls.add_argument("target", help="CONNECTION, CONNECTION:BUCKET or CONNECTION:BUCKET/PREFIX")
    ls.add_argument("-r", "--recursive", action="store_true")
    a3 = sub.add_parser("add-s3", help="add a bucket folder as a library")
    a3.add_argument("connection")
    a3.add_argument("target", help="BUCKET or BUCKET/PREFIX")
    a3.add_argument("--name")
    args = ap.parse_args()
    cfg = load_config()
    {"serve": cmd_serve, "index": cmd_index, "add": cmd_add, "libraries": cmd_libraries, "connect": cmd_connect,
     "connections": cmd_connections, "import": cmd_import, "ls": cmd_ls, "add-s3": cmd_add_s3}[args.cmd](cfg, args)
