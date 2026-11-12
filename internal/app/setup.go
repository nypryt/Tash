package app

import (
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tashnet"
	"github.com/tash/tash/internal/theme"
)

type setupStage int

const (
	stageWelcome setupStage = iota
	stageMainSecret
	stageJoinSecret
	stageJoinURL
	stageJoinApproval
	stageDocker
	stageDockerHost
	stageTheme
	stagePiHole
	stagePiHoleURL
	stagePiHoleSecret
	stageNAS
	stageFinish
)

type setupState struct {
	stage setupStage
	role  string

	secret        string
	deviceID      string
	deviceName    string
	secretVersion int

	input       string
	inputCursor int
	inputSecret bool
	err         string
	busy        bool

	// Join flow.
	discovery      []tashnet.Discovered
	pendingID      string
	pollToken      string
	mainURL        string
	mainDeviceID   string
	mainDeviceName string
	token          string
	joinApproved   bool

	// Choices.
	dockerMode string
	dockerHost string
	themeName  string
	piholeOn   bool
	piholeURL  string
	piholePass string
	nasAuto    bool
}

func newSetup() setupState {
	return setupState{
		stage: stageWelcome, dockerMode: "cli", dockerHost: config.DefaultDockerHost(),
		themeName: "matrix",
	}
}

func (s *setupState) begin(m *Model, opts Options) {
	def := config.Default()
	s.deviceID = def.Tashnet.DeviceID
	s.deviceName = def.Tashnet.DeviceName
	s.dockerMode = def.Docker.Mode
	s.dockerHost = def.Docker.Host
	s.themeName = def.UI.Theme
	if opts.Theme != "" {
		s.themeName = opts.Theme
	}
	m.applyTheme(s.themeName, false)
}

func (s *setupState) setInput(v string) {
	s.input = v
	s.inputCursor = len([]rune(v))
}

func (s *setupState) textKey(msg tea.KeyMsg) (submit, cancel bool) {
	r := []rune(s.input)
	if s.inputCursor > len(r) {
		s.inputCursor = len(r)
	}
	if s.inputCursor < 0 {
		s.inputCursor = 0
	}
	switch msg.Type {
	case tea.KeyEsc:
		return false, true
	case tea.KeyEnter:
		return true, false
	case tea.KeyBackspace:
		if s.inputCursor > 0 {
			r = append(r[:s.inputCursor-1], r[s.inputCursor:]...)
			s.inputCursor--
		}
	case tea.KeyDelete:
		if s.inputCursor < len(r) {
			r = append(r[:s.inputCursor], r[s.inputCursor+1:]...)
		}
	case tea.KeyLeft:
		if s.inputCursor > 0 {
			s.inputCursor--
		}
	case tea.KeyRight:
		if s.inputCursor < len(r) {
			s.inputCursor++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		s.inputCursor = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		s.inputCursor = len(r)
	case tea.KeyCtrlU:
		r = r[:0]
		s.inputCursor = 0
	case tea.KeySpace:
		r = append(r[:s.inputCursor], append([]rune{' '}, r[s.inputCursor:]...)...)
		s.inputCursor++
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			runes := []rune(strings.ReplaceAll(string(msg.Runes), "\n", ""))
			r = append(r[:s.inputCursor], append(runes, r[s.inputCursor:]...)...)
			s.inputCursor += len(runes)
		}
	}
	s.input = string(r)
	return false, false
}

func (s *setupState) maskedInput() string {
	if !s.inputSecret {
		return s.input
	}
	return strings.Repeat("•", len([]rune(s.input)))
}

func (s *setupState) startMain() {
	secret, err := tashnet.GenerateSecret()
	if err != nil {
		s.err = "could not generate a secret key: " + err.Error()
		return
	}
	s.role = "main"
	s.secret = secret
	s.stage = stageMainSecret
	s.err = ""
	s.input = ""
}

