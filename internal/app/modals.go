package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/docker"
	"github.com/tash/tash/internal/nas"
	"github.com/tash/tash/internal/tailscale"
	"github.com/tash/tash/internal/tashnet"
	"github.com/tash/tash/internal/theme"
)

type modalKind int

const (
	modalKindNone modalKind = iota
	modalKindInput
	modalKindConfirm
	modalKindInfo
)

type modal struct {
	kind    modalKind
	title   string
	body    string
	prompt  string
	value   string
	cursor  int
	secret  bool
	require string
	action  string
	scroll  int
}

func (md modal) active() bool { return md.kind != modalKindNone }

func inputModal(title, prompt, value, body, action string, secret bool) modal {
	return modal{
		kind: modalKindInput, title: title, prompt: prompt, value: value,
		body: body, action: action, cursor: len([]rune(value)), secret: secret,
	}
}

func confirmModal(title, body, prompt, action string) modal {
	return modal{kind: modalKindConfirm, title: title, body: body, prompt: prompt, action: action}
}

func infoModal(title, body string) modal {
	return modal{kind: modalKindInfo, title: title, body: body}
}

func dangerModal(title, body, require, action string) modal {
	return modal{kind: modalKindInput, title: title, body: body, require: require, action: action}
}

func (m *Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	md := &m.modal
	key := msg.String()
	switch md.kind {
	case modalKindConfirm:
		switch key {
		case "esc", "n", "N":
			m.modal = modal{}
			return m, nil
		case "enter", "y", "Y":
			return m.runModalAction(md.action, "")
		}
		return m, nil

	case modalKindInfo:
		switch key {
		case "esc", "enter", "q":
			m.modal = modal{}
		case "up", "k":
			if md.scroll > 0 {
				md.scroll--
			}
		case "down", "j":
			md.scroll++
		case "pgup":
			md.scroll -= maxInt(m.bodyHeight()-8, 4)
			if md.scroll < 0 {
				md.scroll = 0
			}
		case "pgdown":
			md.scroll += maxInt(m.bodyHeight()-8, 4)
		}
		return m, nil

	case modalKindInput:
		if key == "esc" {
			m.modal = modal{}
			return m, nil
		}
		if key == "enter" {
			if md.require != "" && !strings.EqualFold(strings.TrimSpace(md.value), md.require) {
				m.setToast("type "+md.require+" exactly to confirm", true)
				return m, nil
			}
			return m.runModalAction(md.action, md.value)
		}
		editModalInput(md, msg)
		return m, nil
	}
	m.modal = modal{}
	return m, nil
}

func editModalInput(md *modal, msg tea.KeyMsg) {
	r := []rune(md.value)
	if md.cursor > len(r) {
		md.cursor = len(r)
	}
	if md.cursor < 0 {
		md.cursor = 0
	}
	switch msg.Type {
	case tea.KeyBackspace:
		if md.cursor > 0 {
			r = append(r[:md.cursor-1], r[md.cursor:]...)
			md.cursor--
		}
	case tea.KeyDelete:
		if md.cursor < len(r) {
			r = append(r[:md.cursor], r[md.cursor+1:]...)
		}
	case tea.KeyLeft:
		if md.cursor > 0 {
			md.cursor--
		}
	case tea.KeyRight:
		if md.cursor < len(r) {
			md.cursor++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		md.cursor = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		md.cursor = len(r)
	case tea.KeyCtrlU:
		r = r[:0]
		md.cursor = 0
	case tea.KeySpace:
		r = append(r[:md.cursor], append([]rune{' '}, r[md.cursor:]...)...)
		md.cursor++
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			runes := []rune(strings.ReplaceAll(string(msg.Runes), "\n", ""))
			r = append(r[:md.cursor], append(runes, r[md.cursor:]...)...)
			md.cursor += len(runes)
		}
	}
	md.value = string(r)
}

