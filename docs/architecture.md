# Architecture

Tash is a Bubble Tea terminal application wrapped in a small CLI. Services are
plain Go packages; the UI never shells out for anything except the Tailscale and
Docker CLIs when those modes are selected.

## Package map

| Package | Responsibility |
| --- | --- |
| `cmd/tash` | CLI entrypoint, flags, interactive prompts, headless service commands, `doctor`. |
| `cmd/installer` | Self-contained installer with an embedded `tash` payload. |
| `internal/app` | Bubble Tea model: splash, setup wizard, dashboard tabs, modals, polling, service lifecycle. |
| `internal/config` | On-disk config, atomic writes, `TASH_HOME`, secret file, platform paths, Termux detection. |
| `internal/theme` | Built-in palettes, custom JSON themes, validation, registry. |
| `internal/ui` | Theme-bound Lip Gloss styles, panels, headers, footers, bars, sparklines, text fitting. |
| `internal/tailscale` | `tailscale status --json` parsing, peer normalization, ping loop. |
| `internal/tashnet` | Secret generation/normalization, HMAC proofs, control server, client, discovery, transfer codes. |
| `internal/pihole` | Pi-hole v6 session client with v5 fallback, snapshots, block/gravity/blocking actions. |
| `internal/docker` | Docker CLI mode and Engine API mode (Unix socket, npipe, TCP), snapshots and actions. |
| `internal/nas` | Authenticated WebDAV server, root management, landing page, request counter. |
| `internal/claude` | Claude Code CLI detection, project discovery, one-shot prompts, in-tab chat sessions. |
| `internal/editor` | Vim-style modal editor used both in a tab and standalone. |
| `internal/sysinfo` | Hostname, user, OS name, local/Tailscale IPs, runtime version. |
| `internal/version` | Version, commit, and build date injected with ldflags. |

## Startup

`cmd/tash` dispatches on the first argument. `dashboard` and `setup` both call
`app.New`:

1. Collect system information.
2. Load themes from the config directory.
3. Open `config.json` (or enter setup when it does not exist).
4. Build the UI styles for the saved theme.
5. Start enabled services: Tashnet control server (main only), NAS (if
   auto-start), Pi-hole and Docker clients.
6. Show the splash, then the dashboard.

First run has no config, so `app.New` enters the setup wizard instead. The wizard
is a small state machine (`setupState.stage`). It creates the config only at the
end, via `config.Create` (main) or after approval (client). Nothing is written
for a client until the main device approves it.

## Update loop

Bubble Tea messages drive everything:

- `clockMsg` once per second for the header clock and splash timer.
- `refreshTickMsg` every five seconds, which fans out to Tailscale, Pi-hole,
  Docker, and client heartbeat commands.
- `pingTickMsg` on the Tailscale interval.
- Service-specific messages (`tailscaleMsg`, `piholeMsg`, `dockerMsg`,
  `pingMsg`, `heartbeatMsg`).
- Modal actions run as `tea.Cmd` functions with their own contexts and timeouts.

Blocking I/O always runs in a command, never in `Update`. Service clients have
their own timeouts so a dead Pi-hole cannot freeze the UI.

The dashboard has nine tabs. Each tab is a pure view over the current model;
key handling is split per tab (`keyTailscale`, `keyPiHole`, and so on). Modals
share a small `modal` struct with input, confirmation, info, and danger modes.

## Configuration store

`config.Store` holds a mutex-protected `Config` and the secret bytes. Writes go
through `Store.Update`, which clones the config, applies a function, normalizes
it, and writes JSON atomically (temp file + rename) with mode `0600`. The secret
is written separately as `secret.key`, also `0600`.

`config.Dir` chooses:

- `TASH_HOME` when set,
- `%AppData%\Tash` on Windows,
- `~/Library/Application Support/Tash` on macOS,
- `$XDG_CONFIG_HOME/tash` or `~/.config/tash` on Linux/Termux.

`config.Default` generates a device ID and a random NAS password. Normalization
clamps ports and fills missing fields, so old or hand-edited configs keep
working.

The main device's config contains approved devices and pending requests. Clients
contain only the main URL, main identity, their token, and the secret version.

## Tashnet server

`tashnet.Server` is started by the dashboard when the role is `main`. It binds
the configured control port and serves:

