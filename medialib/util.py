"""Paths, constants and small helpers shared by every module."""

import hashlib, os, pathlib, re, subprocess, sys, threading


HERE = pathlib.Path(__file__).resolve().parent.parent
WEB = HERE / "web"
# config.json and cache/ sit next to the package unless MEDIALIB_HOME points somewhere else.
HOME = pathlib.Path(os.environ.get("MEDIALIB_HOME") or HERE).resolve()
CACHE = HOME / "cache"
CONFIG = HOME / "config.json"

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

def slug(text):
    s = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")[:40] or "library"
    return "lib-" + s if re.fullmatch(r"[0-9a-f]{12}", s) else s  # 12 hex chars would read as a media id


def lib_dir(lib):
    return CACHE / lib["id"]


def run(cmd, timeout=120, data=None):
    try:
        return subprocess.run(cmd, input=data, capture_output=True, timeout=timeout, creationflags=NO_WINDOW)
    except subprocess.TimeoutExpired:
        # Not the default message: it quotes the whole command line, presigned URL included.
        raise RuntimeError(f"{os.path.basename(cmd[0])} timed out after {timeout}s") from None


def _count(n):
    global _fetched
    with _fetched_lock:
        _fetched += n


def fetched_total():
    """Bytes read from media so far by the MP4 fast path (for progress lines)."""
    return _fetched

def human(n):
    for unit in ("B", "KiB", "MiB", "GiB", "TiB"):
        if n < 1024 or unit == "TiB":
            return f"{n:.1f} {unit}" if unit != "B" else f"{n} B"
        n /= 1024

