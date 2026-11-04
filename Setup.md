# Tash

**Tash** is a hackery terminal control deck for your network. One command opens
a live dashboard for Tailscale latency, Pi hole DNS, Docker, a WebDAV NAS, a
vim style editor, themes, and the **Tashnet** secret key network.

```text
  ████████╗ █████╗ ███████╗██╗  ██╗
  ╚══██╔══╝██╔══██╗██╔════╝██║  ██║
     ██║   ███████║███████╗███████║
     ██║   ██╔══██║╚════██║██╔══██║
     ██║   ██║  ██║███████║██║  ██║
     ╚═╝   ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝
```

The entrypoint is always:

```sh
tash dashboard
```

## Features

- **Tailscale**: node state, peer list, latency sparklines, and a ping loop.
- **Pi hole**: v6 session auth with v5 fallback: query feed, blocked counts,
  gravity updates, blocklist additions, and blocking on/off.
- **Docker**: choose the Docker CLI or a direct Engine API connection over a
  Unix socket, Windows npipe, or protected TCP.
- **NAS**: an authenticated WebDAV server with no root and no proot.
- **Editor**: a built in vim style modal editor.
- **Themes**: bundled palettes, live preview, and custom JSON themes.
- **Tashnet**: a secret key network where every new device needs explicit
  approval on the main device. Main role handover and secret regeneration are
  built in.

Tash runs on Windows x64/arm64, macOS Intel/Apple Silicon, Linux x64/arm64, and
unrooted Termux arm64.

## Install

Every installer is terminal based: it installs one self contained `tash`
binary, adds it to your user `PATH`, and immediately opens first run setup.

### Windows

1. Download `Tash-Setup-<version>-windows-amd64.exe` (or `arm64`).
2. Double click it. A console opens.
3. SmartScreen may warn about an unsigned installer: choose **More info** →
   **Run anyway**.
4. Tash installs to `%LOCALAPPDATA%\Programs\Tash` and updates your user `PATH`.
5. Open a new terminal and run `tash dashboard` whenever you want the deck.

### macOS

1. Download the DMG for your Mac: `arm64` for Apple Silicon, `amd64` for Intel.
2. Open the DMG and double click **Install Tash.command**. Terminal opens.
3. The installer copies `tash` to `~/.local/bin` and adds it to `PATH`.
4. If Gatekeeper blocks the unsigned installer, right click it and choose
   **Open**, or run:

   ```sh
   xattr -d com.apple.quarantine "Install Tash.command"
   ```

5. Run `tash dashboard` from a new terminal.

The DMG must be created on macOS. The release workflow does this on real Intel
and Apple Silicon runners.

### Linux

```sh
chmod +x Tash-Setup-<version>-linux-amd64.elf
./Tash-Setup-<version>-linux-amd64.elf
```

The installer writes `~/.local/bin/tash`, updates your shell startup file, and
opens setup. For arm64 Linux use the `linux-arm64.elf`.

### Termux (Android, unrooted)

Use the `linux-arm64.elf` installer:

```sh
chmod +x Tash-Setup-<version>-linux-arm64.elf
./Tash-Setup-<version>-linux-arm64.elf
```

It installs to `$PREFIX/bin/tash` — no root and no proot. The Tailscale CLI is
not exposed to unrooted Termux, so install the Tailscale Android app instead;
Tash detects this and still runs Pi-hole, the NAS, the editor, and Tashnet.

For a NAS that stays alive with the screen off:

```sh
termux-wake-lock
tash nas serve
```

### From source

Requires Go 1.26 or newer.

```sh
git clone https://github.com/nypryt/tash.git
cd tash
CGO_ENABLED=0 go build -trimpath -o tash ./cmd/tash
./tash dashboard
```

## First run

On first launch Tash has no configuration and opens the setup wizard:

1. **Choose the device role.**
   - **Main device** — the first machine. Tash generates a
     `TASH-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX` Secret Key.
   - **Join Tashnet** — any later machine. Tash prompts exactly
     `Secret Key:` and asks for the main device address.
2. **Docker mode** — CLI, socket/API, or skip. The wizard explains the tradeoff.
3. **Theme** — every choice previews live.
4. **Pi-hole** (optional) — URL plus a v6 app password or v5 API token.
5. **NAS** — choose whether WebDAV starts with the dashboard.
6. **Finish** — on a joining device, this step is only available after the main
   device approves the request.

