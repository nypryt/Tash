// Package app wires Tash's dashboard, setup wizard, and live services together.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	cc "github.com/tash/tash/internal/claude"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/docker"
	vim "github.com/tash/tash/internal/editor"
	"github.com/tash/tash/internal/nas"
	"github.com/tash/tash/internal/pihole"
	"github.com/tash/tash/internal/plugin"
	"github.com/tash/tash/internal/sysinfo"
	"github.com/tash/tash/internal/tailscale"
	"github.com/tash/tash/internal/tashnet"
	"github.com/tash/tash/internal/theme"
	"github.com/tash/tash/internal/ui"
	"github.com/tash/tash/internal/version"
)

// Screen is the top-level TUI state.
type Screen int

const (
	ScreenSplash Screen = iota
	ScreenSetup
	ScreenDashboard
)

// Tab is a dashboard page.
type Tab int

const (
	TabOverview Tab = iota
	TabTailscale
	TabPiHole
	TabDocker
	TabTashnet
	TabNAS
	TabEditor
	TabAI
	TabSettings
)

// tabDef ties a stable config ID to a Tab and its display name.
type tabDef struct {
	ID   string
	Tab  Tab
	Name string
}

var tabDefs = []tabDef{
	{"deck", TabOverview, "DECK"},
	{"tailscale", TabTailscale, "TAILSCALE"},
	{"pihole", TabPiHole, "PI-HOLE"},
	{"docker", TabDocker, "DOCKER"},
	{"tashnet", TabTashnet, "TASHNET"},
	{"nas", TabNAS, "NAS"},
	{"editor", TabEditor, "EDITOR"},
	{"ai", TabAI, "AI"},
	{"settings", TabSettings, "SETTINGS"},
}

func tabDefFor(t Tab) tabDef {
	for _, d := range tabDefs {
		if d.Tab == t {
			return d
		}
	}
	return tabDefs[0]
}

func tabDefByID(id string) (tabDef, bool) {
	for _, d := range tabDefs {
		if d.ID == id {
			return d, true
		}
	}
	return tabDef{}, false
}

func tabID(t Tab) string { return tabDefFor(t).ID }

// aiTurn is one message in the in-app AI conversation.
type aiTurn struct {
	Role string
	Text string
	Err  bool
}

// settingsMode selects the Settings sub-view.
type settingsMode int

const (
	settingsMenu settingsMode = iota
	settingsThemes
	settingsTabs
)

// Options tune startup.
type Options struct {
	Theme      string
	EditorFile string
	Setup      bool
}

// Model is the whole Tash application.
type Model struct {
	width, height int
	now           time.Time
	spinner       int

	screen      Screen
	tab         Tab
	splashUntil time.Time

	store    *config.Store
	cfg      config.Config
	registry *theme.Registry
	styles   ui.Styles
	info     sysinfo.Info
	editor   *vim.Model

	setup setupState

	ts tailscale.Status
	ph pihole.Snapshot
	dk docker.Snapshot

	piholeClient *pihole.Client
	dockerClient docker.Client
	tnet         *tashnet.Server
	nas          *nas.Server

	pings    map[string][]float64
	lastPing map[string]tailscale.PingResult
	peerIdx  int
	contIdx  int
	themeIdx int
	devIdx   int
	pendIdx  int
	showPass bool

	ai        cc.Status
	aiChat    []aiTurn
	aiInput   string
	aiCursor  int
	aiScroll  int
	aiBusy    bool
	aiSession string
	aiChatErr string
	aiGen     int
	aiFocused bool
	settings      settingsMode
	settingsIdx   int
	tabCfgIdx     int
	nasPath       string
	nasIdx        int

	discovery []tashnet.Discovered
	netStatus string

	modal    modal
	toast    string
	toastAt  time.Time
	toastBad bool

	startErr string
	quitting bool

	pluginMgr *plugin.Manager
	widgets   []plugin.WidgetSpec
}

