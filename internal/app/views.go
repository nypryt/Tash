package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tailscale"
	"github.com/tash/tash/internal/tashnet"
	"github.com/tash/tash/internal/theme"
	"github.com/tash/tash/internal/ui"
	"github.com/tash/tash/internal/version"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *Model) frame() string {
	if len(spinnerFrames) == 0 {
		return "·"
	}
	return spinnerFrames[m.spinner%len(spinnerFrames)]
}

// fitBlock forces a rendered block to exactly w x h cells.
func fitBlock(block string, w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	out := make([]string, 0, h)
	for _, line := range lines {
		out = append(out, ui.Fit(line, w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return strings.Join(out, "\n")
}

func centerBlock(block string, w, h int) string {
	lines := strings.Split(block, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	top := (h - len(lines)) / 2
	if top < 0 {
		top = 0
	}
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	for _, line := range lines {
		pad := (w - lipgloss.Width(line)) / 2
		if pad < 0 {
			pad = 0
		}
		out = append(out, strings.Repeat(" ", pad)+line)
	}
	return fitBlock(strings.Join(out, "\n"), w, h)
}

func (m *Model) viewSplash() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return "TASH"
	}
	logo := m.styles.Logo(w)
	block := []string{
		logo,
		"",
		m.styles.Muted.Render("terminal control deck  ·  tashnet secure mesh"),
		"",
		m.styles.Accent.Render(m.frame() + "  INITIALIZING TASHNET CONTROL PLANE"),
		"",
		m.styles.Muted.Render(fmt.Sprintf("v%s  ·  %s/%s  ·  %s", versionString(), m.info.OS, m.info.Arch, m.deviceLabel())),
	}
	return centerBlock(strings.Join(block, "\n"), w, h)
}

func versionString() string {
	v := version.Version
	if strings.HasPrefix(v, "v") {
		v = v[1:]
	}
	return v
}

func (m *Model) viewDashboard() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return "TASH"
	}
	bodyH := m.bodyHeight()
	role := "MAIN"
	if m.cfg.Tashnet.Role == "client" {
		role = "CLIENT"
	}
	header := m.styles.Header(w, m.cfg.Tashnet.Name, role, m.deviceLabel(), m.now, m.cfg.UI.Clock24)
	specs := m.visibleTabSpecs()
	active := 0
	for i, d := range m.visibleTabDefs() {
		if d.Tab == m.tab {
			active = i
			break
		}
	}
	tabs := m.styles.TabsWithKeys(specs, active, w)

	var body string
	if m.modal.active() {
		body = centerBlock(m.modalView(), w, bodyH)
	} else {
		body = m.tabBody(m.tab)
	}
	body = fitBlock(body, w, bodyH)
	status := m.statusLine(w)
	footer := m.styles.Footer(w, m.footerHints())

	tabPos := m.cfg.UI.TabPosition
	if tabPos != "bottom" {
		tabPos = "top"
	}

	if tabPos == "bottom" {
		return strings.Join([]string{header, body, status, tabs, footer}, "\n")
	}
	return strings.Join([]string{header, tabs, body, status, footer}, "\n")
}

func (m *Model) statusLine(w int) string {
	var line string
	if m.toastActive() {
		if m.toastBad {
			line = m.styles.Danger.Render("▸ " + m.toast)
		} else {
			line = m.styles.Success.Render("▸ " + m.toast)
		}
	} else if m.startErr != "" {
		line = m.styles.Danger.Render("▸ " + m.startErr)
	} else {
		sync := "no sync yet"
		if !m.dk.FetchedAt.IsZero() || !m.ph.FetchedAt.IsZero() || !m.ts.FetchedAt.IsZero() {
			sync = "services synced " + ui.HumanAgo(m.lastServiceSync())
		}
		line = m.styles.Muted.Render("▸ "+sync) + m.styles.Muted.Render("  ·  ") +
			m.styles.Key.Render("r") + m.styles.Muted.Render(" refresh")
	}
	return ui.Fit(" "+line, w)
}

func (m *Model) lastServiceSync() time.Time {
	t := m.ts.FetchedAt
	if m.ph.FetchedAt.After(t) {
		t = m.ph.FetchedAt
	}
	if m.dk.FetchedAt.After(t) {
		t = m.dk.FetchedAt
	}
	return t
}

func (m *Model) tabBody(t Tab) string {
	switch t {
	case TabTailscale:
		return m.viewTailscale()
	case TabPiHole:
		return m.viewPiHole()
	case TabDocker:
		return m.viewDocker()
	case TabTashnet:
		return m.viewTashnet()
	case TabNAS:
		return m.viewNAS()
	case TabEditor:
		return m.editor.View()
	case TabAI:
		return m.viewAI()
	case TabSettings:
		return m.viewSettings()
	default:
		return m.viewOverview()
	}
}

func (m *Model) footerHints() [][2]string {
	if m.modal.active() {
		return [][2]string{{"esc", "close"}, {"enter", "confirm"}}
	}
	if m.tab == TabEditor {
		return [][2]string{{"ctrl+1..9", "tabs"}, {"ctrl+o", "open"}, {"ctrl+n", "new"}, {":w", "save"}, {":q", "close"}, {"ctrl+c", "quit"}}
	}
	hints := [][2]string{{"1..9", "tabs"}, {m.keybind("refresh"), "refresh"}}
	switch m.tab {
	case TabTailscale:
		hints = append(hints, [2]string{"↑↓", "peer"}, [2]string{"p", "ping"}, [2]string{"P", "ping all"}, [2]string{"s", "ssh"})
	case TabPiHole:
		hints = append(hints, [2]string{"a", "add block"}, [2]string{"A", "add allow"}, [2]string{"b", "blocking"}, [2]string{"g", "gravity"})
	case TabDocker:
		hints = append(hints, [2]string{"↑↓", "container"}, [2]string{"s/x", "start/stop"}, [2]string{"p/u", "pause/resume"}, [2]string{"R", "restart"}, [2]string{"D", "delete"}, [2]string{"l", "logs"}, [2]string{"f", "follow logs"}, [2]string{"e", "exec command"})
	case TabTashnet:
		if m.cfg.Tashnet.Role == "main" {
			hints = append(hints, [2]string{"a/d", "approve/deny"}, [2]string{"s", "show key"}, [2]string{"g", "regen key"}, [2]string{"t", "transfer"}, [2]string{"x", "ssh to device"})
		} else {
			hints = append(hints, [2]string{"j", "rejoin help"})
		}
	case TabNAS:
		hints = append(hints, [2]string{"s", "start/stop"}, [2]string{"P", "set password"}, [2]string{"p", "reveal"}, [2]string{"enter", "open"}, [2]string{"⌫", "up"})
	case TabAI:
		hints = append(hints, [2]string{"c", "launch"}, [2]string{"p", "prompt"}, [2]string{"w", "workdir"}, [2]string{"a", "api key"}, [2]string{"m", "models"}, [2]string{"↑↓", "project"}, [2]string{"enter", "use project"})
	case TabSettings:
		hints = append(hints, [2]string{"↑↓", "select"}, [2]string{"enter", "edit"}, [2]string{"esc", "back"})
	}
	hints = append(hints, [2]string{m.keybind("help"), "about"}, [2]string{m.keybind("quit"), "quit"})
	return hints
}

func (m *Model) viewOverview() string {
	w := m.contentWidth()
	bodyH := m.bodyHeight()
	layout := m.cfg.UI.DeckLayout
	switch layout {
	case "compact":
		return m.compactOverview(w)
	case "cards":
		if w < 64 || bodyH < 14 {
			return m.compactOverview(w)
		}
	default:
		if w < 88 || bodyH < 18 {
			return m.compactOverview(w)
		}
	}

	logoW := 44
	if w < 110 {
		logoW = 40
	}
	idW := w - logoW
	logo := m.styles.Panel("TASH // "+strings.ToUpper(m.cfg.Tashnet.Name), m.logoContent(), logoW, 9, true)
	identity := m.styles.Panel("NODE IDENTITY", m.identityContent(idW-4), idW, 9, false)
	row1 := lipgloss.JoinHorizontal(lipgloss.Top, logo, identity)

	cardW := (w - 2) / 3
	card1 := m.styles.Panel("TAILSCALE", m.tailscaleCard(cardW-4), cardW, 9, false)
	card2 := m.styles.Panel("PI-HOLE", m.piholeCard(cardW-4), cardW, 9, false)
	card3 := m.styles.Panel("DOCKER", m.dockerCard(cardW-4), cardW, 9, false)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, card1, card2, card3)

	half := (w - 1) / 2
	left := m.styles.Panel("TASHNET", m.tashnetCard(half-4), half, 8, false)
	right := m.styles.Panel("NAS // WEBDAV", m.nasCard(w-half-4), w-half, 8, false)
	row3 := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	return strings.Join([]string{row1, row2, row3}, "\n")
}

func (m *Model) logoContent() string {
	logo := m.styles.Logo(60)
	sub := m.styles.Muted.Render("terminal control deck")
	line := m.styles.Accent.Render("◢ SECURE MESH INTERFACE ◣")
	ver := m.styles.Muted.Render("v" + versionString() + " · " + m.info.OSName() + "/" + m.info.Arch)
	return strings.Join([]string{logo, sub, line, ver}, "\n")
}

func (m *Model) identityContent(width int) string {
	role := "MAIN DEVICE"
	if m.cfg.Tashnet.Role == "client" {
		role = "CLIENT"
	}
	rows := []string{
		m.styles.KV("host", m.deviceLabel(), 10),
		m.styles.KV("user", m.info.User, 10),
		m.styles.KV("system", m.info.OSName()+" · "+m.info.Arch, 10),
		m.styles.KV("address", firstNonEmpty(m.info.TailscaleIP, m.info.LocalIP, "offline"), 10),
		m.styles.KV("role", role, 10),
		m.styles.KV("config", config.Dir(), 10),
	}
	return strings.Join(rows, "\n")
}

func (m *Model) tailscaleCard(width int) string {
	st := m.ts
	if !st.Available {
		return strings.Join([]string{
			m.styles.Danger.Render("○ CLI unavailable"),
			m.styles.Muted.Render("Use the Tailscale app on Android;"),
			m.styles.Muted.Render("Tash will still run Pi-hole/NAS."),
		}, "\n")
	}
	filtered := m.filteredPeers()
	online := 0
	for _, p := range filtered {
		if p.Online {
			online++
		}
	}
	state := st.BackendState
	if !st.Running {
		state = "stopped"
	}
	dot := m.styles.Dot(st.Running && st.Error == "")
	line1 := dot + " " + m.styles.Text.Render(state) + m.styles.Muted.Render(fmt.Sprintf("  %d/%d peers", online, len(filtered)))
	self := "no tailscale IP"
	if st.Self.IP != "" {
		self = st.Self.IP
	}
	var spark string
	for _, p := range filtered {
		if !p.Online {
			continue
		}
		key := peerKey(p)
		if vals := m.pings[key]; len(vals) > 0 {
			spark = m.styles.Sparkline(vals, minInt(width-2, 18)) + " " + m.styles.Muted.Render(p.DisplayName())
			last := m.lastPing[key]
			if last.Err == "" {
				spark += m.styles.Accent.Render(fmt.Sprintf(" %.0fms", float64(last.Latency.Microseconds())/1000))
			}
			break
		}
	}
	if spark == "" {
		spark = m.styles.Muted.Render("no pings yet")
	}
	return strings.Join([]string{
		line1,
		m.styles.KV("self", self, 6),
		m.styles.KV("ping", spark, 6),
	}, "\n")
}

