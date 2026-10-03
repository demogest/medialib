#!/bin/sh
# Turns the build outputs into the files a release offers, with names that say what each one is:
#
#   medialib-<version>-<os>-<arch>[-<kind>].<ext>      os: windows, macos, linux   arch: x64, arm64
#
#   medialib-3.2.0-windows-x64-setup.exe       desktop app, installer
#   medialib-3.2.0-windows-x64-portable.zip    desktop app, unpacked
#   medialib-3.2.0-macos-arm64.tar.gz          desktop app
#   medialib-3.2.0-linux-x64.tar.gz            desktop app
#   medialib-3.2.0-linux-x64-server.tar.gz     server and command line (and the same for every os and arch)
#
# Every archive holds one program named medialib (medialib.exe), the name the README's commands use, already
# executable: a downloaded bare binary is neither. Windows gets .zip, macOS and Linux .tar.gz.
#
#   scripts/package-release.sh v3.2.0 build/ dist/
#
# build/ holds what `make cross`, scripts/build-desktop.sh and scripts/package-windows.ps1 write; anything there this
# script does not know is an error, so a new build output cannot slip out unnamed.
set -eu
version=${1:?usage: scripts/package-release.sh VERSION BUILD_DIR OUT_DIR}
in=${2:?build dir}
out=${3:?output dir}
version=${version#v}
mkdir -p "$out"
in=$(cd "$in" && pwd) out=$(cd "$out" && pwd) # absolute: some steps below work from a scratch folder
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

os_of() { case $1 in darwin) echo macos ;; *) echo "$1" ;; esac; }
arch_of() { case $1 in amd64) echo x64 ;; *) echo "$1" ;; esac; }

# pack NAME PROGRAM FILE: an archive NAME holding FILE as PROGRAM (medialib or medialib.exe)
pack() {
  rm -rf "$stage/p" && mkdir -p "$stage/p"
  cp "$3" "$stage/p/$2"
  chmod 755 "$stage/p/$2" # artifacts lose the executable bit on the way between jobs
  case $1 in
    *.zip) (cd "$stage/p" && zip -q -X "$stage/a.zip" "$2") && mv "$stage/a.zip" "$out/$1" ;;
    *.tar.gz) tar -C "$stage/p" --owner=0 --group=0 -czf "$out/$1" "$2" ;;
  esac
  echo "$out/$1"
}

for f in "$in"/*; do
  [ -f "$f" ] || continue
  name=$(basename "$f")
  case $name in
    medialib-setup-windows-*.exe) # the installer, named by package-windows.ps1
      arch=${name#medialib-setup-windows-}; arch=$(arch_of "${arch%.exe}")
      cp "$f" "$out/medialib-$version-windows-$arch-setup.exe"
      echo "$out/medialib-$version-windows-$arch-setup.exe" ;;
    medialib-desktop-windows-*.zip) # the portable zip: repacked so the program inside is medialib.exe
      arch=${name#medialib-desktop-windows-}; arch=$(arch_of "${arch%.zip}")
      rm -rf "$stage/u" && mkdir -p "$stage/u" && (cd "$stage/u" && unzip -q "$f")
      exe=$(find "$stage/u" -type f -name '*.exe' | head -n 1)
      [ -n "$exe" ] || { echo "no .exe in $name" >&2; exit 1; }
      pack "medialib-$version-windows-$arch-portable.zip" medialib.exe "$exe" ;;
    medialib-desktop-*-*) # scripts/build-desktop.sh for macOS and Linux
      rest=${name#medialib-desktop-}; os=$(os_of "${rest%-*}"); arch=$(arch_of "${rest##*-}")
      pack "medialib-$version-$os-$arch.tar.gz" medialib "$f" ;;
    medialib-windows-*.exe) # make cross
      arch=${name#medialib-windows-}; arch=$(arch_of "${arch%.exe}")
      pack "medialib-$version-windows-$arch-server.zip" medialib.exe "$f" ;;
    medialib-*-*) # make cross: medialib-<goos>-<goarch>
      rest=${name#medialib-}; os=$(os_of "${rest%-*}"); arch=$(arch_of "${rest##*-}")
      pack "medialib-$version-$os-$arch-server.tar.gz" medialib "$f" ;;
    *)
      echo "package-release.sh: does not know what $name is" >&2
      exit 1 ;;
  esac
done
