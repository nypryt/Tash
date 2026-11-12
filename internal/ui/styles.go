// Package ui contains Tash's visual language: panels, widgets, and ASCII art.
package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tash/tash/internal/theme"
)

// Styles is a theme-bound style set.
type Styles struct {
	Theme theme.Theme

	Title   lipgloss.Style
	Text    lipgloss.Style
	Muted   lipgloss.Style
	Primary lipgloss.Style
	Accent  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Danger  lipgloss.Style
	Key     lipgloss.Style
	Badge   lipgloss.Style
}

// New builds a Styles from a theme.
func New(t theme.Theme) Styles {
	c := func(s string) lipgloss.Color { return lipgloss.Color(s) }
	return Styles{
		Theme:   t,
		Title:   lipgloss.NewStyle().Foreground(c(t.Primary)).Bold(true),
		Text:    lipgloss.NewStyle().Foreground(c(t.Text)),
		Muted:   lipgloss.NewStyle().Foreground(c(t.Muted)),
		Primary: lipgloss.NewStyle().Foreground(c(t.Primary)),
		Accent:  lipgloss.NewStyle().Foreground(c(t.Accent)),
		Success: lipgloss.NewStyle().Foreground(c(t.Success)),
		Warning: lipgloss.NewStyle().Foreground(c(t.Warning)),
		Danger:  lipgloss.NewStyle().Foreground(c(t.Danger)),
		Key:     lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		Badge:   lipgloss.NewStyle().Background(c(t.Primary)).Foreground(c("#001008")).Bold(true),
	}
}

// Width is an ANSI-aware display width.
func Width(s string) int { return lipgloss.Width(s) }

// Fit truncates or pads s to exactly w display cells.
func Fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// FitRight right-aligns s in w cells.
func FitRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - Width(s); pad > 0 {
		s = strings.Repeat(" ", pad) + s
	}
	return s
}

// Panel draws a titled, coloured box with an exact outer size.
func (st Styles) Panel(title, content string, w, h int, active bool) string {
	if w < 6 || h < 3 {
		return Fit(content, max(w, 0))
	}
	borderColor := st.Theme.Border
	if active {
		borderColor = st.Theme.Primary
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(borderColor))
	inner := w - 2
	titleText := " " + strings.TrimSpace(title) + " "
	if Width(titleText) > inner-2 {
		titleText = ansi.Truncate(titleText, inner-2, "…")
	}
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(st.Theme.Primary)).Bold(true)
	if active {
		titleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(st.Theme.Accent)).Bold(true)
	}
	fill := inner - 1 - Width(titleText)
	if fill < 0 {
		fill = 0
	}
	top := border.Render("╭─") + titleStyle.Render(titleText) + border.Render(strings.Repeat("─", fill)) + border.Render("╮")
	bottom := border.Render("╰" + strings.Repeat("─", inner) + "╯")
	rows := h - 2
	lines := strings.Split(content, "\n")
	out := make([]string, 0, h)
	out = append(out, top)
	for i := 0; i < rows; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, border.Render("│")+" "+Fit(line, w-4)+" "+border.Render("│"))
	}
	out = append(out, bottom)
	return strings.Join(out, "\n")
}

// Header renders the top status bar.
func (st Styles) Header(width int, network, role, device string, t time.Time, clock24 bool) string {
	if width < 30 {
		return st.Primary.Render("▚ TASH") + st.Muted.Render(" // "+network)
	}
	left := st.Primary.Bold(true).Render("▚ TASH") + st.Muted.Render(" // "+network)
	mid := st.Muted.Render(role + " · " + device)
	clock := t.Format("15:04:05")
	if !clock24 {
		clock = t.Format("03:04:05 PM")
	}
	right := st.Accent.Render(clock)
	gap1 := max(1, (width-Width(left)-Width(mid)-Width(right))/3)
	line := left + strings.Repeat(" ", gap1) + mid
	pad := width - Width(line) - Width(right)
	if pad < 1 {
		pad = 1
	}
	return line + strings.Repeat(" ", pad) + right
}

// Tabs renders the primary navigation.
func (st Styles) Tabs(names []string, active, width int) string {
	var parts []string
	for i, name := range names {
		key := fmt.Sprintf("%d", i+1)
		if i == active {
			parts = append(parts, lipgloss.NewStyle().
				Background(lipgloss.Color(st.Theme.Primary)).
				Foreground(lipgloss.Color("#001008")).Bold(true).
				Render(" "+key+" "+name+" "))
		} else {
			parts = append(parts, st.Muted.Render(" "+key+" ")+st.Text.Render(name+" "))
		}
	}
	line := strings.Join(parts, " ")
	if Width(line) > width {
		// On narrow terminals use compact numeric tabs only.
		parts = nil
		for i, name := range names {
			label := fmt.Sprintf("%d", i+1)
			if Width(strings.Join(parts, " ")) > width {
				break
			}
			if i == active {
				parts = append(parts, lipgloss.NewStyle().Background(lipgloss.Color(st.Theme.Primary)).Foreground(lipgloss.Color("#001008")).Bold(true).Render(" "+label+" "+shortName(name)+" "))
			} else {
				parts = append(parts, st.Muted.Render(" "+label+" "))
			}
		}
		line = strings.Join(parts, " ")
	}
	return Fit(line, width)
}

// TabSpec is one visible dashboard tab with its assigned number key.
type TabSpec struct {
	Key  string
	Name string
}

