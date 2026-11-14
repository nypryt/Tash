// Package plugin provides the plugin system for Tash.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tash/tash/internal/config"
)

// PluginKind represents the type of plugin.
type PluginKind string

const (
	PluginKindTab       PluginKind = "tab"       // Adds a dashboard tab
	PluginKindWidget    PluginKind = "widget"    // Adds a dashboard widget
	PluginKindCommand   PluginKind = "command"   // Adds a CLI command
	PluginKindBackground PluginKind = "background" // Background service
)

// PluginManifest describes a plugin.
type PluginManifest struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Author      string     `json:"author"`
	Kind        PluginKind `json:"kind"`
	EntryPoint  string     `json:"entry_point"` // Script/binary to execute
	APIVersion  int        `json:"api_version"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// PluginInstance represents a loaded plugin.
type PluginInstance struct {
	Manifest  PluginManifest
	Path      string
	Process   *exec.Cmd
	Stdin     *os.File
	Stdout    *os.File
	Stderr    *os.File
	mu        sync.Mutex
	running   bool
	ctx       context.Context
	cancel    context.CancelFunc
}

// PluginAPI is the interface plugins use to communicate with Tash.
type PluginAPI struct {
	// Config access
	GetConfig func(key string) (string, error)
	SetConfig func(key, value string) error

	// UI interaction
	ShowToast    func(message string, bad bool)
	ShowModal    func(title, body string)
	AddTab       func(id, name string, kind PluginKind) error
	RemoveTab    func(id string) error
	AddWidget    func(widget WidgetSpec) error
	RemoveWidget func(id string) error

	// Tashnet
	GetDevices   func() []config.Device
	GetPeers     func() []string // Tailscale peer IPs
	SendCommand  func(deviceID, command string) (string, error)

	// NAS
	ReadFile  func(path string) ([]byte, error)
	WriteFile func(path string, data []byte) error
	ListFiles func(path string) ([]string, error)

	// Docker
	ListContainers func() ([]ContainerInfo, error)
	ContainerAction func(id, action string) error

	// Pi-hole
	GetPiHoleStats func() (map[string]interface{}, error)
	PiHoleAction   func(action string) error

	// Events
	OnEvent func(event string, handler func(data json.RawMessage))
	EmitEvent func(event string, data interface{}) error

	// Logging
	Log func(level, message string)
}

// WidgetSpec defines a dashboard widget.
type WidgetSpec struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"` // text, chart, table, status, custom
	Position string `json:"position"` // overview, sidebar, tab:name
	Size     string `json:"size"` // small, medium, large
	Refresh  int    `json:"refresh_seconds"`
	Content  string `json:"content"` // Initial content or script
}

// ContainerInfo represents a Docker container.
type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Running bool   `json:"running"`
}

// Manager handles plugin loading and lifecycle.
type Manager struct {
	pluginsDir string
	plugins    map[string]*PluginInstance
	api        *PluginAPI
	mu         sync.RWMutex
	events     map[string][]func(json.RawMessage)
}

// NewManager creates a new plugin manager.
func NewManager(pluginsDir string) *Manager {
	if pluginsDir == "" {
		pluginsDir = filepath.Join(config.Dir(), "plugins")
	}
	os.MkdirAll(pluginsDir, 0o755)
	return &Manager{
		pluginsDir: pluginsDir,
		plugins:    make(map[string]*PluginInstance),
		events:     make(map[string][]func(json.RawMessage)),
	}
}

// SetAPI sets the plugin API implementation.
func (m *Manager) SetAPI(api *PluginAPI) {
	m.api = api
}

// Discover scans the plugins directory for valid plugins.
func (m *Manager) Discover() ([]PluginManifest, error) {
	var manifests []PluginManifest
	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(m.pluginsDir, entry.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // Skip directories without manifest
		}
		var manifest PluginManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			m.log("warn", fmt.Sprintf("plugin %s: invalid manifest: %v", entry.Name(), err))
			continue
		}
		manifest.EntryPoint = filepath.Join(m.pluginsDir, entry.Name(), manifest.EntryPoint)
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

