// Command tash is the terminal entrypoint for the Tash dashboard.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tash/tash/internal/app"
	cc "github.com/tash/tash/internal/claude"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/docker"
	vim "github.com/tash/tash/internal/editor"
	"github.com/tash/tash/internal/nas"
	"github.com/tash/tash/internal/pihole"
	"github.com/tash/tash/internal/tailscale"
	"github.com/tash/tash/internal/tashnet"
	"github.com/tash/tash/internal/theme"
	"github.com/tash/tash/internal/version"
	"golang.org/x/term"
)

var stdin = bufio.NewReader(os.Stdin)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return cmdDashboard(nil)
	}
	cmd := strings.ToLower(args[0])
	rest := args[1:]
	switch cmd {
	case "dashboard", "dash", "d", "ui", "tui":
		return cmdDashboard(rest)
	case "setup", "onboard":
		return cmdDashboard(rest)
	case "edit", "vim":
		return cmdEdit(rest)
	case "join", "pair":
		return cmdJoin(rest)
	case "secret", "key":
		return cmdSecret(rest)
	case "main":
		return cmdMain(rest)
	case "nas":
		return cmdNAS(rest)
	case "docker", "dock":
		return cmdDocker(rest)
	case "pihole", "pi-hole", "pi":
		return cmdPiHole(rest)
	case "theme", "themes":
		return cmdTheme(rest)
	case "ai":
		return cmdAI(rest)
	case "update":
		return cmdUpdate(rest)
	case "doctor", "check", "diag":
		return cmdDoctor(rest)
	case "version", "-v", "--version":
		printVersion()
		return 0
	case "help", "-h", "--help":
		printHelp()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tash: unknown command %q\n\n", args[0])
		printHelp()
		return 2
	}
}

func cmdDashboard(args []string) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	themeName := fs.String("theme", "", "override the saved theme for this run")
	editorFile := fs.String("edit", "", "open the editor tab on this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	model, err := app.New(app.Options{Theme: *themeName, EditorFile: *editorFile})
	if err != nil {
		if errors.Is(err, config.ErrNotSetup) {
			fmt.Fprintln(os.Stderr, "Tash is not set up yet. Run `tash dashboard` from an interactive terminal.")
		} else {
			fmt.Fprintln(os.Stderr, "tash:", err)
			fmt.Fprintln(os.Stderr, "If the Tashnet key file is missing, restore it or re-run first-time setup.")
		}
		return 1
	}
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tash dashboard:", err)
		return 1
	}
	return 0
}

// Standalone editor --------------------------------------------------------

type standaloneEditor struct {
	m *vim.Model
}

func (e *standaloneEditor) Init() tea.Cmd { return nil }

func (e *standaloneEditor) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		e.m.SetSize(msg.Width, msg.Height)
		return e, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return e, tea.Quit
		}
		e.m.HandleKey(msg)
		if e.m.CloseRequested() {
			return e, tea.Quit
		}
	}
	return e, nil
}

func (e *standaloneEditor) View() string { return e.m.View() }

func cmdEdit(args []string) int {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	themeName := fs.String("theme", "", "override theme")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := fs.Arg(0)
	store, err := config.Open()
	var cfg config.Config
	if err != nil {
		if !errors.Is(err, config.ErrNotSetup) {
			fmt.Fprintln(os.Stderr, "tash edit: warning:", err)
		}
		cfg = config.Default()
	} else {
		cfg = store.Snapshot()
	}
	if *themeName != "" {
		cfg.UI.Theme = *themeName
	}
	registry := theme.NewRegistry(config.Dir())
	palette := vim.PaletteFromTheme(registry.Get(cfg.UI.Theme))
	model := vim.New(palette)
	model.SetSize(100, 30)
	if path == "" {
		path = cfg.LastEditorFile
	}
	if path == "" {
		path = filepath.Join(config.Dir(), "notes.md")
	}
	if err := model.Load(path); err != nil {
		fmt.Fprintln(os.Stderr, "tash edit:", err)
		return 1
	}
	if _, err := tea.NewProgram(&standaloneEditor{m: model}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tash edit:", err)
		return 1
	}
	if store != nil {
		_ = store.Update(func(c *config.Config) error {
			c.LastEditorFile = model.Path()
			return nil
		})
	}
	return 0
}

// Join ---------------------------------------------------------------------

