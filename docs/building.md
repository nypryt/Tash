# Building and releasing

Tash is pure Go with `CGO_ENABLED=0`. Every release artifact is a single static
binary or an installer that embeds one.

## Requirements

- Go 1.26 or newer (`go.mod` pins the language version).
- Git (for version metadata; builds fall back to `0.20-dev` without it).
- PowerShell 5.1+ for `scripts/build.ps1`.
- Bash for `scripts/build.sh`.
- macOS with `hdiutil` to create DMGs.
- Optional: Docker for building the container image.

## Quick builds

```sh
# Current platform
CGO_ENABLED=0 go build -trimpath -o tash ./cmd/tash

# Tests
go build ./...
go vet ./...
go test ./...
```

The only user-facing entrypoint is `tash dashboard`. The installer launches it
after writing the binary.

## Release scripts

### Windows PowerShell

```powershell
./scripts/build.ps1
./scripts/build.ps1 -Targets windows,linux/arm64 -Clean
./scripts/build.ps1 -Version 1.2.0 -SkipInstallers
```

### Linux / Termux / macOS

```sh
chmod +x scripts/build.sh
./scripts/build.sh --clean
./scripts/build.sh --targets "linux/amd64 linux/arm64" --skip-dmg
./scripts/build.sh --version 1.2.0 --skip-installers
```

Both scripts:

1. Resolve a version from `git describe --tags --always --dirty` unless one is
   supplied.
2. Build `tash` for every selected target with `-trimpath` and version ldflags.
3. Copy the target binary into `cmd/installer/payload/tash_payload`, build the
   installer for the same target, and remove the payload again.
4. On macOS, stage `Install Tash.command` plus the installer binary and create a
   compressed DMG with `hdiutil`.
5. Write `dist/checksums.txt` with SHA-256 hashes.
6. Copy friendly-named installers and portable binaries into the `TEMP/` bundle
   with its own `README.txt` and `checksums.txt`.

### Targets and outputs

| Target | Portable build | Installer |
| --- | --- | --- |
| `windows/amd64` | `Tash-Portable-<v>-windows-x64.exe` | `Tash-Setup-<v>-windows-x64.exe` |
| `windows/arm64` | `Tash-Portable-<v>-windows-arm64.exe` | `Tash-Setup-<v>-windows-arm64.exe` |
| `linux/amd64` | `Tash-Portable-<v>-linux-x64` | `Tash-Setup-<v>-linux-x64.elf` |
| `linux/arm64` | `Tash-Portable-<v>-linux-arm64` | `Tash-Setup-<v>-linux-arm64.elf` |
| `darwin/amd64` | `Tash-Portable-<v>-mac-intel` | `Tash-Setup-<v>-mac-intel` / `.dmg` on macOS |
| `darwin/arm64` | `Tash-Portable-<v>-mac-apple-silicon` | `Tash-Setup-<v>-mac-apple-silicon` / `.dmg` on macOS |

`dist/` keeps the raw build names (`tash_<v>_<os>_<arch>` and
`Tash-Setup-<v>-<os>-<arch>`), which are useful for scripts and CI. `TEMP/`
contains the names a person can scan and pick from. Both directories are
gitignored.

Linux ARM64 builds run on arm64 Linux and unrooted Termux (Android arm64).
`GOOS=linux` is used because Termux is Linux userspace on an Android kernel;
Tash avoids anything that needs root or proot.

### TEMP bundle

A full build produces:

```text
TEMP/
  Tash-Setup-<v>-windows-x64.exe
  Tash-Setup-<v>-windows-arm64.exe
  Tash-Setup-<v>-linux-x64.elf
  Tash-Setup-<v>-linux-arm64.elf
  Tash-Setup-<v>-mac-intel          (plus .dmg on macOS)
  Tash-Setup-<v>-mac-apple-silicon  (plus .dmg on macOS)
  Tash-Portable-<v>-windows-x64.exe
  Tash-Portable-<v>-windows-arm64.exe
  Tash-Portable-<v>-linux-x64
  Tash-Portable-<v>-linux-arm64
  Tash-Portable-<v>-mac-intel
  Tash-Portable-<v>-mac-apple-silicon
  README.txt
  checksums.txt
```

The portable builds are the raw `tash` binaries. They need no installer, make no
`PATH` changes, and run `tash dashboard` directly. On Windows the file keeps the
`.exe` suffix; the macOS and Linux files are executable without an extension.

Change the bundle directory with `-TempDir DIR` in PowerShell or `--temp DIR` in
bash. A `-Clean`/`--clean` build removes both `dist/` and the bundle first.

## Installer design

