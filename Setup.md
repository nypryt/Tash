# Tash Complete Guide 🚀

**Tash** is a terminal control deck for managing your infrastructure: Tailscale, Pi-hole, Docker, NAS, AI, and a secure mesh network (Tashnet) — all from one TUI.

---

## 🎯 What Is Tash?

Tash runs in your terminal and gives you a dashboard with tabs for:

| Tab | Purpose |
|-----|---------|
| **1 DECK** | Overview of all services at a glance |
| **2 TAILSCALE** | Tailnet peers, latency, ping, SSH |
| **3 PI-HOLE** | DNS firewall stats, queries, gravity, blocking |
| **4 DOCKER** | Containers: start/stop/logs/exec/restart |
| **5 TASHNET** | Secure mesh: approve devices, transfer main role |
| **6 NAS** | WebDAV file server (password, mount help) |
| **7 EDITOR** | Vim-style editor (`:w`, `:q`, search, split) |
| **8 AI** | In-app chat (Claude Code, OpenAI-compatible) |
| **9 SETTINGS** | Themes, keys, tabs, AI, NAS, Tailscale, reset |

---

## 📁 Release Artifacts

Each version folder (`V1.0.1/`, `V1.0.2/`, `V1.1/`, `V1.1.1/`) contains:

```
V1.1.1/
├── checksums.txt              # SHA256 checksums
├── tashinstaller_win64.exe    # Windows x64 installer
├── tashinstaller_winarm.exe   # Windows ARM64 installer
├── tashinstaller_linux64.elf  # Linux x64 installer
├── tashinstaller_linuxarm.elf # Linux ARM64 installer
├── tashinstaller_appleintel.dmg   # macOS Intel installer
├── tashinstaller_applesilicon.dmg # macOS Apple Silicon installer
├── tash_portable_win64.exe         # Windows x64 portable
├── tash_portable_winarm.exe        # Windows ARM64 portable
├── tash_portable_linux64.elf       # Linux x64 portable
├── tash_portable_linuxarm.elf      # Linux ARM64 portable
├── tash_portable_appleintel.dmg    # macOS Intel portable
└── tash_portable_applesilicon.dmg  # macOS Apple Silicon portable
```

**Choose:**
- **Installer** — First-time setup, PATH integration, auto-start services
- **Portable** — USB, no admin, temp use, multiple versions side-by-side

---

## 💻 Installation

### Windows 🪟
```powershell
# Installer (run as Admin for PATH)
.\tashinstaller_win64.exe

# Options
.\tashinstaller_win64.exe --prefix "C:\Tash" --portable --with-go --no-path --yes

# Portable
.\tash_portable_win64.exe dashboard
```

### Linux 🐧
```bash
chmod +x tashinstaller_linux64.elf
./tashinstaller_linux64.elf              # Install (sudo for system-wide)
./tashinstaller_linux64.elf --prefix /opt/tash --portable --yes

# Portable
chmod +x tash_portable_linux64.elf
./tash_portable_linux64.elf dashboard
```

### macOS 🍎
```bash
# Intel
open tashinstaller_appleintel.dmg
# Apple Silicon
open tashinstaller_applesilicon.dmg

# In Finder: double-click "Install Tash.command"
# If blocked: xattr -d com.apple.quarantine "Install Tash.command"

# Portable
chmod +x tash_portable_appleintel.dmg
./tash_portable_appleintel.dmg dashboard
```

> **Note:** macOS `.dmg` files are true DMG packages on macOS; on other platforms they're raw Mach-O binaries.

---

## ✅ Verify Downloads

```bash
# Linux/macOS
sha256sum -c checksums.txt

# Windows PowerShell
Get-FileHash -Algorithm SHA256 tashinstaller_win64.exe
# Compare with checksums.txt
```

---

## 🚀 First Run

```bash
tash dashboard          # Opens TUI; runs setup wizard on first run
tash setup              # Same as dashboard
```

**Setup wizard guides you through:**
1. Platform detection (Windows/macOS/Linux/Termux)
2. Config directory creation (`~/.config/tash` or `%APPDATA%\tash`)
3. Tashnet role: **MAIN DEVICE** (first) or **JOIN TASHNET** (clients)
4. Optional: Tailscale, Pi-hole, Docker, NAS configuration
5. Launch dashboard

---

## 🎮 Dashboard Controls