func cmdJoin(args []string) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "main device URL")
	secretFlag := fs.String("secret", "", "secret key (prompted when omitted)")
	portFlag := fs.Int("port", 8787, "control port to discover")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var store *config.Store
	cfg := config.Default()
	if s, err := config.Open(); err == nil {
		store = s
		cfg = s.Snapshot()
		if cfg.Tashnet.Role == "main" {
			fmt.Fprintln(os.Stderr, "This device is the Tashnet MAIN device.")
			fmt.Fprintln(os.Stderr, "Use `tash main transfer` to hand the role to another device first.")
			return 1
		}
	} else if !errors.Is(err, config.ErrNotSetup) {
		fmt.Fprintln(os.Stderr, "tash join:", err)
		return 1
	}
	if cfg.Tashnet.DeviceName == "" {
		cfg = config.Default()
	}
	if *portFlag > 0 {
		cfg.Tashnet.ControlPort = *portFlag
	}
	secretRaw := *secretFlag
	if secretRaw == "" {
		secretRaw = readSecret("Secret Key: ")
	}
	secret, err := tashnet.NormalizeSecret(secretRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash join:", err)
		return 1
	}
	mainURL := strings.TrimSpace(*urlFlag)
	if mainURL == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		fmt.Print("Searching the tailnet for a main device")
		found := tashnet.Discover(ctx, cfg.Tashnet.ControlPort, 1200*time.Millisecond)
		cancel()
		fmt.Println()
		if len(found) > 0 {
			mainURL = found[0].URL
			fmt.Printf("Found %s (%s)\n", found[0].Hello.MainDeviceName, mainURL)
			answer := strings.ToLower(strings.TrimSpace(readLine("Use this device? [Y/n]: ")))
			if strings.HasPrefix(answer, "n") {
				mainURL = ""
			}
		}
	}
	if mainURL == "" {
		mainURL = readLine("Main device address (host:port or URL): ")
	}
	mainURL, err = tashnet.NormalizeURL(mainURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash join:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := tashnet.NewClient(mainURL, "", version.Version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash join:", err)
		return 1
	}
	resp, err := client.Join(ctx, secret, cfg.Tashnet.DeviceID, cfg.Tashnet.DeviceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash join:", err)
		return 1
	}
	if resp.Status == "pending" {
		fmt.Printf("Request sent. Approve %q on the main device (%s).\n", cfg.Tashnet.DeviceName, resp.MainDeviceName)
		fmt.Println("Waiting for approval… (Ctrl+C to cancel)")
		for {
			time.Sleep(2 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			poll, err := client.Poll(ctx, resp.PendingID, resp.PollToken, cfg.Tashnet.DeviceID)
			cancel()
			if err != nil {
				fmt.Fprintln(os.Stderr, "poll:", err)
				continue
			}
			switch poll.Status {
			case "approved":
				resp.Token = poll.Token
				resp.MainURL = poll.MainURL
				resp.MainDeviceName = poll.MainDeviceName
				resp.SecretVersion = poll.SecretVersion
				goto approved
			case "denied":
				fmt.Fprintln(os.Stderr, "The main device denied the request:", poll.Message)
				return 1
			}
		}
	}
approved:
	if resp.Token == "" {
		fmt.Fprintln(os.Stderr, "tash join: main device returned an empty token")
		return 1
	}
	cfg.Tashnet.Role = "client"
	cfg.Tashnet.MainURL = resp.MainURL
	cfg.Tashnet.MainDeviceID = resp.MainDeviceID
	cfg.Tashnet.MainDeviceName = resp.MainDeviceName
	cfg.Tashnet.Token = resp.Token
	cfg.Tashnet.SecretVersion = resp.SecretVersion
	cfg.Tashnet.Devices = map[string]config.Device{}
	cfg.Tashnet.Pending = map[string]config.Pending{}
	if store == nil {
		store, err = config.Create(cfg, "")
	} else {
		err = store.Update(func(c *config.Config) error {
			*c = mergeJoin(*c, cfg)
			return nil
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash join: save:", err)
		return 1
	}
	_ = store.ClearSecret()
	fmt.Printf("Paired with %s at %s\n", resp.MainDeviceName, resp.MainURL)
	fmt.Println("Run `tash dashboard` to open the deck.")
	return 0
}

func mergeJoin(old, next config.Config) config.Config {
	out := old
	out.Tashnet = next.Tashnet
	out.UpdatedAt = time.Now().UTC()
	return out
}

// Secret -------------------------------------------------------------------

func cmdSecret(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash secret show [--reveal] | regen")
		return 2
	}
	store, err := requireStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash secret:", err)
		return 1
	}
	cfg := store.Snapshot()
	switch strings.ToLower(args[0]) {
	case "show", "reveal":
		if cfg.Tashnet.Role != "main" {
			fmt.Println("This device is a Tashnet client; the network secret is only stored on the main device.")
			return 0
		}
		secret := store.Secret()
		reveal := false
		for _, a := range args[1:] {
			if a == "--reveal" || a == "-r" {
				reveal = true
			}
		}
		if secret == "" {
			fmt.Fprintln(os.Stderr, "No secret key on disk. Restore secret.key or re-run setup.")
			return 1
		}
		if reveal {
			fmt.Println(secret)
		} else {
			fmt.Println(tashnet.MaskSecret(secret))
			fmt.Println("(add --reveal to print the full key)")
		}
		return 0
	case "regen", "regenerate", "rotate":
		return regenSecret(store)
	default:
		fmt.Fprintln(os.Stderr, "usage: tash secret show [--reveal] | regen")
		return 2
	}
}

func regenSecret(store *config.Store) int {
	cfg := store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		fmt.Fprintln(os.Stderr, "Only the Tashnet main device owns the secret key.")
		return 1
	}
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  EXTREME WARNING — THIS DISCONNECTS EVERY EXISTING DEVICE   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Regenerating the Tashnet secret key will immediately:")
	fmt.Println("  • invalidate every approved device token")
	fmt.Println("  • disconnect every client from this main device")
	fmt.Println("  • reject every pending join request")
	fmt.Println("  • require every device to rejoin with the new key and be re-approved")
	fmt.Println()
	fmt.Println("This cannot be undone. If you only want to remove one device, do that")
	fmt.Println("from the TASHNET tab instead (or deny its next request).")
	fmt.Println()
	answer := strings.TrimSpace(readLine(`Type REGENERATE to continue: `))
	if answer != "REGENERATE" {
		fmt.Println("Aborted. Nothing changed.")
		return 1
	}
	newSecret, err := tashnet.GenerateSecret()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash secret regen:", err)
		return 1
	}
	now := time.Now().UTC()
	err = store.Update(func(c *config.Config) error {
		c.Tashnet.SecretVersion++
		c.Tashnet.Pending = map[string]config.Pending{}
		self := c.Tashnet.Devices[c.Tashnet.DeviceID]
		self.ID = c.Tashnet.DeviceID
		self.Name = c.Tashnet.DeviceName
		self.Token = tashnet.NewToken()
		self.ApprovedAt = now
		self.LastSeen = now
		c.Tashnet.Devices = map[string]config.Device{c.Tashnet.DeviceID: self}
		c.Tashnet.TransferNonce = ""
		c.Tashnet.TransferExpiry = time.Time{}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash secret regen:", err)
		return 1
	}
	if err := store.SetSecret(newSecret); err != nil {
		fmt.Fprintln(os.Stderr, "tash secret regen:", err)
		return 1
	}
	fmt.Println()
	fmt.Println("New Secret Key:")
	fmt.Println()
	fmt.Println("  " + newSecret)
	fmt.Println()
	fmt.Println("All previous devices are disconnected. Restart `tash dashboard` on")
	fmt.Println("this device so the running control server picks up the new key.")
	return 0
}