func (m *Model) runModalAction(action, value string) (tea.Model, tea.Cmd) {
	action = strings.TrimSpace(action)
	switch action {
	case "add-block":
		entry := strings.TrimSpace(value)
		if entry == "" {
			m.modal = modal{}
			m.setToast("nothing to add", true)
			return m, nil
		}
		client := m.piholeClient
		if client == nil {
			m.modal = modal{}
			m.setToast("Pi-hole is not configured", true)
			return m, nil
		}
		m.modal = modal{}
		m.setToast("adding "+entry+" …", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			text, err := client.AddBlock(ctx, entry)
			if err != nil {
				return notify("add block", err)
			}
			return notify(text, nil)
		}

	case "add-allow":
		entry := strings.TrimSpace(value)
		if entry == "" {
			m.modal = modal{}
			m.setToast("nothing to add", true)
			return m, nil
		}
		client := m.piholeClient
		if client == nil {
			m.modal = modal{}
			m.setToast("Pi-hole is not configured", true)
			return m, nil
		}
		m.modal = modal{}
		m.setToast("adding allow entry "+entry+" …", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			text, err := client.AddAllow(ctx, entry)
			if err != nil {
				return notify("add allow", err)
			}
			return notify(text, nil)
		}

	case "pihole-blocking":
		client := m.piholeClient
		if client == nil {
			m.modal = modal{}
			m.setToast("Pi-hole is not configured", true)
			return m, nil
		}
		enable := m.ph.Blocking != "enabled"
		m.modal = modal{}
		word := "disable"
		if enable {
			word = "enable"
		}
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := client.SetBlocking(ctx, enable); err != nil {
				return notify(word+" Pi-hole blocking", err)
			}
			return notify("Pi-hole blocking "+word+"d", nil)
		}

	case "pihole-gravity":
		client := m.piholeClient
		if client == nil {
			m.modal = modal{}
			m.setToast("Pi-hole is not configured", true)
			return m, nil
		}
		m.modal = modal{}
		m.setToast("gravity update started…", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			out, err := client.RunGravity(ctx)
			if err != nil {
				return notify("gravity update", err)
			}
			return notify("gravity: "+firstLine(out), nil)
		}

	case "regen-secret":
		return m.regenerateSecret()

	case "transfer":
		return m.showTransferCode()

	case "approve-pending":
		pending := sortedPending(m.cfg.Tashnet.Pending)
		if len(pending) == 0 {
			m.modal = modal{}
			m.setToast("no pending requests", true)
			return m, nil
		}
		if m.pendIdx >= len(pending) {
			m.pendIdx = len(pending) - 1
		}
		p := pending[m.pendIdx]
		m.modal = modal{}
		return m, m.approveDeviceCmd(p)

	case "deny-pending":
		pending := sortedPending(m.cfg.Tashnet.Pending)
		if len(pending) == 0 {
			m.modal = modal{}
			m.setToast("no pending requests", true)
			return m, nil
		}
		if m.pendIdx >= len(pending) {
			m.pendIdx = len(pending) - 1
		}
		p := pending[m.pendIdx]
		m.modal = modal{}
		return m, m.denyDeviceCmd(p)

	case "editor-open":
		path := strings.TrimSpace(value)
		if path == "" {
			m.modal = modal{}
			return m, nil
		}
		if err := m.editor.Load(path); err != nil {
			m.modal = modal{}
			m.setToast("open: "+err.Error(), true)
			return m, nil
		}
		if m.store != nil {
			_ = m.store.Update(func(c *config.Config) error {
				c.LastEditorFile = m.editor.Path()
				return nil
			})
		}
		m.modal = modal{}
		m.setTab(TabEditor)
		return m, nil

	case "theme-copy":
		name := strings.ToLower(strings.TrimSpace(value))
		if name == "" {
			m.modal = modal{}
			m.setToast("theme name is required", true)
			return m, nil
		}
		base := m.registry.Get(m.currentThemeName())
		t := theme.Template(base, name)
		path, err := m.registry.Save(t)
		if err != nil {
			m.modal = modal{}
			m.setToast("theme: "+err.Error(), true)
			return m, nil
		}
		m.registry.Reload()
		m.applyTheme(t.Name, true)
		_ = m.editor.Load(path)
		m.modal = modal{}
		m.setTab(TabEditor)
		m.setToast("custom theme created: "+path, false)
		return m, nil

	case "docker-start", "docker-stop", "docker-restart", "docker-pause", "docker-unpause", "docker-delete":
		if m.dockerClient == nil {
			m.modal = modal{}
			m.setToast("Docker is not configured", true)
			return m, nil
		}
		c, ok := m.selectedContainer()
		if !ok {
			m.modal = modal{}
			m.setToast("no container selected", true)
			return m, nil
		}
		label := strings.TrimPrefix(action, "docker-")
		apiAction := label
		if label == "delete" {
			apiAction = "remove"
		}
		client := m.dockerClient
		m.modal = modal{}
		m.setToast(label+" "+c.Name+"…", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			if err := client.Action(ctx, c.ID, apiAction); err != nil {
				return notify(label+" "+c.Name, err)
			}
			return notify(label+" "+c.Name, nil)
		}

	case "docker-logs":
		if m.dockerClient == nil {
			m.modal = modal{}
			m.setToast("Docker is not configured", true)
			return m, nil
		}
		c, ok := m.selectedContainer()
		if !ok {
			m.modal = modal{}
			m.setToast("no container selected", true)
			return m, nil
		}
		client := m.dockerClient
		m.modal = modal{}
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			text, err := client.Logs(ctx, c.ID, 160)
			return dockerLogsMsg{name: c.Name, text: text, err: err}
		}

	case "nas-start":
		cfg := m.cfg.NAS
		if m.nas != nil && m.nas.Running() {
			m.modal = modal{}
			m.setToast("NAS is already running", false)
			return m, nil
		}
		m.modal = modal{}
		if err := nas.EnsureRoot(cfg.Root); err != nil {
			m.setToast("NAS root: "+err.Error(), true)
			return m, nil
		}
		srv := nas.New(cfg)
		if err := srv.Start(); err != nil {
			m.setToast("NAS: "+err.Error(), true)
			return m, nil
		}
		m.nas = srv
		m.setToast("NAS listening at "+srv.URL(), false)
		return m, nil

	case "nas-stop":
		if m.nas == nil {
			m.modal = modal{}
			return m, nil
		}
		m.modal = modal{}
		srv := m.nas
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			m.setToast("NAS stop: "+err.Error(), true)
			return m, nil
		}
		m.nas = nil
		m.setToast("NAS stopped", false)
		return m, nil

	case "nas-password":
		return m.setNASPassword(value, false)

	case "nas-password-random":
		return m.setNASPassword("", true)

	case "set-nas-root":
		root := strings.TrimSpace(value)
		if root == "" {
			m.modal = modal{}
			m.setToast("NAS root cannot be empty", true)
			return m, nil
		}
		if m.store == nil {
			m.modal = modal{}
			return m, nil
		}
		if err := m.store.Update(func(c *config.Config) error {
			c.NAS.Root = config.ExpandPath(root)
			return nil
		}); err != nil {
			m.modal = modal{}
			m.setToast("NAS root: "+err.Error(), true)
			return m, nil
		}
		m.cfg = m.store.Snapshot()
		if err := nas.EnsureRoot(m.cfg.NAS.Root); err != nil {
			m.modal = modal{}
			m.setToast("NAS root: "+err.Error(), true)
			return m, nil
		}
		m.nasPath = ""
		m.nasIdx = 0
		if err := m.restartNAS(); err != nil {
			m.modal = modal{}
			m.setToast("NAS restart: "+err.Error(), true)
			return m, nil
		}
		m.modal = modal{}
		m.setToast("NAS root: "+m.cfg.NAS.Root, false)
		return m, nil

	case "set-ai-key":
		m.updateAI(func(c *config.AIConfig) {
			c.APIKey = strings.TrimSpace(value)
		})
		m.modal = modal{}
		m.setToast("AI API key updated", false)
		return m, m.aiDetectCmd()

	case "set-ai-model":
		m.updateAI(func(c *config.AIConfig) {
			c.Model = strings.TrimSpace(value)
		})
		m.modal = modal{}
		m.setToast("AI model updated", false)
		return m, nil

	case "set-ai-provider":
		m.updateAI(func(c *config.AIConfig) {
			c.Provider = strings.TrimSpace(value)
		})
		m.modal = modal{}
		m.setToast("AI provider updated", false)
		return m, m.aiDetectCmd()

	case "set-ai-baseurl":
		m.updateAI(func(c *config.AIConfig) {
			c.BaseURL = strings.TrimSpace(value)
		})
		m.modal = modal{}
		m.setToast("AI base URL updated", false)
		return m, m.aiDetectCmd()

	case "set-ai-command":
		command := strings.TrimSpace(value)
		if command == "" {
			command = "claude"
		}
		m.updateAI(func(c *config.AIConfig) {
			c.Command = command
		})
		m.modal = modal{}
		m.setToast("AI command: "+command, false)
		return m, m.aiDetectCmd()

	case "set-ai-workdir":
		dir := strings.TrimSpace(value)
		if dir != "" {
			dir = config.ExpandPath(dir)
		}
		m.updateAI(func(c *config.AIConfig) {
			c.WorkDir = dir
		})
		m.modal = modal{}
		if dir == "" {
			m.setToast("AI workdir: current directory", false)
		} else {
			m.setToast("AI workdir: "+dir, false)
		}
		return m, nil

	case "set-ai-permission":
		mode := strings.TrimSpace(value)
		if mode == "" {
			mode = "default"
		}
		m.updateAI(func(c *config.AIConfig) {
			c.PermissionMode = mode
		})
		m.modal = modal{}
		m.setToast("AI permission mode: "+mode, false)
		return m, nil

	case "set-key-refresh", "set-key-help", "set-key-quit":
		key := strings.TrimSpace(value)
		if key == "" {
			m.modal = modal{}
			m.setToast("key binding cannot be empty", true)
			return m, nil
		}
		action := strings.TrimPrefix(action, "set-key-")
		m.updateUI(func(c *config.UIConfig) {
			if c.Keybinds == nil {
				c.Keybinds = config.DefaultKeybinds()
			}
			c.Keybinds[action] = key
		})
		m.modal = modal{}
		m.setToast(action+" key: "+key, false)
		return m, nil

	case "client-rejoin-help":
		m.modal = infoModal("RE-PAIR THIS DEVICE", strings.Join([]string{
			"Run this from a terminal:",
			"",
			"  tash join",
			"",
			"You will be prompted for the Secret Key and the main device",
			"address. The main device must approve the new request.",
			"",
			"Re-pairing is required after the main device regenerates its",
			"secret key or removes this device.",
		}, "\n"))
		return m, nil

	case "show-secret":
		secret := ""
		if m.store != nil {
			secret = m.store.Secret()
		}
		if secret == "" {
			m.modal = infoModal("SECRET KEY", "No secret key is stored on this device. This is normal for clients.")
			return m, nil
		}
		m.modal = infoModal("SECRET KEY // REVEALED", strings.Join([]string{
			"Secret Key:",
			"",
			secret,
			"",
			"Anyone with this key can REQUEST to join, but you still must",
			"approve each device on this main device.",
			"",
			"EXTREME WARNING: regenerating the key disconnects every",
			"existing device immediately.",
		}, "\n"))
		return m, nil
	}

	m.modal = modal{}
	return m, nil
}