func (s *setupState) startJoin() {
	s.role = "client"
	s.secret = ""
	s.stage = stageJoinSecret
	s.err = ""
	s.inputSecret = true
	s.setInput("")
}

func (m *Model) handleSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.setup
	key := msg.String()

	if key == "esc" {
		if s.stage == stageWelcome {
			m.quitting = true
			return m, tea.Quit
		}
		if s.busy {
			return m, nil
		}
		m.setupBack()
		return m, nil
	}
	if key == "q" && s.stage == stageWelcome {
		m.quitting = true
		return m, tea.Quit
	}
	if key == "ctrl+r" && (s.stage == stageJoinSecret || s.stage == stagePiHoleSecret) {
		s.inputSecret = !s.inputSecret
		return m, nil
	}
	if key == "ctrl+d" && s.stage == stageJoinURL {
		return m, m.discoverCmd()
	}

	switch s.stage {
	case stageWelcome:
		switch key {
		case "1", "m", "M":
			s.startMain()
		case "2", "j", "J":
			s.startJoin()
		case "enter":
			s.startMain()
		}
		return m, nil

	case stageMainSecret:
		switch key {
		case "r", "R":
			secret, err := tashnet.GenerateSecret()
			if err != nil {
				s.err = err.Error()
			} else {
				s.secret = secret
				s.err = ""
				m.setToast("new setup secret generated", false)
			}
		case "enter":
			s.stage = stageDocker
			s.err = ""
		}
		return m, nil

	case stageJoinSecret:
		submit, cancel := s.textKey(msg)
		if cancel {
			m.setupBack()
			return m, nil
		}
		if submit {
			norm, err := tashnet.NormalizeSecret(s.input)
			if err != nil {
				s.err = err.Error()
				return m, nil
			}
			s.secret = norm
			s.stage = stageJoinURL
			s.err = ""
			s.busy = false
			s.setInput("")
			m.discovery = nil
			return m, m.discoverCmd()
		}
		s.err = ""

	case stageJoinURL:
		submit, cancel := s.textKey(msg)
		if cancel {
			m.setupBack()
			return m, nil
		}
		if submit {
			target := strings.TrimSpace(s.input)
			if target == "" && len(m.discovery) > 0 {
				target = m.discovery[0].URL
			}
			if target == "" {
				s.err = "enter the main device address (example: 100.64.0.5:8787)"
				return m, nil
			}
			norm, err := tashnet.NormalizeURL(target)
			if err != nil {
				s.err = err.Error()
				return m, nil
			}
			s.mainURL = norm
			s.busy = true
			s.err = ""
			return m, m.joinCmd(norm)
		}
		s.err = ""

	case stageJoinApproval:
		// Polling is automatic. Esc returns to the address field.
		return m, nil

	case stageDocker:
		switch key {
		case "up", "k":
			if s.dockerIndex() > 0 {
				s.setDockerIndex(s.dockerIndex() - 1)
			}
		case "down", "j":
			if s.dockerIndex() < 2 {
				s.setDockerIndex(s.dockerIndex() + 1)
			}
		case "1":
			s.dockerMode = "cli"
		case "2":
			s.dockerMode = "socket"
		case "3":
			s.dockerMode = "off"
		case "enter":
			if s.dockerMode == "socket" {
				s.inputSecret = false
				s.setInput(s.dockerHost)
				s.stage = stageDockerHost
			} else {
				m.enterTheme()
			}
		}
		return m, nil

	case stageDockerHost:
		submit, cancel := s.textKey(msg)
		if cancel {
			s.stage = stageDocker
			return m, nil
		}
		if submit {
			v := strings.TrimSpace(s.input)
			if v == "" {
				v = config.DefaultDockerHost()
			}
			s.dockerHost = v
			m.enterTheme()
		}
		s.err = ""

	case stageTheme:
		names := m.themeNames()
		switch key {
		case "up", "k":
			if m.themeIdx > 0 {
				m.themeIdx--
				m.previewSetupTheme(names)
			}
		case "down", "j":
			if m.themeIdx < len(names)-1 {
				m.themeIdx++
				m.previewSetupTheme(names)
			}
		case "enter":
			s.themeName = m.currentSetupTheme(names)
			s.stage = stagePiHole
			s.err = ""
		}
		return m, nil

	case stagePiHole:
		switch key {
		case "y", "Y", "1":
			s.piholeOn = true
			s.stage = stagePiHoleURL
			s.setInput("http://")
			s.inputSecret = false
			s.err = ""
		case "n", "N", "2", "enter":
			s.piholeOn = false
			s.piholeURL = ""
			s.piholePass = ""
			s.stage = stageNAS
			s.err = ""
		}
		return m, nil

	case stagePiHoleURL:
		submit, cancel := s.textKey(msg)
		if cancel {
			s.stage = stagePiHole
			return m, nil
		}
		if submit {
			v := strings.TrimSpace(s.input)
			if v == "" {
				s.err = "Pi-hole URL is required (or press Esc and skip)"
				return m, nil
			}
			u, err := url.Parse(v)
			if err != nil || u.Host == "" {
				s.err = "enter a URL like http://192.168.1.2 or https://pi.hole"
				return m, nil
			}
			s.piholeURL = strings.TrimRight(v, "/")
			s.inputSecret = true
			s.setInput("")
			s.stage = stagePiHoleSecret
			s.err = ""
		}
		s.err = ""

	case stagePiHoleSecret:
		submit, cancel := s.textKey(msg)
		if cancel {
			s.inputSecret = false
			s.stage = stagePiHoleURL
			s.setInput(s.piholeURL)
			return m, nil
		}
		if submit {
			s.piholePass = strings.TrimSpace(s.input)
			s.inputSecret = false
			s.stage = stageNAS
			s.err = ""
		}
		s.err = ""

	case stageNAS:
		switch key {
		case "y", "Y", "1":
			s.nasAuto = true
			s.stage = stageFinish
			s.err = ""
		case "n", "N", "2", "enter":
			s.nasAuto = false
			s.stage = stageFinish
			s.err = ""
		}
		return m, nil

	case stageFinish:
		if key == "enter" || key == "s" {
			return m.finishSetup()
		}
	}
	return m, nil
}