// Main role ----------------------------------------------------------------

func cmdMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash main transfer | claim <code> | info")
		return 2
	}
	switch strings.ToLower(args[0]) {
	case "transfer":
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash main transfer:", err)
			return 1
		}
		code, expires, err := tashnet.ExportTransfer(store)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash main transfer:", err)
			return 1
		}
		fmt.Println("╔══════════════════════════════════════════════════════════════╗")
		fmt.Println("║  MAIN-ROLE TRANSFER CODE — EXPIRES IN 10 MINUTES            ║")
		fmt.Println("╚══════════════════════════════════════════════════════════════╝")
		fmt.Println()
		fmt.Println("This code contains the Tashnet secret and every approved device")
		fmt.Println("token. Treat it exactly like the secret key. On the NEW device run:")
		fmt.Println()
		fmt.Println("  tash main claim <code>")
		fmt.Println()
		fmt.Println(code)
		fmt.Println()
		fmt.Printf("Expires: %s\n", expires.Local().Format(time.RFC1123))
		fmt.Println("The old main is demoted only after the new device claims the role.")
		return 0
	case "claim":
		return cmdMainClaim(args[1:])
	case "info", "status":
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash main:", err)
			return 1
		}
		cfg := store.Snapshot().Tashnet
		url := ""
		if cfg.Role == "main" {
			url = tashnet.AdvertiseURL(cfg.ControlPort)
		}
		fmt.Printf("role:            %s\n", cfg.Role)
		fmt.Printf("device:          %s (%s)\n", cfg.DeviceName, cfg.DeviceID)
		fmt.Printf("network:         %s\n", cfg.Name)
		fmt.Printf("control URL:     %s\n", firstNonEmpty(url, cfg.MainURL, "—"))
		fmt.Printf("secret version:  %d\n", cfg.SecretVersion)
		fmt.Printf("approved:        %d\n", len(cfg.Devices))
		fmt.Printf("pending:         %d\n", len(cfg.Pending))
		if cfg.Role == "main" {
			fmt.Printf("secret:          %s (use `tash secret show --reveal`)\n", tashnet.MaskSecret(store.Secret()))
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: tash main transfer | claim <code> | info")
		return 2
	}
}