// TabsWithKeys renders navigation for a custom, possibly filtered tab list.
func (st Styles) TabsWithKeys(items []TabSpec, active, width int) string {
	if len(items) == 0 {
		return Fit("", width)
	}
	var parts []string
	for i, item := range items {
		if i == active {
			parts = append(parts, lipgloss.NewStyle().
				Background(lipgloss.Color(st.Theme.Primary)).
				Foreground(lipgloss.Color("#001008")).Bold(true).
				Render(" "+item.Key+" "+item.Name+" "))
		} else {
			parts = append(parts, st.Muted.Render(" "+item.Key+" ")+st.Text.Render(item.Name+" "))
		}
	}
	line := strings.Join(parts, " ")
	if Width(line) > width {
		parts = nil
		for i, item := range items {
			label := item.Key
			if i == active {
				parts = append(parts, lipgloss.NewStyle().Background(lipgloss.Color(st.Theme.Primary)).Foreground(lipgloss.Color("#001008")).Bold(true).Render(" "+label+" "+shortName(item.Name)+" "))
			} else {
				parts = append(parts, st.Muted.Render(" "+label+" "))
			}
		}
		line = strings.Join(parts, " ")
	}
	return Fit(line, width)
}

func shortName(n string) string {
	if len(n) <= 4 {
		return n
	}
	return n[:4]
}

// Footer renders key hints.
func (st Styles) Footer(width int, hints [][2]string) string {
	var parts []string
	for _, h := range hints {
		parts = append(parts, st.Key.Render(h[0])+" "+st.Muted.Render(h[1]))
	}
	line := " " + strings.Join(parts, st.Muted.Render(" · "))
	return Fit(line, width)
}

// BadgeLabel renders a small inverted label.
func (st Styles) BadgeLabel(label, color string) string {
	return lipgloss.NewStyle().Background(lipgloss.Color(color)).Foreground(lipgloss.Color("#001008")).Bold(true).Render(" " + label + " ")
}

// Dot renders an online/offline indicator.
func (st Styles) Dot(ok bool) string {
	if ok {
		return st.Success.Render("●")
	}
	return st.Danger.Render("○")
}

// Bar draws a percentage bar.
func (st Styles) Bar(percent float64, width int) string {
	if width < 2 {
		width = 2
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round(percent / 100 * float64(width)))
	if filled > width {
		filled = width
	}
	color := st.Theme.Primary
	if percent >= 50 {
		color = st.Theme.Warning
	}
	if percent >= 80 {
		color = st.Theme.Danger
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color(st.Theme.Muted)).Render(strings.Repeat("░", width-filled))
}

// Sparkline draws a tiny latency graph from values in milliseconds.
func (st Styles) Sparkline(values []float64, width int) string {
	if width < 1 {
		return ""
	}
	if len(values) == 0 {
		return st.Muted.Render(strings.Repeat("·", min(width, 12)))
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	lo, hi := values[0], values[0]
	for _, v := range values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	var b strings.Builder
	for _, v := range values {
		idx := 0
		if span > 0 {
			idx = int((v - lo) / span * float64(len(levels)-1))
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(levels) {
			idx = len(levels) - 1
		}
		color := st.Theme.Primary
		if v > 150 {
			color = st.Theme.Warning
		}
		if v > 400 {
			color = st.Theme.Danger
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(string(levels[idx])))
	}
	return b.String()
}

// Logo returns the big TASH wordmark, or a compact one when narrow.
func (st Styles) Logo(width int) string {
	if width < 48 {
		return st.Primary.Bold(true).Render("◢ T A S H ◣") + st.Muted.Render("  TASHNET")
	}
	lines := []string{
		"████████╗ █████╗ ███████╗██╗  ██╗",
		"╚══██╔══╝██╔══██╗██╔════╝██║  ██║",
		"   ██║   ███████║███████╗███████║",
		"   ██║   ██╔══██║╚════██║██╔══██║",
		"   ██║   ██║  ██║███████║██║  ██║",
		"   ╚═╝   ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝",
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		color := st.Theme.Primary
		if i == len(lines)-1 {
			color = st.Theme.Muted
		}
		out[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(i < 3).Render(line)
	}
	return strings.Join(out, "\n")
}

// MiniLogo is used in compact headers.
func (st Styles) MiniLogo() string {
	return st.Primary.Bold(true).Render("▚ TASH") + st.Muted.Render(" // TASHNET")
}

// SectionTitle renders a labelled divider.
func (st Styles) SectionTitle(label string, width int) string {
	line := st.Title.Render(" " + label + " ")
	rest := width - Width(line)
	if rest < 2 {
		return Fit(line, width)
	}
	return line + st.Muted.Render(strings.Repeat("─", rest))
}

// KV renders a label/value line with a fixed label width.
func (st Styles) KV(label, value string, labelWidth int) string {
	if labelWidth < 1 {
		labelWidth = 12
	}
	return st.Muted.Render(Fit(label, labelWidth)) + st.Text.Render(value)
}

// Wrap hard-wraps plain text (no ANSI) to width.
func Wrap(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			if Width(line+" "+word) <= width {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// HumanBytes formats a byte count.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanAgo formats a relative time.
func HumanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// SortTop sorts a top list descending.
func SortTop(items []struct {
	Name  string
	Count int64
}) {
	sort.Slice(items, func(i, j int) bool { return items[i].Count > items[j].Count })
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