| Key | Action |
|-----|--------|
| `1-9` | Switch tabs |
| `Tab` / `Shift+Tab` | Next/prev tab |
| `r` | Refresh all services |
| `?` | About / help |
| `q` | Quit |
| `↑↓` | Navigate lists |
| `p` / `P` | Ping peer / ping all (Tailscale) |
| `s` | SSH to peer (Tailscale) / Start/stop (NAS/Docker) |
| `f` | Follow logs (Docker) |
| `e` | Exec command in container (Docker) |
| `a/d` | Approve/deny device (Tashnet main) |
| `x` | SSH to device (Tashnet) |
| `ctrl+p` | **Command Palette** (fuzzy search all actions) |
| `esc` | Close modal / back in settings |

---

## 🌐 Tashnet — Secure Mesh

**Main Device (first machine):**
```bash
tash dashboard
# Choose MAIN DEVICE → generates secret key
```

**Client Devices:**
```bash
tash dashboard
# Choose JOIN TASHNET → paste secret key → wait for approval
```

**Main device approves in TASHNET tab (5):** `a` approve, `d` deny

**Transfer Main Role:**
```bash
tash main transfer                    # On current main
# Output: transfer code (expires 10 min)
tash main claim <code>                # On new device
```

**Rejoin after key rotation:**
```bash
tash join                             # Prompts for secret + main URL
```

---

## 🔧 Configuration

**Config file:** `~/.config/tash/config.json` (auto-managed)

**Settings (Tab 9) covers:**
- **Themes** — Built-in + custom (press `c` to copy & edit)
- **AI** — Provider (Claude/OpenAI/Anthropic/Custom), API key, model, command, workdir, permissions
- **NAS** — Password, root folder, port, read-only
- **Tailscale** — Display mode (all/active/custom), ping interval (1-60s), max peers
- **Keybindings** — Refresh, help, quit keys
- **Tabs** — Hide/show, reorder (J/K), reassign numbers (1-9)
- **UI** — Splash, 24h clock, deck layout (auto/cards/compact), tab position (top/bottom)
- **Reset to Defaults** — Factory reset (preserves Tashnet identity)

**CLI config:**
```bash
tash docker use cli|socket <host>|off
tash docker status
tash pihole setup|test|block|gravity|blocking
tash theme list|set|new|dir
tash ai status|prompt|launch
tash update check|install
tash doctor
tash secret show|regen
```

---

## 🤖 AI Integration (Tab 8)

**Supported:**
- **Claude Code** (default) — `npm i -g @anthropic-ai/claude-code` then `claude`
- **OpenAI-compatible** — Custom base URL + API key
- **Anthropic API** — Direct API key

**Features:**
- Persistent chat sessions (survives tab switches)
- Workdir-aware (reads files, runs tools)
- `ctrl+n` new chat, `ctrl+o` change workdir, `ctrl+a` API key, `ctrl+m` model, `ctrl+r` refresh

**Setup in Settings → AI:**
- Enable toggle
- Provider, base URL, API key, model, command, permission mode

---

## 🐳 Docker (Tab 4)

**Modes:**
- `cli` — Uses your `docker` command (follows contexts)
- `socket` — Direct socket (`unix:///var/run/docker.sock`)
- `off` — Disabled

**Container actions:**
| Key | Action |
|-----|--------|
| `s`/`x` | Start/stop |
| `p`/`u` | Pause/resume |
| `R` | Restart |
| `D` | Delete (force) |
| `l` | View logs (160 lines) |
| `f` | **Follow logs** (streaming, like `docker logs -f`) |
| `e` | **Exec command** (`sh`, `bash`, `ls`, `cat /var/log/...`) |

---

## 🕳️ Pi-hole (Tab 3)

**Setup:** `tash pihole setup` → URL + password/app-password → restart dashboard

**Actions:**
| Key | Action |
|-----|--------|
| `a` | Add block (URL/domain/regex) |
| `A` | Add allow |
| `b` | Toggle blocking |
| `g` | Run gravity update |

Shows: queries, blocked %, gravity domains, clients, recent queries, top blocked.

---

## 📁 NAS / WebDAV (Tab 6)

**Start/stop:** `s` key
**Mount help:** Platform-specific (Windows Explorer, Finder, davfs2, Android apps)
**File browser:** `Enter` open, `Backspace` up, Basic auth over HTTP
**Use Tailscale for remote access.**

---

## 🔑 Tailscale (Tab 2)

**Features:**
- Peer list with latency sparklines
- `p` ping selected, `P` ping all online
- `s` SSH to peer (uses NAS user)
- **Display modes** (Settings): `all` / `active` / `custom` (select peer IPs)
- **Ping interval:** 1-60 seconds
- **Max peers per interval:** 1-50

