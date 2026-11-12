// Package config owns Tash's on-disk configuration and secret key.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrNotSetup is returned when no Tash configuration exists yet.
var ErrNotSetup = errors.New("tash is not set up yet")

// CurrentVersion is the configuration schema version.
const CurrentVersion = 2

// PiHoleConfig describes a Pi-hole connection. Secret is either a v6
// password/application password or a legacy v5 API token.
type PiHoleConfig struct {
	Enabled       bool   `json:"enabled"`
	URL           string `json:"url"`
	Secret        string `json:"secret"`
	APIVersion    int    `json:"api_version"` // 0 = auto, 5 or 6
	SkipTLSVerify bool   `json:"skip_tls_verify"`
	RefreshSec    int    `json:"refresh_seconds"`
}

// NASConfig describes the WebDAV file server.
type NASConfig struct {
	AutoStart bool   `json:"auto_start"`
	Root      string `json:"root"`
	User      string `json:"user"`
	Password  string `json:"password"`
	Port      int    `json:"port"`
	ReadOnly  bool   `json:"read_only"`
}

// DockerConfig selects how Tash talks to the Docker engine.
type DockerConfig struct {
	Mode string `json:"mode"` // cli, socket, off
	Host string `json:"host"`
}

// TailscaleConfig controls the peer ping loop.
type TailscaleConfig struct {
	PingEnabled bool `json:"ping_enabled"`
	IntervalSec int  `json:"interval_seconds"`
	MaxPeers    int  `json:"max_peers"`
}

// UIConfig holds cosmetic settings, tab layout, and key bindings.
type UIConfig struct {
	Theme      string            `json:"theme"`
	Splash     bool              `json:"splash"`
	Clock24    bool              `json:"clock_24h"`
	TabOrder   []string          `json:"tab_order,omitempty"`
	TabNumbers map[string]string `json:"tab_numbers,omitempty"`
	HiddenTabs []string          `json:"hidden_tabs,omitempty"`
	Keybinds   map[string]string `json:"keybinds,omitempty"`
	DeckLayout string            `json:"deck_layout,omitempty"` // auto, cards, compact
}

// ClaudeConfig controls the Claude Code integration.
type ClaudeConfig struct {
	Enabled        bool   `json:"enabled"`
	Command        string `json:"command"`
	APIKey         string `json:"api_key,omitempty"`
	Model          string `json:"model,omitempty"`
	WorkDir        string `json:"work_dir,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

// TabIDs are the stable identifiers used for tab numbering and visibility.
var TabIDs = []string{"deck", "tailscale", "pihole", "docker", "tashnet", "nas", "editor", "claude", "settings"}

// DefaultTabOrder is the factory dashboard tab order.
func DefaultTabOrder() []string { return append([]string(nil), TabIDs...) }

// DeckLayouts are the valid DECK rendering modes.
var DeckLayouts = []string{"auto", "cards", "compact"}

// DefaultTabNumbers maps every tab to its factory key.
func DefaultTabNumbers() map[string]string {
	out := make(map[string]string, len(TabIDs))
	for i, id := range TabIDs {
		out[id] = fmt.Sprintf("%d", i+1)
	}
	return out
}

// DefaultKeybinds returns the built-in global key bindings.
func DefaultKeybinds() map[string]string {
	return map[string]string{
		"refresh":  "r",
		"help":     "?",
		"quit":     "q",
		"next_tab": "tab",
		"prev_tab": "shift+tab",
	}
}

// IsTabID reports whether id is a known dashboard tab identifier.
func IsTabID(id string) bool {
	for _, known := range TabIDs {
		if id == known {
			return true
		}
	}
	return false
}

// Device is an approved Tashnet member. Tokens live only on the main device.
type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	IP         string    `json:"ip,omitempty"`
	Token      string    `json:"token,omitempty"`
	PollHash   string    `json:"poll_hash,omitempty"`
	ApprovedAt time.Time `json:"approved_at"`
	LastSeen   time.Time `json:"last_seen"`
}

// Pending is a join request awaiting approval on the main device.
type Pending struct {
	ID          string    `json:"id"`
	DeviceID    string    `json:"device_id"`
	Name        string    `json:"name"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	IP          string    `json:"ip,omitempty"`
	PollHash    string    `json:"poll_hash"`
	RequestedAt time.Time `json:"requested_at"`
}

