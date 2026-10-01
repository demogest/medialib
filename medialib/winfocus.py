"""Bring a freshly started player (or dialog) to the foreground on Windows."""
import os, sys, time

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