func cmdMainClaim(args []string) int {
	fs := flag.NewFlagSet("main claim", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "advertised URL for this new main device")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	code := strings.Join(fs.Args(), "")
	code = strings.ReplaceAll(code, "\n", "")
	code = strings.ReplaceAll(code, "\r", "")
	if code == "" {
		fmt.Fprintln(os.Stderr, "usage: tash main claim [-url http://host:8787] <transfer-code>")
		return 2
	}
	payload, err := tashnet.ParseTransfer(code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash main claim:", err)
		return 1
	}
	store, cfg, err := optionalStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash main claim:", err)
		return 1
	}
	if store != nil && cfg.Tashnet.Role == "main" {
		fmt.Fprintln(os.Stderr, "This device is already the main device; refusing to overwrite its role.")
		fmt.Fprintln(os.Stderr, "Run the claim on a client or fresh device instead.")
		return 1
	}
	claim, err := tashnet.BuildClaim(payload, cfg.Tashnet.DeviceID, cfg.Tashnet.DeviceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash main claim:", err)
		return 1
	}
	newURL := strings.TrimSpace(*urlFlag)
	if newURL == "" {
		newURL = tashnet.AdvertiseURL(claim.Tashnet.ControlPort)
	}
	newURL, err = tashnet.NormalizeURL(newURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash main claim:", err)
		return 1
	}
	cfg.Tashnet = claim.Tashnet
	if store == nil {
		store, err = config.Create(cfg, claim.Secret)
	} else {
		err = store.Update(func(c *config.Config) error {
			c.Tashnet = claim.Tashnet
			return nil
		})
		if err == nil {
			err = store.SetSecret(claim.Secret)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash main claim:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notifyErr := tashnet.NotifyTransferComplete(ctx, claim.OldURL, claim.Secret, claim.Nonce,
		newURL, claim.Tashnet.DeviceID, claim.Tashnet.DeviceName)
	fmt.Println()
	fmt.Println("This device is now the Tashnet MAIN device.")
	fmt.Printf("  device:  %s (%s)\n", claim.Tashnet.DeviceName, claim.Tashnet.DeviceID)
	fmt.Printf("  URL:     %s\n", newURL)
	fmt.Printf("  devices: %d approved\n", len(claim.Tashnet.Devices))
	fmt.Println()
	if notifyErr != nil {
		fmt.Println("WARNING: the old main could not be notified:")
		fmt.Println("  " + notifyErr.Error())
		fmt.Println("Both devices may believe they are main (split brain). Contact the")
		fmt.Println("old main if possible and have it stop its dashboard / rejoin as a client.")
	} else {
		fmt.Println("The old main was notified and demoted to a client.")
	}
	fmt.Println()
	fmt.Println("Run `tash dashboard` on this device to start the main control server.")
	return 0
}

// NAS ----------------------------------------------------------------------

func cmdNAS(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash nas serve")
		return 2
	}
	if strings.ToLower(args[0]) != "serve" && strings.ToLower(args[0]) != "start" {
		fmt.Fprintln(os.Stderr, "usage: tash nas serve")
		return 2
	}
	store, err := requireStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash nas:", err)
		return 1
	}
	cfg := store.Snapshot().NAS
	if err := nas.EnsureRoot(cfg.Root); err != nil {
		fmt.Fprintln(os.Stderr, "tash nas:", err)
		return 1
	}
	server := nas.New(cfg)
	if err := server.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "tash nas:", err)
		return 1
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	}()
	fmt.Println("Tash NAS (WebDAV) running")
	fmt.Println("  URL:  " + server.URL())
	fmt.Println("  DAV:  " + server.DAVURL())
	fmt.Println("  user: " + cfg.User)
	fmt.Println("  pass: " + cfg.Password)
	fmt.Println("  root: " + cfg.Root)
	fmt.Println()
	fmt.Println(nas.MountHelp(platformName()))
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")
	ctx, stop := signalContext()
	defer stop()
	<-ctx.Done()
	fmt.Println("\nstopping…")
	return 0
}

// Docker -------------------------------------------------------------------

func cmdDocker(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash docker use cli|socket <host>|off | status")
		return 2
	}
	switch strings.ToLower(args[0]) {
	case "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: tash docker use cli|socket <host>|off")
			return 2
		}
		mode := strings.ToLower(args[1])
		host := ""
		if len(args) > 2 {
			host = strings.Join(args[2:], " ")
		}
		if mode != "cli" && mode != "socket" && mode != "api" && mode != "off" && mode != "none" {
			fmt.Fprintln(os.Stderr, "mode must be cli, socket, or off")
			return 2
		}
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash docker:", err)
			return 1
		}
		cfg := store.Snapshot()
		host = firstNonEmpty(strings.TrimSpace(host), cfg.Docker.Host, config.DefaultDockerHost())
		if mode == "api" {
			mode = "socket"
		}
		err = store.Update(func(c *config.Config) error {
			c.Docker.Mode = mode
			c.Docker.Host = host
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash docker:", err)
			return 1
		}
		if mode == "off" || mode == "none" {
			fmt.Println("Docker integration disabled.")
		} else {
			fmt.Printf("Docker mode: %s\nhost: %s\n", mode, host)
			if mode == "socket" {
				fmt.Println("Note: the Docker socket grants root-equivalent control of the host.")
				fmt.Println("Never expose TCP mode to the public internet.")
			}
		}
		fmt.Println("Restart `tash dashboard` to apply.")
		return 0
	case "status":
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash docker:", err)
			return 1
		}
		cfg := store.Snapshot().Docker
		client, err := docker.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash docker:", err)
			return 1
		}
		if client == nil {
			fmt.Println("Docker integration is off.")
			return 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		snap := client.Fetch(ctx)
		if snap.Error != "" {
			fmt.Fprintln(os.Stderr, "docker:", snap.Error)
			return 1
		}
		fmt.Printf("mode:    %s\nengine:  %s\nserver:  %s\nos/arch: %s/%s\nrunning: %d/%d\n",
			snap.Mode, snap.Engine, snap.ServerVer, snap.OS, snap.Arch, snap.Running, len(snap.Containers))
		for _, c := range snap.Containers {
			state := "stopped"
			if c.Running {
				state = "running"
			}
			fmt.Printf("  %-24s %-10s %s\n", c.Name, state, c.Image)
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: tash docker use cli|socket <host>|off | status")
		return 2
	}
}

