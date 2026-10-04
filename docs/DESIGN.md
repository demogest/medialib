# Design notes

What Media Library is for, who it serves, how its features are ranked, and the principles the interface follows.
Read this before adding a screen, a button or a setting.

## Who it is for

| | Who | What they come to do | How often |
|---|---|---|---|
| 1 | **The collector**: folders of videos on a disk or NAS (recordings, downloads, courses, family video, anime, films), mostly on Windows, often bilingual | Find something and watch it in their own player; see what is new; keep it tidy | Daily |
| 2 | **The self-hoster**: media in a bucket (RustFS, MinIO, S3, R2), a server on the LAN | Same as 1, over the network, from any device | Weekly |
| 3 | **The storage keeper**: manages the buckets themselves | Browse, upload, move, delete, share links | Now and then |

Person 1 is the one to win. Everything person 1 does not need is still there, but out of their way.

## Features, sorted

| Tier | Features | Where they live |
|---|---|---|
| **Every visit** | Play, search, browse folders, recently played, recently added | Home; the libraries in the sidebar; `Ctrl K` |
| **Every week** | Add a library, scan for new videos, choose the player, shuffle, playlists | Home ("Add a library"); the library's header and its "⋯" menu; view options |
| **Setup, rarely** | Players, scanning schedule, cover quality, tools, where the index lives, updates, theme | Settings |
| **Specialist** | Buckets, connections, activity | Storage (shown once a store is connected); Settings → Cloud storage; the activity chip |

Rules that follow from it: a tier-1 action is one click from where the user already is; a tier-3 setting never sits in a
toolbar; a specialist screen never appears to someone who has not set that feature up.

## What the audit found (3.2)

1. **The first run was an error.** A fresh install created a "Videos" library for `~/Videos` whether or not it existed,
   so the first screen was a yellow "Folder not reachable: <path>" over "0 items · 0 min · 0 B" and three buttons that
   did nothing. The welcome screen existed but could never show.
2. **Three layers of navigation.** Five equal sections (Library, Storage, Connections, Activity, Settings), a list of
   libraries and stores under them, then a folder tree inside the library: four places to click before a video.
3. **A big library was one endless page**: every file of every subfolder, grouped, at once.
4. **Engineer's words**: *index*, *keyframe*, *MiB*, *presigned*, *stream URL*, *127.0.0.1*, the server's port.
5. **Nothing to come back to**: no record of what was watched, nothing that says what is new.
6. **Phones**: the library toolbar wrapped over the page title; five bottom tabs, two of them for administration.
7. **Settings you could only read**: automatic indexing, players and the index folder were shown, and changing them
   meant editing `config.json`.

## Principles

- **Content first.** Covers are the interface. Chrome is quiet (neutral surfaces, one accent colour, no borders where
  spacing will do) so the colour on screen is the user's own media. The dark theme is a deep neutral for watching at night.
- **One obvious next step.** Every empty or broken state says what happened in plain words and offers the one action
  that fixes it ("Can't find this folder" → *Edit library*; "Not scanned yet" → *Scan for videos*).
- **Progressive disclosure.** Storage appears once there is a store; the activity chip only while something runs; the
  folder tree on request; file sizes and paths in Details, not on every card.
- **Plain language.** *Scan* and *covers*, not *index* and *keyframes*; *KB/MB/GB*; *Added 3 d ago*, not a timestamp;
  links that are real (a file path, a link into the bucket), never an address on `127.0.0.1`.
- **The user's tools.** Their player, their files, their folders. Nothing is moved, converted or uploaded without them.

## What changed in the redesign

- **Home** opens the app: *Recently played* and *Recently added* (one click plays), then each library as a tile of its
  newest covers, and a banner while a scan makes covers.
- **First run** is a guide on Home: the folders on this computer that hold videos (Videos, Downloads, other disks),
  each added and scanned with one click; *Choose a folder…*; *My videos are in cloud storage*; and, if ffmpeg is
  missing, the one command that installs it on this system.
- **Navigation**: Home and the libraries; Storage once a store exists; Settings. The activity chip and update notice
  appear only when there is something to say. Phones: Home, Library, Search, (Storage), Settings.
- **Library**: folders as cards with a mosaic of their newest covers; the files of the folder after them; a header of
  the folder's name with *Play all*, *Shuffle* and a menu; the old grouped list one switch away (*All videos*).
- **Settings** you can change: players (add, remove, default, look again), scanning, cover quality, tools, the index
  folder (moved for you), updates, cloud storage.
- **Look**: refreshed colour tokens, larger titles, sentence-case headings, softer radii, lift and a play button on hover.

## Next, in order of value

1. **Resume where you stopped.** Ask mpv/VLC for the position when playback ends (mpv's `--watch-later`, VLC's
   recently played), show a progress bar on covers and a *Continue watching* row.
2. **Shows and seasons.** Recognise `S01E02`, `Season 1`, `第1集`, and present a series as one card with its episodes in order
   and *Play next*.
3. **Watched marks.** A tick on what was played to the end; *Hide watched* as a filter.
4. **TV mode.** A 10-foot layout for the server on a TV browser: big focus states, arrow-key navigation, no hover.
5. **Collections and favourites.** Pin videos or folders to Home; user-made collections across libraries.
6. **Share a video.** A time-limited link with a simple player page, for a bucket library or through the server.
7. **Posters from the internet (opt-in).** Film and series artwork from TMDB for libraries of films, off by default.
