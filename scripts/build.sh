#!/usr/bin/env bash
# Build Tash binaries and installers. Works on Linux, Termux, and macOS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO="${GO:-go}"
VERSION=""
OUT="dist"
TEMP="TEMP"
TARGETS=""
SKIP_INSTALLERS=0
SKIP_DMG=0
CLEAN=0

usage() {
  cat <<'EOF'
Usage: scripts/build.sh [options]

  --version VERSION      release version (default: git describe or 0.20-dev)
  --out DIR              output directory (default: dist)
  --temp DIR             friendly installer bundle directory (default: TEMP)
  --targets "LIST"       space/comma separated targets, e.g. "windows/amd64 linux/arm64"
  --skip-installers      build only the tash binaries
  --skip-dmg             do not create macOS DMGs (also automatic off macOS)
  --clean                remove the output directory first
  -h, --help             show this help

Targets: windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --out) OUT="${2:-}"; shift 2 ;;
    --temp) TEMP="${2:-}"; shift 2 ;;
    --targets) TARGETS="${2:-}"; shift 2 ;;
    --skip-installers) SKIP_INSTALLERS=1; shift ;;
    --skip-dmg) SKIP_DMG=1; shift ;;
    --clean) CLEAN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "build.sh: unknown option $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! command -v "$GO" >/dev/null 2>&1; then
  echo "build.sh: Go was not found. Install Go or set GO to the go executable." >&2
  exit 1
fi

if [ -z "$VERSION" ]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null | head -n1 || true)"
fi
if [ -z "$VERSION" ]; then
  VERSION="0.20-dev"
fi
FILE_VERSION="$(printf '%s' "$VERSION" | tr -c 'A-Za-z0-9._+-' '-')"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo local)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-s -w -X github.com/tash/tash/internal/version.Version=${VERSION} -X github.com/tash/tash/internal/version.Commit=${COMMIT} -X github.com/tash/tash/internal/version.BuildDate=${BUILD_DATE}"

friendly_os() {
  case "$1" in
    darwin) echo "mac" ;;
    *) echo "$1" ;;
  esac
}

friendly_arch() {
  case "$1/$2" in
    darwin/amd64) echo "intel" ;;
    darwin/arm64) echo "apple-silicon" ;;
    windows/amd64|linux/amd64) echo "x64" ;;
    *) echo "$2" ;;
  esac
}

installer_file_name() {
  local base="Tash-Setup-${FILE_VERSION}-$(friendly_os "$1")-$(friendly_arch "$1" "$2")"
  case "$1" in
    windows) echo "${base}.exe" ;;
    linux) echo "${base}.elf" ;;
    *) echo "$base" ;;
  esac
}

portable_file_name() {
  local base="Tash-Portable-${FILE_VERSION}-$(friendly_os "$1")-$(friendly_arch "$1" "$2")"
  if [ "$1" = "windows" ]; then
    echo "${base}.exe"
  else
    echo "$base"
  fi
}

