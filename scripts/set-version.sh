#!/bin/sh
# Sets the version everywhere it is written down: VERSION, the Windows version resource (winres/versioninfo.json and
# the resource_windows_*.syso made from it, what a plain `go build` puts in Properties > Details) and the installer's
# default. After a release the release workflow runs it on main, so VERSION always says what was released last.
# Needs Go (for `make winres`).
#
#   scripts/set-version.sh 3.4.0        (or v3.4.0)
set -eu
v=${1:?usage: scripts/set-version.sh X.Y.Z}
v=${v#v}
echo "$v" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || { echo "set-version: $v is not a version like 3.4.0" >&2; exit 1; }
cd "$(dirname "$0")/.."
major=${v%%.*} minor=${v#*.} patch=${v##*.}
minor=${minor%.*}

printf '%s\n' "$v" > VERSION
sed -i.bak -E \
  -e "s/\"(File|Product)Version\": \{ \"Major\": [0-9]+, \"Minor\": [0-9]+, \"Patch\": [0-9]+/\"\1Version\": { \"Major\": $major, \"Minor\": $minor, \"Patch\": $patch/" \
  -e "s/\"(File|Product)Version\": \"[0-9.]+\"/\"\1Version\": \"$v\"/" \
  cmd/medialib/winres/versioninfo.json
sed -i.bak -E "s/(#define AppVersion )\"[0-9.]+\"/\1\"$v\"/" installer/medialib.iss
rm -f cmd/medialib/winres/versioninfo.json.bak installer/medialib.iss.bak
make winres VERSION="$v"