// Pi-hole ------------------------------------------------------------------

func cmdPiHole(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash pihole setup | test | block <url|domain|regex:...> | gravity | blocking on|off")
		return 2
	}
	switch strings.ToLower(args[0]) {
	case "setup", "config":
		return piholeSetup(args[1:])
	case "test", "status":
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		cfg := store.Snapshot().PiHole
		client, err := pihole.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		snap, err := client.Test(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pihole:", err)
			return 1
		}
		printPiHoleSummary(snap)
		return 0
	case "block", "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: tash pihole block <blocklist-url|domain|regex:pattern>")
			return 2
		}
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		cfg := store.Snapshot().PiHole
		client, err := pihole.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		out, err := client.AddBlock(ctx, strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, "pihole:", err)
			return 1
		}
		fmt.Println(out)
		return 0
	case "gravity", "update":
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		cfg := store.Snapshot().PiHole
		client, err := pihole.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		out, err := client.RunGravity(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pihole:", err)
			return 1
		}
		fmt.Println(firstLine(out))
		return 0
	case "blocking", "enable", "disable":
		mode := strings.ToLower(args[0])
		if mode == "blocking" {
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: tash pihole blocking on|off")
				return 2
			}
			mode = strings.ToLower(args[1])
		}
		enable := mode == "on" || mode == "enable" || mode == "enabled" || mode == "true" || mode == "1"
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		cfg := store.Snapshot().PiHole
		client, err := pihole.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash pihole:", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := client.SetBlocking(ctx, enable); err != nil {
			fmt.Fprintln(os.Stderr, "pihole:", err)
			return 1
		}
		if enable {
			fmt.Println("Pi-hole blocking enabled.")
		} else {
			fmt.Println("Pi-hole blocking disabled.")
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: tash pihole setup | test | block <entry> | gravity | blocking on|off")
		return 2
	}
}

func piholeSetup(args []string) int {
	fs := flag.NewFlagSet("pihole setup", flag.ContinueOnError)
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	store, err := requireStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash pihole:", err)
		return 1
	}
	cfg := store.Snapshot().PiHole
	current := cfg.URL
	if current == "" {
		current = "http://"
	}
	raw := strings.TrimSpace(readLineDefault("Pi-hole URL", current))
	if raw == "" || raw == "http://" {
		fmt.Fprintln(os.Stderr, "Pi-hole URL is required")
		return 1
	}
	secret := readSecret("Pi-hole password / app password / v5 API token (blank keeps current): ")
	if secret == "" {
		secret = cfg.Secret
	}
	next := cfg
	next.Enabled = true
	next.URL = strings.TrimRight(raw, "/")
	next.Secret = secret
	next.SkipTLSVerify = *insecure
	client, err := pihole.New(next)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash pihole:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	snap, testErr := client.Test(ctx)
	cancel()
	if testErr != nil {
		fmt.Fprintln(os.Stderr, "Connection test failed:", testErr)
		answer := strings.ToLower(strings.TrimSpace(readLine("Save anyway? [y/N]: ")))
		if !strings.HasPrefix(answer, "y") {
			fmt.Println("Aborted. Nothing saved.")
			return 1
		}
	} else {
		printPiHoleSummary(snap)
	}
	err = store.Update(func(c *config.Config) error {
		c.PiHole = next
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tash pihole:", err)
		return 1
	}
	fmt.Println("Pi-hole configuration saved. Restart `tash dashboard`.")
	return 0
}

func printPiHoleSummary(s pihole.Snapshot) {
	fmt.Printf("Pi-hole v%d at %s\n", s.Version, s.URL)
	fmt.Printf("  queries:   %d\n", s.Summary.TotalQueries)
	fmt.Printf("  blocked:   %d (%.2f%%)\n", s.Summary.Blocked, s.Summary.PercentBlocked)
	fmt.Printf("  gravity:   %d domains\n", s.Summary.GravityDomains)
	fmt.Printf("  clients:   %d active / %d total\n", s.Summary.ActiveClients, s.Summary.TotalClients)
	fmt.Printf("  blocking:  %s\n", firstNonEmpty(s.Blocking, "unknown"))
}