func (m *Model) setNASPassword(value string, forceRandom bool) (tea.Model, tea.Cmd) {
	if m.store == nil {
		m.modal = modal{}
		m.setToast("no configuration", true)
		return m, nil
	}
	password := strings.TrimSpace(value)
	if forceRandom || password == "" {
		password = config.RandomPassword(16)
	}
	if err := m.store.Update(func(c *config.Config) error {
		c.NAS.Password = password
		return nil
	}); err != nil {
		m.modal = modal{}
		m.setToast("NAS password: "+err.Error(), true)
		return m, nil
	}
	m.cfg = m.store.Snapshot()
	if err := m.restartNAS(); err != nil {
		m.modal = modal{}
		m.setToast("NAS restart: "+err.Error(), true)
		return m, nil
	}
	m.modal = infoModal("NAS PASSWORD UPDATED", strings.Join([]string{
		"The WebDAV password was changed.",
		"If the NAS server was running it was restarted with the new credentials.",
		"Connected clients must reconnect with the new password.",
		"",
		"New password:",
		"",
		password,
	}, "\n"))
	return m, nil
}

func (m *Model) restartNAS() error {
	if m.nas == nil || !m.nas.Running() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := m.nas.Stop(ctx); err != nil {
		return err
	}
	m.nas = nil
	srv := nas.New(m.cfg.NAS)
	if err := srv.Start(); err != nil {
		return err
	}
	m.nas = srv
	return nil
}