func (m *Model) piholeCard(width int) string {
	if !m.cfg.PiHole.Enabled || m.cfg.PiHole.URL == "" {
		return strings.Join([]string{
			m.styles.Muted.Render("○ not configured"),
			m.styles.Muted.Render("run `tash pihole setup`"),
			m.styles.Muted.Render("then restart the dashboard"),
		}, "\n")
	}
	if m.ph.Error != "" {
		return m.styles.Danger.Render("✖ "+truncate(m.ph.Error, width)) + "\n" + m.styles.Muted.Render(m.ph.URL)
	}
	s := m.ph.Summary
	blocked := m.styles.Danger.Render(formatCount(s.Blocked)) + m.styles.Muted.Render(" blocked")
	line1 := m.styles.Accent.Render(formatCount(s.TotalQueries)) + m.styles.Muted.Render(" queries · ") + blocked
	bar := m.styles.Bar(s.PercentBlocked, maxInt(8, width-2))
	return strings.Join([]string{
		line1,
		fmt.Sprintf("%s %s", bar, m.styles.Text.Render(fmt.Sprintf("%.1f%%", s.PercentBlocked))),
		m.styles.KV("gravity", formatCount(s.GravityDomains)+" domains", 8),
		m.styles.KV("clients", fmt.Sprintf("%d active", s.ActiveClients), 8),
	}, "\n")
}

func (m *Model) dockerCard(width int) string {
	if m.cfg.Docker.Mode == "" || m.cfg.Docker.Mode == "off" {
		return strings.Join([]string{
			m.styles.Muted.Render("○ docker disabled"),
			m.styles.Muted.Render("`tash docker use cli`"),
			m.styles.Muted.Render("or socket/API mode"),
		}, "\n")
	}
	if m.dk.Error != "" {
		return m.styles.Danger.Render("✖ "+truncate(m.dk.Error, width)) + "\n" + m.styles.Muted.Render("mode: "+m.cfg.Docker.Mode)
	}
	line1 := m.styles.Dot(m.dk.Available) + " " + m.styles.Text.Render("engine "+firstNonEmpty(m.dk.ServerVer, "?")) +
		m.styles.Muted.Render(" · "+m.cfg.Docker.Mode)
	return strings.Join([]string{
		line1,
		m.styles.KV("running", fmt.Sprintf("%d / %d containers", m.dk.Running, len(m.dk.Containers)), 8),
		m.styles.KV("os", firstNonEmpty(m.dk.OS, m.info.OSName()), 8),
		m.styles.KV("engine", firstNonEmpty(m.dk.Engine, m.cfg.Docker.Host), 8),
	}, "\n")
}

func (m *Model) tashnetCard(width int) string {
	cfg := m.cfg.Tashnet
	line1 := m.styles.Accent.Render(strings.ToUpper(cfg.Role)) + m.styles.Muted.Render(" · device "+shortID(cfg.DeviceID))
	if cfg.Role == "main" {
		url := ""
		if m.tnet != nil {
			url = m.tnet.URL()
		}
		pending := m.styles.Muted.Render("no pending joins")
		if len(cfg.Pending) > 0 {
			pending = m.styles.Warning.Render(fmt.Sprintf("%d pending approval(s)", len(cfg.Pending)))
		}
		return strings.Join([]string{
			line1,
			m.styles.KV("control", firstNonEmpty(url, fmt.Sprintf("port %d", cfg.ControlPort)), 8),
			m.styles.KV("devices", fmt.Sprintf("%d approved", len(cfg.Devices)), 8),
			pending,
		}, "\n")
	}
	status := m.styles.Muted.Render("waiting for heartbeat")
	if m.netStatus != "" {
		status = m.styles.Danger.Render(truncate(m.netStatus, maxInt(width, 12)))
	} else if m.ts.FetchedAt.After(time.Now().Add(-time.Hour)) {
		status = m.styles.Success.Render("heartbeat active")
	}
	return strings.Join([]string{
		line1,
		m.styles.KV("main", firstNonEmpty(cfg.MainDeviceName, "?"), 6),
		m.styles.KV("url", firstNonEmpty(cfg.MainURL, "not paired"), 6),
		status,
	}, "\n")
}

func (m *Model) nasCard(width int) string {
	if m.nas == nil || !m.nas.Running() {
		return strings.Join([]string{
			m.styles.Muted.Render("○ server stopped"),
			m.styles.Muted.Render("open the NAS tab and press s"),
			m.styles.Muted.Render("WebDAV · Basic auth · no root"),
		}, "\n")
	}
	return strings.Join([]string{
		m.styles.Dot(true) + " " + m.styles.Text.Render(m.nas.URL()),
		m.styles.KV("dav", m.nas.DAVURL(), 5),
		m.styles.KV("root", config.ExpandPath(m.nas.Root()), 5),
		m.styles.KV("requests", fmt.Sprintf("%d", m.nas.Requests()), 5),
	}, "\n")
}

func (m *Model) compactOverview(w int) string {
	role := strings.ToUpper(m.cfg.Tashnet.Role)
	lines := []string{
		m.styles.Primary.Bold(true).Render("◢ T A S H ◣") + m.styles.Muted.Render("  terminal control deck"),
		m.styles.Muted.Render(strings.Repeat("─", maxInt(w-2, 10))),
		m.kvLine("node", m.deviceLabel()+" · "+m.info.OSName()+"/"+m.info.Arch, w),
		m.kvLine("addr", firstNonEmpty(m.info.TailscaleIP, m.info.LocalIP, "offline"), w),
		m.kvLine("role", role+" · "+shortID(m.cfg.Tashnet.DeviceID), w),
		"",
		m.styles.SectionTitle("TAILSCALE", w),
	}
	if m.ts.Available {
		state := m.ts.BackendState
		if !m.ts.Running {
			state = "stopped"
		}
		filtered := m.filteredPeers()
		online := 0
		for _, p := range filtered {
			if p.Online {
				online++
			}
		}
		lines = append(lines, m.kvLine("state", fmt.Sprintf("%s · %d online peers", state, online), w))
	} else {
		lines = append(lines, m.styles.Muted.Render("CLI unavailable — use the Tailscale Android app on Termux"))
	}
	lines = append(lines, "", m.styles.SectionTitle("PI-HOLE", w))
	if m.cfg.PiHole.Enabled && m.ph.Error == "" {
		s := m.ph.Summary
		lines = append(lines, m.kvLine("queries", fmt.Sprintf("%s · %s blocked (%.1f%%)", formatCount(s.TotalQueries), formatCount(s.Blocked), s.PercentBlocked), w))
		if w >= 36 {
			lines = append(lines, m.styles.Bar(s.PercentBlocked, w-4))
		}
	} else if m.ph.Error != "" {
		lines = append(lines, m.styles.Danger.Render(truncate(m.ph.Error, w-2)))
	} else {
		lines = append(lines, m.styles.Muted.Render("not configured · tash pihole setup"))
	}
	lines = append(lines, "", m.styles.SectionTitle("DOCKER", w))
	if m.dk.Error != "" {
		lines = append(lines, m.styles.Danger.Render(truncate(m.dk.Error, w-2)))
	} else {
		lines = append(lines, m.kvLine("engine", fmt.Sprintf("%s · %d/%d running", firstNonEmpty(m.dk.ServerVer, "---"), m.dk.Running, len(m.dk.Containers)), w))
	}
	lines = append(lines, "", m.styles.SectionTitle("TASHNET / NAS", w))
	if m.cfg.Tashnet.Role == "main" {
		lines = append(lines, m.kvLine("control", fmt.Sprintf("%d approved · %d pending", len(m.cfg.Tashnet.Devices), len(m.cfg.Tashnet.Pending)), w))
	} else {
		lines = append(lines, m.kvLine("main", firstNonEmpty(m.cfg.Tashnet.MainDeviceName, "not paired"), w))
	}
	lines = append(lines, m.kvLine("nas", m.nasStatusText(), w))
	lines = append(lines, "", m.styles.Muted.Render("1-9 tabs · r refresh · ? help · q quit"))
	return strings.Join(lines, "\n")
}

func (m *Model) kvLine(label, value string, w int) string {
	return m.styles.Muted.Render(ui.Fit(label, 8)) + m.styles.Text.Render(ui.Fit(value, maxInt(w-10, 12)))
}

// deviceLabel prefers the Tailscale machine name with the real hostname in
// parentheses, e.g. "hp (DESKTOP-TEE67)".
func (m *Model) deviceLabel() string {
	if name := strings.TrimSpace(m.ts.Self.DisplayName()); name != "" {
		return name
	}
	return m.info.Hostname
}

func (m *Model) nasStatusText() string {
	if m.nas != nil && m.nas.Running() {
		return m.nas.URL()
	}
	if m.cfg.NAS.AutoStart {
		return "auto-start failed · open NAS tab"
	}
	return "stopped · open NAS tab"
}