// New builds the application, loading configuration when it exists.
func New(opts Options) (*Model, error) {
	m := &Model{
		width: 100, height: 34, now: time.Now(),
		screen: ScreenDashboard, tab: TabOverview,
		pings: map[string][]float64{}, lastPing: map[string]tailscale.PingResult{},
		themeIdx: 0, peerIdx: 0,
	}
	m.info = sysinfo.Collect()
	m.registry = theme.NewRegistry(config.Dir())
	m.editor = vim.New(vim.PaletteFromTheme(theme.Default()))
	m.setup = newSetup()

	store, err := config.Open()
	switch {
	case errors.Is(err, config.ErrNotSetup):
		m.screen = ScreenSetup
		m.cfg = config.Default()
		m.setup.begin(m, opts)
		return m, nil
	case err != nil:
		return nil, err
	}
	m.store = store
	m.cfg = store.Snapshot()

	name := m.cfg.UI.Theme
	if opts.Theme != "" {
		name = opts.Theme
	}
	m.applyTheme(name, false)

	if opts.EditorFile != "" {
		m.tab = TabEditor
		if err := m.editor.Load(opts.EditorFile); err != nil {
			m.setToast("open: "+err.Error(), true)
		}
	} else if m.cfg.LastEditorFile != "" && m.tab == TabEditor {
		_ = m.editor.Load(m.cfg.LastEditorFile)
	}

	m.startServices()
	if opts.EditorFile == "" {
		m.screen = ScreenSplash
		m.splashUntil = time.Now().Add(1100 * time.Millisecond)
	}
	return m, nil
}