func (m *Model) regenerateSecret() (tea.Model, tea.Cmd) {
	if m.store == nil || m.cfg.Tashnet.Role != "main" {
		m.modal = modal{}
		m.setToast("only the main device can regenerate the secret", true)
		return m, nil
	}
	newSecret, err := tashnet.GenerateSecret()
	if err != nil {
		m.modal = modal{}
		m.setToast("secret: "+err.Error(), true)
		return m, nil
	}
	now := time.Now().UTC()
	err = m.store.Update(func(c *config.Config) error {
		c.Tashnet.SecretVersion++
		c.Tashnet.Pending = map[string]config.Pending{}
		devices := map[string]config.Device{}
		devices[c.Tashnet.DeviceID] = config.Device{
			ID: c.Tashnet.DeviceID, Name: c.Tashnet.DeviceName, OS: m.info.OS, Arch: m.info.Arch,
			IP: m.info.LocalIP, Token: tashnet.NewToken(), ApprovedAt: now, LastSeen: now,
		}
		c.Tashnet.Devices = devices
		c.Tashnet.TransferNonce = ""
		c.Tashnet.TransferExpiry = time.Time{}
		return nil
	})
	if err != nil {
		m.modal = modal{}
		m.setToast("secret: "+err.Error(), true)
		return m, nil
	}
	if err := m.store.SetSecret(newSecret); err != nil {
		m.modal = modal{}
		m.setToast("secret written to config but key file failed: "+err.Error(), true)
		return m, nil
	}
	m.cfg = m.store.Snapshot()
	if m.tnet != nil {
		m.tnet.AddEvent("secret", "SECRET KEY REGENERATED — all client tokens were revoked")
	}
	m.modal = infoModal("SECRET REGENERATED // ALL DEVICES DISCONNECTED", strings.Join([]string{
		"Every existing device token was revoked immediately.",
		"Each device must rejoin with the new key and be approved again.",
		"",
		"New Secret Key:",
		"",
		newSecret,
		"",
		"Store it somewhere safe. Regenerating again will disconnect",
		"everyone once more.",
	}, "\n"))
	return m, m.refreshCmd()
}

func (m *Model) showTransferCode() (tea.Model, tea.Cmd) {
	if m.store == nil || m.cfg.Tashnet.Role != "main" {
		m.modal = modal{}
		m.setToast("only the main device can issue a transfer code", true)
		return m, nil
	}
	code, expires, err := tashnet.ExportTransfer(m.store)
	if err != nil {
		m.modal = modal{}
		m.setToast("transfer: "+err.Error(), true)
		return m, nil
	}
	if m.tnet != nil {
		m.tnet.AddEvent("transfer", "main-role transfer code issued; expires "+expires.Format("15:04:05"))
	}
	body := strings.Join([]string{
		"ONE-TIME MAIN-ROLE HANDOVER",
		"",
		"On the NEW device, run:",
		"",
		"  tash main claim " + wrapCode(code, 72),
		"",
		"This code contains the Tashnet secret and every approved token.",
		"Treat it like the secret key. It expires at " + expires.Local().Format("15:04:05") + ".",
		"",
		"The old main is demoted only after the new main claims and notifies it.",
	}, "\n")
	m.modal = infoModal("TRANSFER CODE // EXTREME CARE", body)
	m.cfg = m.store.Snapshot()
	return m, m.refreshCmd()
}

func wrapCode(code string, width int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	for len(code) > width {
		b.WriteString(code[:width])
		b.WriteString("\n  ")
		code = code[width:]
	}
	b.WriteString(code)
	return b.String()
}