func (m *Model) viewTailscale() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	st := m.ts
	var content []string
	if !st.Available {
		content = []string{
			m.styles.Danger.Render("TAILSCALE CLI NOT FOUND"),
			"",
			m.styles.Muted.Render("On Termux (unrooted) the Tailscale CLI is not exposed."),
			m.styles.Muted.Render("Install and sign in with the Tailscale Android app; Tash will"),
			m.styles.Muted.Render("still see the tailnet IP and other services work normally."),
			"",
			m.styles.Muted.Render("Windows/macOS/Linux: install Tailscale, then restart Tash."),
		}
	} else {
		dot := m.styles.Dot(st.Running)
		state := st.BackendState
		if state == "" {
			state = "unknown"
		}
		content = append(content,
			fmt.Sprintf("%s %s  %s", dot, m.styles.Accent.Render("STATE "+state),
				m.styles.Muted.Render("version "+firstNonEmpty(st.Version, "?"))),
			m.styles.KV("self", fmt.Sprintf("%s · %s · %s", firstNonEmpty(st.Self.DisplayName(), m.deviceLabel(), m.info.Hostname), firstNonEmpty(st.Self.IP, "no ip"), st.Self.OS), 8),
			m.styles.KV("online", fmt.Sprintf("%d of %d peers", len(st.OnlinePeers()), len(st.Peers)), 8),
		)
		if st.Error != "" {
			content = append(content, m.styles.Danger.Render("! "+truncate(st.Error, w-6)))
		}
		content = append(content, "", m.styles.SectionTitle("PEERS", w))
		
		peers := m.filteredPeers()
		if len(peers) == 0 {
			content = append(content, m.styles.Muted.Render("no peers match current filter"))
		}
		rows := maxInt(h-10, 3)
		start := 0
		if m.peerIdx >= rows {
			start = m.peerIdx - rows + 1
		}
		for i := start; i < len(peers) && i < start+rows; i++ {
			p := peers[i]
			sel := "  "
			if i == m.peerIdx {
				sel = m.styles.Primary.Render("▸ ")
			}
			icon := m.styles.Dot(p.Online)
			nameW := maxInt(minInt(w-46, 30), 16)
			name := ui.Fit(p.DisplayName(), nameW)
			ip := ui.Fit(firstNonEmpty(p.IP, "—"), 16)
			lat := m.styles.Muted.Render("—")
			key := peerKey(p)
			if last, ok := m.lastPing[key]; ok {
				if last.Err != "" {
					lat = m.styles.Danger.Render("fail")
				} else {
					lat = m.styles.Accent.Render(fmt.Sprintf("%.0fms", float64(last.Latency.Microseconds())/1000))
				}
			}
			spark := m.styles.Sparkline(m.pings[key], 14)
			content = append(content, fmt.Sprintf("%s%s %s %s %s %s", sel, icon,
				m.styles.Text.Render(name), m.styles.Muted.Render(ip), lat, spark))
		}
		if len(peers) > 0 {
			content = append(content, "", m.styles.Muted.Render("p ping selected · P ping all online · s ssh to selected · ↑↓ select"))
		}
	}
	return m.styles.Panel("TAILSCALE // TAILNET LATENCY", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) filteredPeers() []tailscale.Peer {
	mode := m.cfg.Tailscale.DisplayMode
	if mode == "" {
		mode = "all"
	}
	switch mode {
	case "active":
		return m.ts.OnlinePeers()
	case "custom":
		customSet := make(map[string]bool, len(m.cfg.Tailscale.CustomPeerIDs))
		for _, id := range m.cfg.Tailscale.CustomPeerIDs {
			customSet[id] = true
		}
		var filtered []tailscale.Peer
		for _, p := range m.ts.Peers {
			if customSet[p.IP] || customSet[p.Name] || customSet[p.TailscaleName] {
				filtered = append(filtered, p)
			}
		}
		return filtered
	default: // "all"
		return m.ts.Peers
	}
}

func (m *Model) viewPiHole() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	if !m.cfg.PiHole.Enabled || m.cfg.PiHole.URL == "" {
		content := []string{
			m.styles.Warning.Render("PI-HOLE IS NOT CONFIGURED"),
			"",
			m.styles.Muted.Render("Configure it from a terminal (the key is stored in your 0600 config):"),
			"",
			m.styles.Accent.Render("  tash pihole setup"),
			"",
			m.styles.Muted.Render("Supports Pi-hole v6 password/app-password sessions and the"),
			m.styles.Muted.Render("legacy v5 API token. Then restart `tash dashboard`."),
		}
		return m.styles.Panel("PI-HOLE // DNS FIREWALL", strings.Join(content, "\n"), w, h, true)
	}
	if m.ph.Error != "" {
		content := []string{
			m.styles.Danger.Render("PI-HOLE UNREACHABLE"),
			"",
			m.styles.Text.Render(truncate(m.ph.Error, maxInt(w-6, 20))),
			"",
			m.styles.Muted.Render("URL: " + m.cfg.PiHole.URL),
			m.styles.Muted.Render("Check the address, password/app password, TLS settings and firewall."),
			"",
			m.styles.Muted.Render("Reconfigure with `tash pihole setup` if needed."),
		}
		return m.styles.Panel("PI-HOLE // DNS FIREWALL", strings.Join(content, "\n"), w, h, true)
	}
	s := m.ph.Summary
	blockState := "unknown"
	stateColor := m.styles.Muted
	if m.ph.Blocking == "enabled" {
		blockState = "ENABLED"
		stateColor = m.styles.Success
	} else if m.ph.Blocking == "disabled" {
		blockState = "DISABLED"
		stateColor = m.styles.Danger
	}
	head := fmt.Sprintf("%s  %s  %s", m.styles.Dot(true), m.styles.Text.Render("v"+itoa(m.ph.Version)),
		stateColor.Render("BLOCKING "+blockState)) +
		m.styles.Muted.Render("  ·  "+m.ph.URL)
	blocked := m.styles.Danger.Render(formatCount(s.Blocked))
	content := []string{
		head,
		"",
		fmt.Sprintf("%s %s   %s %s   %s %s",
			m.styles.Muted.Render("queries"), m.styles.Accent.Render(formatCount(s.TotalQueries)),
			m.styles.Muted.Render("blocked"), blocked,
			m.styles.Muted.Render("percent"), m.styles.Warning.Render(fmt.Sprintf("%.1f%%", s.PercentBlocked))),
		fmt.Sprintf("%s %s", m.styles.Bar(s.PercentBlocked, maxInt(minInt(w-24, 42), 10)),
			m.styles.Muted.Render(fmt.Sprintf("  gravity %s · clients %d", formatCount(s.GravityDomains), s.ActiveClients))),
		"",
		m.styles.SectionTitle("RECENT QUERIES", w),
	}
	rows := minInt(12, maxInt(h-16, 3))
	if len(m.ph.Queries) == 0 {
		content = append(content, m.styles.Muted.Render("no queries returned"))
	}
	for i := 0; i < len(m.ph.Queries) && i < rows; i++ {
		q := m.ph.Queries[i]
		status := ui.Fit(q.Status, 14)
		if q.Blocked {
			status = m.styles.Danger.Render(status)
		} else {
			status = m.styles.Muted.Render(status)
		}
		when := q.At.Format("15:04:05")
		if q.At.IsZero() {
			when = "--:--:--"
		}
		content = append(content, fmt.Sprintf("%s  %s  %s  %s",
			m.styles.Muted.Render(when), m.styles.Text.Render(ui.Fit(q.Domain, maxInt(w-38, 16))),
			status, m.styles.Muted.Render(ui.Fit(q.Client, 14))))
	}
	content = append(content, "", m.styles.SectionTitle("TOP BLOCKED", w))
	if len(m.ph.TopBlocked) == 0 {
		content = append(content, m.styles.Muted.Render("nothing blocked yet"))
	}
	for i, item := range m.ph.TopBlocked {
		if i >= 5 {
			break
		}
		content = append(content, fmt.Sprintf("%s  %s",
			m.styles.Danger.Render(ui.Fit(item.Name, maxInt(w-18, 16))),
			m.styles.Muted.Render(formatCount(item.Count))))
	}
	if m.ph.QueryError != "" {
		content = append(content, m.styles.Warning.Render("query feed: "+truncate(m.ph.QueryError, w-8)))
	}
	return m.styles.Panel("PI-HOLE // DNS FIREWALL", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) viewDocker() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	if m.cfg.Docker.Mode == "" || m.cfg.Docker.Mode == "off" {
		content := []string{
			m.styles.Warning.Render("DOCKER IS DISABLED"),
			"",
			m.styles.Muted.Render("Choose CLI mode on a desktop with Docker installed, or socket/API"),
			m.styles.Muted.Render("mode for a headless server or when Tash runs inside a container."),
			"",
			m.styles.Accent.Render("  tash docker use cli"),
			m.styles.Accent.Render("  tash docker use socket unix:///var/run/docker.sock"),
			"",
			m.styles.Muted.Render("Restart `tash dashboard` afterwards."),
		}
		return m.styles.Panel("DOCKER // ENGINE CONTROL", strings.Join(content, "\n"), w, h, true)
	}
	dk := m.dk
	content := []string{}
	if dk.Error != "" {
		content = append(content,
			m.styles.Danger.Render("ENGINE ERROR"),
			m.styles.Text.Render(truncate(dk.Error, maxInt(w-6, 20))),
			"",
			m.styles.Muted.Render("mode: "+m.cfg.Docker.Mode+" · host: "+m.cfg.Docker.Host),
			m.styles.Muted.Render("The socket/API endpoint is root-equivalent; protect remote TCP."),
		)
	} else {
		content = append(content,
			fmt.Sprintf("%s %s  %s",
				m.styles.Dot(dk.Available),
				m.styles.Accent.Render("engine "+firstNonEmpty(dk.ServerVer, "?")),
				m.styles.Muted.Render(fmt.Sprintf("mode %s · %s/%s · %d CPU · %s",
					m.cfg.Docker.Mode, firstNonEmpty(dk.OS, "?"), firstNonEmpty(dk.Arch, "?"), dk.CPUs, ui.HumanBytes(dk.Memory)))),
			m.styles.Muted.Render("name: "+firstNonEmpty(dk.Engine, "—")+"  host: "+m.cfg.Docker.Host),
			"",
			m.styles.SectionTitle(fmt.Sprintf("CONTAINERS  %d running / %d total", dk.Running, len(dk.Containers)), w),
		)
		if len(dk.Containers) == 0 {
			content = append(content, m.styles.Muted.Render("no containers"))
		}
		rows := maxInt(h-10, 3)
		start := 0
		if m.contIdx >= rows {
			start = m.contIdx - rows + 1
		}
		for i := start; i < len(dk.Containers) && i < start+rows; i++ {
			c := dk.Containers[i]
			sel := "  "
			if i == m.contIdx {
				sel = m.styles.Primary.Render("▸ ")
			}
			icon := m.styles.Dot(c.Running)
			state := ui.Fit(c.State, 10)
			if c.Running {
				state = m.styles.Success.Render(state)
			} else {
				state = m.styles.Muted.Render(state)
			}
			content = append(content, fmt.Sprintf("%s%s %s %s %s %s",
				sel, icon, m.styles.Text.Render(ui.Fit(c.Name, 22)), state,
				m.styles.Muted.Render(ui.Fit(c.Image, maxInt(w-58, 12))),
				m.styles.Muted.Render(ui.Fit(c.Ports, 18))))
		}
		if len(dk.Containers) > 0 {
			content = append(content, "", m.styles.Muted.Render("s start · x stop · p pause · u resume · R restart · D delete · l logs · ↑↓ select"))
		}
	}
	return m.styles.Panel("DOCKER // ENGINE CONTROL", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) viewTashnet() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	cfg := m.cfg.Tashnet
	if cfg.Role == "client" {
		content := []string{
			m.styles.SectionTitle("CLIENT UPLINK", w),
			m.styles.KV("main", firstNonEmpty(cfg.MainDeviceName, "not paired"), 10),
			m.styles.KV("url", firstNonEmpty(cfg.MainURL, "—"), 10),
			m.styles.KV("device", cfg.DeviceID, 10),
			m.styles.KV("token", tokenState(cfg.Token), 10),
			m.styles.KV("secret ver", itoa(cfg.SecretVersion), 10),
			"",
		}
		if m.netStatus != "" {
			content = append(content,
				m.styles.Danger.Render("UPLINK PROBLEM"),
				m.styles.Text.Render(wrapMultiline(m.netStatus, w-6)...),
				"",
				m.styles.Muted.Render("If the main device regenerated its secret key, re-pair with:"),
				m.styles.Accent.Render("  tash join"),
			)
		} else {
			content = append(content,
				m.styles.Success.Render("● authenticated heartbeat active"),
				m.styles.Muted.Render("Your token was approved by the main device. If the main"),
				m.styles.Muted.Render("device regenerates the network key, this device disconnects."),
			)
		}
		content = append(content, "", m.styles.SectionTitle("HOW TASHNET WORKS", w))
		content = append(content,
			m.styles.Muted.Render("• The secret key never leaves this device during pairing;"),
			m.styles.Muted.Render("  Tash sends an HMAC proof and the main device approves you."),
			m.styles.Muted.Render("• The main device runs `tash dashboard` to accept joins."),
			m.styles.Muted.Render("• Use `tash main transfer` on the main device to hand over the role."),
		)
		return m.styles.Panel("TASHNET // CLIENT UPLINK", strings.Join(content, "\n"), w, h, true)
	}

	secretMasked := "missing"
	secretFull := m.store.Secret()
	if secretFull != "" {
		secretMasked = tashnet.MaskSecret(secretFull)
	}
	control := fmt.Sprintf("port %d", cfg.ControlPort)
	if m.tnet != nil {
		if m.tnet.Err() != "" {
			control = m.tnet.Err()
		} else {
			control = m.tnet.URL()
		}
	}
	content := []string{
		m.styles.SectionTitle("MAIN DEVICE", w),
		m.styles.KV("network", cfg.Name, 10),
		m.styles.KV("control", control, 10),
		m.styles.KV("device", cfg.DeviceName+" · "+shortID(cfg.DeviceID), 10),
		m.styles.KV("secret", secretMasked+"  (s to reveal)", 10),
		m.styles.KV("version", "secret v"+itoa(cfg.SecretVersion), 10),
	}
	if m.startErr != "" {
		content = append(content, m.styles.Danger.Render("! "+m.startErr))
	}
	content = append(content, "", m.styles.SectionTitle(fmt.Sprintf("PENDING APPROVALS  %d", len(cfg.Pending)), w))
	pending := sortedPending(cfg.Pending)
	if len(pending) == 0 {
		content = append(content, m.styles.Muted.Render("no pending devices — ask the client to run `tash dashboard` and join"))
	}
	for i, p := range pending {
		if i >= 4 {
			content = append(content, m.styles.Muted.Render(fmt.Sprintf("… %d more", len(pending)-i)))
			break
		}
		sel := "  "
		if i == m.pendIdx {
			sel = m.styles.Primary.Render("▸ ")
		}
		content = append(content, fmt.Sprintf("%s%s  %s  %s  %s", sel,
			m.styles.Warning.Render(ui.Fit(p.Name, 18)), m.styles.Muted.Render(ui.Fit(p.OS+"/"+p.Arch, 16)),
			m.styles.Muted.Render(ui.Fit(p.IP, 15)), m.styles.Muted.Render(ui.HumanAgo(p.RequestedAt))))
	}
	content = append(content, "", m.styles.SectionTitle(fmt.Sprintf("APPROVED DEVICES  %d", len(cfg.Devices)), w))
	devices := sortedDevices(cfg.Devices)
	if len(devices) == 0 {
		content = append(content, m.styles.Muted.Render("no approved devices yet"))
	}
	maxDev := 5
	if h > 32 {
		maxDev = 8
	}
	for i, d := range devices {
		if i >= maxDev {
			content = append(content, m.styles.Muted.Render(fmt.Sprintf("… %d more", len(devices)-i)))
			break
		}
		tag := ""
		if d.ID == cfg.DeviceID {
			tag = m.styles.Accent.Render(" (this device)")
		}
		sel := "  "
		if i == m.devIdx && len(pending) == 0 {
			sel = m.styles.Primary.Render("▸ ")
		}
		content = append(content, fmt.Sprintf("%s%s %s%s  %s  %s", sel, m.styles.Dot(true),
			m.styles.Text.Render(ui.Fit(d.Name, 20)), tag,
			m.styles.Muted.Render(ui.Fit(d.OS+"/"+d.Arch, 16)),
			m.styles.Muted.Render("seen "+ui.HumanAgo(d.LastSeen))))
	}
	if m.tnet != nil {
		events := m.tnet.Events()
		if len(events) > 0 && h > 30 {
			content = append(content, "", m.styles.SectionTitle("AUDIT FEED", w))
			start := len(events) - 3
			if start < 0 {
				start = 0
			}
			for _, e := range events[start:] {
				content = append(content, fmt.Sprintf("%s  %s  %s",
					m.styles.Muted.Render(e.At.Format("15:04:05")),
					m.styles.Accent.Render(ui.Fit(e.Kind, 10)),
					m.styles.Text.Render(truncate(e.Message, maxInt(w-28, 16)))))
			}
		}
	}
	content = append(content, "", m.styles.Muted.Render("a approve · d deny · s reveal secret · g regenerate (disconnects everyone) · t transfer code"))
	return m.styles.Panel("TASHNET // MAIN CONTROL", strings.Join(content, "\n"), w, h, true)
}