// TashnetConfig describes this node's membership in the Tashnet.
type TashnetConfig struct {
	Name           string             `json:"name"`
	Role           string             `json:"role"` // main | client
	MainURL        string             `json:"main_url,omitempty"`
	MainDeviceID   string             `json:"main_device_id,omitempty"`
	MainDeviceName string             `json:"main_device_name,omitempty"`
	DeviceID       string             `json:"device_id"`
	DeviceName     string             `json:"device_name"`
	ControlPort    int                `json:"control_port"`
	SecretVersion  int                `json:"secret_version"`
	Devices        map[string]Device  `json:"devices,omitempty"`
	Pending        map[string]Pending `json:"pending,omitempty"`
	Token          string             `json:"token,omitempty"`
	TransferNonce  string             `json:"transfer_nonce,omitempty"`
	TransferExpiry time.Time          `json:"transfer_expiry,omitempty"`
}

// Config is the persisted Tash configuration.
type Config struct {
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	UI             UIConfig        `json:"ui"`
	Tashnet        TashnetConfig   `json:"tashnet"`
	PiHole         PiHoleConfig    `json:"pihole"`
	NAS            NASConfig       `json:"nas"`
	Docker         DockerConfig    `json:"docker"`
	Tailscale      TailscaleConfig `json:"tailscale"`
	Claude         ClaudeConfig    `json:"claude"`
	LastEditorFile string          `json:"last_editor_file,omitempty"`
}

// Default returns a sane, uninitialised configuration for this machine.
func Default() Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "tash-node"
	}
	now := time.Now().UTC()
	return Config{
		Version:   CurrentVersion,
		CreatedAt: now,
		UpdatedAt: now,
		UI: UIConfig{
			Theme: "matrix", Splash: true, Clock24: true, DeckLayout: "auto",
			TabOrder: DefaultTabOrder(), TabNumbers: DefaultTabNumbers(), Keybinds: DefaultKeybinds(),
		},
		Tashnet: TashnetConfig{
			Name:          "Tashnet",
			Role:          "main",
			DeviceID:      "dev_" + randomHex(8),
			DeviceName:    host,
			ControlPort:   8787,
			SecretVersion: 1,
			Devices:       map[string]Device{},
			Pending:       map[string]Pending{},
		},
		NAS: NASConfig{
			AutoStart: false,
			Root:      DefaultNASRoot(),
			User:      "tash",
			Password:  RandomPassword(14),
			Port:      8788,
		},
		Docker:    DockerConfig{Mode: "cli", Host: DefaultDockerHost()},
		Tailscale: TailscaleConfig{PingEnabled: true, IntervalSec: 10, MaxPeers: 5},
		PiHole:    PiHoleConfig{RefreshSec: 5},
		Claude: ClaudeConfig{
			Enabled: true, Command: "claude", PermissionMode: "default",
		},
	}
}