func (m *Model) approveDeviceCmd(p config.Pending) tea.Cmd {
	return func() tea.Msg {
		if m.store == nil {
			return notify("approve", fmt.Errorf("no configuration"))
		}
		err := m.store.Update(func(c *config.Config) error {
			pending, ok := c.Tashnet.Pending[p.ID]
			if !ok {
				return fmt.Errorf("request is no longer pending")
			}
			delete(c.Tashnet.Pending, p.ID)
			c.Tashnet.Devices[pending.DeviceID] = config.Device{
				ID: pending.DeviceID, Name: pending.Name, OS: pending.OS, Arch: pending.Arch,
				IP: pending.IP, Token: tashnet.NewToken(), PollHash: pending.PollHash,
				ApprovedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
			}
			return nil
		})
		if err != nil {
			return notify("approve "+p.Name, err)
		}
		if m.tnet != nil {
			m.tnet.AddEvent("approved", p.Name+" ("+p.OS+"/"+p.Arch+") joined from "+p.IP)
		}
		return notify("approved "+p.Name, nil)
	}
}

func (m *Model) denyDeviceCmd(p config.Pending) tea.Cmd {
	return func() tea.Msg {
		if m.store == nil {
			return notify("deny", fmt.Errorf("no configuration"))
		}
		err := m.store.Update(func(c *config.Config) error {
			if _, ok := c.Tashnet.Pending[p.ID]; !ok {
				return fmt.Errorf("request is no longer pending")
			}
			delete(c.Tashnet.Pending, p.ID)
			return nil
		})
		if err != nil {
			return notify("deny "+p.Name, err)
		}
		if m.tnet != nil {
			m.tnet.AddEvent("denied", p.Name+" was denied")
		}
		return notify("denied "+p.Name, nil)
	}
}

func (m *Model) selectedContainer() (docker.Container, bool) {
	if len(m.dk.Containers) == 0 {
		return docker.Container{}, false
	}
	if m.contIdx < 0 {
		m.contIdx = 0
	}
	if m.contIdx >= len(m.dk.Containers) {
		m.contIdx = len(m.dk.Containers) - 1
	}
	return m.dk.Containers[m.contIdx], true
}

// Tab key handlers ---------------------------------------------------------

func (m *Model) keyOverview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if len(m.cfg.Tashnet.Pending) > 0 {
			m.setTab(TabTashnet)
		}
	}
	return m, nil
}

func (m *Model) keyTailscale(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	peers := m.ts.Peers
	switch msg.String() {
	case "up", "k":
		if m.peerIdx > 0 {
			m.peerIdx--
		}
	case "down", "j":
		if m.peerIdx < len(peers)-1 {
			m.peerIdx++
		}
	case "p":
		if len(peers) == 0 {
			m.setToast("no peers to ping", true)
			return m, nil
		}
		if m.peerIdx >= len(peers) {
			m.peerIdx = 0
		}
		peer := peers[m.peerIdx]
		m.setToast("pinging "+peer.DisplayName()+"…", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			return pingMsg{results: []tailscale.PingResult{tailscale.Ping(ctx, peer, 3*time.Second)}}
		}
	case "P":
		if len(m.ts.OnlinePeers()) == 0 {
			m.setToast("no online peers to ping", true)
			return m, nil
		}
		m.setToast("pinging online peers…", false)
		return m, m.pingCmd()
	}
	return m, nil
}

func (m *Model) keyPiHole(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a":
		m.modal = inputModal("ADD BLOCK", "Blocklist URL, domain, or regex:<pattern>:", "", strings.Join([]string{
			"A URL is added as a blocklist and gravity is refreshed (v6).",
			"A plain domain becomes an exact denylist entry.",
			"Prefix a pattern with regex: for a regex denylist entry.",
		}, "\n"), "add-block", false)
	case "A":
		m.modal = inputModal("ADD ALLOW", "Allowlist URL, domain, or regex:<pattern>:", "", strings.Join([]string{
			"A URL is added as an allowlist and gravity is refreshed (v6).",
			"A plain domain becomes an exact allowlist entry.",
			"Allowlist entries override blocklists for matching domains.",
		}, "\n"), "add-allow", false)
	case "b", "B":
		return m.runModalAction("pihole-blocking", "")
	case "g", "G":
		return m.runModalAction("pihole-gravity", "")
	}
	return m, nil
}

func (m *Model) keyDocker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.contIdx > 0 {
			m.contIdx--
		}
	case "down", "j":
		if m.contIdx < len(m.dk.Containers)-1 {
			m.contIdx++
		}
	case "s":
		return m.runModalAction("docker-start", "")
	case "x":
		return m.runModalAction("docker-stop", "")
	case "p":
		return m.runModalAction("docker-pause", "")
	case "u":
		return m.runModalAction("docker-unpause", "")
	case "R":
		return m.runModalAction("docker-restart", "")
	case "D":
		c, ok := m.selectedContainer()
		if !ok {
			m.setToast("no container selected", true)
			return m, nil
		}
		m.modal = dangerModal("DELETE CONTAINER // "+c.Name, strings.Join([]string{
			"This removes the container with force, including its writable layer.",
			"Images, named volumes, and bind mounts are not deleted.",
			"",
			"Container ID: " + shortID(c.ID),
			"",
			"Type DELETE to confirm.",
		}, "\n"), "DELETE", "docker-delete")
		return m, nil
	case "l":
		return m.runModalAction("docker-logs", "")
	}
	return m, nil
}