func tokenState(tok string) string {
	if tok == "" {
		return "missing — rejoin required"
	}
	if len(tok) > 10 {
		return tok[:6] + "…" + tok[len(tok)-4:]
	}
	return "present"
}

func (m *Model) viewNAS() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	cfg := m.cfg.NAS
	running := m.nas != nil && m.nas.Running()
	state := m.styles.Danger.Render("○ STOPPED")
	if running {
		state = m.styles.Success.Render("● RUNNING")
	}
	url, dav := "", ""
	if m.nas != nil {
		url = m.nas.URL()
		dav = m.nas.DAVURL()
	}
	content := []string{
		state + m.styles.Muted.Render("  ·  authenticated WebDAV vault"),
		"",
		m.styles.KV("url", firstNonEmpty(url, fmt.Sprintf("http://<this-host>:%d", cfg.Port)), 10),
		m.styles.KV("dav", firstNonEmpty(dav, fmt.Sprintf("http://<this-host>:%d/dav/", cfg.Port)), 10),
		m.styles.KV("user", cfg.User, 10),
		m.styles.KV("pass", m.nasPassword(), 10),
		m.styles.KV("root", cfg.Root, 10),
		m.styles.KV("mode", nasMode(cfg.ReadOnly), 10),
	}
	if m.nas != nil && m.nas.Err() != "" {
		content = append(content, m.styles.Danger.Render("! "+m.nas.Err()))
	}
	content = append(content, "", m.styles.SectionTitle("MOUNT HELP", w))
	for _, line := range wrapMultiline(nasMountHelp(m.info.OS), maxInt(w-6, 20)) {
		content = append(content, m.styles.Muted.Render(line))
	}
	content = append(content, "", m.styles.SectionTitle("FILES // "+m.nasBreadcrumb(), w))
	entries, err := m.nasEntries()
	if err != nil {
		content = append(content, m.styles.Muted.Render("folder not created yet — press s to start the server"))
	} else if len(entries) == 0 {
		content = append(content, m.styles.Muted.Render("empty — upload through WebDAV or create a file in the Editor tab"))
	} else {
		max := minInt(len(entries), maxInt(h-24, 3))
		start := 0
		if m.nasIdx >= max {
			start = m.nasIdx - max + 1
		}
		for i := start; i < len(entries) && i < start+max; i++ {
			e := entries[i]
			sel := "  "
			if i == m.nasIdx {
				sel = m.styles.Primary.Render("▸ ")
			}
			icon := "·"
			size := ""
			switch {
			case e.Parent:
				icon = "↰"
				size = "up"
			case e.Dir:
				icon = "▸"
				size = "dir"
			default:
				size = ui.HumanBytes(e.Size)
			}
			content = append(content, fmt.Sprintf("%s%s %s  %s", sel, m.styles.Accent.Render(icon),
				m.styles.Text.Render(ui.Fit(e.Name, maxInt(w-20, 16))), m.styles.Muted.Render(size)))
		}
	}
	content = append(content, "", m.styles.Muted.Render("s start/stop · P set password · p reveal · enter open · ⌫ up · Basic auth over HTTP; use Tailscale for remote access"))
	return m.styles.Panel("NAS // WEBDAV VAULT", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) nasPassword() string {
	p := m.cfg.NAS.Password
	if p == "" {
		return "—"
	}
	if m.showPass {
		return p
	}
	if len(p) <= 4 {
		return strings.Repeat("•", len(p))
	}
	return p[:2] + strings.Repeat("•", len(p)-4) + p[len(p)-2:]
}

func startupPasswordState(cfg config.Config) string {
	if cfg.Tashnet.StartupPassword == "" {
		return "disabled"
	}
	return "enabled"
}

type nasEntry struct {
	Name   string
	Path   string
	Dir    bool
	Parent bool
	Size   int64
}

func cleanNASPath(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	p = strings.TrimPrefix(p, "/")
	cleaned := filepath.Clean(p)
	if cleaned == "." || cleaned == "" {
		return ""
	}
	cleaned = filepath.ToSlash(cleaned)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return filepath.FromSlash(cleaned)
}

func (m *Model) nasAbsPath() string {
	return filepath.Join(m.cfg.NAS.Root, cleanNASPath(m.nasPath))
}