// Load loads and starts a plugin.
func (m *Manager) Load(manifest PluginManifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.plugins[manifest.Name]; exists {
		return fmt.Errorf("plugin %s already loaded", manifest.Name)
	}

	// Validate entry point
	if _, err := os.Stat(manifest.EntryPoint); err != nil {
		return fmt.Errorf("entry point not found: %s", manifest.EntryPoint)
	}

	ctx, cancel := context.WithCancel(context.Background())
	instance := &PluginInstance{
		Manifest: manifest,
		Path:     filepath.Dir(manifest.EntryPoint),
		ctx:      ctx,
		cancel:   cancel,
		running:  true,
	}

	// Start plugin process
	cmd := exec.CommandContext(ctx, manifest.EntryPoint)
	cmd.Dir = instance.Path
	cmd.Env = append(os.Environ(),
		"TASH_PLUGIN_NAME="+manifest.Name,
		"TASH_PLUGIN_API_VERSION="+fmt.Sprintf("%d", manifest.APIVersion),
		"TASH_CONFIG_DIR="+config.Dir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	instance.Process = cmd
	instance.Stdin = stdin.(*os.File)
	instance.Stdout = stdout.(*os.File)
	instance.Stderr = stderr.(*os.File)

	if err := cmd.Start(); err != nil {
		return err
	}

	m.plugins[manifest.Name] = instance
	m.log("info", fmt.Sprintf("plugin loaded: %s v%s", manifest.Name, manifest.Version))

	// Handle stdout/stderr
	go m.handleOutput(instance)
	go m.handleError(instance)

	// Send init message
	m.sendToPlugin(instance, map[string]interface{}{
		"type":    "init",
		"config":  manifest.Config,
		"api_ver": manifest.APIVersion,
	})

	return nil
}

// Unload stops and removes a plugin.
func (m *Manager) Unload(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	instance, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %s not loaded", name)
	}

	instance.cancel()
	if instance.Process != nil {
		instance.Process.Wait()
	}
	delete(m.plugins, name)
	m.log("info", fmt.Sprintf("plugin unloaded: %s", name))
	return nil
}

// LoadAll discovers and loads all plugins.
func (m *Manager) LoadAll() error {
	manifests, err := m.Discover()
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		if err := m.Load(manifest); err != nil {
			m.log("error", fmt.Sprintf("failed to load plugin %s: %v", manifest.Name, err))
		}
	}
	return nil
}

// UnloadAll stops all plugins.
func (m *Manager) UnloadAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, instance := range m.plugins {
		instance.cancel()
		if instance.Process != nil {
			instance.Process.Wait()
		}
		delete(m.plugins, name)
	}
}

// HandleAPIRequest processes a request from a plugin.
func (m *Manager) HandleAPIRequest(name string, request map[string]interface{}) (map[string]interface{}, error) {
	if m.api == nil {
		return nil, fmt.Errorf("API not initialized")
	}

	action, _ := request["action"].(string)
	switch action {
	case "get_config":
		key, _ := request["key"].(string)
		val, err := m.api.GetConfig(key)
		return map[string]interface{}{"value": val, "error": err}, nil
	case "set_config":
		key, _ := request["key"].(string)
		val, _ := request["value"].(string)
		err := m.api.SetConfig(key, val)
		return map[string]interface{}{"error": err}, nil
	case "show_toast":
		msg, _ := request["message"].(string)
		bad, _ := request["bad"].(bool)
		m.api.ShowToast(msg, bad)
		return map[string]interface{}{}, nil
	case "show_modal":
		title, _ := request["title"].(string)
		body, _ := request["body"].(string)
		m.api.ShowModal(title, body)
		return map[string]interface{}{}, nil
	case "add_tab":
		id, _ := request["id"].(string)
		name, _ := request["name"].(string)
		kindStr, _ := request["kind"].(string)
		err := m.api.AddTab(id, name, PluginKind(kindStr))
		return map[string]interface{}{"error": err}, nil
	case "remove_tab":
		id, _ := request["id"].(string)
		err := m.api.RemoveTab(id)
		return map[string]interface{}{"error": err}, nil
	case "add_widget":
		var widget WidgetSpec
		data, _ := json.Marshal(request["widget"])
		json.Unmarshal(data, &widget)
		err := m.api.AddWidget(widget)
		return map[string]interface{}{"error": err}, nil
	case "remove_widget":
		id, _ := request["id"].(string)
		err := m.api.RemoveWidget(id)
		return map[string]interface{}{"error": err}, nil
	case "get_devices":
		devices := m.api.GetDevices()
		data, _ := json.Marshal(devices)
		return map[string]interface{}{"devices": json.RawMessage(data)}, nil
	case "get_peers":
		peers := m.api.GetPeers()
		return map[string]interface{}{"peers": peers}, nil
	case "send_command":
		deviceID, _ := request["device_id"].(string)
		cmd, _ := request["command"].(string)
		out, err := m.api.SendCommand(deviceID, cmd)
		return map[string]interface{}{"output": out, "error": err}, nil
	case "read_file":
		path, _ := request["path"].(string)
		data, err := m.api.ReadFile(path)
		return map[string]interface{}{"data": data, "error": err}, nil
	case "write_file":
		path, _ := request["path"].(string)
		data, _ := request["data"].([]byte)
		err := m.api.WriteFile(path, data)
		return map[string]interface{}{"error": err}, nil
	case "list_files":
		path, _ := request["path"].(string)
		files, err := m.api.ListFiles(path)
		return map[string]interface{}{"files": files, "error": err}, nil
	case "list_containers":
		containers, err := m.api.ListContainers()
		data, _ := json.Marshal(containers)
		return map[string]interface{}{"containers": json.RawMessage(data), "error": err}, nil
	case "container_action":
		id, _ := request["id"].(string)
		action, _ := request["action"].(string)
		err := m.api.ContainerAction(id, action)
		return map[string]interface{}{"error": err}, nil
	case "get_pihole_stats":
		stats, err := m.api.GetPiHoleStats()
		return map[string]interface{}{"stats": stats, "error": err}, nil
	case "pihole_action":
		action, _ := request["action"].(string)
		err := m.api.PiHoleAction(action)
		return map[string]interface{}{"error": err}, nil
	case "on_event":
		// Plugin wants to subscribe to events - store handler
		_ = request["event"]
		return map[string]interface{}{}, nil
	case "emit_event":
		event, _ := request["event"].(string)
		data := request["data"]
		err := m.api.EmitEvent(event, data)
		return map[string]interface{}{"error": err}, nil
	case "log":
		level, _ := request["level"].(string)
		msg, _ := request["message"].(string)
		m.api.Log(level, msg)
		return map[string]interface{}{}, nil
	}
	return nil, fmt.Errorf("unknown action: %s", action)
}