func (m *Model) startServices() {
	if m.store == nil {
		return
	}
	m.cfg = m.store.Snapshot()
	m.applyTheme(m.cfg.UI.Theme, false)
	m.editor.SetPalette(vim.PaletteFromTheme(m.styles.Theme))

	if m.cfg.PiHole.Enabled && strings.TrimSpace(m.cfg.PiHole.URL) != "" {
		if c, err := pihole.New(m.cfg.PiHole); err != nil {
			m.setToast("Pi-hole: "+err.Error(), true)
		} else {
			m.piholeClient = c
		}
	}
	if m.cfg.Docker.Mode != "" && m.cfg.Docker.Mode != "off" {
		if c, err := docker.New(m.cfg.Docker); err != nil {
			m.setToast("Docker: "+err.Error(), true)
		} else {
			m.dockerClient = c
		}
	}
	if m.cfg.Tashnet.Role == "main" && m.tnet == nil {
		srv := tashnet.NewServer(m.store, version.Version)
		if err := srv.Start(); err != nil {
			m.startErr = "Tashnet control: " + err.Error()
		} else {
			m.tnet = srv
			m.tnet.AddEvent("server", "main device online at "+srv.URL())
		}
	}
	if m.cfg.NAS.AutoStart && m.nas == nil {
		srv := nas.New(m.cfg.NAS)
		if err := srv.Start(); err != nil {
			m.startErr = "NAS: " + err.Error()
		} else {
			m.nas = srv
		}
	}
	if m.cfg.LastEditorFile != "" && m.editor.Path() == "" {
		_ = m.editor.Load(m.cfg.LastEditorFile)
	}

	// Initialize plugin manager
	m.pluginMgr = plugin.NewManager("")
	api := &plugin.PluginAPI{
		GetConfig: func(key string) (string, error) {
			// TODO: implement config access
			return "", nil
		},
		SetConfig: func(key, value string) error {
			if m.store == nil {
				return fmt.Errorf("no store")
			}
			return m.store.Update(func(c *config.Config) error {
				// Simple key-value store in config
				return nil
			})
		},
		ShowToast: m.setToast,
		ShowModal: func(title, body string) {
			m.modal = infoModal(title, body)
		},
		AddTab: func(id, name string, kind plugin.PluginKind) error {
			// TODO: implement dynamic tab addition
			return nil
		},
		RemoveTab: func(id string) error {
			return nil
		},
		AddWidget: func(widget plugin.WidgetSpec) error {
			// TODO: implement widget addition
			return nil
		},
		RemoveWidget: func(id string) error {
			return nil
		},
		GetDevices: func() []config.Device {
			devices := make([]config.Device, 0, len(m.cfg.Tashnet.Devices))
			for _, d := range m.cfg.Tashnet.Devices {
				devices = append(devices, d)
			}
			return devices
		},
		GetPeers: func() []string {
			peers := make([]string, 0, len(m.ts.Peers))
			for _, p := range m.ts.Peers {
				peers = append(peers, p.IP)
			}
			return peers
		},
		SendCommand: func(deviceID, command string) (string, error) {
			// TODO: implement remote command execution
			return "", fmt.Errorf("not implemented")
		},
		ReadFile: func(path string) ([]byte, error) {
			return os.ReadFile(path)
		},
		WriteFile: func(path string, data []byte) error {
			return os.WriteFile(path, data, 0o644)
		},
		ListFiles: func(path string) ([]string, error) {
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return names, nil
		},
		ListContainers: func() ([]plugin.ContainerInfo, error) {
			if m.dockerClient == nil {
				return nil, fmt.Errorf("docker not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snap := m.dockerClient.Fetch(ctx)
			containers := make([]plugin.ContainerInfo, len(snap.Containers))
			for i, c := range snap.Containers {
				containers[i] = plugin.ContainerInfo{
					ID:      c.ID,
					Name:    c.Name,
					Image:   c.Image,
					State:   c.State,
					Status:  c.Status,
					Ports:   c.Ports,
					Running: c.Running,
				}
			}
			return containers, nil
		},
		ContainerAction: func(id, action string) error {
			if m.dockerClient == nil {
				return fmt.Errorf("docker not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return m.dockerClient.Action(ctx, id, action)
		},
		GetPiHoleStats: func() (map[string]interface{}, error) {
			if m.piholeClient == nil {
				return nil, fmt.Errorf("pihole not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snap := m.piholeClient.Fetch(ctx)
			return map[string]interface{}{
				"queries":        snap.Summary.TotalQueries,
				"blocked":        snap.Summary.Blocked,
				"percent_blocked": snap.Summary.PercentBlocked,
				"gravity_domains": snap.Summary.GravityDomains,
				"active_clients": snap.Summary.ActiveClients,
				"blocking":       snap.Blocking,
			}, nil
		},
		PiHoleAction: func(action string) error {
			if m.piholeClient == nil {
				return fmt.Errorf("pihole not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			switch action {
			case "enable":
				return m.piholeClient.SetBlocking(ctx, true)
			case "disable":
				return m.piholeClient.SetBlocking(ctx, false)
			case "gravity":
				_, err := m.piholeClient.RunGravity(ctx)
				return err
			}
			return fmt.Errorf("unknown action: %s", action)
		},
		OnEvent: func(event string, handler func(json.RawMessage)) {
			m.pluginMgr.Subscribe(event, handler)
		},
		EmitEvent: func(event string, data interface{}) error {
			_ = data
			m.pluginMgr.Subscribe(event, func(d json.RawMessage) {
				// This is a hack - in reality we'd have a proper event bus
			})
			return nil
		},
		Log: func(level, message string) {
			m.setToast(fmt.Sprintf("[%s] %s", level, message), level == "error")
		},
	}
	m.pluginMgr.SetAPI(api)
	if err := m.pluginMgr.LoadAll(); err != nil {
		m.setToast("plugins: "+err.Error(), true)
	}
}

func (m *Model) applyTheme(name string, save bool) {
	t := m.registry.Get(name)
	m.styles = ui.New(t)
	if m.editor != nil {
		m.editor.SetPalette(vim.PaletteFromTheme(t))
	}
	if save && m.store != nil {
		if err := m.store.Update(func(c *config.Config) error {
			c.UI.Theme = t.Name
			return nil
		}); err != nil {
			m.setToast("theme: "+err.Error(), true)
			return
		}
		m.cfg = m.store.Snapshot()
	}
}

func (m *Model) setToast(text string, bad bool) {
	m.toast = text
	m.toastAt = time.Now()
	m.toastBad = bad
}

func (m *Model) toastActive() bool {
	return m.toast != "" && time.Since(m.toastAt) < 9*time.Second
}

// Init starts clocks, polling, and optional startup work.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) }),
		tea.Tick(1200*time.Millisecond, func(t time.Time) tea.Msg { return refreshTickMsg{} }),
		tea.Tick(8*time.Second, func(t time.Time) tea.Msg { return pingTickMsg{} }),
	}
	if m.screen == ScreenDashboard {
		if cmd := m.refreshCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.screen == ScreenSetup {
		if cmd := m.setup.enterCmd(m); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// Update handles every Bubble Tea message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case clockMsg:
		m.now = time.Time(msg)
		m.spinner++
		if m.screen == ScreenSplash && time.Now().After(m.splashUntil) {
			m.screen = ScreenDashboard
		}
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) })

	case refreshTickMsg:
		if cmd := m.onRefreshTick(); cmd != nil {
			return m, cmd
		}
		return m, nil

	case pingTickMsg:
		var cmd tea.Cmd
		if m.screen == ScreenDashboard && m.cfg.Tailscale.PingEnabled {
			cmd = m.pingCmd()
		}
		return m, tea.Batch(cmd, tea.Tick(time.Duration(maxInt(m.cfg.Tailscale.IntervalSec, 4))*time.Second,
			func(time.Time) tea.Msg { return pingTickMsg{} }))

	case setupDiscoverMsg:
		if m.screen == ScreenSetup && m.setup.stage == stageJoinURL {
			m.discovery = msg.found
			if m.setup.input == "" && len(msg.found) > 0 {
				m.setup.input = msg.found[0].URL
			}
		}
		return m, nil

	case setupJoinMsg:
		m.setup.busy = false
		if msg.err != nil {
			m.setup.err = msg.err.Error()
			return m, nil
		}
		m.setup.pendingID = msg.resp.PendingID
		m.setup.pollToken = msg.resp.PollToken
		m.setup.mainURL = msg.resp.MainURL
		m.setup.mainDeviceID = msg.resp.MainDeviceID
		m.setup.mainDeviceName = msg.resp.MainDeviceName
		m.setup.secretVersion = msg.resp.SecretVersion
		m.setup.stage = stageJoinApproval
		m.setup.err = ""
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return setupApprovalTickMsg{} })

	case setupApprovalTickMsg:
		if m.screen == ScreenSetup && m.setup.stage == stageJoinApproval && !m.setup.busy {
			m.setup.busy = true
			return m, m.pollJoinCmd()
		}
		if m.screen == ScreenSetup && m.setup.stage == stageJoinApproval {
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return setupApprovalTickMsg{} })
		}
		return m, nil

	case setupPollMsg:
		m.setup.busy = false
		if msg.err != nil {
			m.setup.err = msg.err.Error()
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return setupApprovalTickMsg{} })
		}
		switch msg.resp.Status {
		case "approved":
			m.setup.token = msg.resp.Token
			m.setup.mainURL = msg.resp.MainURL
			m.setup.mainDeviceName = msg.resp.MainDeviceName
			m.setup.secretVersion = msg.resp.SecretVersion
			m.setup.joinApproved = true
			m.setup.stage = stageDocker
			m.setup.err = ""
			m.setToast("Approved by "+msg.resp.MainDeviceName+"; finish setup", false)
		case "denied":
			m.setup.err = msg.resp.Message
			if m.setup.err == "" {
				m.setup.err = "the main device denied this request"
			}
			m.setup.stage = stageJoinURL
			m.setup.pendingID = ""
			m.setup.pollToken = ""
		default:
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return setupApprovalTickMsg{} })
		}
		return m, nil

	case setupDoneMsg:
		m.store = msg.store
		m.cfg = m.store.Snapshot()
		m.screen = ScreenDashboard
		m.tab = TabOverview
		m.startServices()
		m.setToast(msg.message, false)
		return m, m.refreshCmd()

	case tailscaleMsg:
		m.ts = msg.status
		return m, nil
	case piholeMsg:
		m.ph = msg.snapshot
		return m, nil
	case dockerMsg:
		m.dk = msg.snapshot
		return m, nil
	case pingMsg:
		for _, r := range msg.results {
			key := peerKey(r.Peer)
			m.lastPing[key] = r
			if r.Err == "" {
				ms := float64(r.Latency.Microseconds()) / 1000.0
				list := m.pings[key]
				list = append(list, ms)
				if len(list) > 40 {
					list = list[len(list)-40:]
				}
				m.pings[key] = list
			}
		}
		return m, nil

	case heartbeatMsg:
		m.netStatus = ""
		if msg.err != nil {
			if errors.Is(msg.err, tashnet.ErrRevoked) {
				m.netStatus = "REVOKED: this device's token was invalidated (secret regenerated or device removed). Run `tash join` to pair again."
			} else {
				m.netStatus = msg.err.Error()
			}
			return m, nil
		}
		m.netStatus = ""
		if m.store != nil {
			_ = m.store.Update(func(c *config.Config) error {
				c.Tashnet.MainDeviceName = msg.resp.MainDeviceName
				c.Tashnet.SecretVersion = msg.resp.SecretVersion
				return nil
			})
			m.cfg = m.store.Snapshot()
		}
		return m, nil

	case actionMsg:
		m.modal = modal{}
		if msg.err != nil {
			m.setToast(msg.text+" failed: "+msg.err.Error(), true)
		} else {
			m.setToast(msg.text+" ok", false)
		}
		return m, m.refreshCmd()

	case toastMsg:
		m.setToast(msg.text, msg.err)
		return m, nil

	case dockerLogsMsg:
		if msg.err != nil {
			m.setToast("logs failed: "+msg.err.Error(), true)
			return m, nil
		}
		text := strings.Join(wrapMultiline(msg.text, maxInt(m.contentWidth()-8, 20)), "\n")
		m.modal = infoModal("CONTAINER LOGS // "+msg.name, text)
		return m, nil

	case discoverMsg:
		m.discovery = msg.found
		if m.setup.stage == stageJoinURL && m.setup.input == "" && len(msg.found) > 0 {
			m.setup.input = msg.found[0].URL
		}
		return m, nil

	case claudeStatusMsg:
		m.ai = msg.status
		return m, nil

	case claudeChatMsg:
		if msg.gen != m.aiGen {
			return m, nil
		}
		m.aiBusy = false
		if msg.err != nil {
			m.aiChat = append(m.aiChat, aiTurn{Role: "error", Text: msg.err.Error(), Err: true})
			m.aiChatErr = msg.err.Error()
		} else {
			if msg.sessionID != "" {
				m.aiSession = msg.sessionID
			}
			m.aiChat = append(m.aiChat, aiTurn{Role: "assistant", Text: msg.text})
			m.aiChatErr = ""
		}
		m.aiScroll = 0
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.screen == ScreenDashboard && m.tab == TabEditor {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) layout() {
	bodyH := m.bodyHeight()
	m.editor.SetSize(maxInt(m.width, 20), maxInt(bodyH, 6))
}