func (m *Model) nasBreadcrumb() string {
	rel := cleanNASPath(m.nasPath)
	if rel == "" {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

func (m *Model) nasEntries() ([]nasEntry, error) {
	var out []nasEntry
	rel := cleanNASPath(m.nasPath)
	if rel != "" {
		out = append(out, nasEntry{Name: "..", Parent: true, Dir: true})
	}
	entries, err := os.ReadDir(m.nasAbsPath())
	if err != nil {
		return out, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, e := range entries {
		item := nasEntry{Name: e.Name(), Path: filepath.Join(rel, e.Name()), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			item.Size = info.Size()
		}
		out = append(out, item)
	}
	return out, nil
}

func (m *Model) nasSelected() (nasEntry, bool) {
	entries, err := m.nasEntries()
	if err != nil || len(entries) == 0 {
		return nasEntry{}, false
	}
	if m.nasIdx < 0 {
		m.nasIdx = 0
	}
	if m.nasIdx >= len(entries) {
		m.nasIdx = len(entries) - 1
	}
	return entries[m.nasIdx], true
}

func nasMountHelp(osName string) string {
	switch osName {
	case "windows":
		return "Explorer > This PC > Map network drive. Paste the DAV URL. Windows may need the WebClient service enabled."
	case "darwin":
		return "Finder > Go > Connect to Server (Cmd+K). Paste the DAV URL."
	case "linux":
		if config.IsTermux() {
			return "Android: add a WebDAV storage in Solid Explorer, RCX, or Material Files. Termux can use curl or a WebDAV client."
		}
		return "GNOME Files: Other Locations > Connect to Server. CLI: mount.davfs DAV-URL /mnt/tash."
	default:
		return "Connect using any WebDAV client: Windows Explorer, Finder, davfs2, or an Android file manager."
	}
}

func nasMode(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "read/write"
}

type settingRow struct {
	label string
	value string
	hint  string
}

func (m *Model) settingsRows() []settingRow {
	aiState := "off"
	if m.cfg.AI.Enabled {
		aiState = "on"
	}
	keyState := "not set"
	if strings.TrimSpace(m.cfg.AI.APIKey) != "" {
		keyState = "set"
	}
	providerState := firstNonEmpty(m.cfg.AI.Provider, "claude")
	modelState := firstNonEmpty(m.cfg.AI.Model, "default")
	commandState := firstNonEmpty(m.cfg.AI.Command, "claude")
	baseURLState := firstNonEmpty(m.cfg.AI.BaseURL, "(default)")
	hidden := len(m.cfg.UI.HiddenTabs)
	installDir := firstNonEmpty(config.Dir(), "(default)")
	updateState := "check for updates"
	rows := []settingRow{
		{"Theme", m.currentThemeName(), "visual matrix and custom themes"},
		{"AI integration", aiState, "enable or disable in-app AI chat"},
		{"AI provider", providerState, "claude, openai, anthropic, custom"},
		{"AI base URL", baseURLState, "custom API endpoint (for BYOK)"},
		{"AI API key", keyState, "API key for the selected provider"},
		{"AI model", modelState, "model name to use"},
		{"AI command", commandState, "CLI name or absolute path (for claude provider)"},
		{"AI workdir", firstNonEmpty(m.cfg.AI.WorkDir, "(current directory)"), "default project directory"},
		{"AI permission", firstNonEmpty(m.cfg.AI.PermissionMode, "default"), "default, acceptEdits, plan, bypassPermissions"},
		{"NAS password", m.nasPassword(), "WebDAV Basic auth password"},
		{"NAS root", m.cfg.NAS.Root, "folder exposed over WebDAV"},
		{"NAS port", fmt.Sprintf("%d", m.cfg.NAS.Port), "WebDAV server port (1024-65535)"},
		{"Tailscale display", firstNonEmpty(m.cfg.Tailscale.DisplayMode, "all"), "all, active, or custom peer list"},
		{"Tailscale ping interval", fmt.Sprintf("%ds", m.cfg.Tailscale.IntervalSec), "seconds between pings (1-60)"},
		{"Tailscale max peers", fmt.Sprintf("%d", m.cfg.Tailscale.MaxPeers), "max peers to ping per interval (1-50)"},
		{"Startup password", startupPasswordState(m.cfg), "password required on startup to unlock Tash"},
		{"Config sync interval", fmt.Sprintf("%ds", m.cfg.Tashnet.ConfigSyncSec), "seconds between config sync from main (1-60)"},
		{"Install directory", installDir, "where Tash is installed (requires reinstall)"},
		{"Check for updates", updateState, "check GitHub for latest version"},
		{"Refresh key", m.keybind("refresh"), "global refresh binding"},
		{"Help key", m.keybind("help"), "opens About / help"},
		{"Quit key", m.keybind("quit"), "leave the dashboard"},
		{"Tabs & numbers", fmt.Sprintf("%d visible · %d hidden", len(m.visibleTabDefs()), hidden), "hide tabs and reassign 1..9"},
		{"Splash screen", onOff(m.cfg.UI.Splash), "animated startup screen"},
		{"24-hour clock", onOff(m.cfg.UI.Clock24), "header clock format"},
		{"Deck layout", firstNonEmpty(m.cfg.UI.DeckLayout, "auto"), "auto, cards, or compact overview"},
		{"Tab position", firstNonEmpty(m.cfg.UI.TabPosition, "top"), "top or bottom"},
		{"About Tash", "v" + versionString() + " · Nypr", "version, help, paths, copyright"},
		{"Reset to Defaults", "confirm to reset all settings", "resets UI, AI, NAS, keybinds, tabs to factory defaults"},
	}
	return rows
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (m *Model) viewSettings() string {
	switch m.settings {
	case settingsThemes:
		return m.viewSettingsThemes()
	case settingsTabs:
		return m.viewSettingsTabs()
	default:
		return m.viewSettingsMenu()
	}
}

func (m *Model) viewSettingsMenu() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	rows := m.settingsRows()
	content := []string{
		m.styles.Muted.Render("Global control plane. Enter edits the selected row; changes save to config immediately."),
		"",
	}
	visible := maxInt(h-8, 5)
	start := 0
	if m.settingsIdx >= visible {
		start = m.settingsIdx - visible + 1
	}
	for i := start; i < len(rows) && i < start+visible; i++ {
		row := rows[i]
		sel := "  "
		if i == m.settingsIdx {
			sel = m.styles.Primary.Render("▸ ")
		}
		content = append(content, fmt.Sprintf("%s%s  %s", sel,
			m.styles.Text.Render(ui.Fit(row.label, 20)), m.styles.Accent.Render(ui.Fit(row.value, maxInt(w-32, 12)))))
		if i == m.settingsIdx && row.hint != "" {
			content = append(content, "    "+m.styles.Muted.Render(truncate(row.hint, maxInt(w-8, 20))))
		}
	}
	content = append(content, "", m.styles.Muted.Render("enter edit · ↑↓ select · esc back · all settings are stored in config.json"))
	return m.styles.Panel("SETTINGS // CONTROL PLANE", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) viewSettingsThemes() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	names := m.themeNames()
	content := []string{
		m.styles.Muted.Render("Live theme previews. Enter applies and saves. `c` copies the current theme to JSON and opens it in the editor."),
		"",
	}
	rows := maxInt(h-8, 4)
	start := 0
	if m.themeIdx >= rows {
		start = m.themeIdx - rows + 1
	}
	for i := start; i < len(names) && i < start+rows; i++ {
		t := m.registry.Get(names[i])
		sel := "  "
		if i == m.themeIdx {
			sel = m.styles.Primary.Render("▸ ")
		}
		current := " "
		if strings.EqualFold(names[i], m.currentThemeName()) {
			current = m.styles.Success.Render("●")
		}
		swatches := themeSwatches(t)
		kind := "custom"
		if theme.IsBuiltin(names[i]) {
			kind = "builtin"
		}
		content = append(content, fmt.Sprintf("%s%s %s %s  %s", sel, current,
			m.styles.Text.Render(ui.Fit(names[i], 16)), swatches, m.styles.Muted.Render(kind)))
	}
	if m.themeIdx >= 0 && m.themeIdx < len(names) {
		t := m.registry.Get(names[m.themeIdx])
		content = append(content, "",
			m.styles.SectionTitle("PREVIEW // "+strings.ToUpper(t.Name), w),
			fmt.Sprintf("%s  %s  %s  %s",
				m.styles.BadgeLabel("PRIMARY", t.Primary), m.styles.BadgeLabel("ACCENT", t.Accent),
				m.styles.BadgeLabel("SUCCESS", t.Success), m.styles.BadgeLabel("DANGER", t.Danger)),
			"",
			m.styles.Text.Render("The quick brown fox jumps over the lazy dog. 0123456789"),
			m.styles.Muted.Render("muted prose · path/to/file · peer@100.64.0.2 · 42ms"),
			m.styles.Primary.Render("▚ ")+m.styles.Accent.Render("TASH")+m.styles.Text.Render(" dashboard")+m.styles.Muted.Render(" // TASHNET"),
			m.styles.Bar(67, maxInt(minInt(w-12, 36), 10)),
		)
		if !theme.IsBuiltin(names[m.themeIdx]) {
			content = append(content, m.styles.Muted.Render("custom file: "+filepath.Join(m.registry.Dir(), names[m.themeIdx]+".json")))
		}
	}
	content = append(content, "", m.styles.Muted.Render("enter apply · c copy custom · ↑↓ preview · esc back"))
	return m.styles.Panel("SETTINGS // THEMES", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) viewSettingsTabs() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	hidden := m.hiddenTabs()
	content := []string{
		m.styles.Muted.Render("Space shows or hides a tab. 1..9 assigns that number. J/K moves it in the deck."),
		m.styles.Muted.Render("Settings is always visible so you can undo a mistake. Hidden tabs keep their data."),
		"",
	}
	rows := maxInt(h-9, 4)
	start := 0
	if m.tabCfgIdx >= rows {
		start = m.tabCfgIdx - rows + 1
	}
	order := m.tabOrderDefs()
	for i := start; i < len(order) && i < start+rows; i++ {
		d := order[i]
		sel := "  "
		if i == m.tabCfgIdx {
			sel = m.styles.Primary.Render("▸ ")
		}
		shown := !hidden[d.ID]
		mark := m.styles.Success.Render("[x]")
		if !shown {
			mark = m.styles.Muted.Render("[ ]")
		}
		lock := ""
		if d.ID == "settings" {
			lock = m.styles.Muted.Render("  locked visible")
			mark = m.styles.Success.Render("[x]")
		}
		num := m.cfg.UI.TabNumbers[d.ID]
		content = append(content, fmt.Sprintf("%s%s  %s  %s%s", sel, mark,
			m.styles.Warning.Render("["+num+"]"), m.styles.Text.Render(ui.Fit(d.Name, 16)), lock))
	}
	content = append(content, "", m.styles.Muted.Render("space show/hide · 1..9 assign number · J/K move · ↑↓ select · esc back"))
	return m.styles.Panel("SETTINGS // TABS & NUMBERS", strings.Join(content, "\n"), w, h, true)
}

func themeSwatches(t theme.Theme) string {
	colors := []string{t.Primary, t.Secondary, t.Accent, t.Success, t.Warning, t.Danger}
	var b strings.Builder
	for _, c := range colors {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("██"))
	}
	return b.String()
}

func (m *Model) viewAI() string {
	w := m.contentWidth()
	h := m.bodyHeight()
	st := m.ai
	state := m.styles.Warning.Render("○ NOT DETECTED")
	if st.Available {
		state = m.styles.Success.Render("● READY")
	}
	if !st.Enabled {
		state = m.styles.Danger.Render("○ DISABLED")
	}
	meta := []string{}
	if st.Version != "" {
		meta = append(meta, st.Version)
	}
	if st.AuthMode != "" && st.AuthMode != "not detected" {
		meta = append(meta, "auth "+st.AuthMode)
	}
	provider := firstNonEmpty(strings.TrimSpace(m.cfg.AI.Provider), "claude")
	if model := strings.TrimSpace(m.cfg.AI.Model); model != "" {
		meta = append(meta, "model "+model)
	}
	content := []string{
		state + m.styles.Muted.Render("  ·  AI conversation ("+provider+")"),
		m.styles.Muted.Render(firstNonEmpty(strings.Join(meta, " · "), "detecting AI provider…")),
		m.styles.KV("workdir", m.aiWorkDir(), 9),
	}
	if !st.Enabled {
		content = append(content, "", m.styles.Warning.Render("! Enable AI integration in Settings to chat here."))
		return m.styles.Panel("AI // CHAT", strings.Join(content, "\n"), w, h, true)
	}
	if !st.Available {
		content = append(content, "",
			m.styles.SectionTitle("AI PROVIDER SETUP", w),
			m.styles.Muted.Render("Configure your AI provider in Settings (provider, base URL, API key)."),
			m.styles.Muted.Render("For Claude Code CLI: install it, then run `claude` once to sign in:"),
			m.styles.Accent.Render("  npm install -g @anthropic-ai/claude-code"),
			m.styles.Muted.Render("or use the installer from docs.anthropic.com/en/docs/claude-code."),
			"",
			m.styles.Muted.Render("For OpenAI-compatible APIs: set provider, base URL, and API key in Settings."),
		)
		if st.Error != "" {
			content = append(content, "", m.styles.Warning.Render("! "+truncate(st.Error, maxInt(w-8, 20))))
		}
		return m.styles.Panel("AI // CHAT", strings.Join(content, "\n"), w, h, true)
	}
	if st.Error != "" && len(m.aiChat) == 0 {
		content = append(content, m.styles.Warning.Render("! "+truncate(st.Error, maxInt(w-8, 20))))
	}

	width := maxInt(w-8, 20)
	lines := m.aiChatLines(width)
	if len(lines) == 0 {
		content = append(content, "",
			m.styles.Muted.Render("Ask AI anything about the current workdir. It can read files"),
			m.styles.Muted.Render("and run tools there, and this conversation stays inside Tash."),
			"",
			m.styles.Muted.Render("ctrl+n starts a fresh conversation · ctrl+o changes the workdir · ctrl+m models"))
	}
	reserved := len(content) + 3 // blank + input line + footer
	if m.aiBusy {
		reserved += 2
	}
	avail := maxInt((h-2)-reserved, 3)
	if len(lines) > 0 {
		maxScroll := maxInt(len(lines)-avail, 0)
		if m.aiScroll > maxScroll {
			m.aiScroll = maxScroll
		}
		end := len(lines) - m.aiScroll
		start := maxInt(end-avail, 0)
		content = append(content, lines[start:end]...)
	}
	if m.aiBusy {
		content = append(content, "", m.styles.Accent.Render(m.frame()+" AI is thinking…"))
	}
	content = append(content, "", m.styles.Primary.Render("› ")+m.aiInputView(maxInt(w-8, 12)))
	footer := "enter send · ↑↓ scroll · ctrl+n new chat · ctrl+o workdir · ctrl+a api key · ctrl+m models · ctrl+r refresh"
	content = append(content, m.styles.Muted.Render(truncate(footer, maxInt(w-8, 20))))
	return m.styles.Panel("AI // CHAT", strings.Join(content, "\n"), w, h, true)
}

func (m *Model) aiChatLines(width int) []string {
	var lines []string
	for _, turn := range m.aiChat {
		label := "YOU"
		style := m.styles.Primary
		switch turn.Role {
		case "assistant":
			label, style = "AI", m.styles.Accent
		case "error":
			label, style = "ERROR", m.styles.Danger
		}
		lines = append(lines, style.Bold(true).Render("▌ "+label))
		for _, l := range wrapMultiline(turn.Text, maxInt(width-2, 8)) {
			lines = append(lines, "  "+m.styles.Text.Render(l))
		}
		lines = append(lines, "")
	}
	return lines
}

func (m *Model) aiInputView(width int) string {
	width = maxInt(width, 4)
	r := []rune(m.aiInput)
	c := minInt(m.aiCursor, len(r))
	start := 0
	if c >= width-1 {
		start = c - (width - 2)
	}
	end := minInt(start+width-1, len(r))
	before := string(r[start:c])
	after := ""
	if c < len(r) {
		after = string(r[c:end])
	}
	return before + m.styles.Accent.Render("█") + after
}

func keyState(key string) string {
	if strings.TrimSpace(key) == "" {
		return "not set"
	}
	return "set (" + maskKey(key) + ")"
}

func maskKey(key string) string {
	if len(key) <= 6 {
		return strings.Repeat("•", len(key))
	}
	return key[:4] + strings.Repeat("•", 6) + key[len(key)-2:]
}

func (m *Model) aboutBody() string {
	w := m.contentWidth()
	var b strings.Builder
	add := func(format string, args ...any) { b.WriteString(fmt.Sprintf(format, args...) + "\n") }
	section := func(label string) {
		b.WriteString("\n" + m.styles.SectionTitle(label, w) + "\n")
	}
	add("%s %s", m.styles.Primary.Bold(true).Render("▚ TASH"), m.styles.Muted.Render("terminal control deck"))
	add("%s", m.styles.Muted.Render("Version "+versionString()+"  ·  "+m.info.OS+"/"+m.info.Arch+"  ·  "+m.deviceLabel()))
	add("%s", m.styles.Accent.Render("Copyright © 2026 Nypr. All rights reserved."))
	section("START")
	add("%s %s", m.styles.Accent.Render("tash dashboard"), m.styles.Muted.Render("open this TUI"))
	add("%s %s", m.styles.Accent.Render("tash setup"), m.styles.Muted.Render("onboarding guidance"))
	add("%s %s", m.styles.Accent.Render("tash doctor"), m.styles.Muted.Render("check services and config"))
	add("%s %s", m.styles.Accent.Render("tash join"), m.styles.Muted.Render("pair with a main device"))
	add("%s %s", m.styles.Accent.Render("tash main transfer"), m.styles.Muted.Render("issue a 10-minute handover code"))
	section("KEYS")
	add("%s %s   %s %s   %s %s", m.styles.Key.Render("1..9"), m.styles.Muted.Render("tabs"),
		m.styles.Key.Render(m.keybind("next_tab")), m.styles.Muted.Render("cycle"),
		m.styles.Key.Render(m.keybind("refresh")), m.styles.Muted.Render("refresh"))
	add("%s %s   %s %s   %s %s", m.styles.Key.Render(m.keybind("help")), m.styles.Muted.Render("about/help"),
		m.styles.Key.Render(m.keybind("quit")), m.styles.Muted.Render("quit"),
		m.styles.Key.Render("ctrl+c"), m.styles.Muted.Render("force quit"))
	add("%s", m.styles.Muted.Render("Tab numbers, visibility, and global keys are editable in Settings (9)."))
	section("EDITOR")
	add("%s", m.styles.Muted.Render("Vim-style editor: i insert, Esc normal, :w save, :q quit, :wq write+quit,"))
	add("%s", m.styles.Muted.Render("/ search, n/N next, dd delete, yy yank, p paste, u undo, ctrl+o open."))
	section("AI CHAT")
	add("%s", m.styles.Muted.Render("Tab 8 is an in-app AI conversation: type a message, press enter,"))
	add("%s", m.styles.Muted.Render("and keep talking in the same session without leaving Tash. AI runs"))
	add("%s", m.styles.Muted.Render("in the workdir shown on the tab. ctrl+n starts fresh, ctrl+o sets the"))
	add("%s", m.styles.Muted.Render("workdir, ctrl+a sets an API key, ctrl+m selects model, ctrl+r refreshes."))
	section("TASHNET SECURITY")
	add("%s", m.styles.Warning.Render("The secret key is the root of trust for your Tashnet."))
	add("%s", m.styles.Muted.Render("Regenerating it disconnects EVERY existing device immediately."))
	add("%s", m.styles.Muted.Render("New joins still require explicit approval on the main device."))
	add("%s", m.styles.Muted.Render("Treat main-role transfer codes like the secret key itself."))
	section("PATHS / ENVIRONMENT")
	add("%s", m.styles.KV("config", config.Dir(), 10))
	add("%s", m.styles.KV("secret", filepath.Join(config.Dir(), "secret.key"), 10))
	add("%s", m.styles.KV("themes", filepath.Join(config.Dir(), "themes"), 10))
	add("%s", m.styles.KV("nas", m.cfg.NAS.Root, 10))
	add("%s", m.styles.KV("runtime", m.info.GoVersion, 10))
	add("%s", m.styles.Muted.Render("Tash does not need root. Termux cannot access the Tailscale CLI;"))
	add("%s", m.styles.Muted.Render("the Android Tailscale app provides the tunnel instead."))
	return strings.TrimRight(b.String(), "\n")
}

// Setup --------------------------------------------------------------------

func (m *Model) viewSetup() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return "TASH SETUP"
	}
	s := &m.setup
	_ = s
	title, lines := m.setupContent()
	content := strings.Join(lines, "\n")
	panelW := minInt(92, w-2)
	if panelW < 24 {
		panelW = w
	}
	contentH := strings.Count(content, "\n") + 1
	panelH := minInt(contentH+4, h-2)
	if panelH < 8 {
		panelH = minInt(h, 12)
	}
	panel := m.styles.Panel("SETUP // "+title, content, panelW, panelH, true)
	hint := m.styles.Footer(panelW, m.setupHints())
	block := panel + "\n" + hint
	// On very narrow terminals, drop the panel chrome.
	if w < 50 {
		block = content + "\n" + hint
	}
	return centerBlock(block, w, h)
}

