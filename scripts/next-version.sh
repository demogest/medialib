#!/bin/sh
# Prints the next version to release, from VERSION and the released versions (the v* tags). The release workflow uses
# it; run it to see what a release would be called.
#
#   scripts/next-version.sh patch|minor|major [alpha|beta]
#   scripts/next-version.sh fix
#
# patch, minor, major: the next such version after the newest of VERSION and the released versions (after 3.3.0:
#   v3.3.1, v3.4.0, v4.0.0). With alpha or beta, the next pre-release of it instead: v3.4.0-beta.1, then v3.4.0-beta.2.
# fix: what a bug fix releases: the next patch, or, while a version is in alpha or beta (a pre-release of it is out
#   and the version itself is not), its next pre-release: v3.4.0-beta.2 after v3.4.0-beta.1.
set -eu
bump=${1:?usage: scripts/next-version.sh patch|minor|major|fix [alpha|beta]}
pre=${2:-}
cd "$(dirname "$0")/.."
newest() { sed '/^$/d' | sort -V | tail -n 1; }
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | newest)" = "$1" ]; } # whether X.Y.Z $1 > $2

tags=$(git tag --list 'v*' | sed 's/^v//')
finals=$(echo "$tags" | grep -Ex '[0-9]+\.[0-9]+\.[0-9]+' || true)
pres=$(echo "$tags" | grep -Ex '[0-9]+\.[0-9]+\.[0-9]+-(alpha|beta)(\.[0-9]+)?' || true)
final=$(printf '%s\n%s\n' "$(tr -d ' \t\r\n' < VERSION)" "$finals" | newest)

target=
if [ "$bump" = fix ]; then
  bump=patch pre=
  p=$(echo "$pres" | newest)
  if [ -n "$p" ] && newer "${p%%-*}" "$final"; then
    target=${p%%-*} pre=${p#*-}
    pre=${pre%%.*}
  fi
fi
if [ -z "$target" ]; then
  major=${final%%.*} minor=${final#*.} patch=${final##*.}
  minor=${minor%.*}
  case $bump in
    major) target=$((major + 1)).0.0 ;;
    minor) target=$major.$((minor + 1)).0 ;;
    patch) target=$major.$minor.$((patch + 1)) ;;
    *) echo "next-version: $bump is not patch, minor, major or fix" >&2; exit 2 ;;
  esac
fi
case $pre in
  '') echo "v$target" ;;
  alpha | beta)
    re=$(echo "$target" | sed 's/\./\\./g')-$pre
    n=$(echo "$pres" | grep -E "^$re(\.[0-9]+)?\$" | sed -E "s/^$re\.?//; s/^\$/0/" | sort -n | tail -n 1)
    echo "v$target-$pre.$((${n:-0} + 1))" ;;
  *) echo "next-version: $pre is not alpha or beta" >&2; exit 2 ;;
esac