// Theme --------------------------------------------------------------------

func cmdAI(args []string) int {
	cfg := config.Default().AI
	if store, err := config.Open(); err == nil {
		cfg = store.Snapshot().AI
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch strings.ToLower(args[0]) {
	case "status", "info":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st := cc.Detect(ctx, cfg)
		fmt.Printf("enabled:  %v\n", st.Enabled)
		fmt.Printf("available: %v\n", st.Available)
		fmt.Printf("command:  %s\n", st.Command)
		if st.Path != "" {
			fmt.Printf("path:     %s\n", st.Path)
		}
		if st.Version != "" {
			fmt.Printf("version:  %s\n", st.Version)
		}
		fmt.Printf("auth:     %s\n", st.AuthMode)
		if st.ConfigFile != "" {
			fmt.Printf("config:   %s (found=%v)\n", st.ConfigFile, st.ConfigFound)
		}
		fmt.Printf("workdir:  %s\n", cc.WorkDir(cfg))
		fmt.Printf("projects: %d\n", len(st.Projects))
		for i, p := range st.Projects {
			if i >= 10 {
				break
			}
			fmt.Printf("  - %s\n", p.Path)
		}
		if st.Error != "" {
			fmt.Println("note:     " + st.Error)
		}
		return 0
	case "prompt", "ask", "run":
		prompt := strings.TrimSpace(strings.Join(args[1:], " "))
		if prompt == "" {
			prompt = strings.TrimSpace(readLine("Prompt: "))
		}
		if prompt == "" {
			fmt.Fprintln(os.Stderr, "usage: tash ai prompt <text>")
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute+15*time.Second)
		defer cancel()
		text, err := cc.Prompt(ctx, cfg, prompt, cc.WorkDir(cfg))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ai:", err)
			return 1
		}
		fmt.Println(text)
		return 0
	case "launch", "open":
		dir := cc.WorkDir(cfg)
		if len(args) > 1 {
			dir = config.ExpandPath(strings.TrimSpace(args[1]))
		}
		cmd, err := cc.Command(cfg, dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ai:", err)
			return 1
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "ai:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: tash ai status | prompt <text> | launch [dir]")
		return 2
	}
}

func cmdUpdate(args []string) int {
	action := "check"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	fmt.Printf("Tash %s\n", version.Version)
	fmt.Println("Checking for updates...")
	
	// Check GitHub releases for latest version
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/nypryt/Tash/releases/latest", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: failed to create request:", err)
		return 1
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: failed to check for updates:", err)
		return 1
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "update: GitHub API returned %s\n", resp.Status)
		return 1
	}
	
	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Assets  []struct {
			Name        string `json:"name"`
			BrowserURL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		fmt.Fprintln(os.Stderr, "update: failed to parse response:", err)
		return 1
	}
	
	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(version.Version, "v")
	
	fmt.Printf("Current version: %s\n", version.Version)
	fmt.Printf("Latest version:  v%s\n", latest)
	
	if latest == current {
		fmt.Println("You are already on the latest version.")
		return 0
	}
	
	fmt.Printf("Update available: %s -> %s\n", version.Version, "v"+latest)
	
	if action == "check" {
		fmt.Println("\nRun `tash update install` to download and install the latest version.")
		return 0
	}
	
	if action != "install" {
		fmt.Fprintf(os.Stderr, "usage: tash update [check|install]\n")
		return 2
	}
	
	// Find the appropriate asset for this platform
	assetName := fmt.Sprintf("Tash-Setup-%s-%s-%s", release.TagName, platformName(), platformArch())
	if runtime.GOOS == "windows" {
		assetName += ".exe"
	} else if runtime.GOOS == "linux" {
		assetName += ".elf"
	} else if runtime.GOOS == "darwin" {
		assetName += ".dmg"
	}
	
	var downloadURL string
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, assetName) {
			downloadURL = asset.BrowserURL
			break
		}
	}
	
	if downloadURL == "" {
		fmt.Fprintf(os.Stderr, "update: no installer found for %s/%s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Println("Visit:", release.HTMLURL)
		return 1
	}
	
	fmt.Printf("Downloading %s...\n", assetName)
	
	req, err = http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: failed to create download request:", err)
		return 1
	}
	
	resp, err = client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: download failed:", err)
		return 1
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "update: download returned %s\n", resp.Status)
		return 1
	}
	
	// Save to temp file
	tmpFile, err := os.CreateTemp("", "tash-update-*.tmp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: failed to create temp file:", err)
		return 1
	}
	tmpPath := tmpFile.Name()
	
	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		fmt.Fprintln(os.Stderr, "update: failed to save installer:", err)
		return 1
	}
	
	// Make executable on Unix
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "update: failed to make installer executable:", err)
			os.Remove(tmpPath)
			return 1
		}
	}
	
	fmt.Println("Launching installer...")
	cmd := exec.Command(tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	os.Remove(tmpPath)
	
	if err != nil {
		fmt.Fprintln(os.Stderr, "update: installer failed:", err)
		return 1
	}
	
	fmt.Println("Update complete! Restart Tash to use the new version.")
	return 0
}

