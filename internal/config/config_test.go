package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateOpenUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TASH_HOME", home)

	if _, err := Open(); !errors.Is(err, ErrNotSetup) {
		t.Fatalf("expected ErrNotSetup, got %v", err)
	}
	cfg := Default()
	cfg.Tashnet.Role = "main"
	cfg.Tashnet.Devices[cfg.Tashnet.DeviceID] = Device{
		ID: cfg.Tashnet.DeviceID, Name: cfg.Tashnet.DeviceName, Token: "token",
	}
	secret := "TASH-AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH"
	store, err := Create(cfg, secret)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if store.Secret() != secret {
		t.Fatalf("secret mismatch: %q", store.Secret())
	}
	if Dir() != home {
		t.Fatalf("Dir() = %q, want %q", Dir(), home)
	}

	opened, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Snapshot().Tashnet.Role != "main" {
		t.Fatal("role was not persisted")
	}
	if opened.Secret() != secret {
		t.Fatal("secret was not persisted")
	}

	if err := opened.Update(func(c *Config) error {
		c.UI.Theme = "nord"
		c.NAS.Port = 9999
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reopened, err := Open()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Snapshot().UI.Theme != "nord" || reopened.Snapshot().NAS.Port != 9999 {
		t.Fatal("update was not persisted")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.ConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("config mode = %o, want 600", mode)
		}
		info, err = os.Stat(filepath.Join(store.Dir(), "secret.key"))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("secret mode = %o, want 600", mode)
		}
	}
}

func TestNormalizeClamps(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if cfg.Tashnet.Role != "main" {
		t.Fatalf("default role = %q", cfg.Tashnet.Role)
	}
	if cfg.Tashnet.ControlPort != 8787 {
		t.Fatalf("default control port = %d", cfg.Tashnet.ControlPort)
	}
	if cfg.NAS.Port != 8788 {
		t.Fatalf("default NAS port = %d", cfg.NAS.Port)
	}
	if cfg.Tashnet.Devices == nil || cfg.Tashnet.Pending == nil {
		t.Fatal("maps were not initialized")
	}
	if cfg.UI.Theme == "" {
		t.Fatal("theme was not initialized")
	}
}

func TestNormalizeTabsAndKeybinds(t *testing.T) {
	cfg := Config{UI: UIConfig{
		TabNumbers: map[string]string{"deck": "1", "tailscale": "1", "pihole": "99", "bogus": "3"},
		HiddenTabs: []string{"nas", "nas", "settings", "bogus"},
	}}
	cfg.Normalize()

	used := map[string]bool{}
	for _, id := range TabIDs {
		num := cfg.UI.TabNumbers[id]
		if len(num) != 1 || num[0] < '1' || num[0] > '9' {
			t.Fatalf("tab %q has invalid number %q", id, num)
		}
		if used[num] {
			t.Fatalf("tab number %q was assigned twice", num)
		}
		used[num] = true
	}
	if len(cfg.UI.HiddenTabs) != 1 || cfg.UI.HiddenTabs[0] != "nas" {
		t.Fatalf("hidden tabs = %#v", cfg.UI.HiddenTabs)
	}
	for action, want := range DefaultKeybinds() {
		if cfg.UI.Keybinds[action] != want {
			t.Fatalf("keybind %q = %q, want %q", action, cfg.UI.Keybinds[action], want)
		}
	}
	if !cfg.Claude.Enabled || cfg.Claude.Command != "claude" || cfg.Claude.PermissionMode != "default" {
		t.Fatalf("claude defaults = %+v", cfg.Claude)
	}
	if len(cfg.UI.TabOrder) != len(TabIDs) {
		t.Fatalf("tab order = %#v", cfg.UI.TabOrder)
	}
	if cfg.UI.DeckLayout != "auto" {
		t.Fatalf("deck layout = %q", cfg.UI.DeckLayout)
	}
}

func TestNormalizeTabOrder(t *testing.T) {
	cfg := Config{UI: UIConfig{
		TabOrder:   []string{"claude", "deck", "claude", "bogus"},
		DeckLayout: "weird",
	}}
	cfg.Normalize()
	if len(cfg.UI.TabOrder) != len(TabIDs) {
		t.Fatalf("tab order = %#v", cfg.UI.TabOrder)
	}
	if cfg.UI.TabOrder[0] != "claude" || cfg.UI.TabOrder[1] != "deck" {
		t.Fatalf("custom order was not preserved: %#v", cfg.UI.TabOrder)
	}
	if cfg.UI.DeckLayout != "auto" {
		t.Fatalf("deck layout = %q", cfg.UI.DeckLayout)
	}
}

func TestCloneIsDeep(t *testing.T) {
	cfg := Default()
	clone := cfg.Clone()
	clone.UI.TabNumbers["deck"] = "9"
	clone.UI.Keybinds["refresh"] = "R"
	clone.UI.HiddenTabs = append(clone.UI.HiddenTabs, "nas")
	clone.UI.TabOrder[0] = "settings"
	clone.Tashnet.Devices["x"] = Device{ID: "x"}
	if cfg.UI.TabNumbers["deck"] == "9" || cfg.UI.Keybinds["refresh"] == "R" {
		t.Fatal("UI maps were shared with the clone")
	}
	if len(cfg.UI.HiddenTabs) != 0 {
		t.Fatal("hidden tabs slice was shared with the clone")
	}
	if cfg.UI.TabOrder[0] == "settings" {
		t.Fatal("tab order slice was shared with the clone")
	}
	if _, ok := cfg.Tashnet.Devices["x"]; ok {
		t.Fatal("device map was shared with the clone")
	}
}