---

## 🎨 Themes

```bash
tash theme list              # Built-in (*) + custom
tash theme set matrix        # Apply theme
tash theme new mytheme       # Copy current to custom + open in editor
tash theme dir               # Show themes directory
```

**In Settings → Themes:** Live preview, `Enter` apply, `c` copy to custom.

---

## 🔌 Plugin System

**Directory:** `~/.config/tash/plugins/` (each plugin in its own folder with `manifest.json`)

**Plugin kinds:** `tab`, `widget`, `command`, `background`

**Built-in examples:**
- `example-widget` (Python) — System stats widget
- `ssh-connect` (Go) — SSH tab/command
- `file-sync` (Go) — Background NAS sync between devices

**Create your own:**
```json
{
  "name": "my-plugin",
  "version": "1.0.0",
  "kind": "command",
  "entry_point": "run.sh",
  "api_version": 1
}
```

**API available to plugins:** Config, UI (tabs/widgets), Tashnet, NAS, Docker, Pi-hole, Events, Logging.

---

## 🔄 File Sync (Plugin)

**Background plugin** syncing `sync/` folder via NAS between Tashnet devices.

**Config (manifest.json):**
```json
{
  "sync_interval": 30,
  "conflict_resolution": "newest",
  "watch_dirs": ["sync"]
}
```

---

## 🛠 Troubleshooting

| Issue | Fix |
|-------|-----|
| `command not found` | Add install dir to PATH, restart shell |
| Permission denied | `chmod +x` (Linux/macOS); Run as Admin (Windows) |
| macOS Gatekeeper blocks | `xattr -d com.apple.quarantine <file>` |
| Tailscale not found | Install Tailscale app, restart Tash |
| Docker not working | `tash docker use cli` or `socket <host>` |
| Config corrupted | Delete `~/.config/tash`, re-run `tash dashboard` |
| AI not detected | Check Settings → AI provider/key; `tash ai status` |
| NAS won't start | Check port free, root exists, `tash nas serve` for logs |

---

## 📦 What Gets Installed

| Component | Location |
|-----------|----------|
| `tash` binary | `--prefix`/bin or `~/.local/bin` / `%APPDATA%\tash\bin` |
| Config | `~/.config/tash` / `%APPDATA%\tash` |
| Secret key | `~/.config/tash/secret.key` |
| Themes | `~/.config/tash/themes` |
| Plugins | `~/.config/tash/plugins` |
| NAS root | `~/tash-nas` (configurable) |
| Logs | Stdout/stderr (run in terminal) |

---

## 📚 Command Reference

```bash
tash dashboard [--theme NAME] [--edit FILE]   # Open TUI
tash setup                                    # Same as dashboard
tash join [--url URL]                         # Pair with main device
tash edit [file]                              # Standalone editor
tash secret show [--reveal]                   # Show Tashnet secret
tash secret regen                             # Rotate key (DISCONNECTS ALL)
tash main transfer                            # 10-min handover code
tash main claim <code>                        # Claim main role
tash main info                                # Show role/status
tash nas serve                                # Run WebDAV headless
tash docker use cli|socket <host>|off         # Configure Docker
tash docker status                            # Engine status
tash pihole setup|test|block|gravity|blocking # Pi-hole control
tash theme list|set|new|dir                   # Theme management
tash ai status|prompt|launch                  # AI chat
tash update check|install                     # Check/install updates
tash doctor                                   # Full system diagnostics
tash version                                  # Version info
```

---

## 🎯 Quick Tips

- **`ctrl+p`** — Command palette: fuzzy search tabs, actions, settings, SSH, exec
- **`r`** — Refresh all services instantly
- **`Tab`** — Cycle tabs; `Shift+Tab` reverse
- **Settings → Tabs** — Hide unused tabs, reorder with J/K, renumber with 1-9
- **Settings → Tailscale** — Set ping to `1` for real-time latency
- **Docker `f`** — Follow logs like `tail -f`; `e` — run `bash` for shell
- **Tashnet `t`** — Transfer main role safely with 10-min code
- **Plugin `file-sync`** — Drop files in `~/tash-nas/sync` to replicate across devices

---

## 🔗 Resources

- **GitHub:** https://github.com/tash/tash
- **Issues:** https://github.com/tash/tash/issues
- **In-app help:** Press `?` in dashboard
- **Doctor:** `tash doctor` for full system check

---

**Tash — Terminal Control Deck** 🎉

*Built with Go • Terminal-first • Privacy-focused • Extensible*