- `/healthz`
- `/v1/hello`
- `/v1/join`
- `/v1/poll`
- `/v1/heartbeat`
- `/v1/network`
- `/v1/whoami`
- `/v1/transfer/complete`

Join requests require an HMAC proof bound to the device ID and timestamp, are
rate-limited per IP, and become pending entries. A human approves or denies them
from the TASHNET tab. The poll token is stored as a SHA-256 hash.

The server reads the live store on every request, so secret regeneration and
approval take effect without restarting. It also keeps a small in-memory audit
feed for the TASHNET tab.

`tashnet.ExportTransfer` serializes the secret, devices, and tokens into a
gzip/base64url code with a 10-minute nonce. `tashnet.NotifyTransferComplete`
authenticates the new main to the old main, which then rewrites itself as a
client. Failure to notify is reported as a split-brain risk.

## Service clients

### Tailscale

`tailscale.ReadStatus` locates the CLI per platform, runs `tailscale status
--json`, and normalizes the result into `Status`/`Peer`. `tailscale.Ping` runs
`tailscale ping` with a timeout. The app keeps a ring of latency samples per peer
for sparklines.

### Pi-hole

`pihole.Client` tries the v6 API first unless forced to v5. It stores the session
ID and expiry under a mutex, logs in with the password/app password, and falls
back to the v5 token flow when needed. `Fetch` builds a snapshot of summary,
queries, top blocked domains, and blocking state. Actions (`AddBlock`,
`RunGravity`, `SetBlocking`) use version-specific endpoints.

### Docker

`docker.New` returns a `Client` interface with two implementations:

- CLI: runs `docker` commands and parses output.
- API: uses the Engine HTTP API over a Unix socket, Windows npipe, or TCP. The
  npipe transport uses `github.com/Microsoft/go-winio`.

`Fetch` returns engine metadata and containers. `Action` starts/stops/restarts a
container. `Logs` returns recent log text.

### NAS

`nas.Server` wraps `golang.org/x/net/webdav` with Basic auth. It creates the root
directory, binds the configured port, serves a small landing page at `/`, and
WebDAV at `/dav/`. `Stop` uses a context with a timeout. The app owns the server
instance and starts/stops it from the NAS tab.

## UI and themes

`internal/ui` builds all styles from a `theme.Theme`. Colours are hex strings
validated by the theme package. `Styles.Panel` is the main layout primitive:
fixed width/height, title, border, and content clipping. `Fit`, `Wrap`,
`HumanBytes`, and `HumanAgo` handle terminal cell math.

The app renders a fixed-height frame: header, tab bar, body, status line, and
footer. Every screen is padded to the terminal size so resizes do not leave
artifacts. Narrow terminals get a compact overview.

Themes are stored as JSON under `<config>/themes`. The registry merges built-ins
with custom files and falls back to Matrix on invalid colours. Live preview
applies a theme to the model without saving; leaving the Settings theme picker
restores the saved theme.

Tab order, visibility, layout, and `1`…`9` number keys are config-driven.
`internal/app` owns `tabDefs` with stable IDs (`deck`, `tailscale`, `pihole`,
`docker`, `tashnet`, `nas`, `editor`, `claude`, `settings`).
`config.UIConfig` stores `tab_order`, `tab_numbers`, `hidden_tabs`,
`deck_layout` (`auto`, `cards`, `compact`), and global `keybinds`; `Normalize`
guarantees every tab appears exactly once in the order, each visible tab has a
unique key, and Settings can never be hidden.

## Editor

`internal/editor` implements a small vim-like state machine: normal, insert,
command, and search modes; motions; line operations; undo; yank/paste; search;
and file save. It can be embedded in the dashboard or run standalone through
`tash edit`. It is intentionally not a full vim replacement.

## Installer

The installer is a separate Go program. Release scripts copy the freshly built
`tash` binary to `cmd/installer/payload/tash_payload`, compile the installer for
the same target, and delete the payload. The installer validates the executable
magic, prompts for an install directory, optionally installs the Go toolchain
(with a SHA-256 check against go.dev), updates PATH, and runs `tash dashboard`.
`--portable` skips PATH and keeps the binary beside the installer.

This keeps the distributed installer a single file while allowing the normal
source tree to build without checked-in binaries. Release scripts also emit
standalone portable binaries and a friendly `TEMP/` bundle.
