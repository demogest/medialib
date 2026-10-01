"""Indexing: thumbnails and metadata for every media file of a library."""
import concurrent.futures as cf
import json
import os
import pathlib
import time

from .config import default_workers, lib_dir, location
from .mp4 import _frame_pool, ffprobe_meta, grab_seek, cover_score, mp4_keyframes, number_frames
from .sources import FileReader, make_source
from .util import FRACTIONS, MP4_EXT, SCALE, Mutex, fetched_total, human, run

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

    started, errors, fetched0, saved = time.time(), 0, fetched_total(), time.time()
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
                   line=f"[{done}/{len(todo)}] {human(fetched_total() - fetched0)} read, {time.time() - started:.0f}s | {it['key']} -> {flag}")
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
                              f"{human(fetched_total() - fetched0)} read by the MP4 fast path, {len(orphans)} stale thumbnails removed.")