func (m *Model) keyTashnet(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cfg.Tashnet.Role != "main" {
		if msg.String() == "j" || msg.String() == "J" {
			return m.runModalAction("client-rejoin-help", "")
		}
		return m, nil
	}
	pending := sortedPending(m.cfg.Tashnet.Pending)
	devices := sortedDevices(m.cfg.Tashnet.Devices)
	switch msg.String() {
	case "up", "k":
		if len(pending) > 0 && m.pendIdx > 0 {
			m.pendIdx--
		} else if len(pending) == 0 && m.devIdx > 0 {
			m.devIdx--
		}
	case "down", "j":
		if len(pending) > 0 && m.pendIdx < len(pending)-1 {
			m.pendIdx++
		} else if len(pending) == 0 && m.devIdx < len(devices)-1 {
			m.devIdx++
		}
	case "a", "A":
		return m.runModalAction("approve-pending", "")
	case "d", "D":
		return m.runModalAction("deny-pending", "")
	case "s", "S":
		return m.runModalAction("show-secret", "")
	case "g", "G":
		m.modal = dangerModal("REGENERATE SECRET // EXTREME WARNING", strings.Join([]string{
			"THIS IS THE EXTREME-CONSEQUENCE ACTION.",
			"",
			"Regenerating the Tashnet secret key immediately:",
			"  • disconnects EVERY existing device",
			"  • invalidates every approved token",
			"  • rejects every pending join request",
			"  • requires every device to rejoin with the new key",
			"",
			"The main device stays main. This cannot be undone.",
			"",
			"Type REGENERATE to confirm.",
		}, "\n"), "REGENERATE", "regen-secret")
		return m, nil
	case "t", "T":
		return m.runModalAction("transfer", "")
	}
	return m, nil
}

func (m *Model) keyNAS(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.nasIdx > 0 {
			m.nasIdx--
		}
	case "down", "j":
		if entries, err := m.nasEntries(); err == nil && m.nasIdx < len(entries)-1 {
			m.nasIdx++
		}
	case "enter":
		e, ok := m.nasSelected()
		if !ok {
			return m, nil
		}
		if e.Parent {
			m.nasUp()
			return m, nil
		}
		if e.Dir {
			m.nasPath = cleanNASPath(e.Path)
			m.nasIdx = 0
			return m, nil
		}
		abs := filepath.Join(m.cfg.NAS.Root, e.Path)
		if err := m.editor.Load(abs); err != nil {
			m.setToast("open: "+err.Error(), true)
			return m, nil
		}
		if m.store != nil {
			_ = m.store.Update(func(c *config.Config) error {
				c.LastEditorFile = m.editor.Path()
				return nil
			})
		}
		m.setTab(TabEditor)
		return m, nil
	case "backspace", "left", "h":
		m.nasUp()
	case "s":
		if m.nas != nil && m.nas.Running() {
			return m.runModalAction("nas-stop", "")
		}
		return m.runModalAction("nas-start", "")
	case "p":
		m.showPass = !m.showPass
		if m.showPass {
			m.setToast("NAS password revealed", false)
		}
	case "P":
		m.modal = inputModal("SET NAS PASSWORD", "New WebDAV password (blank = random):", "", strings.Join([]string{
			"Changing the password restarts the NAS server if it is running.",
			"Connected clients must update their saved credentials.",
		}, "\n"), "nas-password", true)
	case "R":
		m.modal = confirmModal("RANDOM NAS PASSWORD", "Generate a new random 16-character password and restart the NAS server?", "Generate", "nas-password-random")
	}
	return m, nil
}

func (m *Model) nasUp() {
	rel := cleanNASPath(m.nasPath)
	if rel == "" {
		return
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." || parent == "/" {
		parent = ""
	}
	m.nasPath = cleanNASPath(parent)
	m.nasIdx = 0
}

func (m *Model) keyAI(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		if m.aiBusy {
			m.setToast("AI is still thinking…", false)
			return m, nil
		}
		return m, m.aiSend()
	case "up":
		m.aiScroll++
	case "down":
		if m.aiScroll > 0 {
			m.aiScroll--
		}
	case "pgup":
		m.aiScroll += 8
	case "pgdown":
		m.aiScroll = maxInt(m.aiScroll-8, 0)
	case "left":
		m.aiCursor = maxInt(m.aiCursor-1, 0)
	case "right":
		m.aiCursor = minInt(m.aiCursor+1, len([]rune(m.aiInput)))
	case "home", "ctrl+a":
		m.aiCursor = 0
	case "end", "ctrl+e":
		m.aiCursor = len([]rune(m.aiInput))
	case "backspace":
		m.aiDeleteBack()
	case "delete", "ctrl+d":
		m.aiDeleteForward()
	case "ctrl+u":
		m.aiInput = ""
		m.aiCursor = 0
	case "ctrl+w":
		m.aiDeleteWord()
	case "ctrl+n":
		m.aiNewChat()
		m.setToast("Started a new AI conversation", false)
	case "ctrl+r":
		return m, m.aiDetectCmd()
	case "ctrl+o":
		m.modal = inputModal("AI WORKDIR", "Project directory:", m.aiWorkDir(), "AI runs here. Blank uses the current directory.", "set-ai-workdir", false)
	case "ctrl+y":
		m.modal = inputModal("AI API KEY", "API key (blank clears):", m.cfg.AI.APIKey, "Stored in config.json with 0600 permissions.", "set-ai-key", true)
	case "ctrl+l":
		m.modal = inputModal("AI MODEL", "Model name (blank = default):", m.cfg.AI.Model, "Enter the model identifier for your provider.", "set-ai-model", false)
	case "tab":
		m.cycleTab(1)
	case "shift+tab":
		m.cycleTab(-1)
	case "esc":
		if m.aiInput != "" {
			m.aiInput = ""
			m.aiCursor = 0
		}
	case " ":
		m.aiInsert(" ")
	default:
		if len(key) == 1 && key >= " " && key <= "~" {
			m.aiInsert(key)
		}
	}
	return m, nil
}