ALL_TARGETS="windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
SELECTED=""
if [ -n "$TARGETS" ]; then
  for entry in $(printf '%s' "$TARGETS" | tr ',' ' '); do
    case "$entry" in
      */*) SELECTED="$SELECTED $entry" ;;
      windows|linux|darwin)
        for t in $ALL_TARGETS; do
          case "$t" in "$entry"/*) SELECTED="$SELECTED $t" ;; esac
        done
        ;;
      *) echo "build.sh: unknown target $entry" >&2; exit 2 ;;
    esac
  done
else
  SELECTED="$ALL_TARGETS"
fi

TARGET_LIST=""
for t in $SELECTED; do
  case " $TARGET_LIST " in *" $t "*) ;; *) TARGET_LIST="$TARGET_LIST $t" ;; esac
done
if [ -z "$TARGET_LIST" ]; then
  echo "build.sh: no matching targets" >&2
  exit 2
fi

if [ "$CLEAN" = "1" ]; then
  rm -rf "$OUT" "$TEMP"
fi
mkdir -p "$OUT" "$TEMP"

PAYLOAD="$ROOT/cmd/installer/payload/tash_payload"
cleanup() { rm -f "$PAYLOAD"; }
trap cleanup EXIT INT TERM

echo
echo "TASH RELEASE BUILD"
echo "  version: $VERSION"
echo "  commit:  $COMMIT"
echo "  go:      $GO"
echo "  out:     $OUT"
echo "  targets:$TARGET_LIST"
echo

build_target() {
  os="$1"
  arch="$2"
  package="$3"
  output="$4"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$output" "$package"
}

for target in $TARGET_LIST; do
  os="${target%%/*}"
  arch="${target##*/}"
  ext=""
  [ "$os" = "windows" ] && ext=".exe"

  tash_out="$OUT/tash_${FILE_VERSION}_${os}_${arch}${ext}"
  echo "==> tash $target"
  build_target "$os" "$arch" "./cmd/tash" "$tash_out"
  if [ "$os" = "linux" ]; then chmod +x "$tash_out"; fi
  cp "$tash_out" "$TEMP/$(portable_file_name "$os" "$arch")"

  if [ "$SKIP_INSTALLERS" = "1" ]; then continue; fi

  installer_name="Tash-Setup-${FILE_VERSION}-${os}-${arch}"
  case "$os" in
    windows) installer_name="${installer_name}.exe" ;;
    linux) installer_name="${installer_name}.elf" ;;
  esac
  installer_out="$OUT/$installer_name"

  echo "==> installer $target"
  cp "$tash_out" "$PAYLOAD"
  build_target "$os" "$arch" "./cmd/installer" "$installer_out"
  rm -f "$PAYLOAD"
  if [ "$os" = "linux" ] || [ "$os" = "darwin" ]; then chmod +x "$installer_out"; fi
  cp "$installer_out" "$TEMP/$(installer_file_name "$os" "$arch")"
done

host_os="$(uname -s 2>/dev/null || echo unknown)"
if [ "$SKIP_INSTALLERS" = "0" ] && [ "$SKIP_DMG" = "0" ]; then
  darwin_targets=""
  for t in $TARGET_LIST; do
    case "$t" in darwin/*) darwin_targets="$darwin_targets $t" ;; esac
  done
  if [ -n "$darwin_targets" ] && [ "$host_os" != "Darwin" ]; then
    echo "==> macOS DMG skipped: hdiutil is only available on macOS"
  fi
  if [ "$host_os" = "Darwin" ]; then
    for target in $darwin_targets; do
      arch="${target##*/}"
      installer="$OUT/Tash-Setup-${FILE_VERSION}-darwin-${arch}"
      [ -f "$installer" ] || continue
      stage="$OUT/.dmg-${arch}"
      rm -rf "$stage"
      mkdir -p "$stage"
      cp "$installer" "$stage/tash-installer"
      chmod +x "$stage/tash-installer"

      cat > "$stage/Install Tash.command" <<'LAUNCHER'
#!/bin/bash
# Double-click this file to open Terminal and install Tash.
cd "$(dirname "$0")" || exit 1
chmod +x ./tash-installer
exec ./tash-installer "$@"
LAUNCHER
      chmod +x "$stage/Install Tash.command"

      cat > "$stage/README.txt" <<'README'
TASH FOR MACOS
==============

1. Double-click "Install Tash.command".
   Terminal opens automatically. The installer copies tash to ~/.local/bin,
   adds it to PATH, and starts first-run setup.

2. Choose MAIN DEVICE for the first machine. Choose JOIN TASHNET for every
   other machine and paste the Tashnet Secret Key. The main device must
   approve the request in its TASHNET tab.

If macOS blocks the unsigned installer, right-click it and choose Open, or run:

  xattr -d com.apple.quarantine "Install Tash.command"

Then run `tash dashboard` from any new terminal.

