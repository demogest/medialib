#!/bin/sh
# Writes the release notes of a version as Markdown: the tag's own message when it says more than a title, every
# change since the previous version grouped under Fixes, Features and Other changes (each commit's title and text, so a
# squash-merged pull request brings its list of changes), the downloads, and a link to the full comparison.
#
#   scripts/release-notes.sh v3.2.0 [dist/] > notes.md       dist/: the files being released, for the downloads list
#
# The version need not be tagged yet (a dry run): the changes then run up to HEAD. Needs the history and the tags
# (a full clone, or actions/checkout with fetch-depth: 0).
set -eu
tag=${1:?usage: scripts/release-notes.sh VERSION [DIST_DIR]}
dist=${2:-}
repo=${GITHUB_REPOSITORY:-demogest/medialib}

# The previous version: for a tag, the one before it; for a dry run, the newest at or before HEAD. A full release
# counts from the full release before it, so its notes hold everything its alphas and betas brought; a pre-release
# (v3.4.0-beta.2) from whichever release came last.
previous() {
  case $tag in
    *-*) git describe --tags --abbrev=0 --match 'v[0-9]*' "$1" 2>/dev/null || true ;;
    *) git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude 'v*-*' "$1" 2>/dev/null || true ;;
  esac
}
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  end=$tag
  prev=$(previous "$tag^")
else
  end=HEAD
  prev=$(previous HEAD)
fi
range=$end
[ -n "$prev" ] && range="$prev..$end"
# The release workflow's own commits (scripts/set-version.sh after a release) are not changes.
skip='^Set the version to [0-9.]*, as released$'

# A message written on the tag itself (git tag -a) leads; a title alone ("medialib 3.2.0") adds nothing.
if [ "$(git cat-file -t "refs/tags/$tag" 2>/dev/null || true)" = tag ]; then
  intro=$(git for-each-ref --format='%(contents:body)' "refs/tags/$tag" | sed '/-----BEGIN PGP SIGNATURE-----/,$d')
  if [ -n "$(printf '%s' "$intro" | tr -d '[:space:]')" ]; then
    printf '%s\n\n' "$intro"
  fi
fi

# Each change goes under Fixes, Features or Other changes, by its title's prefix ("fix: …", "Fix …", "feat: …") or the
# labels of its pull request (bug, enhancement); the same test of a fix as the release workflow's. Labels need gh and
# GH_TOKEN; without them the title alone decides.
labels() {
  [ -n "${GH_TOKEN:-}" ] && command -v gh >/dev/null 2>&1 || return 0
  gh api "repos/$repo/commits/$1/pulls" --jq '.[].labels[].name' 2>/dev/null || true
}
kind() { # kind SHA TITLE -> fix, feat or other
  if printf '%s\n' "$2" | grep -Eq '^[Ff]ix(es|ed)?[ :(!]'; then echo fix; return; fi
  if printf '%s\n' "$2" | grep -Eiq '^feat(ure)?(\([^)]*\))?!?:'; then echo feat; return; fi
  if printf '%s\n' "$2" | grep -Eiq '^(build|chore|ci|deps|docs|perf|refactor|revert|style|tests?)(\([^)]*\))?!?:'; then echo other; return; fi
  l=$(labels "$1")
  if printf '%s\n' "$l" | grep -qx bug; then echo fix
  elif printf '%s\n' "$l" | grep -Eqx 'enhancement|feature'; then echo feat
  else echo other; fi
}
# The title without its type ("fix(ui): keep …" -> "Keep …"), as the section says it.
heading() {
  printf '%s\n' "$1" | sed -E 's/^(fix(es|ed)?|feat(ure)?|build|chore|ci|deps|docs|perf|refactor|revert|style|tests?)(\([^)]*\))?!?:[[:space:]]*//I' |
    awk '{ print toupper(substr($0, 1, 1)) substr($0, 2) }'
}
# A commit's text without the trailers (Co-Authored-By: and the like).
text() {
  git log -1 --format=%b "$1" | awk '
    /^[A-Za-z][A-Za-z-]*: / && !/^- / { next }  # trailer
    /^-{3,}[ \t]*$/ { next }  # the line GitHub puts between squashed commits
    /^[ \t]*$/ { if (out != "") blank = 1; next }
    { if (blank) { out = out "\n"; blank = 0 } out = out $0 "\n" }
    END { if (out != "") printf "\n%s", out }'
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
: > "$tmp/fix"; : > "$tmp/feat"; : > "$tmp/other"
for c in $(git rev-list --no-merges --invert-grep --grep="$skip" "$range"); do
  title=$(git log -1 --format=%s "$c")
  [ -n "$title" ] || continue
  { printf '### %s\n' "$(heading "$title")"; text "$c"; echo; } >> "$tmp/$(kind "$c" "$title")"
done
if [ ! -s "$tmp/fix" ] && [ ! -s "$tmp/feat" ] && [ ! -s "$tmp/other" ]; then
  printf 'No changes since %s yet.\n\n' "${prev:-the start}"
fi
for k in fix:Fixes feat:Features other:'Other changes'; do
  [ -s "$tmp/${k%%:*}" ] || continue
  printf '## %s\n\n' "${k#*:}"
  cat "$tmp/${k%%:*}"
done

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