func (m *Model) aiSend() tea.Cmd {
	prompt := strings.TrimSpace(m.aiInput)
	if prompt == "" {
		return nil
	}
	if !m.cfg.AI.Enabled {
		m.setToast("AI integration is disabled in Settings", true)
		return nil
	}
	m.aiChat = append(m.aiChat, aiTurn{Role: "you", Text: prompt})
	m.aiInput = ""
	m.aiCursor = 0
	m.aiBusy = true
	m.aiChatErr = ""
	m.aiScroll = 0
	return m.aiChatCmd(prompt, m.aiWorkDir(), m.aiSession, m.aiGen)
}

func (m *Model) aiNewChat() {
	m.aiChat = nil
	m.aiSession = ""
	m.aiInput = ""
	m.aiCursor = 0
	m.aiScroll = 0
	m.aiBusy = false
	m.aiChatErr = ""
	m.aiGen++
}

func (m *Model) aiInsert(text string) {
	if text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.NewReplacer("\n", " ", "\r", " ", "\t", "  ").Replace(text)
	r := []rune(m.aiInput)
	c := minInt(m.aiCursor, len(r))
	added := []rune(text)
	m.aiInput = string(r[:c]) + string(added) + string(r[c:])
	m.aiCursor = c + len(added)
}

func (m *Model) aiDeleteBack() {
	r := []rune(m.aiInput)
	c := minInt(m.aiCursor, len(r))
	if c <= 0 {
		return
	}
	m.aiInput = string(r[:c-1]) + string(r[c:])
	m.aiCursor = c - 1
}

func (m *Model) aiDeleteForward() {
	r := []rune(m.aiInput)
	c := minInt(m.aiCursor, len(r))
	if c >= len(r) {
		return
	}
	m.aiInput = string(r[:c]) + string(r[c+1:])
}

func (m *Model) aiDeleteWord() {
	r := []rune(m.aiInput)
	c := minInt(m.aiCursor, len(r))
	if c <= 0 {
		return
	}
	start := c
	for start > 0 && r[start-1] == ' ' {
		start--
	}
	for start > 0 && r[start-1] != ' ' {
		start--
	}
	m.aiInput = string(r[:start]) + string(r[c:])
	m.aiCursor = start
}

func (m *Model) keySettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.settings {
	case settingsThemes:
		names := m.themeNames()
		switch msg.String() {
		case "up", "k":
			if m.themeIdx > 0 {
				m.themeIdx--
				m.applyTheme(names[m.themeIdx], false)
			}
		case "down", "j":
			if m.themeIdx < len(names)-1 {
				m.themeIdx++
				m.applyTheme(names[m.themeIdx], false)
			}
		case "enter":
			if len(names) > 0 {
				m.applyTheme(names[m.themeIdx], true)
				m.setToast("theme applied: "+names[m.themeIdx], false)
			}
		case "c", "C":
			m.modal = inputModal("COPY THEME TO CUSTOM", "New theme name (a-z, 0-9, dash):", "", strings.Join([]string{
				"Creates a custom JSON theme based on the current theme and opens",
				"it in the vim editor. Saved under the Tash config directory.",
			}, "\n"), "theme-copy", false)
		}
		return m, nil

	case settingsTabs:
		order := m.tabOrderDefs()
		switch msg.String() {
		case "up", "k":
			if m.tabCfgIdx > 0 {
				m.tabCfgIdx--
			}
		case "down", "j":
			if m.tabCfgIdx < len(order)-1 {
				m.tabCfgIdx++
			}
		case "J", "shift+down":
			m.moveTab(m.tabCfgIdx, 1)
			if m.tabCfgIdx < len(m.tabOrderDefs())-1 {
				m.tabCfgIdx++
			}
		case "K", "shift+up":
			m.moveTab(m.tabCfgIdx, -1)
			if m.tabCfgIdx > 0 {
				m.tabCfgIdx--
			}
		case " ":
			if m.tabCfgIdx >= 0 && m.tabCfgIdx < len(order) {
				m.toggleTabVisibility(order[m.tabCfgIdx].ID)
			}
		default:
			key := msg.String()
			if len(key) == 1 && key[0] >= '1' && key[0] <= '9' && m.tabCfgIdx >= 0 && m.tabCfgIdx < len(order) {
				m.assignTabNumber(order[m.tabCfgIdx].ID, key)
			}
		}
		return m, nil

	default:
		switch msg.String() {
		case "up", "k":
			if m.settingsIdx > 0 {
				m.settingsIdx--
			}
		case "down", "j":
			if m.settingsIdx < len(m.settingsRows())-1 {
				m.settingsIdx++
			}
		case "enter":
			return m.activateSetting()
		}
		return m, nil
	}
}