func (m *Model) bodyHeight() int {
	h := m.height - 4
	if h < 6 {
		h = 6
	}
	return h
}

func (m *Model) contentWidth() int {
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	return w
}

// View renders the active screen.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	switch m.screen {
	case ScreenSplash:
		return m.viewSplash()
	case ScreenSetup:
		return m.viewSetup()
	default:
		return m.viewDashboard()
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.screen == ScreenSplash {
		m.screen = ScreenDashboard
		return m, nil
	}
	if m.screen == ScreenSetup {
		return m.handleSetupKey(msg)
	}
	if !m.modal.active() && m.tab == TabEditor {
		if t, ok := m.tabNumber(key); ok {
			m.setTab(t)
			return m, nil
		}
		switch key {
		case "ctrl+o":
			m.modal = inputModal("OPEN FILE", "Path to open in the editor:", m.editor.Path(), "", "editor-open", false)
			return m, nil
		case "ctrl+n":
			m.editor = vim.New(vim.PaletteFromTheme(m.styles.Theme))
			m.editor.SetSize(maxInt(m.width, 20), maxInt(m.bodyHeight(), 6))
			return m, nil
		}
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		if m.editor.CloseRequested() {
			m.editor.ClearCloseRequest()
			m.setTab(TabOverview)
		}
		return m, cmd
	}
	if m.modal.active() {
		return m.handleModalKey(msg)
	}
	if !(m.tab == TabSettings && m.settings == settingsTabs) && m.tab != TabAI {
		if t, ok := m.tabNumber(key); ok {
			m.setTab(t)
			return m, nil
		}
	}
	if m.tab == TabAI {
		if strings.HasPrefix(key, "ctrl+") {
			if t, ok := m.tabNumber(key); ok {
				m.setTab(t)
				return m, nil
			}
		}
		return m.keyAI(msg)
	}
	switch key {
	case m.keybind("quit"):
		m.quitting = true
		return m, tea.Quit
	case m.keybind("help"):
		m.modal = infoModal("ABOUT TASH // OPERATOR MANUAL", m.aboutBody())
		return m, nil
	case "ctrl+p":
		m.modal = commandPaletteModal(m)
		return m, nil
	case m.keybind("refresh"):
		m.setToast("refreshing…", false)
		return m, m.refreshCmd()
	case m.keybind("next_tab"):
		m.cycleTab(1)
		return m, nil
	case m.keybind("prev_tab"):
		m.cycleTab(-1)
		return m, nil
	case "esc":
		if m.tab == TabSettings && m.settings != settingsMenu {
			m.settings = settingsMenu
			return m, nil
		}
		return m, nil
	}
	switch m.tab {
	case TabTailscale:
		return m.keyTailscale(msg)
	case TabPiHole:
		return m.keyPiHole(msg)
	case TabDocker:
		return m.keyDocker(msg)
	case TabTashnet:
		return m.keyTashnet(msg)
	case TabNAS:
		return m.keyNAS(msg)
	case TabAI:
		return m.keyAI(msg)
	case TabSettings:
		return m.keySettings(msg)
	case TabOverview:
		return m.keyOverview(msg)
	}
	return m, nil
}