func (s *setupState) dockerIndex() int {
	switch s.dockerMode {
	case "socket":
		return 1
	case "off":
		return 2
	default:
		return 0
	}
}

func (s *setupState) setDockerIndex(i int) {
	switch i {
	case 1:
		s.dockerMode = "socket"
	case 2:
		s.dockerMode = "off"
	default:
		s.dockerMode = "cli"
	}
}

func (m *Model) enterTheme() {
	m.syncThemeIndex()
	m.previewSetupTheme(m.themeNames())
	m.setup.stage = stageTheme
	m.setup.err = ""
}

func (m *Model) previewSetupTheme(names []string) {
	if len(names) == 0 {
		return
	}
	if m.themeIdx < 0 {
		m.themeIdx = 0
	}
	if m.themeIdx >= len(names) {
		m.themeIdx = len(names) - 1
	}
	m.setup.themeName = names[m.themeIdx]
	m.applyTheme(m.setup.themeName, false)
}

func (m *Model) currentSetupTheme(names []string) string {
	if len(names) == 0 {
		return m.setup.themeName
	}
	if m.themeIdx < 0 || m.themeIdx >= len(names) {
		return m.setup.themeName
	}
	return names[m.themeIdx]
}

func (m *Model) setupBack() {
	s := &m.setup
	s.err = ""
	switch s.stage {
	case stageWelcome:
		m.quitting = true
	case stageMainSecret:
		s.secret = ""
		s.stage = stageWelcome
	case stageJoinSecret:
		s.stage = stageWelcome
	case stageJoinURL:
		s.stage = stageJoinSecret
		s.inputSecret = true
		s.setInput(s.secret)
	case stageJoinApproval:
		s.stage = stageJoinURL
		s.pendingID = ""
		s.pollToken = ""
		s.busy = false
		s.inputSecret = false
		s.setInput(s.mainURL)
	case stageDocker:
		if s.role == "main" {
			s.stage = stageMainSecret
		} else {
			s.stage = stageJoinURL
			s.setInput(s.mainURL)
		}
	case stageDockerHost:
		s.stage = stageDocker
	case stageTheme:
		s.stage = stageDocker
	case stagePiHole:
		s.stage = stageTheme
	case stagePiHoleURL:
		s.stage = stagePiHole
	case stagePiHoleSecret:
		s.stage = stagePiHoleURL
		s.inputSecret = false
		s.setInput(s.piholeURL)
	case stageNAS:
		if s.piholeOn {
			s.stage = stagePiHoleSecret
			s.inputSecret = true
			s.setInput(s.piholePass)
		} else {
			s.stage = stagePiHole
		}
	case stageFinish:
		s.stage = stageNAS
	}
}

