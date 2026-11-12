package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tash/tash/internal/config"
)

func TestSetupViewRenders(t *testing.T) {
	t.Setenv("TASH_HOME", t.TempDir())
	m, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.screen != ScreenSetup {
		t.Fatalf("expected setup screen, got %v", m.screen)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	view := m.View()
	if !strings.Contains(view, "WELCOME, OPERATOR") {
		t.Fatalf("setup view missing welcome copy:\n%s", view)
	}
	if !strings.Contains(view, "MAIN DEVICE") || !strings.Contains(view, "JOIN TASHNET") {
		t.Fatalf("setup view missing role choices:\n%s", view)
	}
}

func TestClientDashboardTabsRender(t *testing.T) {
	t.Setenv("TASH_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Tashnet.Role = "client"
	cfg.Tashnet.MainURL = "http://127.0.0.1:8787"
	cfg.Tashnet.MainDeviceName = "main"
	cfg.Tashnet.Token = "test-token"
	cfg.NAS.AutoStart = false
	cfg.Docker.Mode = "off"
	cfg.PiHole.Enabled = false
	cfg.Tailscale.PingEnabled = false
	if _, err := config.Create(cfg, ""); err != nil {
		t.Fatalf("config.Create: %v", err)
	}
	m, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.screen = ScreenDashboard
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 36})

	want := map[Tab]string{
		TabOverview:  "NODE IDENTITY",
		TabTailscale: "TAILSCALE // TAILNET LATENCY",
		TabPiHole:    "PI-HOLE // DNS FIREWALL",
		TabDocker:    "DOCKER // ENGINE CONTROL",
		TabTashnet:   "TASHNET // CLIENT UPLINK",
		TabNAS:       "NAS // WEBDAV VAULT",
		TabEditor:    "",
		TabClaude:    "CLAUDE // CODE INTEGRATION",
		TabSettings:  "SETTINGS // CONTROL PLANE",
	}
	for tab, marker := range want {
		m.setTab(tab)
		view := m.View()
		if marker != "" && !strings.Contains(view, marker) {
			t.Errorf("tab %d missing %q:\n%s", tab, marker, view)
		}
		if strings.TrimSpace(view) == "" {
			t.Errorf("tab %d rendered empty", tab)
		}
	}
}

func TestSetupDockerOptions(t *testing.T) {
	t.Setenv("TASH_HOME", t.TempDir())
	m, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.setup.startMain()
	m.setup.stage = stageDocker
	view := m.View()
	for _, marker := range []string{"CLI mode", "Socket / API mode", "Skip Docker", "root-equivalent"} {
		if !strings.Contains(view, marker) {
			t.Errorf("docker setup missing %q:\n%s", marker, view)
		}
	}
}

func TestNarrowTerminalRenders(t *testing.T) {
	t.Setenv("TASH_HOME", t.TempDir())
	m, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Update(tea.WindowSizeMsg{Width: 52, Height: 18})
	if view := m.View(); !strings.Contains(view, "WELCOME") {
		t.Fatalf("narrow setup view missing welcome copy:\n%s", view)
	}

	cfg := config.Default()
	cfg.Tashnet.Role = "client"
	cfg.Tashnet.MainURL = "http://127.0.0.1:8787"
	cfg.Tashnet.MainDeviceName = "main"
	cfg.Tashnet.Token = "test-token"
	cfg.NAS.AutoStart = false
	cfg.Docker.Mode = "off"
	cfg.PiHole.Enabled = false
	cfg.Tailscale.PingEnabled = false
	store, err := config.Create(cfg, "")
	if err != nil {
		t.Fatalf("config.Create: %v", err)
	}
	m.store = store
	m.cfg = store.Snapshot()
	m.screen = ScreenDashboard
	m.Update(tea.WindowSizeMsg{Width: 52, Height: 18})

	for tab := TabOverview; tab <= TabSettings; tab++ {
		m.setTab(tab)
		view := m.View()
		if strings.TrimSpace(view) == "" {
			t.Errorf("narrow tab %d rendered empty", tab)
		}
	}
}

func TestMainSetupFlowCreatesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TASH_HOME", home)

	m, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.screen != ScreenSetup {
		t.Fatalf("expected setup screen, got %v", m.screen)
	}
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 40})

	press := func(msg tea.KeyMsg) tea.Cmd {
		_, cmd := m.Update(msg)
		return cmd
	}
	runes := func(s string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	press(runes("1"))
	if m.setup.stage != stageMainSecret {
		t.Fatalf("after main choice stage = %v", m.setup.stage)
	}
	if !strings.HasPrefix(m.setup.secret, "TASH-") {
		t.Fatalf("setup secret not generated: %q", m.setup.secret)
	}
	press(enter)
	if m.setup.stage != stageDocker {
		t.Fatalf("after secret stage = %v", m.setup.stage)
	}
	press(runes("3"))
	press(enter)
	if m.setup.stage != stageTheme {
		t.Fatalf("after docker stage = %v", m.setup.stage)
	}
	press(enter)
	if m.setup.stage != stagePiHole {
		t.Fatalf("after theme stage = %v", m.setup.stage)
	}
	press(runes("n"))
	if m.setup.stage != stageNAS {
		t.Fatalf("after pihole stage = %v", m.setup.stage)
	}
	press(runes("n"))
	if m.setup.stage != stageFinish {
		t.Fatalf("after nas stage = %v", m.setup.stage)
	}

	cmd := press(enter)
	if cmd == nil {
		t.Fatal("finishSetup returned no command")
	}
	msg, ok := cmd().(setupDoneMsg)
	if !ok {
		t.Fatalf("finishSetup produced %T", cmd())
	}
	if msg.store == nil || msg.store.Secret() == "" {
		t.Fatal("setup did not persist the secret")
	}
	m.Update(msg)
	if m.screen != ScreenDashboard {
		t.Fatalf("after setup screen = %v", m.screen)
	}
	view := m.View()
	if !strings.Contains(view, "NODE IDENTITY") {
		t.Fatalf("dashboard missing overview:\n%s", view)
	}

	store, err := config.Open()
	if err != nil {
		t.Fatalf("Open after setup: %v", err)
	}
	cfg := store.Snapshot()
	if cfg.Tashnet.Role != "main" || cfg.Tashnet.MainDeviceID == "" {
		t.Fatalf("unexpected tashnet config: %+v", cfg.Tashnet)
	}
	if cfg.Docker.Mode != "off" {
		t.Fatalf("docker mode = %q, want off", cfg.Docker.Mode)
	}
	if cfg.PiHole.Enabled {
		t.Fatal("Pi-hole should be disabled")
	}
	if cfg.NAS.AutoStart {
		t.Fatal("NAS auto-start should be off")
	}
	if store.Secret() != m.setup.secret {
		t.Fatal("stored secret does not match the generated setup secret")
	}
	if _, err := os.Stat(filepath.Join(home, "secret.key")); err != nil {
		t.Fatalf("secret.key: %v", err)
	}

	if m.tnet != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m.tnet.Stop(ctx)
		cancel()
	}
}