func (m *Model) activateSetting() (tea.Model, tea.Cmd) {
	switch m.settingsIdx {
	case 0:
		m.settings = settingsThemes
		m.syncThemeIndex()
	case 1:
		m.updateAI(func(c *config.AIConfig) {
			c.Enabled = !c.Enabled
		})
	case 2:
		m.modal = inputModal("AI PROVIDER", "Provider (claude, openai, anthropic, custom):", m.cfg.AI.Provider, "Choose your AI provider.", "set-ai-provider", false)
	case 3:
		m.modal = inputModal("AI BASE URL", "Custom API base URL (blank = default):", m.cfg.AI.BaseURL, "For OpenAI-compatible APIs, e.g. https://api.openai.com/v1", "set-ai-baseurl", false)
	case 4:
		m.modal = inputModal("AI API KEY", "API key (blank clears):", m.cfg.AI.APIKey, "Stored in config.json with 0600 permissions.", "set-ai-key", true)
	case 5:
		m.modal = inputModal("AI MODEL", "Model name (blank = default):", m.cfg.AI.Model, "Enter the model identifier for your provider.", "set-ai-model", false)
	case 6:
		m.modal = inputModal("AI COMMAND", "CLI name or absolute path (for claude provider):", m.cfg.AI.Command, "Default: claude", "set-ai-command", false)
	case 7:
		m.modal = inputModal("AI WORKDIR", "Default project directory:", m.cfg.AI.WorkDir, "Blank uses the current directory or the selected project.", "set-ai-workdir", false)
	case 8:
		m.modal = inputModal("AI PERMISSION MODE", "default, acceptEdits, plan, or bypassPermissions:", m.cfg.AI.PermissionMode, "Passed as --permission-mode for one-shot prompts.", "set-ai-permission", false)
	case 9:
		m.modal = inputModal("SET NAS PASSWORD", "New WebDAV password (blank = random):", "", "The NAS server restarts if it is running.", "nas-password", true)
	case 10:
		m.modal = inputModal("NAS ROOT", "Folder exposed over WebDAV:", m.cfg.NAS.Root, "Created automatically when the NAS starts.", "set-nas-root", false)
	case 11:
		m.modal = inputModal("REFRESH KEY", "Global refresh key:", m.keybind("refresh"), "Examples: r, R, ctrl+r", "set-key-refresh", false)
	case 12:
		m.modal = inputModal("HELP KEY", "About/help key:", m.keybind("help"), "Examples: ?, h, ctrl+h", "set-key-help", false)
	case 13:
		m.modal = inputModal("QUIT KEY", "Quit key:", m.keybind("quit"), "Examples: q, Q, ctrl+q. ctrl+c always quits.", "set-key-quit", false)
	case 14:
		m.settings = settingsTabs
		m.tabCfgIdx = 0
	case 15:
		m.updateUI(func(c *config.UIConfig) {
			c.Splash = !c.Splash
		})
	case 16:
		m.updateUI(func(c *config.UIConfig) {
			c.Clock24 = !c.Clock24
		})
	case 17:
		m.cycleDeckLayout()
	case 18:
		m.updateUI(func(c *config.UIConfig) {
			if c.TabPosition == "top" {
				c.TabPosition = "bottom"
			} else {
				c.TabPosition = "top"
			}
		})
	case 19:
		m.modal = infoModal("ABOUT TASH // OPERATOR MANUAL", m.aboutBody())
	}
	return m, nil
}

func (m *Model) updateUI(fn func(*config.UIConfig)) {
	if m.store == nil {
		m.setToast("no configuration", true)
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		fn(&c.UI)
		return nil
	}); err != nil {
		m.setToast("settings: "+err.Error(), true)
		return
	}
	m.cfg = m.store.Snapshot()
}

func (m *Model) updateAI(fn func(*config.AIConfig)) {
	if m.store == nil {
		m.setToast("no configuration", true)
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		fn(&c.AI)
		return nil
	}); err != nil {
		m.setToast("settings: "+err.Error(), true)
		return
	}
	m.cfg = m.store.Snapshot()
}

func (m *Model) assignTabNumber(id, number string) {
	if m.store == nil {
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		for other, num := range c.UI.TabNumbers {
			if other != id && num == number {
				c.UI.TabNumbers[other] = c.UI.TabNumbers[id]
			}
		}
		c.UI.TabNumbers[id] = number
		return nil
	}); err != nil {
		m.setToast("tabs: "+err.Error(), true)
		return
	}
	m.cfg = m.store.Snapshot()
	m.setToast("tab "+id+" -> key "+number, false)
}

func (m *Model) toggleTabVisibility(id string) {
	if id == "settings" {
		m.setToast("the settings tab is always visible", true)
		return
	}
	if m.store == nil {
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		hidden := make([]string, 0, len(c.UI.HiddenTabs)+1)
		found := false
		for _, h := range c.UI.HiddenTabs {
			if h == id {
				found = true
				continue
			}
			hidden = append(hidden, h)
		}
		if !found {
			hidden = append(hidden, id)
		}
		c.UI.HiddenTabs = hidden
		return nil
	}); err != nil {
		m.setToast("tabs: "+err.Error(), true)
		return
	}
	m.cfg = m.store.Snapshot()
	if !m.tabVisible(m.tab) {
		m.setTab(TabSettings)
	}
}