The main device must be running `tash dashboard` (specifically its TASHNET tab)
to see and approve join requests. Nothing is saved on a client until approval.

### Changing the main device later

On the current main device:

```sh
tash main transfer
```

This prints a one-time `TASHXL1-…` code that expires in **10 minutes**. It
contains the Tashnet secret and every approved device token, so treat it exactly
like the Secret Key. On the new device:

```sh
tash main claim -url http://<new-tailscale-ip>:8787 '<code>'
```

The new device becomes main and notifies the old main to demote itself. If the
old main is offline, Tash warns about split brain. Run the claim on a client or
fresh device, not on the existing main.

### Regenerating the secret

From the TASHNET tab press `g`, or run:

```sh
tash secret regen
```

Tash shows an **EXTREME WARNING** and requires typing `REGENERATE`. The action
immediately:

- invalidates every approved device token,
- disconnects every client,
- rejects every pending join,
- requires every device to rejoin with the new key and be re-approved.

The main device stays main.

## Tashnet security model

- The Secret Key is the root of trust. It is stored only on the main device in
  `secret.key` with mode `0600`.
- A joining device never sends the secret. It sends an HMAC proof bound to its
  device ID and the join request; the main verifies the proof.
- Even someone with the key cannot silently join: the main device must approve
  every request.
- Clients receive a random bearer token. Heartbeats detect revocation after a
  secret regeneration or device removal.
- The control API is plain HTTP. Run Tashnet over Tailscale or another trusted
  VPN, and never port-forward the control or NAS ports to the internet.
- Transfer codes contain the secret and all tokens. They are as sensitive as the
  Secret Key itself.

See [docs/tashnet.md](docs/tashnet.md) for the protocol.

## Dashboard

| Key | Action |
| --- | --- |
| `1` … `9` | Jump to a tab |
| `tab` / `shift+tab` | Cycle tabs |
| `r` | Refresh services |
| `?` | Help |
| `q` | Quit |
| `ctrl+c` | Force quit |

Tabs: **DECK**, **TAILSCALE**, **PI-HOLE**, **DOCKER**, **TASHNET**, **NAS**,
**EDITOR**, **THEMES**, **HELP**.

### Tailscale

`↑`/`↓` selects a peer, `p` pings it, and `P` pings every online peer. The ping
loop runs automatically when enabled. On unrooted Termux the Tailscale CLI is
unavailable; use the Android app and the rest of Tash still works.

### Pi-hole

Configure interactively:

```sh
tash pihole setup
```

Pi-hole v6 uses the password or an application password to create a session.
Pi-hole v5 uses the API token. Other commands:

```sh
tash pihole test
tash pihole block https://example.com/hosts.txt
tash pihole block ads.example.com
tash pihole block 'regex:^ads\.'
tash pihole gravity
tash pihole blocking off
tash pihole blocking on
```

From the PI-HOLE tab: `a` adds a block entry, `b` toggles blocking, `g` runs
gravity. Use `tash pihole setup -insecure` for a self-signed HTTPS certificate.

### Docker

Choose how Tash talks to the engine:

```sh
tash docker use cli
tash docker use socket unix:///var/run/docker.sock
tash docker use socket npipe:////./pipe/docker_engine
tash docker use socket tcp://192.168.1.20:2375
tash docker use off
tash docker status
```

- **CLI mode** uses your `docker` command and current context. Best for laptops,
  desktops, and Docker Desktop.
- **Socket/API mode** talks directly to the Engine API. Best for headless
  servers, containers, or machines without the CLI.

The Docker socket is **root-equivalent** on the host. Only expose TCP mode over
a trusted VPN or firewall, never the public internet.

### NAS (WebDAV)

The default root is `~/Tash/NAS`, the default port is `8788`, and the generated
username is `tash`. Start it from the NAS tab with `s`, or headlessly:

```sh
tash nas serve
```

The NAS tab shows the WebDAV URL and password (`p` reveals it). Mount examples:

- **Windows** — Explorer → This PC → Map network drive. Paste the DAV URL.
  Windows may need the WebClient service enabled.
- **macOS** — Finder → Go → Connect to Server (`⌘K`).
- **Linux** — GNOME Files → Other Locations → Connect to Server, or
  `mount.davfs <dav-url> /mnt/tash`.