The DMG is architecture-specific. Use the arm64 DMG on Apple Silicon and the
amd64 DMG on Intel Macs. The Tash binary is self-contained; no root is needed.
README

      dmg="$OUT/Tash-Setup-${FILE_VERSION}-macos-${arch}.dmg"
      echo "==> DMG darwin/$arch"
      hdiutil create -volname "Tash" -srcfolder "$stage" -ov -format UDZO "$dmg" >/dev/null
      cp "$dmg" "$TEMP/Tash-Setup-${FILE_VERSION}-mac-$(friendly_arch darwin "$arch").dmg"
      rm -rf "$stage"
    done
  fi
fi

checksum_file="$OUT/checksums.txt"
: > "$checksum_file"
if command -v sha256sum >/dev/null 2>&1; then
  for f in "$OUT"/*; do
    if [ ! -f "$f" ]; then continue; fi
    if [ "$(basename "$f")" = "checksums.txt" ]; then continue; fi
    (cd "$OUT" && sha256sum "$(basename "$f")") >> "$checksum_file"
  done
elif command -v shasum >/dev/null 2>&1; then
  for f in "$OUT"/*; do
    if [ ! -f "$f" ]; then continue; fi
    if [ "$(basename "$f")" = "checksums.txt" ]; then continue; fi
    (cd "$OUT" && shasum -a 256 "$(basename "$f")") >> "$checksum_file"
  done
fi

cat > "$TEMP/README.txt" <<'README'
TASH ARTIFACTS
==============

Installers (run from a terminal; they prompt for the install directory and
offer to install the Go toolchain):

  Windows x64          Tash-Setup-<version>-windows-x64.exe
  Windows arm64        Tash-Setup-<version>-windows-arm64.exe
  Linux x64            Tash-Setup-<version>-linux-x64.elf
  Linux arm64          Tash-Setup-<version>-linux-arm64.elf
  macOS Intel          Tash-Setup-<version>-mac-intel
  macOS Apple Silicon  Tash-Setup-<version>-mac-apple-silicon

Portable builds (one self-contained executable; no installer, no PATH changes):

  Tash-Portable-<version>-windows-x64.exe
  Tash-Portable-<version>-windows-arm64.exe
  Tash-Portable-<version>-linux-x64
  Tash-Portable-<version>-linux-arm64
  Tash-Portable-<version>-mac-intel
  Tash-Portable-<version>-mac-apple-silicon

Notes
-----
- The macOS DMGs are created on a macOS host with hdiutil. If they are missing
  here, run the build on macOS or use CI.
- The macOS installer binaries are unsigned; right-click -> Open if Gatekeeper
  blocks them.
- Portable binaries can be run directly: `tash dashboard`.
- Installers support --prefix DIR, --portable, --with-go, --no-path,
  --no-launch, --yes, and --dry-run.
README

temp_checksum_file="$TEMP/checksums.txt"
: > "$temp_checksum_file"
if command -v sha256sum >/dev/null 2>&1; then
  for f in "$TEMP"/*; do
    if [ ! -f "$f" ]; then continue; fi
    if [ "$(basename "$f")" = "checksums.txt" ]; then continue; fi
    (cd "$TEMP" && sha256sum "$(basename "$f")") >> "$temp_checksum_file"
  done
elif command -v shasum >/dev/null 2>&1; then
  for f in "$TEMP"/*; do
    if [ ! -f "$f" ]; then continue; fi
    if [ "$(basename "$f")" = "checksums.txt" ]; then continue; fi
    (cd "$TEMP" && shasum -a 256 "$(basename "$f")") >> "$temp_checksum_file"
  done
fi

echo
echo "Build complete."
for f in "$OUT"/*; do
  if [ ! -f "$f" ]; then continue; fi
  if [ "$(basename "$f")" = "checksums.txt" ]; then continue; fi
  size="$(wc -c < "$f" | tr -d ' ')"
  printf '  %-52s %10s bytes\n' "$(basename "$f")" "$size"
done
echo "  checksums: $checksum_file"
echo "  TEMP:      $TEMP"
echo
