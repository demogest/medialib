"""A disposable demo: a local mock S3 (moto) with synthetic videos, images and text, plus medialib pointed at it.

    python -m tests.e2e.demo_env [--port 8780] [--s3-port 5556] [--home DIR]

Nothing here touches your real config.json, cache or storage. Needs moto (requirements-dev.txt), ffmpeg and Go (or
MEDIALIB_BIN pointing at a built binary).
"""
import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(ROOT))

from tests.e2e.common import Store, binary  # noqa: E402

SOURCES = [  # (lavfi source, name)
    ("testsrc=size=640x360:rate=24", "Color bars"), ("testsrc2=size=640x360:rate=24", "Test pattern"), ("smptebars=size=640x360:rate=24", "SMPTE"),
    ("mandelbrot=size=640x360:rate=24", "Mandelbrot"), ("rgbtestsrc=size=640x360:rate=24", "RGB"), ("life=size=640x360:rate=24:mold=10:ratio=0.1:death_color=#C83232:life_color=#00ff00", "Life"),
]


def make_clip(path, source, seconds):
    subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", f"{source}", "-f", "lavfi", "-i", "sine=frequency=330", "-t", str(seconds), "-pix_fmt", "yuv420p",
                    "-g", "24", "-c:v", "libx264", "-preset", "veryfast", "-c:a", "aac", "-shortest", "-y", str(path)], check=True)


def seed(client, work):
    for b in ("media", "photos", "documents", "backups"):
        try:
            client.create_bucket(b)
        except Exception:  # noqa: BLE001 - already there
            pass
    clips = []
    for i, (src, name) in enumerate(SOURCES):
        p = work / f"{name}.mp4"
        make_clip(p, src, 8 + i * 3)
        clips.append((p, name))
    layout = {
        "shows/Space Documentary/": [0, 1, 2], "shows/Space Documentary/Extras/": [3], "shows/Cooking Basics/Season 1/": [4, 5, 0],
        "shows/Cooking Basics/Season 2/": [1, 2], "movies/": [3, 4], "": [5],
    }
    for prefix, idx in layout.items():
        for n, i in enumerate(idx):
            p, name = clips[i]
            client.put_object("media", f"{prefix}{name} {n + 1}.mp4", p.read_bytes(), "video/mp4")
    client.put_object("media", "shows/notes.txt", b"Season planning notes.\nEpisode order TBD.\n" * 20, "text/plain")
    client.put_object("media", "shows/.keep/", b"", "application/x-directory")
    # photos: generated with Pillow when available
    try:
        from PIL import Image, ImageDraw
        import io
        for n in range(1, 13):
            im = Image.new("RGB", (800, 600), (30 + n * 15 % 200, 80 + n * 37 % 150, 160 - n * 11 % 120))
            d = ImageDraw.Draw(im)
            for k in range(8):
                d.ellipse((60 * k, 40 * k, 60 * k + 220, 40 * k + 220), outline=(255, 255, 255), width=4)
            buf = io.BytesIO()
            im.save(buf, "JPEG", quality=85)
            client.put_object("photos", f"2026/trip/IMG_{n:04d}.jpg", buf.getvalue(), "image/jpeg")
    except ImportError:
        pass
    client.put_object("documents", "readme.md", b"# Demo bucket\n\nSynthetic files for trying medialib.\n", "text/markdown")
    client.put_object("documents", "reports/2026-q3.csv", b"month,views\njul,1200\naug,1900\nsep,2300\n", "text/csv")
    client.put_object("documents", "reports/archive/2025.zip", os.urandom(300_000), "application/zip")
    client.put_object("backups", "db/dump-2026-09-30.sql.gz", os.urandom(2_500_000), "application/gzip")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8780)
    ap.add_argument("--s3-port", type=int, default=5556)
    ap.add_argument("--home", default=str(pathlib.Path(tempfile.gettempdir()) / "medialib-demo"))
    args = ap.parse_args()
    home = pathlib.Path(args.home)
    shutil.rmtree(home, ignore_errors=True)
    home.mkdir(parents=True)
    moto = subprocess.Popen([sys.executable, "-m", "moto.server", "-p", str(args.s3_port)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(3)
    server = None
    try:
        endpoint = f"http://127.0.0.1:{args.s3_port}"
        client = Store(endpoint, "demo", "demo-secret")
        exe = binary()
        work = home / "clips"
        work.mkdir()
        print("Seeding the demo bucket (a minute of ffmpeg)...", flush=True)
        seed(client, work)
        cfg = {"port": args.port, "active": "shows", "default_player": "mpv",
               "connections": [{"id": "demo", "name": "Demo store", "provider": "minio", "endpoint": endpoint, "region": "us-east-1", "access_key": "demo",
                                "secret_key": "demo-secret", "addressing": "path", "verify_tls": True, "default_bucket": ""}],
               "libraries": [{"id": "shows", "name": "Demo shows", "type": "s3", "connection": "demo", "bucket": "media", "prefix": "shows/"}]}
        (home / "config.json").write_text(json.dumps(cfg, indent=2), encoding="utf-8")
        env = {**os.environ, "MEDIALIB_HOME": str(home)}
        subprocess.run([exe, "index", "--library", "shows"], cwd=ROOT, env=env, check=True)
        print(f"\nDemo ready: http://127.0.0.1:{args.port}/   (Ctrl-C to stop)", flush=True)
        server = subprocess.Popen([exe, "serve", "--no-browser", "--port", str(args.port)], cwd=ROOT, env=env)
        server.wait()
    except KeyboardInterrupt:
        pass
    finally:
        for p in (server, moto):
            if p:
                p.terminate()


if __name__ == "__main__":
    main()