- **Android** — add a WebDAV storage in Material Files, Solid Explorer, or RCX.

WebDAV uses Basic auth. Because Tash serves plain HTTP, use it over Tailscale or
a trusted LAN, not the public internet. On Termux, the NAS stays reachable while
Termux is alive; use `termux-wake-lock`.

### Editor

The EDITOR tab is a vim style modal editor. Standalone:

```sh
tash edit notes.md
```

Keys: `i` insert, `esc` normal, `:w` save, `:q` quit, `:wq` save and quit,
`/` search, `n`/`N` next/previous match, `dd` delete line, `yy` yank, `p` paste,
`u` undo. In the dashboard, `ctrl+o` opens a file, `ctrl+n` starts a scratch
buffer, and `ctrl+1`…`ctrl+9` switch tabs.

### Themes

```sh
tash theme list
tash theme set matrix
tash theme new my-theme --from nord
tash theme dir
```

The THEMES tab previews live and `enter` applies. `c` copies the current theme
to a custom JSON file and opens it in the editor. Themes are plain JSON with hex
colours; invalid files fall back to Matrix.

## Command reference

```text
tash dashboard [--theme NAME] [--edit FILE]
tash setup
tash join [--url URL]
tash edit [file]
tash secret show [--reveal]
tash secret regen
tash main transfer
tash main claim [-url URL] <code>
tash main info
tash nas serve
tash docker use cli|socket <host>|off
tash docker status
tash pihole setup|test|block|gravity|blocking
tash theme list|set|new|dir
tash doctor
tash version
tash help
```

## Configuration

| Platform | Default directory |
| --- | --- |
| Windows | `%AppData%\Tash` |
| macOS | `~/Library/Application Support/Tash` |
| Linux / Termux | `$XDG_CONFIG_HOME/tash` or `~/.config/tash` |

Set `TASH_HOME` to override the whole directory:

```sh
TASH_HOME=/srv/tash tash dashboard
```

Files:

- `config.json` — UI, services, Tashnet state, and credentials (mode `0600`).
- `secret.key` — the Tashnet Secret Key on the main device only.
- `themes/*.json` — custom themes.

## Build and release

```sh
# Windows PowerShell
./scripts/build.ps1 -Clean

# Linux / Termux / macOS
./scripts/build.sh --clean

# Quick cross-compile check
./scripts/build.sh --skip-installers --clean
```

Targets: `windows/amd64`, `windows/arm64`, `linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`. Installers embed the matching `tash` binary and
are built as `.exe` (Windows), `.elf` (Linux/Termux), and `.dmg` (macOS).

The DMG job requires macOS because it uses `hdiutil`; the GitHub release
workflow runs it on Intel and Apple Silicon runners. See
[docs/building.md](docs/building.md).

Run the checks:

```sh
go build ./...
go vet ./...
go test ./...
```

## Troubleshooting

Start with:

```sh
tash doctor
```

- **`tash: command not found` after installing** — open a new terminal. The
  installer updates the user PATH, but existing shells do not see it.
- **Main device says `secret.key` is missing** — restore the key file, or
  re run first time setup if you accept that clients must rejoin.
- **A client was disconnected** — the main regenerated the secret or removed the
  device. Run `tash join` on the client and approve it again.
- **Termux shows no Tailscale peers** — the unrooted CLI cannot reach the
  daemon. Use the Tailscale Android app; Tash shows this guidance.
- **Pi hole fails TLS** — run `tash pihole setup -insecure` for a self signed
  certificate, or fix the certificate chain.
- **Docker permission denied** — add your user to the `docker` group on Linux,
  or run Tash as a user that can access the socket. On Windows, make sure Docker
  Desktop is running.
- **NAS port in use** — change `nas.port` in `config.json`, or stop the other
  service.
- **macOS says the installer is damaged** — the DMG is unsigned. Right click →
  Open, or remove the quarantine attribute shown above.

## Documentation

- [Tashnet protocol](docs/tashnet.md)
- [Services: Pi hole, Docker, NAS](docs/services.md)
- [Building and releasing](docs/building.md)
- [Architecture](docs/architecture.md)

Tash does not need root. It is designed for trusted networks and Tailscale, not
for exposure to the public internet.
