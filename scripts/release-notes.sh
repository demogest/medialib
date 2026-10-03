#!/bin/sh
# Writes the release notes of a version as Markdown: the tag's own message when it says more than a title, every
# change since the previous version (each commit's title and text, so a squash-merged pull request brings its list of
# changes), the downloads, and a link to the full comparison.
#
#   scripts/release-notes.sh v3.2.0 [dist/] > notes.md       dist/: the files being released, for the downloads list
#
# The version need not be tagged yet (a dry run): the changes then run up to HEAD. Needs the history and the tags
# (a full clone, or actions/checkout with fetch-depth: 0).
set -eu
tag=${1:?usage: scripts/release-notes.sh VERSION [DIST_DIR]}
dist=${2:-}
repo=${GITHUB_REPOSITORY:-demogest/medialib}

# The previous version: for a tag, the one before it; for a dry run, the newest at or before HEAD.
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  end=$tag
  prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$tag^" 2>/dev/null || true)
else
  end=HEAD
  prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
fi
range=$end
[ -n "$prev" ] && range="$prev..$end"

# A message written on the tag itself (git tag -a) leads; a title alone ("medialib 3.2.0") adds nothing.
if [ "$(git cat-file -t "refs/tags/$tag" 2>/dev/null || true)" = tag ]; then
  intro=$(git for-each-ref --format='%(contents:body)' "refs/tags/$tag" | sed '/-----BEGIN PGP SIGNATURE-----/,$d')
  if [ -n "$(printf '%s' "$intro" | tr -d '[:space:]')" ]; then
    printf '%s\n\n' "$intro"
  fi
fi

if [ -n "$prev" ]; then
  echo "## Changes since $prev"
else
  echo "## Changes"
fi
echo
if [ -z "$(git rev-list --no-merges --max-count=1 "$range")" ]; then
  printf 'No changes yet.\n\n'
fi
# Each commit: its title as a heading, then its text without the trailers (Co-Authored-By: and the like).
git log --no-merges --format='%x1e%s%x1f%b' "$range" | awk '
  BEGIN { RS = "\036"; FS = "\037" }
  NF == 0 || $1 == "" { next }
  {
    printf "### %s\n", $1
    n = split($2, lines, "\n")
    out = ""; blank = 0
    for (i = 1; i <= n; i++) {
      line = lines[i]
      if (line ~ /^[A-Za-z][A-Za-z-]*: / && line !~ /^- /) continue  # trailer
      if (line ~ /^[ \t]*$/) { if (out != "") blank = 1; continue }
      if (blank) { out = out "\n"; blank = 0 }
      out = out line "\n"
    }
    printf "%s\n", (out == "" ? "" : "\n" out)
  }'

# The downloads: the files in DIST_DIR (the ones being released), each described from its name. Without one (notes
# written into a release that already has its files) there is no list.
if [ -n "$dist" ] && [ -d "$dist" ]; then
  describe() {
    os_arch=$(printf '%s' "$1" | sed -nE 's/^medialib-[^-]+(-[^-]+)?-(windows|macos|linux)-(x64|arm64).*/\2 \3/p')
    set -- "$1" $os_arch
    os=${2:-} arch=${3:-}
    case $arch in x64) arch="x64 (Intel, AMD)" ;; arm64) arch=ARM64 ;; esac
    case $os in
      windows) os=Windows ;;
      macos) os=macOS; case ${3:-} in arm64) arch="Apple silicon" ;; x64) arch=Intel ;; esac ;;
      linux) os=Linux ;;
    esac
    case $1 in
      *-setup.exe) echo "1|$os $arch|Desktop app, installer: per user, Start menu, can install ffmpeg" ;;
      *-portable.zip) echo "2|$os $arch|Desktop app, portable: unzip and run medialib.exe" ;;
      *-server.tar.gz | *-server.zip) echo "4|$os $arch|Server and command line (\`medialib serve\`)" ;;
      *-linux-*.tar.gz) echo "3|$os $arch|Desktop app (needs GTK 3 and WebKitGTK 4.1)" ;;
      *.tar.gz) echo "3|$os $arch|Desktop app" ;;
      SHA256SUMS) echo "9||Checksums: \`sha256sum -c SHA256SUMS\`" ;;
      *) echo "8||" ;;
    esac
  }
  echo "## Downloads"
  echo
  echo "| File | For | What it is |"
  echo "|---|---|---|"
  for f in "$dist"/*; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    printf '%s|%s\n' "$(describe "$name")" "$name"
  done | sort -t '|' -k1,1n -k4,4 | while IFS='|' read -r _ what desc name; do
    printf '| `%s` | %s | %s |\n' "$name" "$what" "$desc"
  done
  echo
  echo 'Covers need `ffmpeg` and `ffprobe` on `PATH`.'
fi
if [ -n "$prev" ]; then
  printf '\n**Full changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
