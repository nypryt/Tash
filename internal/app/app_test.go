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
	// First screen is TOS agreement - accept it
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if cmd != nil {
		cmd()
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
		TabAI:        "AI // CHAT",
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

func TestAIChatSendsAndEdits(t *testing.T) {
	m := &Model{cfg: config.Default(), screen: ScreenDashboard, tab: TabAI, aiFocused: true}
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	space := tea.KeyMsg{Type: tea.KeySpace}
	backspace := tea.KeyMsg{Type: tea.KeyBackspace}
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	// Send individual characters
	m.keyAI(key("h"))
	m.keyAI(key("i"))
	if m.aiInput != "hi" {
		t.Fatalf("input = %q", m.aiInput)
	}
	m.keyAI(space)
	m.keyAI(key("t"))
	m.keyAI(key("h"))
	m.keyAI(key("e"))
	m.keyAI(key("r"))
	m.keyAI(key("e"))
	m.keyAI(backspace)
	if m.aiInput != "hi ther" {
		t.Fatalf("edited input = %q", m.aiInput)
	}
	_, cmd := m.keyAI(enter)
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if m.aiInput != "" || !m.aiBusy {
		t.Fatalf("after send input=%q busy=%v", m.aiInput, m.aiBusy)
	}
	if len(m.aiChat) != 1 || m.aiChat[0].Role != "you" || m.aiChat[0].Text != "hi ther" {
		t.Fatalf("chat = %+v", m.aiChat)
	}

	gen := m.aiGen
	m.aiNewChat()
	m.Update(claudeChatMsg{text: "stale", sessionID: "old", gen: gen})
	if len(m.aiChat) != 0 || m.aiSession != "" {
		t.Fatalf("stale response leaked into new chat: %+v", m.aiChat)
	}
	m.Update(claudeChatMsg{text: "fresh", sessionID: "new", gen: m.aiGen})
	if len(m.aiChat) != 1 || m.aiChat[0].Text != "fresh" || m.aiSession != "new" {
		t.Fatalf("fresh response not applied: %+v", m.aiChat)
	}
}

func TestTabOrderAndDeckLayout(t *testing.T) {
	cfg := config.Default()
	cfg.UI.TabOrder = []string{"settings", "ai", "deck", "tailscale", "pihole", "docker", "tashnet", "nas", "editor"}
	m := &Model{cfg: cfg}

	order := m.tabOrderDefs()
	if len(order) != len(tabDefs) {
		t.Fatalf("order has %d tabs, want %d", len(order), len(tabDefs))
	}
	if order[0].ID != "settings" || order[1].ID != "ai" {
		t.Fatalf("custom order not honored: %s, %s", order[0].ID, order[1].ID)
	}

	m.moveTab(0, 1)
	order = m.tabOrderDefs()
	if order[0].ID != "ai" || order[1].ID != "settings" {
		t.Fatalf("moveTab did not swap: %s, %s", order[0].ID, order[1].ID)
	}

	m.cfg.UI.HiddenTabs = []string{"ai", "nas"}
	visible := m.visibleTabDefs()
	sawSettings := false
	for _, d := range visible {
		if d.ID == "ai" || d.ID == "nas" {
			t.Fatalf("hidden tab %s is visible", d.ID)
		}
		if d.ID == "settings" {
			sawSettings = true
		}
	}
	if !sawSettings {
		t.Fatal("settings must remain visible")
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
	// Accept TOS
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if cmd != nil {
		cmd()
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

	// Accept TOS agreement
	press(runes("1"))
	if m.setup.stage != stageWelcome {
		t.Fatalf("after TOS acceptance stage = %v", m.setup.stage)
	}

	// Choose Main Device
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
