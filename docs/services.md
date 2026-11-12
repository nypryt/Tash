# Services

Tash treats every integration as optional. Missing services show an explanatory
empty state instead of crashing the dashboard. Configuration is stored in
`config.json`; credentials are written with mode `0600`.

## Tailscale

Tash reads `tailscale status --json` and runs `tailscale ping` against selected
peers. The ping loop is enabled by default and refreshes every 10 seconds
(configurable with `tailscale.interval_seconds`).

- `tailscale.ping_enabled` — turn the automatic loop on or off.
- `tailscale.interval_seconds` — loop interval.
- `tailscale.max_peers` — maximum peers pinged per cycle.

On unrooted Termux the Tailscale CLI cannot talk to the Android daemon. Install
the Tailscale Android app; Tash detects the missing CLI and explains this in the
TAILSCALE tab. The rest of Tash still works, and the tailnet IP is shown when it
can be detected.

## Pi-hole

Tash supports Pi-hole v6 and the legacy v5 API.

### Configure

```sh
tash pihole setup
```

The prompt asks for a URL and a credential:

- **v6** — password or an application password from Settings → API. Tash creates
  a session and refreshes it as needed.
- **v5** — the API token shown in Settings → API / Web interface.

The connection is tested before saving. If the test fails you can still save the
configuration, which is useful when the Pi-hole is temporarily offline.

For self-signed HTTPS:

```sh
tash pihole setup -insecure
```

This sets `pihole.skip_tls_verify` in the config.

### Use

```sh
tash pihole test
tash pihole block https://example.com/hosts.txt
tash pihole block ads.example.com
tash pihole block 'regex:^ads\.'
tash pihole gravity
tash pihole blocking off
tash pihole blocking on
```

The PI-HOLE tab shows total queries, blocked queries, percent blocked, gravity
domains, active clients, a recent query feed, and top blocked domains. Keys:

- `a` — add a blocklist URL, exact domain, or `regex:` entry.
- `A` — add an allowlist URL, exact domain, or `regex:` entry.
- `b` — toggle blocking.
- `g` — run gravity.

A blocklist URL is added as a list and gravity is refreshed on v6. A plain
domain becomes an exact denylist entry. The v5 path uses the legacy endpoints
where available.

### Config fields

```json
"pihole": {
  "enabled": true,
  "url": "http://192.168.1.2",
  "secret": "…",
  "api_version": 0,
  "skip_tls_verify": false,
  "refresh_seconds": 5
}
```

`api_version` is `0` for auto-detect, or `5` / `6` to force a version.

## Docker

Tash has two connection modes.

### CLI mode

```sh
tash docker use cli
```

Tash runs your `docker` command using the current context. This is the easiest
option on laptops and desktops, follows Docker Desktop contexts, and supports
remote contexts configured in the CLI.

### Socket / API mode

```sh
tash docker use socket unix:///var/run/docker.sock
tash docker use socket npipe:////./pipe/docker_engine
tash docker use socket tcp://192.168.1.20:2375
```

Tash talks directly to the Docker Engine API. This works on headless servers and
inside containers, and does not require the Docker CLI.

The Docker socket is **root-equivalent** on the host: anyone who can use it can
start a privileged container and take over the machine. Protect it with Unix
permissions or an authenticated proxy. Never expose Docker TCP (`2375`) to the
public internet; use a VPN, TLS (`2376`), or a firewall.

### Use

```sh
tash docker status
tash docker use off
```

The DOCKER tab lists containers and supports:

- `↑`/`↓` — select a container.
- `s` — start.
- `x` — stop.
- `p` — pause.
- `u` — resume.
- `R` — restart.
- `D` — delete (force-remove) after typing `DELETE` to confirm.
- `l` — show recent logs.

### Config fields

```json
"docker": {
  "mode": "cli",
  "host": "npipe:////./pipe/docker_engine"
}
```

`mode` is `cli`, `socket`, or `off`.

### Running Tash in a container

See `packaging/docker/Dockerfile` and `packaging/docker/compose.yaml`. To control
the host engine, mount the Docker socket and use socket mode:

```sh
docker run -it --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v tash-data:/data \
  -p 8787:8787 -p 8788:8788 \
  tash dashboard
```

Then configure socket mode with `unix:///var/run/docker.sock`. Mounting the
socket gives the container root-equivalent control of the host; do this only
for a trusted Tash container.

## NAS (WebDAV)

Tash serves a directory over authenticated WebDAV using `golang.org/x/net/webdav`.
It does not need root, proot, or a kernel mount.

### Start

- During setup, choose auto-start.
- In the NAS tab, press `s` to start or stop.
- Headless:

  ```sh
  tash nas serve
  ```

The default root is `~/Tash/NAS`, port `8788`, user `tash`, and a generated
password. The NAS tab shows the password after pressing `p`; `tash nas serve`
prints it at startup.

The NAS tab also browses the root directly: `enter` opens a folder (or loads a
file into the editor), `⌫` goes up, `P` sets a new password, and `R` generates a
random one. Changing the password restarts a running server automatically.

### URLs

- Landing page: `http://<host>:8788/`
- WebDAV endpoint: `http://<host>:8788/dav/`

Use the DAV endpoint in your client.

### Mount

- **Windows** — Explorer → This PC → Map network drive. Paste the DAV URL.
  Enable the WebClient service if mapping fails.
- **macOS** — Finder → Go → Connect to Server (`⌘K`). Paste the DAV URL.
- **Linux** — GNOME Files → Other Locations → Connect to Server, or:

  ```sh
  sudo mount -t davfs http://<host>:8788/dav/ /mnt/tash
  ```

- **Android** — add a WebDAV storage in Material Files, Solid Explorer, or RCX.

WebDAV uses HTTP Basic authentication over plain HTTP. Run it on a trusted LAN
or over Tailscale. Do not port-forward it.

### Config fields

```json
"nas": {
  "auto_start": true,
  "root": "/home/you/Tash/NAS",
  "user": "tash",
  "password": "…",
  "port": 8788,
  "read_only": false
}
```

On Termux the default root is inside the Termux home. To share Android storage,
run `termux-setup-storage` and point `nas.root` at a directory under
`~/storage/shared`. Keep `termux-wake-lock` active while the NAS is running.

## Ports

| Service | Default port | Protocol |
| --- | --- | --- |
| Tashnet control | 8787 | HTTP |
| NAS / WebDAV | 8788 | HTTP |

Both are bound to all interfaces. Use a firewall and Tailscale. There is no TLS
termination built in; put a reverse proxy in front if you need HTTPS on a LAN.