// peerKey identifies a peer for ping history. IP is unique even when two
// machines share a hostname.
func peerKey(p tailscale.Peer) string {
	if p.IP != "" {
		return p.IP
	}
	return p.Name
}

// keybind returns the configured binding for a global action.
func (m *Model) keybind(action string) string {
	if v := strings.TrimSpace(m.cfg.UI.Keybinds[action]); v != "" {
		return v
	}
	return config.DefaultKeybinds()[action]
}

func (m *Model) hiddenTabs() map[string]bool {
	hidden := make(map[string]bool, len(m.cfg.UI.HiddenTabs))
	for _, id := range m.cfg.UI.HiddenTabs {
		hidden[id] = true
	}
	return hidden
}

func (m *Model) tabVisible(t Tab) bool {
	id := tabID(t)
	if id == "settings" {
		return true
	}
	return !m.hiddenTabs()[id]
}

// tabOrderDefs returns every tab in the configured deck order.
func (m *Model) tabOrderDefs() []tabDef {
	out := make([]tabDef, 0, len(tabDefs))
	for _, id := range m.cfg.UI.TabOrder {
		if d, ok := tabDefByID(id); ok {
			out = append(out, d)
		}
	}
	if len(out) != len(tabDefs) {
		seen := make(map[string]bool, len(out))
		for _, d := range out {
			seen[d.ID] = true
		}
		for _, d := range tabDefs {
			if !seen[d.ID] {
				out = append(out, d)
			}
		}
	}
	return out
}

