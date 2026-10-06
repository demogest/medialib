# AGENTS.md

Instructions for coding agents working in this repository. [CONTRIBUTING.md](CONTRIBUTING.md) has the same ground with the reasons; [docs/DESIGN.md](docs/DESIGN.md) decides questions about the interface.

## The project

Media Library: a self-hosted media library and S3 storage browser. One Go binary (`cmd/medialib`) is the server, the command line and the desktop app; the UI (`web/`) is plain ES modules embedded into it, with no build step. Users point it at folders or buckets; it makes covers from real keyframes with ffmpeg and plays videos in the user's own player.

## Commands

```bash
make lint          # gofmt, node --check on every UI script, sh -n on scripts, VERSION format
make test          # go vet + go test -race ./...
make e2e           # API tests against a mock S3; needs `pip install -r requirements-dev.txt` and ffmpeg
make server        # dist/medialib
MEDIALIB_WEB=web MEDIALIB_HOME=$(mktemp -d) dist/medialib serve    # run against a scratch config, UI from disk
python -m tests.e2e.demo_env                                       # demo with a mock S3 and sample media
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12          # after editing .github/workflows/
```

Before you finish: `make lint` and `make test` pass; `make e2e` too if you touched the API. Tests that need ffmpeg skip themselves without it; say so if they skipped.

## Where things are

- `internal/server/routes.go`: every HTTP route and its access class; `http.go`: `guard`, the access rules.
- `internal/config`: `config.json`, settings, paths. `internal/media`: scanning, keyframes, index files. `internal/search`: matching and filters. `internal/s3`: the S3 client. `internal/storage`: bucket operations. `internal/players`: finding and launching players. `internal/update`: self-update.
- `web/js/lib/` (dom, api, state, router, fmt, ui), `web/js/shell/` (sidebar, palette, shortcuts), `web/js/views/` (one file per screen), `web/css/`.
- `tests/e2e/test_api.py`: end-to-end API tests. Unit tests sit beside the code.

## Rules

- **Go**: `gofmt`. Standard library first; do not add a dependency without being asked. No cgo outside the `desktop` build tag. Code for one OS goes in `*_windows.go` with an `*_other.go` or `*_unix.go` counterpart, so every target still builds (`make cross`).
- **Routes**: declare every new route in `routes.go` with the narrowest access class: `open` (anyone who can reach the server: viewing), `private` (this computer or a signed-in user: storage, connections, any change), `machine` (this computer only: anything acting on the host, such as players and dialogs). Never send a secret to the browser. Serve files from user storage through `inert()`.
- **Frontend**: plain ES modules, no frameworks, no npm packages, no build step. Build the DOM with `h()`/`fill()` from `lib/dom.js`; call the API with `get`/`post`/`del` from `lib/api.js` (it adds the `X-Medialib` header that changes require). Every file must pass `node --check`.
- **Interface text**: plain language. *Scan* and *covers*, not *index* and *keyframes*; KB/MB/GB; relative dates; sentence case. Never show `127.0.0.1`, a port or an internal id. Every empty or error state says what happened and offers the one action that fixes it. Read `docs/DESIGN.md` before adding a screen, a button or a setting.
- **Settings**: a new `config.json` key goes in `internal/config` and the README's configuration table (and `config.example.json` if it belongs there). Keep unknown keys intact.
- **Docs**: user-facing changes go in `README.md`, developer-facing ones in `CONTRIBUTING.md`.
- **Versions**: never edit `VERSION`, `cmd/medialib/winres/versioninfo.json`, `cmd/medialib/resource_windows_*.syso` or the installer's `AppVersion` by hand; the release workflow sets them with `scripts/set-version.sh`.
- **Commits and pull requests**: start the title with its kind: `fix:` for a bug fix, `feat:` for something new or improved, `docs:`, `ci:`, `build:`, `chore:`, `refactor:` or `test:` for the rest. The release notes sort changes under Fixes, Features and Other changes by it (or by the PR's **bug** or **enhancement** label) and leave the prefix out. A title starting with `fix:` or `Fix` marks a bug fix, released automatically every other day, so use it for bug fixes only. Pull requests are squash-merged: the title becomes a heading in the release notes, the description the text under it.
- Never commit `config.json`, `cache/`, `dist/` or any credential.
- A vulnerability you find goes to the maintainers privately ([SECURITY.md](SECURITY.md)), not into a public issue, a pull request's title or description, or a commit message.