// Normalize fills absent fields with defaults and clamps unsafe values.
func (c *Config) Normalize() {
	def := Default()
	// Schema 2 added Claude settings; enable them for upgraded configs.
	if c.Version < 2 {
		c.Claude.Enabled = true
		c.Version = 2
	}
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(c.UI.Theme) == "" {
		c.UI.Theme = def.UI.Theme
	}
	c.normalizeUITabs()
	if c.UI.Keybinds == nil {
		c.UI.Keybinds = DefaultKeybinds()
	}
	for key, value := range DefaultKeybinds() {
		if strings.TrimSpace(c.UI.Keybinds[key]) == "" {
			c.UI.Keybinds[key] = value
		}
	}
	if c.Tashnet.Name == "" {
		c.Tashnet.Name = "Tashnet"
	}
	if c.Tashnet.Role != "main" && c.Tashnet.Role != "client" {
		c.Tashnet.Role = "main"
	}
	if c.Tashnet.DeviceID == "" {
		c.Tashnet.DeviceID = def.Tashnet.DeviceID
	}
	if c.Tashnet.DeviceName == "" {
		c.Tashnet.DeviceName = def.Tashnet.DeviceName
	}
	if c.Tashnet.ControlPort < 1024 || c.Tashnet.ControlPort > 65535 {
		c.Tashnet.ControlPort = 8787
	}
	if c.Tashnet.SecretVersion < 1 {
		c.Tashnet.SecretVersion = 1
	}
	if c.Tashnet.Devices == nil {
		c.Tashnet.Devices = map[string]Device{}
	}
	if c.Tashnet.Pending == nil {
		c.Tashnet.Pending = map[string]Pending{}
	}
	if c.NAS.Root == "" {
		c.NAS.Root = def.NAS.Root
	}
	if c.NAS.User == "" {
		c.NAS.User = def.NAS.User
	}
	if c.NAS.Password == "" {
		c.NAS.Password = def.NAS.Password
	}
	if c.NAS.Port < 1024 || c.NAS.Port > 65535 {
		c.NAS.Port = 8788
	}
	if c.Docker.Mode == "" {
		c.Docker.Mode = "cli"
	}
	if c.Docker.Host == "" {
		c.Docker.Host = def.Docker.Host
	}
	if c.Tailscale.IntervalSec < 2 {
		c.Tailscale.IntervalSec = 10
	}
	if c.Tailscale.MaxPeers < 1 || c.Tailscale.MaxPeers > 50 {
		c.Tailscale.MaxPeers = 5
	}
	if c.PiHole.RefreshSec < 2 {
		c.PiHole.RefreshSec = 5
	}
	if c.NAS.Root != "" {
		c.NAS.Root = ExpandPath(c.NAS.Root)
	}
	if strings.TrimSpace(c.Claude.Command) == "" {
		c.Claude.Command = "claude"
	}
	if strings.TrimSpace(c.Claude.PermissionMode) == "" {
		c.Claude.PermissionMode = "default"
	}
	if c.Claude.WorkDir != "" {
		c.Claude.WorkDir = ExpandPath(c.Claude.WorkDir)
	}
}

// normalizeUITabs guarantees every tab has one unique number key and that the
// settings tab can never be hidden (otherwise it could not be restored).
func (c *Config) normalizeUITabs() {
	order := make([]string, 0, len(TabIDs))
	seenOrder := map[string]bool{}
	for _, id := range c.UI.TabOrder {
		if !IsTabID(id) || seenOrder[id] {
			continue
		}
		seenOrder[id] = true
		order = append(order, id)
	}
	for _, id := range TabIDs {
		if !seenOrder[id] {
			order = append(order, id)
		}
	}
	c.UI.TabOrder = order

	if c.UI.DeckLayout != "cards" && c.UI.DeckLayout != "compact" {
		c.UI.DeckLayout = "auto"
	}

	if c.UI.TabNumbers == nil {
		c.UI.TabNumbers = DefaultTabNumbers()
	}
	numbers := map[string]string{}
	used := map[string]bool{}
	for id, num := range c.UI.TabNumbers {
		if !IsTabID(id) {
			continue
		}
		num = strings.TrimSpace(num)
		if len(num) != 1 || num[0] < '1' || num[0] > '9' || used[num] {
			continue
		}
		numbers[id] = num
		used[num] = true
	}
	for _, id := range TabIDs {
		if numbers[id] != "" {
			continue
		}
		for n := '1'; n <= '9'; n++ {
			key := string(n)
			if !used[key] {
				numbers[id] = key
				used[key] = true
				break
			}
		}
	}
	c.UI.TabNumbers = numbers

	hidden := make([]string, 0, len(c.UI.HiddenTabs))
	seen := map[string]bool{}
	for _, id := range c.UI.HiddenTabs {
		if !IsTabID(id) || id == "settings" || seen[id] {
			continue
		}
		seen[id] = true
		hidden = append(hidden, id)
	}
	c.UI.HiddenTabs = hidden
}