func (m *Model) visibleTabDefs() []tabDef {
	all := m.tabOrderDefs()
	out := make([]tabDef, 0, len(all))
	for _, d := range all {
		if m.tabVisible(d.Tab) {
			out = append(out, d)
		}
	}
	return out
}

func (m *Model) visibleTabSpecs() []ui.TabSpec {
	defs := m.visibleTabDefs()
	out := make([]ui.TabSpec, 0, len(defs))
	for _, d := range defs {
		key := m.cfg.UI.TabNumbers[d.ID]
		if key == "" {
			key = "·"
		}
		out = append(out, ui.TabSpec{Key: key, Name: d.Name})
	}
	return out
}

// moveTab swaps a tab with its neighbor in the deck order and persists it.
func (m *Model) moveTab(index, delta int) {
	order := append([]string(nil), m.cfg.UI.TabOrder...)
	if index < 0 || index >= len(order) {
		return
	}
	next := index + delta
	if next < 0 || next >= len(order) {
		return
	}
	order[index], order[next] = order[next], order[index]
	if m.store == nil {
		m.cfg.UI.TabOrder = order
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		c.UI.TabOrder = order
		return nil
	}); err != nil {
		m.setToast("layout: "+err.Error(), true)
		return
	}
	m.cfg = m.store.Snapshot()
}

// cycleDeckLayout rotates auto -> cards -> compact -> auto.
func (m *Model) cycleDeckLayout() {
	current := m.cfg.UI.DeckLayout
	next := "auto"
	switch current {
	case "auto":
		next = "cards"
	case "cards":
		next = "compact"
	}
	m.updateUI(func(c *config.UIConfig) {
		c.DeckLayout = next
	})
	m.setToast("DECK layout: "+next, false)
}