func (m *Model) setupHints() [][2]string {
	s := &m.setup
	switch s.stage {
	case stageWelcome:
		return [][2]string{{"1", "main device"}, {"2", "join tashnet"}, {"q", "quit"}}
	case stageMainSecret:
		return [][2]string{{"r", "new key"}, {"enter", "continue"}, {"esc", "back"}}
	case stageJoinSecret:
		return [][2]string{{"enter", "continue"}, {"ctrl+r", "reveal"}, {"esc", "back"}}
	case stageJoinURL:
		return [][2]string{{"enter", "join"}, {"ctrl+d", "discover"}, {"esc", "back"}}
	case stageJoinApproval:
		return [][2]string{{"esc", "back"}, {"ctrl+c", "quit"}}
	case stageDocker:
		return [][2]string{{"↑↓", "choose"}, {"enter", "continue"}, {"esc", "back"}}
	case stageDockerHost:
		return [][2]string{{"enter", "continue"}, {"esc", "back"}}
	case stageTheme:
		return [][2]string{{"↑↓", "preview"}, {"enter", "continue"}, {"esc", "back"}}
	case stagePiHole, stageNAS, stageFinish:
		return [][2]string{{"y/n", "choose"}, {"enter", "continue"}, {"esc", "back"}}
	default:
		return [][2]string{{"enter", "continue"}, {"esc", "back"}}
	}
}

