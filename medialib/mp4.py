"""MP4 sample-index parsing and keyframe extraction."""
import bisect
import concurrent.futures as cf
import json
import os
import pathlib
import struct
import sys
from array import array

from .util import FRACTIONS, SCALE, SCALE_BT709, run

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


# Keyframes of one video are fetched and decoded side by side on this pool, shared by all files in progress.
_frame_pool = cf.ThreadPoolExecutor(max_workers=max(8, (os.cpu_count() or 4) * 2), thread_name_prefix="frame")


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