func cmdTheme(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tash theme list | set <name> | new <name> [--from <theme>] | dir")
		return 2
	}
	registry := theme.NewRegistry(config.Dir())
	switch strings.ToLower(args[0]) {
	case "list", "ls":
		current := ""
		if store, err := config.Open(); err == nil {
			current = store.Snapshot().UI.Theme
		}
		for _, name := range registry.Names() {
			marker := "  "
			if strings.EqualFold(name, current) {
				marker = "* "
			}
			kind := "custom"
			if theme.IsBuiltin(name) {
				kind = "builtin"
			}
			fmt.Printf("%s%-18s %s\n", marker, name, kind)
		}
		return 0
	case "set", "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: tash theme set <name>")
			return 2
		}
		name := strings.ToLower(args[1])
		valid := false
		for _, n := range registry.Names() {
			if n == name {
				valid = true
				break
			}
		}
		if !valid {
			fmt.Fprintf(os.Stderr, "unknown theme %q; run `tash theme list`\n", name)
			return 1
		}
		store, err := requireStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash theme:", err)
			return 1
		}
		if err := store.Update(func(c *config.Config) error {
			c.UI.Theme = name
			return nil
		}); err != nil {
			fmt.Fprintln(os.Stderr, "tash theme:", err)
			return 1
		}
		fmt.Println("Theme set to", name+". Restart `tash dashboard` or open the Themes tab.")
		return 0
	case "new", "copy":
		fs := flag.NewFlagSet("theme new", flag.ContinueOnError)
		from := fs.String("from", "matrix", "base theme")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		name := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: tash theme new <name> [--from <theme>]")
			return 2
		}
		base := registry.Get(*from)
		t := theme.Template(base, name)
		path, err := registry.Save(t)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tash theme:", err)
			return 1
		}
		fmt.Println("Created", path)
		fmt.Println("Edit it with: tash edit " + path)
		fmt.Println("Apply it with: tash theme set " + name)
		return 0
	case "dir", "path":
		fmt.Println(registry.Dir())
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: tash theme list | set <name> | new <name> [--from <theme>] | dir")
		return 2
	}
}

// Doctor -------------------------------------------------------------------

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Println("TASH DOCTOR")
	fmt.Println("───────────")
	fmt.Printf("version:   %s (%s, built %s)\n", version.Version, version.Commit, version.BuildDate)
	fmt.Printf("runtime:   %s/%s · %s\n", platformName(), platformArch(), versionGo())
	fmt.Printf("config:    %s\n", config.Dir())
	fmt.Printf("termux:    %v\n", config.IsTermux())
	fmt.Println()

	store, err := config.Open()
	if errors.Is(err, config.ErrNotSetup) {
		fmt.Println("[!] config: not set up yet (run `tash dashboard`)")
	} else if err != nil {
		fmt.Println("[✖] config:", err)
	} else {
		cfg := store.Snapshot()
		fmt.Printf("[✓] config: role=%s device=%s\n", cfg.Tashnet.Role, cfg.Tashnet.DeviceName)
		if cfg.Tashnet.Role == "main" {
			if store.Secret() == "" {
				fmt.Println("[✖] secret: main device has no secret.key")
			} else {
				fmt.Printf("[✓] secret: %s (version %d)\n", tashnet.MaskSecret(store.Secret()), cfg.Tashnet.SecretVersion)
			}
		} else {
			fmt.Printf("[✓] uplink: %s (%s)\n", cfg.Tashnet.MainDeviceName, cfg.Tashnet.MainURL)
		}
		fmt.Printf("      approved devices: %d · pending: %d\n", len(cfg.Tashnet.Devices), len(cfg.Tashnet.Pending))
	}

	if bin, ok := tailscale.FindBinary(); !ok {
		if config.IsTermux() {
			fmt.Println("[~] tailscale: CLI unavailable on unrooted Termux; use the Android app")
		} else {
			fmt.Println("[!] tailscale: CLI not found")
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		st := tailscale.ReadStatus(ctx)
		cancel()
		if st.Error != "" {
			fmt.Println("[!] tailscale:", st.Error, "("+bin+")")
		} else {
			fmt.Printf("[✓] tailscale: %s · %d peers online\n", firstNonEmpty(st.BackendState, "unknown"), len(st.OnlinePeers()))
		}
	}

	if store != nil {
		cfg := store.Snapshot()
		if cfg.Docker.Mode == "off" || cfg.Docker.Mode == "" {
			fmt.Println("[~] docker: disabled")
		} else {
			client, err := docker.New(cfg.Docker)
			if err != nil {
				fmt.Println("[✖] docker:", err)
			} else if client == nil {
				fmt.Println("[~] docker: disabled")
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				snap := client.Fetch(ctx)
				cancel()
				if snap.Error != "" {
					fmt.Println("[!] docker:", snap.Error, "(mode "+cfg.Docker.Mode+")")
				} else {
					fmt.Printf("[✓] docker: %s · %d/%d containers running\n", snap.ServerVer, snap.Running, len(snap.Containers))
				}
			}
		}
		if cfg.PiHole.Enabled && cfg.PiHole.URL != "" {
			client, err := pihole.New(cfg.PiHole)
			if err != nil {
				fmt.Println("[✖] pi-hole:", err)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
				snap, err := client.Test(ctx)
				cancel()
				if err != nil {
					fmt.Println("[!] pi-hole:", err)
				} else {
					fmt.Printf("[✓] pi-hole: v%d · %d queries · %d blocked (%.1f%%)\n",
						snap.Version, snap.Summary.TotalQueries, snap.Summary.Blocked, snap.Summary.PercentBlocked)
				}
			}
		} else {
			fmt.Println("[~] pi-hole: not configured (tash pihole setup)")
		}
		fmt.Printf("      NAS: %s (port %d, auto-start %v)\n", cfg.NAS.Root, cfg.NAS.Port, cfg.NAS.AutoStart)
		if !cfg.AI.Enabled {
			fmt.Println("[~] ai: disabled in Settings")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			st := cc.Detect(ctx, cfg.AI)
			cancel()
			if st.Available {
				fmt.Printf("[✓] ai: %s · %s · %d projects\n",
					firstNonEmpty(st.Version, "installed"), st.AuthMode, len(st.Projects))
			} else {
				fmt.Println("[!] ai:", st.Error)
			}
		}
	}
	fmt.Println()
	fmt.Println("If something is wrong: `tash doctor`, then `tash dashboard`.")
	return 0
}