func (m *Model) setupContent() (string, []string) {
	s := &m.setup
	w := minInt(86, m.width-6)
	if w < 20 {
		w = maxInt(m.width-2, 20)
	}
	logo := m.styles.Logo(w)
	if lipgloss.Width(logo) > w {
		logo = m.styles.Primary.Bold(true).Render("◢ T A S H ◣")
	}
	wrap := func(text string, style lipgloss.Style) []string {
		var out []string
		for _, line := range wrapMultiline(text, w-2) {
			out = append(out, style.Render(line))
		}
		return out
	}
	errLine := func() []string {
		if s.err == "" {
			return nil
		}
		return append([]string{""}, wrap("! "+s.err, m.styles.Danger)...)
	}

	switch s.stage {
	case stageTOS:
		lines := []string{logo, "",
			m.styles.Accent.Render("SOFTWARE LICENSE AGREEMENT & TERMS OF USE"),
			"",
		}
		tos := `# NYPR STUDIOZ
## SOFTWARE LICENSE AGREEMENT & TERMS OF USE

**Software:** Tash  
**Developer:** Nypr Studioz  
**Copyright:** © 2026 Nypr. All rights reserved.  
**License Version:** 1.0

---

### 1. Acceptance of Terms

By installing, accessing, launching, or using Tash, you acknowledge that you have read, understood, and agreed to this Software License Agreement and its Terms of Use.

If you do not agree to these terms, you must not install, access, or use Tash.

### 2. Copyright and Ownership

Tash is an independently developed software project owned by Nypr Studioz.

**© 2026 Nypr. All rights reserved.**

Unless explicitly permitted in writing by Nypr Studioz, you may not:

- Redistribute, resell, or commercially exploit Tash.
- Claim ownership of the original software.
- Remove or modify copyright notices.
- Publish modified versions as official Nypr Studioz releases.
- Impersonate Nypr Studioz or its developer.
- Copy or reproduce proprietary components outside the permissions granted by this agreement.

All original source code, interface designs, documentation, branding, and proprietary materials remain the intellectual property of their respective owners.

### 3. Independent Project and No Affiliation

Tash is independently developed and maintained by Nypr Studioz.

Tash is NOT affiliated with, sponsored by, endorsed by, maintained by, or officially connected to:

- Docker
- Tailscale
- OpenAI
- Anthropic or Claude
- Pi-hole
- Microsoft
- Any other third-party company, service, platform, or integration provider.

All third-party names, trademarks, logos, products, and services belong to their respective owners.

References to these products are solely for identification, compatibility, and integration purposes.

### 4. Third-Party Integrations

Tash may provide integrations with external applications, APIs, services, and software.

These integrations do not establish any partnership, sponsorship, ownership, endorsement, or official relationship between Nypr Studioz and the respective providers.

Nypr Studioz is not responsible for:

- Third-party service outages or interruptions.
- API changes, restrictions, or discontinuation.
- External account suspensions.
- Third-party pricing changes.
- Security incidents originating from external services.
- Data loss caused by external platforms.

Users are responsible for complying with the applicable terms and policies of services they connect to Tash.

### 5. Acceptable Use

Users agree not to:

- Use Tash for unlawful activities.
- Use the software to compromise systems or networks.
- Distribute malicious modifications under the Nypr Studioz name.
- Misrepresent the software or its capabilities.
- Circumvent applicable licensing restrictions.
- Infringe upon the intellectual property rights of others.

### 6. Software Disclaimer

Tash is provided on an "AS IS" and "AS AVAILABLE" basis, to the maximum extent permitted by applicable law.

Nypr Studioz does not guarantee:

- Uninterrupted operation.
- Compatibility with every operating system or device.
- Permanent availability of integrations.
- Freedom from bugs, errors, or technical issues.
- Compatibility with future third-party updates.

Users are responsible for maintaining appropriate backups and reviewing the consequences of actions performed through the software.

### 7. Limitation of Liability

To the maximum extent permitted by applicable law, Nypr Studioz and its developer shall not be liable for indirect, incidental, special, or consequential damages arising from the use or inability to use Tash.

Nothing in this agreement excludes liability that cannot legally be excluded.

### 8. Updates and Modifications

Nypr Studioz reserves the right to:

- Update or modify Tash.
- Introduce, modify, or remove features.
- Change supported integrations.
- Discontinue development or distribution.

Updated versions may include revised license terms. Any revised terms will be presented for acceptance where required by applicable law.

### 9. Termination

Nypr Studioz may terminate the permission granted under this agreement if a user materially violates its terms, subject to applicable law.

Upon termination, the user must discontinue use of the software and cease any distribution that is not otherwise legally permitted.

### 10. License Grant

Nypr Studioz grants the user a limited, personal, non-exclusive, non-transferable, and revocable license to use Tash, subject to this agreement.

This license does not transfer copyright, ownership, or intellectual property rights to the user.

### 11. Governing Law

This agreement shall be interpreted in accordance with the applicable laws of Thailand, subject to mandatory consumer protection and other applicable legal requirements.

### 12. Entire Agreement

This agreement represents the terms governing the user's permission to use Tash.

If any provision is found unenforceable, the remaining provisions shall continue to apply to the extent permitted by law.

---

## COPYRIGHT NOTICE

**Tash**

Developed and maintained by Nypr Studioz.

**© 2026 Nypr. All rights reserved.**

Tash is an independent software project. All third-party integrations and referenced services remain the property of their respective owners.`
		lines = append(lines, wrap(tos, m.styles.Text)...)
		lines = append(lines, "",
			m.styles.SectionTitle("ACCEPTANCE REQUIRED", w),
			"",
			m.styles.Primary.Render("  [1] I have read and agree to the Software License Agreement and Terms of Use. I acknowledge the ownership, copyright, limitations, and third-party disclaimers described in this agreement."),
			"",
			m.styles.Danger.Render("  [2] Decline and exit"),
			"",
		)
		lines = append(lines, errLine()...)
		return "00 · TERMS", lines

	case stageWelcome:
		lines := []string{logo, "",
			m.styles.Accent.Render("WELCOME, OPERATOR."),
			"",
		}
		lines = append(lines, wrap("Tash is a terminal control deck for your network: Tailscale latency, Pi-hole DNS, Docker, a WebDAV NAS, a vim editor, and Tashnet pairing.", m.styles.Text)...)
		lines = append(lines, "",
			m.styles.SectionTitle("CHOOSE THIS DEVICE'S ROLE", w),
			"",
			m.styles.Primary.Render("  [1] MAIN DEVICE")+m.styles.Muted.Render("   first device; becomes the approver"),
			m.styles.Muted.Render("      Generates the Tashnet Secret Key. Keep this device online"),
			m.styles.Muted.Render("      while new devices join; you approve each request here."),
			"",
			m.styles.Accent.Render("  [2] JOIN TASHNET")+m.styles.Muted.Render("  a device that already has the Secret Key"),
			m.styles.Muted.Render("      You will be prompted for the Secret Key, then the main"),
			m.styles.Muted.Render("      device must approve this machine."),
			"",
			m.styles.Muted.Render("The first device you set up becomes Main. You can hand the role"),
			m.styles.Muted.Render("over later with `tash main transfer`."),
		)
		lines = append(lines, errLine()...)
		return "01 · WELCOME", lines

	case stageMainSecret:
		lines := []string{
			m.styles.Warning.Render("YOU ARE CREATING A NEW TASHNET MAIN DEVICE"),
			"",
			m.styles.Text.Render("Secret Key:"),
			m.styles.Primary.Bold(true).Render(s.secret),
			"",
		}
		lines = append(lines, wrap("Save this key now. You will need it on every device that joins this Tashnet. It is stored locally in secret.key (mode 0600) and can be revealed later from the TASHNET tab.", m.styles.Muted)...)
		lines = append(lines, "",
			m.styles.Danger.Render("EXTREME WARNING: regenerating this key later disconnects ALL"),
			m.styles.Danger.Render("existing devices. They must rejoin with the new key."),
			"",
			m.styles.Muted.Render("Press r to generate a different key, or Enter to continue."),
		)
		lines = append(lines, errLine()...)
		return "02 · SECRET KEY", lines

	case stageJoinSecret:
		lines := []string{
			m.styles.Accent.Render("JOIN AN EXISTING TASHNET"),
			"",
			m.styles.Text.Render("Secret Key:"),
			m.inputRender(w),
			"",
		}
		lines = append(lines, wrap("Paste the TASH-XXXX-… key from the main device. It is never sent over the network; Tash proves knowledge with an HMAC handshake.", m.styles.Muted)...)
		lines = append(lines, errLine()...)
		return "02 · SECRET KEY", lines

	case stageJoinURL:
		lines := []string{
			m.styles.Accent.Render("FIND THE MAIN DEVICE"),
			"",
			m.styles.Text.Render("Main device address (host:port or URL):"),
			m.inputRender(w),
			"",
		}
		if s.busy {
			lines = append(lines, m.styles.Warning.Render(m.frame()+" contacting main device…"))
		}
		if len(m.discovery) > 0 {
			lines = append(lines, m.styles.SectionTitle("DISCOVERED ON TAILNET", w))
			for i, d := range m.discovery {
				if i >= 4 {
					break
				}
				lines = append(lines, fmt.Sprintf("%s %s  %s",
					m.styles.Accent.Render(d.URL), m.styles.Muted.Render(d.Hello.MainDeviceName),
					m.styles.Muted.Render("v"+d.Hello.Version)))
			}
			lines = append(lines, m.styles.Muted.Render("Type a number is not needed: press Enter with an empty field to use the first result."))
		} else {
			lines = append(lines, m.styles.Muted.Render("Tip: the main device URL is usually http://<tailscale-ip>:8787"))
			lines = append(lines, m.styles.Muted.Render("Automatic discovery needs the Tailscale CLI; on Termux paste the URL."))
		}
		lines = append(lines, errLine()...)
		return "03 · MAIN ADDRESS", lines

	case stageJoinApproval:
		lines := []string{
			m.styles.Warning.Render(m.frame() + " WAITING FOR MAIN-DEVICE APPROVAL"),
			"",
			m.styles.KV("main", firstNonEmpty(s.mainDeviceName, s.mainURL), 10),
			m.styles.KV("request", s.pendingID, 10),
			m.styles.KV("device", s.deviceName, 10),
			"",
		}
		lines = append(lines, wrap("On the MAIN device, open `tash dashboard`, go to the TASHNET tab, and approve this request. This screen polls every 2 seconds.", m.styles.Text)...)
		lines = append(lines, "",
			m.styles.Muted.Render("Keep both devices on the same tailnet. Nothing is saved until approval."),
		)
		lines = append(lines, errLine()...)
		return "04 · APPROVAL", lines

	case stageDocker:
		lines := []string{
			m.styles.Accent.Render("DOCKER CONNECTION MODE"),
			m.styles.Muted.Render("Choose how Tash should talk to Docker. You can change this later."),
			"",
			m.dockerOption(0, "CLI mode", "Best for laptops/desktops with Docker Desktop or Docker Engine installed.",
				"Uses your `docker` command and current context. Easiest, full features, follows remote contexts."),
			m.dockerOption(1, "Socket / API mode", "Best for headless servers, containers, or no docker CLI.",
				"Talks directly to the engine socket/npipe/TCP. Local socket access is effectively root; protect remote TCP with a firewall/VPN."),
			m.dockerOption(2, "Skip Docker", "Best if you do not want Docker data in the dashboard.",
				"Enable later with `tash docker use cli` or `tash docker use socket <host>`."),
			"",
			m.styles.Danger.Render("WARNING: direct socket/API access is root-equivalent on the host."),
			m.styles.Danger.Render("Never expose Docker TCP on the public internet."),
			"",
			m.styles.Muted.Render("↑↓ choose · 1/2/3 jump · Enter continue"),
		}
		lines = append(lines, errLine()...)
		return "05 · DOCKER", lines

	case stageDockerHost:
		lines := []string{
			m.styles.Accent.Render("DOCKER SOCKET / API ENDPOINT"),
			"",
			m.styles.Text.Render("Host:"),
			m.inputRender(w),
			"",
			m.styles.Muted.Render("Examples:"),
			m.styles.Muted.Render("  unix:///var/run/docker.sock"),
			m.styles.Muted.Render("  npipe:////./pipe/docker_engine"),
			m.styles.Muted.Render("  tcp://192.168.1.20:2375   (only over a trusted VPN/firewall)"),
			"",
			m.styles.Danger.Render("The Docker socket grants root-equivalent control of the host."),
			m.styles.Danger.Render("Do not expose TCP mode to the public internet."),
		}
		lines = append(lines, errLine()...)
		return "06 · DOCKER HOST", lines

	case stageTheme:
		names := m.themeNames()
		lines := []string{
			m.styles.Accent.Render("CHOOSE A THEME"),
			m.styles.Muted.Render("Every tab updates live. Enter saves the choice."),
			"",
		}
		rows := minInt(len(names), 9)
		start := 0
		if m.themeIdx >= rows {
			start = m.themeIdx - rows + 1
		}
		for i := start; i < len(names) && i < start+rows; i++ {
			sel := "  "
			if i == m.themeIdx {
				sel = m.styles.Primary.Render("▸ ")
			}
			t := m.registry.Get(names[i])
			lines = append(lines, fmt.Sprintf("%s%s %s", sel, m.styles.Text.Render(ui.Fit(names[i], 16)), themeSwatches(t)))
		}
		lines = append(lines, "", m.styles.Muted.Render("Custom themes live in "+m.registry.Dir()))
		lines = append(lines, errLine()...)
		return "07 · THEME", lines

	case stagePiHole:
		lines := []string{
			m.styles.Accent.Render("PI-HOLE DNS"),
			"",
		}
		lines = append(lines, wrap("Connect a Pi-hole to see queries, blocked counts, top blocked domains, add blocklists, run gravity, and toggle blocking. This is optional.", m.styles.Text)...)
		lines = append(lines, "",
			m.styles.Primary.Render("  [y] Configure Pi-hole now"),
			m.styles.Muted.Render("  [n] Skip — configure later with `tash pihole setup`"),
			"",
			m.styles.Muted.Render("Formula: Home > Pi-hole > Connect. Supports v6 app passwords and v5 API tokens."),
		)
		lines = append(lines, errLine()...)
		return "08 · PI-HOLE", lines

	case stagePiHoleURL:
		lines := []string{
			m.styles.Accent.Render("PI-HOLE ADDRESS"),
			"",
			m.styles.Text.Render("URL:"),
			m.inputRender(w),
			"",
			m.styles.Muted.Render("Examples: http://192.168.1.2  ·  https://pi.hole  ·  http://pi.hole:8080"),
			m.styles.Muted.Render("Self-signed HTTPS can be enabled later in config (skip_tls_verify)."),
		}
		lines = append(lines, errLine()...)
		return "09 · PI-HOLE URL", lines

	case stagePiHoleSecret:
		lines := []string{
			m.styles.Accent.Render("PI-HOLE CREDENTIAL"),
			"",
			m.styles.Text.Render("Password / app password / v5 API token:"),
			m.inputRender(w),
			"",
		}
		lines = append(lines, wrap("Pi-hole v6 prefers an application password (Settings > API). On v5, paste the API token shown in Settings > API/Web interface.", m.styles.Muted)...)
		lines = append(lines, m.styles.Muted.Render("The credential is stored locally in config.json with 0600 permissions."))
		lines = append(lines, errLine()...)
		return "10 · PI-HOLE SECRET", lines

	case stageNAS:
		def := config.Default()
		lines := []string{
			m.styles.Accent.Render("BUILT-IN NAS (WEBDAV)"),
			"",
		}
		lines = append(lines, wrap("Tash can serve a folder over authenticated WebDAV. Mount it in Explorer, Finder, davfs2, or an Android file manager. It works without root and without proot.", m.styles.Text)...)
		lines = append(lines, "",
			m.styles.KV("root", def.NAS.Root, 8),
			m.styles.KV("user", def.NAS.User, 8),
			m.styles.KV("pass", "(generated)", 8),
			m.styles.KV("port", itoa(def.NAS.Port), 8),
			"",
			m.styles.Primary.Render("  [y] Start the NAS automatically with the dashboard"),
			m.styles.Muted.Render("  [n] Leave it stopped; start from the NAS tab"),
		)
		lines = append(lines, errLine()...)
		return "11 · NAS", lines

	case stageFinish:
		role := "MAIN DEVICE"
		if s.role == "client" {
			role = "CLIENT"
		}
		dockerKind := map[string]string{"cli": "CLI", "socket": "socket/API", "off": "disabled"}[s.dockerMode]
		pi := "off"
		if s.piholeOn {
			pi = s.piholeURL
		}
		nas := "manual"
		if s.nasAuto {
			nas = "auto-start"
		}
		lines := []string{
			m.styles.Accent.Render("READY TO INITIALIZE"),
			"",
			m.styles.KV("role", role, 10),
			m.styles.KV("device", s.deviceName+" · "+shortID(s.deviceID), 10),
			m.styles.KV("theme", s.themeName, 10),
			m.styles.KV("docker", dockerKind, 10),
			m.styles.KV("pi-hole", pi, 10),
			m.styles.KV("nas", nas, 10),
			"",
		}
		if s.role == "main" {
			lines = append(lines,
				m.styles.Text.Render("Secret Key:"),
				m.styles.Primary.Bold(true).Render(s.secret),
				"",
				m.styles.Warning.Render("Store the key before pressing Enter. Regeneration later"),
				m.styles.Warning.Render("disconnects every existing device."),
				"",
			)
		} else {
			lines = append(lines,
				m.styles.Success.Render("Approved by "+firstNonEmpty(s.mainDeviceName, s.mainURL)+"."),
				m.styles.Muted.Render("This device will authenticate over Tashnet with its approved token."),
				"",
			)
		}
		lines = append(lines, m.styles.Muted.Render("Press Enter to initialize Tash."))
		lines = append(lines, errLine()...)
		return "12 · FINISH", lines
	}
	return "SETUP", []string{"unknown stage"}
}