func (m *Model) finishSetup() (tea.Model, tea.Cmd) {
	s := &m.setup
	if s.role == "" {
		s.role = "main"
	}
	cfg := m.cfg
	cfg.UI.Theme = s.themeName
	cfg.Docker.Mode = s.dockerMode
	cfg.Docker.Host = s.dockerHost
	cfg.PiHole.Enabled = s.piholeOn
	cfg.PiHole.URL = s.piholeURL
	cfg.PiHole.Secret = s.piholePass
	cfg.PiHole.RefreshSec = 5
	cfg.NAS.AutoStart = s.nasAuto
	cfg.Tashnet.Role = s.role
	cfg.Tashnet.DeviceID = s.deviceID
	cfg.Tashnet.DeviceName = s.deviceName
	cfg.Tashnet.Devices = map[string]config.Device{}
	cfg.Tashnet.Pending = map[string]config.Pending{}
	cfg.Tashnet.SecretVersion = 1
	secret := ""
	message := ""

	if s.role == "main" {
		secret = s.secret
		if secret == "" {
			s.err = "secret key is missing; press Esc and start again"
			return m, nil
		}
		now := time.Now().UTC()
		cfg.Tashnet.MainDeviceID = s.deviceID
		cfg.Tashnet.MainDeviceName = s.deviceName
		cfg.Tashnet.Devices[s.deviceID] = config.Device{
			ID: s.deviceID, Name: s.deviceName, OS: m.info.OS, Arch: m.info.Arch,
			IP: m.info.LocalIP, Token: tashnet.NewToken(), ApprovedAt: now, LastSeen: now,
		}
		message = "Main device online. Open TASHNET to view or regenerate the secret key."
	} else {
		if !s.joinApproved || s.token == "" {
			s.err = "this device has not been approved by the main device yet"
			return m, nil
		}
		cfg.Tashnet.MainURL = s.mainURL
		cfg.Tashnet.MainDeviceID = s.mainDeviceID
		cfg.Tashnet.MainDeviceName = s.mainDeviceName
		cfg.Tashnet.Token = s.token
		cfg.Tashnet.SecretVersion = s.secretVersion
		secret = ""
		message = "Joined " + s.mainDeviceName + "'s Tashnet."
	}

	store, err := config.Create(cfg, secret)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	m.themeIdx = 0
	return m, func() tea.Msg { return setupDoneMsg{store: store, message: message} }
}

// enterCmd runs when the setup screen first appears.
func (s *setupState) enterCmd(m *Model) tea.Cmd { return nil }

func (s *setupState) dockerOptions() []string { return []string{"cli", "socket", "off"} }

func (s *setupState) themeDisplayName(name string) string {
	t := theme.Builtins()[strings.ToLower(name)]
	if t.Name != "" {
		return t.Name
	}
	if name == "" {
		return "matrix"
	}
	return name
}