func (m *Manager) sendToPlugin(instance *PluginInstance, msg map[string]interface{}) {
	data, _ := json.Marshal(msg)
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.Stdin != nil {
		instance.Stdin.Write(append(data, '\n'))
	}
}

func (m *Manager) handleOutput(instance *PluginInstance) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-instance.ctx.Done():
			return
		default:
			n, err := instance.Stdout.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				m.processPluginMessage(instance, buf[:n])
			}
		}
	}
}

func (m *Manager) handleError(instance *PluginInstance) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-instance.ctx.Done():
			return
		default:
			n, err := instance.Stderr.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				m.log("plugin:"+instance.Manifest.Name, string(buf[:n]))
			}
		}
	}
}

func (m *Manager) processPluginMessage(instance *PluginInstance, data []byte) {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			m.log("warn", fmt.Sprintf("plugin %s: invalid message: %v", instance.Manifest.Name, err))
			continue
		}
		msgType, _ := msg["type"].(string)
		switch msgType {
		case "request":
			_ = msg["action"]
			response, err := m.HandleAPIRequest(instance.Manifest.Name, msg)
			if err != nil {
				response = map[string]interface{}{"error": err.Error()}
			}
			response["id"] = msg["id"]
			response["type"] = "response"
			m.sendToPlugin(instance, response)
		case "event":
			event, _ := msg["event"].(string)
			eventData := msg["data"]
			m.emitEvent(event, eventData)
		case "log":
			level, _ := msg["level"].(string)
			message, _ := msg["message"].(string)
			m.log("plugin:"+instance.Manifest.Name, fmt.Sprintf("[%s] %s", level, message))
		}
	}
}

func (m *Manager) emitEvent(event string, data interface{}) {
	m.mu.RLock()
	handlers := m.events[event]
	m.mu.RUnlock()
	for _, h := range handlers {
		eventData, _ := json.Marshal(data)
		h(eventData)
	}
}

// Subscribe registers an event handler.
func (m *Manager) Subscribe(event string, handler func(json.RawMessage)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[event] = append(m.events[event], handler)
}

func (m *Manager) log(level, message string) {
	if m.api != nil && m.api.Log != nil {
		m.api.Log(level, message)
	}
}

// GetLoaded returns all loaded plugin names.
func (m *Manager) GetLoaded() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		names = append(names, name)
	}
	return names
}