func (m *Model) inputRender(w int) string {
	s := &m.setup
	text := s.input
	if s.inputSecret {
		text = strings.Repeat("•", len([]rune(text)))
	}
	r := []rune(text)
	pos := s.inputCursor
	if pos > len(r) {
		pos = len(r)
	}
	if pos < 0 {
		pos = 0
	}
	before := string(r[:pos])
	after := string(r[pos:])
	return m.styles.Accent.Render("  "+before) + m.styles.Primary.Render("▌") + m.styles.Accent.Render(after)
}

func (m *Model) dockerOption(idx int, name, bestFor, tradeoffs string) string {
	selected := idx == m.setup.dockerIndex()
	marker := "  "
	nameStyle := m.styles.Text
	if selected {
		marker = m.styles.Primary.Render("▸ ")
		nameStyle = m.styles.Primary.Bold(true)
	}
	badgeColor := m.styles.Theme.Muted
	if selected {
		badgeColor = m.styles.Theme.Primary
	}
	box := marker + m.styles.BadgeLabel(fmt.Sprintf("%d", idx+1), badgeColor) + " " + nameStyle.Render(name)
	return strings.Join([]string{
		box,
		m.styles.Muted.Render("    best for: " + bestFor),
		m.styles.Muted.Render("    " + tradeoffs),
	}, "\n")
}

// Modal rendering ----------------------------------------------------------

func (m *Model) modalView() string {
	md := m.modal
	w := m.contentWidth()
	if w > 100 {
		w = 100
	}
	if w < 30 {
		w = 30
	}
	body := md.body
	if body != "" {
		body += "\n\n"
	}
	switch md.kind {
	case modalKindInput:
		body += m.styles.Text.Render(md.prompt) + "\n"
		body += m.modalInputRender()
	case modalKindConfirm:
		body += m.styles.Warning.Render(md.prompt)
	case modalKindInfo:
		// body only
	case modalKindCommandPalette:
		cp := md.cmdPalette
		body += m.styles.Text.Render("Search: ") + m.styles.Accent.Render(cp.query) + "█\n"
		body += m.styles.Muted.Render(fmt.Sprintf("  %d commands", len(cp.filtered))) + "\n\n"
		rows := maxInt(m.bodyHeight()-12, 5)
		start := 0
		if cp.cursor >= rows {
			start = cp.cursor - rows + 1
		}
		for i := start; i < len(cp.filtered) && i < start+rows; i++ {
			item := cp.filtered[i]
			sel := "  "
			if i == cp.cursor {
				sel = m.styles.Primary.Render("▸ ")
			}
			body += fmt.Sprintf("%s%s  %s  %s\n", sel,
				m.styles.Text.Render(item.title),
				m.styles.Muted.Render("("+item.keys+")"),
				m.styles.Muted.Render(item.description))
		}
	}
	if md.require != "" {
		body += "\n\n" + m.styles.Danger.Render("Type "+md.require+" to confirm:") + "\n" + m.modalInputRender()
	}
	lines := strings.Split(body, "\n")
	rows := m.bodyHeight() - 6
	if rows < 5 {
		rows = 5
	}
	visible := lines
	if len(visible) > rows {
		start := md.scroll
		if start > len(visible)-rows {
			start = len(visible) - rows
		}
		if start < 0 {
			start = 0
		}
		visible = visible[start : start+rows]
	}
	hint := "esc close"
	switch md.kind {
	case modalKindInfo:
		hint = "↑↓/pgup/pgdn scroll · esc close"
	case modalKindConfirm:
		hint = "enter / y confirm · esc cancel"
	case modalKindInput:
		hint = "enter submit · esc cancel"
	case modalKindCommandPalette:
		hint = "↑↓ select · enter execute · esc close · type to filter"
	}
	if md.require != "" {
		hint = "type the word, then enter · esc cancel"
	}
	panelH := len(visible) + 4
	if panelH > m.bodyHeight() {
		panelH = m.bodyHeight()
	}
	panel := m.styles.Panel("◢ "+md.title, strings.Join(visible, "\n"), w, panelH, true)
	return panel + "\n" + m.styles.Footer(w, [][2]string{{"esc", hint}})
}

func (m *Model) modalInputRender() string {
	md := &m.modal
	text := md.value
	if md.secret {
		text = strings.Repeat("•", len([]rune(text)))
	}
	r := []rune(text)
	pos := md.cursor
	if pos > len(r) {
		pos = len(r)
	}
	if pos < 0 {
		pos = 0
	}
	return m.styles.Accent.Render("  "+string(r[:pos])) + m.styles.Primary.Render("▌") + m.styles.Accent.Render(string(r[pos:]))
}

// Utilities ----------------------------------------------------------------

func formatCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func truncate(s string, w int) string {
	if w <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

func wrapMultiline(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, ui.Wrap(line, width)...)
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	if s == "" {
		return "updated"
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

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:7] + "…"
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func sortedPending(m map[string]config.Pending) []config.Pending {
	out := make([]config.Pending, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out
}

func sortedDevices(m map[string]config.Device) []config.Device {
	out := make([]config.Device, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Name < out[j].Name
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