// Clone returns a deep copy safe to mutate outside the store lock.
func (c Config) Clone() Config {
	out := c
	out.Tashnet.Devices = make(map[string]Device, len(c.Tashnet.Devices))
	for k, v := range c.Tashnet.Devices {
		out.Tashnet.Devices[k] = v
	}
	out.Tashnet.Pending = make(map[string]Pending, len(c.Tashnet.Pending))
	for k, v := range c.Tashnet.Pending {
		out.Tashnet.Pending[k] = v
	}
	out.UI.TabNumbers = make(map[string]string, len(c.UI.TabNumbers))
	for k, v := range c.UI.TabNumbers {
		out.UI.TabNumbers[k] = v
	}
	out.UI.Keybinds = make(map[string]string, len(c.UI.Keybinds))
	for k, v := range c.UI.Keybinds {
		out.UI.Keybinds[k] = v
	}
	out.UI.HiddenTabs = append([]string(nil), c.UI.HiddenTabs...)
	out.UI.TabOrder = append([]string(nil), c.UI.TabOrder...)
	return out
}

// Store is a concurrency-safe configuration handle shared by the TUI and the
// Tashnet control server.
type Store struct {
	mu     sync.RWMutex
	cfg    Config
	secret string
	dir    string
	path   string
}

// Open loads the configuration from disk.
func Open() (*Store, error) {
	dir := Dir()
	path := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotSetup
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Normalize()
	secret, err := LoadSecret(dir)
	if err != nil {
		return nil, err
	}
	// A main device cannot operate without its network key.
	if cfg.Tashnet.Role == "main" && secret == "" {
		return nil, fmt.Errorf("tashnet secret key is missing from %s; restore it or re-run setup", filepath.Join(dir, "secret.key"))
	}
	return &Store{cfg: cfg, secret: secret, dir: dir, path: path}, nil
}

// Create persists a brand-new configuration and secret.
func Create(cfg Config, secret string) (*Store, error) {
	cfg.Normalize()
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	// Create never silently overwrites an existing installation.
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return nil, fmt.Errorf("a Tash configuration already exists in %s", dir)
	}
	s := &Store{cfg: cfg, secret: secret, dir: dir, path: filepath.Join(dir, "config.json")}
	if secret != "" {
		if err := s.SetSecret(secret); err != nil {
			return nil, err
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir returns Tash's config directory.
func (s *Store) Dir() string { return s.dir }

// ConfigPath returns the config.json path.
func (s *Store) ConfigPath() string { return s.path }

// ThemesDir returns the custom themes directory.
func (s *Store) ThemesDir() string { return filepath.Join(s.dir, "themes") }

// Secret returns the Tashnet network key (empty for clients).
func (s *Store) Secret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secret
}

// SetSecret atomically writes a new network secret.
func (s *Store) SetSecret(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "secret.key")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret+"\n"), 0o600); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install secret: %w", err)
	}
	s.secret = secret
	return nil
}

// ClearSecret removes the network secret from disk. Used when a main device is
// demoted after a main-role transfer.
func (s *Store) ClearSecret() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, "secret.key"))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	s.secret = ""
	return nil
}

// Snapshot returns a deep copy of the current config.
func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// Update mutates and persists the configuration atomically.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	next.Normalize()
	next.UpdatedAt = time.Now().UTC()
	if err := writeJSONAtomic(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

// Save persists the current configuration.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	s.cfg.Normalize()
	s.cfg.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(s.path, s.cfg)
}

// LoadSecret reads secret.key from dir; missing is not an error.
func LoadSecret(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "secret.key"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadSecret loads the secret without opening a config.
func ReadSecret() (string, error) { return LoadSecret(Dir()) }

func writeJSONAtomic(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RandomPassword returns a URL-safe, easy-to-type password.
func RandomPassword(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "changeme-tash"
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// DefaultDockerHost returns the conventional engine endpoint for this OS.
func DefaultDockerHost() string {
	switch runtime.GOOS {
	case "windows":
		return "npipe:////./pipe/docker_engine"
	case "darwin":
		return "unix:///var/run/docker.sock"
	default:
		return "unix:///var/run/docker.sock"
	}
}