func (m *Model) cycleTab(delta int) {
	visible := m.visibleTabDefs()
	if len(visible) == 0 {
		return
	}
	idx := -1
	for i, d := range visible {
		if d.Tab == m.tab {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
	} else {
		idx = (idx + delta + len(visible)) % len(visible)
	}
	m.setTab(visible[idx].Tab)
}

func (m *Model) setTab(t Tab) {
	if m.tab == TabSettings && t != TabSettings && m.store != nil {
		// Discard an un-applied theme preview.
		m.applyTheme(m.cfg.UI.Theme, false)
	}
	m.tab = t
	if t == TabSettings {
		m.settings = settingsMenu
		m.syncThemeIndex()
	}
	if t == TabAI {
		m.aiScroll = 0
	}
	m.layout()
}

func (m *Model) tabNumber(key string) (Tab, bool) {
	k := key
	if strings.HasPrefix(k, "ctrl+") {
		k = strings.TrimPrefix(k, "ctrl+")
	}
	if len(k) != 1 || k[0] < '1' || k[0] > '9' {
		return 0, false
	}
	for _, d := range tabDefs {
		if m.tabVisible(d.Tab) && m.cfg.UI.TabNumbers[d.ID] == k {
			return d.Tab, true
		}
	}
	return 0, false
}

func (m *Model) onRefreshTick() tea.Cmd {
	if m.store != nil {
		m.cfg = m.store.Snapshot()
	}
	var cmds []tea.Cmd
	if m.screen == ScreenDashboard {
		if cmd := m.refreshCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	cmds = append(cmds, tea.Tick(5*time.Second, func(time.Time) tea.Msg { return refreshTickMsg{} }))
	return tea.Batch(cmds...)
}

func (m *Model) refreshCmd() tea.Cmd {
	if m.screen != ScreenDashboard || m.store == nil {
		return nil
	}
	var cmds []tea.Cmd
	cmds = append(cmds, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return tailscaleMsg{status: tailscale.ReadStatus(ctx)}
	})
	if m.piholeClient != nil && m.cfg.PiHole.Enabled {
		client := m.piholeClient
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return piholeMsg{snapshot: client.Fetch(ctx)}
		})
	}
	if m.dockerClient != nil {
		client := m.dockerClient
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return dockerMsg{snapshot: client.Fetch(ctx)}
		})
	}
	if m.cfg.Tashnet.Role == "client" && m.cfg.Tashnet.MainURL != "" && m.cfg.Tashnet.Token != "" {
		cfg := m.cfg
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			c, err := tashnet.NewClient(cfg.Tashnet.MainURL, cfg.Tashnet.Token, version.Version)
			if err != nil {
				return heartbeatMsg{err: err}
			}
			resp, err := c.Heartbeat(ctx)
			return heartbeatMsg{resp: resp, err: err}
		})
	}
	if m.tab == TabAI && (m.ai.FetchedAt.IsZero() || time.Since(m.ai.FetchedAt) > 30*time.Second) {
		cmds = append(cmds, m.aiDetectCmd())
	}
	return tea.Batch(cmds...)
}

func (m *Model) pingCmd() tea.Cmd {
	peers := m.ts.OnlinePeers()
	if len(peers) == 0 {
		return nil
	}
	max := m.cfg.Tailscale.MaxPeers
	if max <= 0 || max > len(peers) {
		max = len(peers)
	}
	try := append([]tailscale.Peer(nil), peers[:max]...)
	return func() tea.Msg {
		var results []tailscale.PingResult
		for _, p := range try {
			ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
			results = append(results, tailscale.Ping(ctx, p, 2*time.Second))
			cancel()
		}
		return pingMsg{results: results}
	}
}

func (m *Model) pollJoinCmd() tea.Cmd {
	url := m.setup.mainURL
	pending := m.setup.pendingID
	token := m.setup.pollToken
	deviceID := m.setup.deviceID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		c, err := tashnet.NewClient(url, "", version.Version)
		if err != nil {
			return setupPollMsg{err: err}
		}
		resp, err := c.Poll(ctx, pending, token, deviceID)
		return setupPollMsg{resp: resp, err: err}
	}
}

func (m *Model) joinCmd(rawURL string) tea.Cmd {
	url := rawURL
	secret := m.setup.secret
	deviceID := m.setup.deviceID
	deviceName := m.setup.deviceName
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c, err := tashnet.NewClient(url, "", version.Version)
		if err != nil {
			return setupJoinMsg{err: err}
		}
		resp, err := c.Join(ctx, secret, deviceID, deviceName)
		if err != nil {
			return setupJoinMsg{err: err}
		}
		if resp.Status == "approved" {
			return setupPollMsg{resp: tashnet.PollResponse{
				Status: "approved", Token: resp.Token, MainURL: resp.MainURL,
				MainDeviceName: resp.MainDeviceName, SecretVersion: resp.SecretVersion,
			}}
		}
		return setupJoinMsg{resp: resp}
	}
}