// Helpers ------------------------------------------------------------------

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func platformName() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "mac"
	case "linux", "android":
		return "linux"
	default:
		return runtime.GOOS
	}
}

func platformArch() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "amd64" {
			return "intel"
		}
		if runtime.GOARCH == "arm64" {
			return "apple-silicon"
		}
		return runtime.GOARCH
	case "windows", "linux":
		if runtime.GOARCH == "amd64" {
			return "x64"
		}
		return runtime.GOARCH
	default:
		return runtime.GOARCH
	}
}

func versionGo() string { return runtime.Version() }

func requireStore() (*config.Store, error) {
	store, err := config.Open()
	if errors.Is(err, config.ErrNotSetup) {
		return nil, errors.New("Tash is not set up yet; run `tash dashboard` first")
	}
	return store, err
}

func optionalStore() (*config.Store, config.Config, error) {
	store, err := config.Open()
	if errors.Is(err, config.ErrNotSetup) {
		return nil, config.Default(), nil
	}
	if err != nil {
		return nil, config.Config{}, err
	}
	return store, store.Snapshot(), nil
}

func readLine(label string) string {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func readLineDefault(label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func readSecret(label string) string {
	fmt.Print(label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func printVersion() {
	fmt.Printf("Tash %s\ncommit: %s\nbuilt:  %s\nruntime: %s/%s\n",
		version.Version, version.Commit, version.BuildDate, platformName(), platformArch())
}

func printHelp() {
	fmt.Print(`TASH — terminal control deck

  tash dashboard [--theme NAME] [--edit FILE]
      Open the dashboard. On a fresh install this launches setup.
  tash setup
      Open setup if needed (same as dashboard).

  tash join [--url URL]
      Pair this device with an existing Tashnet main device.
      Prompts for "Secret Key:" and waits for main-device approval.

  tash edit [file]
      Open the built-in vim-style editor standalone.

  tash secret show [--reveal]
  tash secret regen
      Show or regenerate the Tashnet secret key. Regeneration warns
      extremely and disconnects every existing device.

  tash main transfer
  tash main claim [-url URL] <code>
  tash main info
      Issue a 10-minute main-role transfer code / claim it on a new device.

  tash nas serve
      Run the authenticated WebDAV NAS headlessly.

  tash docker use cli|socket <host>|off
  tash docker status
      Choose Docker CLI mode or direct socket/API mode.

  tash pihole setup
  tash pihole test
  tash pihole block <blocklist-url|domain|regex:pattern>
  tash pihole gravity
  tash pihole blocking on|off
      Configure and control Pi-hole v6 or v5.

  tash theme list|set NAME|new NAME [--from BASE]|dir
  tash ai status|prompt <text>|launch [dir]
      Inspect or drive the AI integration.
  tash update [check|install]
      Check for updates and optionally install the latest version.
  tash doctor
  tash version

Dashboard keys: 1-9 tabs, tab next, r refresh, ? about, q quit.
Settings (9) remaps keys, hides tabs, and configures AI + NAS.
Tashnet: joins require approval on the main device; secret regeneration
disconnects ALL existing devices. Use Tailscale for remote access.
`)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	if s == "" {
		return "done"
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