`cmd/installer` embeds the matching `tash` binary from
`cmd/installer/payload/tash_payload`. In a normal source checkout only a README
is present, so `go build ./...` succeeds; release scripts populate the payload
immediately before building each installer.

The installer:

- Verifies the payload's executable magic (`MZ`, ELF, or Mach-O) to catch a
  stale cross-build.
- When run interactively, prompts for the install directory (Enter accepts the
  default) and asks whether to install the Go toolchain.
- Installs to a per-user directory:
  - Windows: `%LOCALAPPDATA%\Programs\Tash`
  - macOS/Linux: `~/.local/bin`
  - Termux: `$PREFIX/bin`
- Adds the directory to the user PATH:
  - Windows: `HKCU\Environment` plus a `WM_SETTINGCHANGE` broadcast.
  - Unix: a guarded `export PATH=…` block in the relevant shell rc files
    (`fish_add_path` for fish).
  - Termux: no change; `$PREFIX/bin` is already on PATH.
- Optionally downloads the official Go distribution, verifies its SHA-256
  checksum from `https://go.dev/dl/?mode=json&include=all`, extracts it beside
  the Tash prefix (`…/go`), and adds `…/go/bin` to PATH. On Termux it points at
  `pkg install golang` instead.
- Launches the installed binary as `tash dashboard`, which opens first-run setup
  in the same terminal.
- If launched without a terminal on Linux, tries `x-terminal-emulator`,
  `gnome-terminal`, `konsole`, `xfce4-terminal`, `alacritty`, `kitty`, or `xterm`.

Windows `.exe` installers are console executables, so double-clicking opens a
console. macOS DMGs contain `Install Tash.command`, which opens Terminal. Linux
`.elf` installers are run from a terminal or relaunch themselves in one.

The installer flags are:

```text
--prefix DIR       install directory
--yes              no prompts (default directory, no Go install)
--no-path          do not modify PATH
--no-launch        install only
--dry-run          print the plan
--portable         keep tash beside the installer; no PATH changes
--with-go          also install the Go toolchain
--go-version VER   Go version to install (default 1.27.3)
```

`--portable` without `--prefix` uses the installer's own directory. It is a
convenience for turning a downloaded installer into a local portable copy; the
prebuilt `Tash-Portable-*` files are simpler when you only want the binary.

## macOS DMG

DMGs are built only on macOS:

```sh
./scripts/build.sh --targets darwin/arm64 --clean
```

The staging folder contains:

- `Install Tash.command` — executable bash launcher that opens Terminal and
  runs the installer.
- `tash-installer` — the embedded-payload installer for that architecture.
- `README.txt` — Gatekeeper and first-run notes.

`hdiutil create -volname Tash -srcfolder <stage> -ov -format UDZO` produces the
compressed DMG. Build separate DMGs for Intel and Apple Silicon; the installer
payload is architecture-specific.

The artifacts are unsigned and not notarized. Gatekeeper may require right-click
→ Open or removal of the quarantine attribute. To sign in a fork, add your
Developer ID certificate and notarization credentials to the workflow and run
`codesign`/`notarytool` after building.

## GitHub Actions

`.github/workflows/ci.yml` runs build, vet, and tests on Linux, Windows, and
macOS, plus a six-target cross-compile check.

`.github/workflows/release.yml` runs on tags matching `v*`:

- `windows-latest` builds both Windows installers and portables.
- `ubuntu-latest` builds both Linux `.elf` installers and portables.
- `macos-15-intel` builds the Intel installer, portable, and DMG.
- `macos-15` builds the Apple Silicon installer, portable, and DMG.
- The publish job merges each job's `TEMP/` bundle, rebuilds a single
  `checksums.txt`, and attaches the friendly-named installers and portables to
  the GitHub release.

To cut a release:

```sh
git tag v1.0.0
git push origin v1.0.0
```

The version in the binary comes from the tag. You can also run the workflow
manually with an existing tag.

## Makefile

POSIX systems can use:

```sh
make build
make test
make release
make install
```

`make install` copies `bin/tash` to `~/.local/bin/tash`.

## Docker image

Build from the repository root:

```sh
docker build -f packaging/docker/Dockerfile -t tash .
docker run -it --rm -v tash-data:/data -p 8787:8787 -p 8788:8788 tash dashboard
```

The image sets `TASH_HOME=/data/tash` and runs as a non-root user. Mount the
Docker socket only if you want Tash to control the host engine; that grants
root-equivalent access.

## Reproducibility notes

- `-trimpath` removes local paths.
- `CGO_ENABLED=0` avoids libc dependencies.
- Version, commit, and build date are injected with `-ldflags -X`.
- The embedded installer payload changes the installer hash; build the payload
  from the same commit as the installer.
- DMG hashes differ across runs because `hdiutil` metadata is not deterministic.