func (m *Model) discoverCmd() tea.Cmd {
	port := m.cfg.Tashnet.ControlPort
	if port == 0 {
		port = 8787
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
		defer cancel()
		return discoverMsg{found: tashnet.Discover(ctx, port, 1200*time.Millisecond)}
	}
}

func (m *Model) aiDetectCmd() tea.Cmd {
	cfg := m.cfg.AI
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return claudeStatusMsg{status: cc.Detect(ctx, cfg)}
	}
}

func (m *Model) checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/nypryt/Tash/releases/latest", nil)
		if err != nil {
			return toastMsg{text: "update check failed: " + err.Error(), err: true}
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return toastMsg{text: "update check failed: " + err.Error(), err: true}
		}
		defer resp.Body.Close()
		
		if resp.StatusCode != http.StatusOK {
			return toastMsg{text: fmt.Sprintf("update check failed: GitHub API returned %s", resp.Status), err: true}
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
			return toastMsg{text: "update check failed: " + err.Error(), err: true}
		}
		
		latest := strings.TrimPrefix(release.TagName, "v")
		current := strings.TrimPrefix(version.Version, "v")
		
		if latest == current {
			return toastMsg{text: "You are on the latest version (" + version.Version + ")", err: false}
		}
		
		return toastMsg{text: fmt.Sprintf("Update available: %s -> v%s (check release page)", version.Version, latest), err: false}
	}
}

func (m *Model) aiChatCmd(prompt, dir, sessionID string, gen int) tea.Cmd {
	cfg := m.cfg.AI
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute+15*time.Second)
		defer cancel()
		res, err := cc.Chat(ctx, cfg, prompt, dir, sessionID)
		return claudeChatMsg{text: res.Text, sessionID: res.SessionID, err: err, gen: gen}
	}
}

func (m *Model) aiWorkDir() string {
	if dir := strings.TrimSpace(m.cfg.AI.WorkDir); dir != "" {
		return dir
	}
	return cc.WorkDir(m.cfg.AI)
}

func (m *Model) themeNames() []string {
	names := m.registry.Names()
	sort.Strings(names)
	// Built-ins first, then custom, each alphabetical.
	sort.SliceStable(names, func(i, j int) bool {
		bi, bj := theme.IsBuiltin(names[i]), theme.IsBuiltin(names[j])
		if bi != bj {
			return bi
		}
		return names[i] < names[j]
	})
	return names
}

func (m *Model) currentThemeName() string {
	if m.store == nil {
		return m.setup.themeName
	}
	return m.cfg.UI.Theme
}

func (m *Model) syncThemeIndex() {
	names := m.themeNames()
	current := m.currentThemeName()
	for i, n := range names {
		if strings.EqualFold(n, current) {
			m.themeIdx = i
			return
		}
	}
	m.themeIdx = 0
}

// Messages -----------------------------------------------------------------

type clockMsg time.Time
type refreshTickMsg struct{}
type pingTickMsg struct{}
type setupDiscoverMsg struct{ found []tashnet.Discovered }
type discoverMsg struct{ found []tashnet.Discovered }
type setupJoinMsg struct {
	resp tashnet.JoinResponse
	err  error
}
type setupPollMsg struct {
	resp tashnet.PollResponse
	err  error
}
type setupApprovalTickMsg struct{}
type setupDoneMsg struct {
	store   *config.Store
	message string
}
type tailscaleMsg struct{ status tailscale.Status }
type piholeMsg struct{ snapshot pihole.Snapshot }
type dockerMsg struct{ snapshot docker.Snapshot }
type dockerLogsMsg struct {
	name string
	text string
	err  error
}
type pingMsg struct{ results []tailscale.PingResult }
type heartbeatMsg struct {
	resp tashnet.HeartbeatResponse
	err  error
}
type actionMsg struct {
	text string
	err  error
}

type toastMsg struct {
	text string
	err  bool
}

type claudeStatusMsg struct{ status cc.Status }
type claudeChatMsg struct {
	text      string
	sessionID string
	err       error
	gen       int
}

func notify(text string, err error) actionMsg { return actionMsg{text: text, err: err} }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
