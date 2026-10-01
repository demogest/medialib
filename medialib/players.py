"""Media players."""
import os, pathlib, shutil, sys

# Players picked up automatically when their executable exists.
KNOWN_PLAYERS = [
    ("mpv", "mpv", ["%USERPROFILE%\\scoop\\apps\\mpv\\current\\mpv.exe", "mpv"], "--force-media-title={title}"),
    ("potplayer", "PotPlayer", [r"%ProgramFiles%\DAUM\PotPlayer\PotPlayerMini64.exe"], None),
    ("vlc", "VLC", [r"%ProgramFiles%\VideoLAN\VLC\vlc.exe", r"%ProgramFiles(x86)%\VideoLAN\VLC\vlc.exe"], "--meta-title={title}"),
    ("mpc-hc", "MPC-HC", [r"%ProgramFiles%\MPC-HC\mpc-hc64.exe"], None),
    ("mpc-be", "MPC-BE", [r"%ProgramFiles%\MPC-BE x64\mpc-be64.exe", r"%ProgramFiles%\MPC-BE\mpc-be64.exe"], None),
]

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
