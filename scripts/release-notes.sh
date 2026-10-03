#!/bin/sh
# Writes the release notes of a version as Markdown: the tag's own message when it says more than a title, every
# change since the previous version (each commit's title and text, so a squash-merged pull request brings its list of
# changes), the downloads, and a link to the full comparison.
#
#   scripts/release-notes.sh v3.2.0 > notes.md
#
# The version need not be tagged yet (a dry run): the changes then run up to HEAD. Needs the history and the tags
# (a full clone, or actions/checkout with fetch-depth: 0).
set -eu
tag=${1:?usage: scripts/release-notes.sh VERSION}
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

cat <<EOF
## Downloads

| File | What it is |
|---|---|
| \`medialib-setup-windows-amd64.exe\` | Windows: desktop app installer (per-user; can install ffmpeg) |
| \`medialib-desktop-windows-amd64.zip\` | Windows: desktop app, portable |
| \`medialib-desktop-darwin-arm64\` | macOS (Apple silicon): desktop app |
| \`medialib-desktop-linux-amd64\` | Linux: desktop app (GTK 3 and WebKitGTK 4.1) |
| \`medialib-<os>-<arch>\` | Server and command line (\`medialib serve\`), one binary with the UI inside |
| \`SHA256SUMS\` | Checksums: \`sha256sum -c SHA256SUMS --ignore-missing\` |

Covers need \`ffmpeg\` and \`ffprobe\` on \`PATH\`.
EOF
if [ -n "$prev" ]; then
  printf '\n**Full changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
