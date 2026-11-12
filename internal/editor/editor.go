// Package editor implements a small vim-flavoured modal text editor for Tash.
package editor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/theme"
)

// Mode is the current vim mode.
type Mode int

const (
	Normal Mode = iota
	Insert
	Command
	Search
)

// Palette carries the UI theme colours used by the editor.
type Palette struct {
	Primary   string
	Secondary string
	Accent    string
	Success   string
	Warning   string
	Danger    string
	Muted     string
	Text      string
	Selection string
}

// PaletteFromTheme converts a Tash theme.
func PaletteFromTheme(t theme.Theme) Palette {
	return Palette{
		Primary: t.Primary, Secondary: t.Secondary, Accent: t.Accent,
		Success: t.Success, Warning: t.Warning, Danger: t.Danger,
		Muted: t.Muted, Text: t.Text, Selection: t.Selection,
	}
}

type snapshot struct {
	lines [][]rune
	cx    int
	cy    int
}

// Model is a modal text buffer.
type Model struct {
	lines    [][]rune
	cx       int
	cy       int
	offX     int
	offY     int
	mode     Mode
	cmd      string
	message  string
	path     string
	dirty    bool
	eolCRLF  bool
	trailing bool
	width    int
	height   int
	palette  Palette
	undo     []snapshot
	clip     [][]rune
	pending  string
	search   string
	// quitRequest asks the host to leave the editor tab after :q/:wq.
	quitRequest bool
	// newFile is true for paths that did not exist when opened.
	newFile bool
}

const maxFileSize = 8 << 20

// New returns an empty editor with a scratch buffer.
func New(p Palette) *Model {
	return &Model{
		lines: [][]rune{{}}, palette: p, width: 80, height: 20,
		message: "o: open file  :help  i: insert  :q to close",
	}
}

// SetPalette updates the colour palette.
func (m *Model) SetPalette(p Palette) { m.palette = p }

// SetSize sets the inner drawing area (including status bars).
func (m *Model) SetSize(w, h int) {
	if w < 10 {
		w = 10
	}
	if h < 4 {
		h = 4
	}
	m.width, m.height = w, h
	m.clamp()
}

// Path returns the current file path (empty for a scratch buffer).
func (m *Model) Path() string { return m.path }

// Dirty reports unsaved changes.
func (m *Model) Dirty() bool { return m.dirty }

// ModeLabel returns a short mode name for the footer.
func (m *Model) ModeLabel() string {
	switch m.mode {
	case Insert:
		return "INSERT"
	case Command:
		return "COMMAND"
	case Search:
		return "SEARCH"
	default:
		return "NORMAL"
	}
}

// CloseRequested reports a pending :q/:wq.
func (m *Model) CloseRequested() bool { return m.quitRequest }

// ClearCloseRequest clears the quit flag after the host handles it.
func (m *Model) ClearCloseRequest() { m.quitRequest = false }

// Filename returns a friendly name for the status bar.
func (m *Model) Filename() string {
	if m.path == "" {
		return "[No Name]"
	}
	name := filepath.Base(m.path)
	if m.newFile {
		return name + " [new]"
	}
	return name
}

// Load opens path, creating a buffer for a missing file.
func (m *Model) Load(path string) error {
	path = config.ExpandPath(path)
	if path == "" {
		return fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		m.lines = [][]rune{{}}
		m.path = path
		m.cx, m.cy, m.offX, m.offY = 0, 0, 0, 0
		m.dirty = false
		m.newFile = true
		m.eolCRLF = false
		m.trailing = true
		m.undo = nil
		m.message = "new file: " + path
		return nil
	}
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if st.Size() > maxFileSize {
		return fmt.Errorf("file is larger than 8 MiB")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.IndexByte(string(b), 0) >= 0 {
		return fmt.Errorf("refusing to open a binary file")
	}
	text := string(b)
	text = strings.TrimPrefix(text, "\ufeff")
	m.eolCRLF = strings.Contains(text, "\r\n")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	m.trailing = strings.HasSuffix(text, "\n")
	if m.trailing {
		text = strings.TrimSuffix(text, "\n")
	}
	parts := strings.Split(text, "\n")
	m.lines = make([][]rune, len(parts))
	for i, p := range parts {
		m.lines[i] = []rune(p)
	}
	if len(m.lines) == 0 {
		m.lines = [][]rune{{}}
	}
	m.path = path
	m.cx, m.cy, m.offX, m.offY = 0, 0, 0, 0
	m.dirty = false
	m.newFile = false
	m.undo = nil
	m.mode = Normal
	m.message = fmt.Sprintf("loaded %s (%d lines)", filepath.Base(path), len(m.lines))
	m.clamp()
	return nil
}

// HandleKey processes one Bubble Tea key.
func (m *Model) HandleKey(msg tea.KeyMsg) {
	switch m.mode {
	case Insert:
		m.keyInsert(msg)
	case Command, Search:
		m.keyCommand(msg)
	default:
		m.keyNormal(msg)
	}
}

// Update makes the editor usable as a standalone Bubble Tea model.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		m.HandleKey(key)
	}
	return m, nil
}

func (m *Model) keyNormal(msg tea.KeyMsg) {
	// Pending two-key commands (dd, yy, gg, r<char>).
	if m.pending != "" {
		pending := m.pending
		m.pending = ""
		switch pending {
		case "g":
			if msg.String() == "g" {
				m.cy, m.cx = 0, 0
			}
			m.clamp()
			return
		case "d":
			if msg.String() == "d" {
				m.deleteLine()
				return
			}
			m.message = ""
			return
		case "y":
			if msg.String() == "y" {
				m.yankLine()
				return
			}
			m.message = ""
			return
		case "r":
			if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
				m.pushUndo()
				line := m.lines[m.cy]
				if m.cx < len(line) {
					line[m.cx] = msg.Runes[0]
					m.dirty = true
				}
			}
			return
		default:
			m.message = ""
			return
		}
	}
	key := msg.String()
	switch key {
	case "h", "left":
		if m.cx > 0 {
			m.cx--
		}
	case "l", "right":
		if m.cx < m.maxXValue() {
			m.cx++
		}
	case "j", "down":
		if m.cy < len(m.lines)-1 {
			m.cy++
			m.clamp()
		}
	case "k", "up":
		if m.cy > 0 {
			m.cy--
			m.clamp()
		}
	case "0", "home":
		m.cx = 0
	case "$", "end":
		m.cx = m.maxXValue()
	case "w":
		m.wordForward()
	case "b":
		m.wordBack()
	case "e":
		m.wordEnd()
	case "i":
		m.pushUndo()
		m.mode = Insert
		m.message = ""
	case "a":
		m.pushUndo()
		if m.cx < len(m.lines[m.cy]) {
			m.cx++
		}
		m.mode = Insert
		m.message = ""
	case "I":
		m.pushUndo()
		m.cx = firstNonSpace(m.lines[m.cy])
		m.mode = Insert
	case "A":
		m.pushUndo()
		m.cx = len(m.lines[m.cy])
		m.mode = Insert
	case "o":
		m.pushUndo()
		m.lines = append(m.lines, nil)
		copy(m.lines[m.cy+2:], m.lines[m.cy+1:])
		m.lines[m.cy+1] = []rune{}
		m.cy++
		m.cx = 0
		m.mode = Insert
		m.dirty = true
	case "O":
		m.pushUndo()
		m.lines = append(m.lines, nil)
		copy(m.lines[m.cy+1:], m.lines[m.cy:])
		m.lines[m.cy] = []rune{}
		m.cx = 0
		m.mode = Insert
		m.dirty = true
	case "x":
		m.pushUndo()
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.lines[m.cy] = append(line[:m.cx], line[m.cx+1:]...)
			m.dirty = true
			m.clamp()
		}
	case "X":
		if m.cx > 0 {
			m.pushUndo()
			line := m.lines[m.cy]
			m.lines[m.cy] = append(line[:m.cx-1], line[m.cx:]...)
			m.cx--
			m.dirty = true
		}
	case "d":
		m.pending = "d"
	case "y":
		m.pending = "y"
	case "g":
		m.pending = "g"
	case "G":
		m.cy = len(m.lines) - 1
		m.cx = 0
		m.clamp()
	case "p":
		m.paste(false)
	case "P":
		m.paste(true)
	case "u":
		m.undoOne()
	case "ctrl+d":
		m.cy += m.contentHeight() / 2
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clamp()
	case "ctrl+u":
		m.cy -= m.contentHeight() / 2
		if m.cy < 0 {
			m.cy = 0
		}
		m.clamp()
	case "pgdown":
		m.cy += m.contentHeight()
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clamp()
	case "pgup":
		m.cy -= m.contentHeight()
		if m.cy < 0 {
			m.cy = 0
		}
		m.clamp()
	case ":":
		m.mode = Command
		m.cmd = ""
		m.message = ""
	case "/":
		m.mode = Search
		m.cmd = ""
		m.message = ""
	case "n":
		m.findNext(1)
	case "N":
		m.findNext(-1)
	case "r":
		m.pending = "r"
	case "~":
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.pushUndo()
			if unicode.IsUpper(line[m.cx]) {
				line[m.cx] = unicode.ToLower(line[m.cx])
			} else if unicode.IsLower(line[m.cx]) {
				line[m.cx] = unicode.ToUpper(line[m.cx])
			}
			m.dirty = true
			if m.cx < m.maxXValue() {
				m.cx++
			}
		}
	}
	if m.mode == Insert {
		m.clampInsert()
	} else {
		m.clamp()
	}
}

func (m *Model) keyInsert(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = Normal
		if m.cx > m.maxXValue() {
			m.cx = m.maxXValue()
		}
		m.clamp()
		return
	case tea.KeyEnter:
		m.splitLine()
		m.dirty = true
		return
	case tea.KeyBackspace:
		m.backspace()
		m.dirty = true
		return
	case tea.KeyDelete:
		m.deleteForward()
		m.dirty = true
		return
	case tea.KeyTab:
		m.insertText([]rune("    "))
		m.dirty = true
		return
	case tea.KeyLeft:
		if m.cx > 0 {
			m.cx--
		}
		return
	case tea.KeyRight:
		if m.cx < len(m.lines[m.cy]) {
			m.cx++
		}
		return
	case tea.KeyUp:
		if m.cy > 0 {
			m.cy--
			m.clampInsert()
		}
		return
	case tea.KeyDown:
		if m.cy < len(m.lines)-1 {
			m.cy++
			m.clampInsert()
		}
		return
	case tea.KeyHome:
		m.cx = 0
		return
	case tea.KeyEnd:
		m.cx = len(m.lines[m.cy])
		return
	case tea.KeyRunes:
		m.insertText(msg.Runes)
		m.dirty = true
		return
	}
}

func (m *Model) keyCommand(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = Normal
		m.cmd = ""
		m.message = ""
		return
	case tea.KeyEnter:
		text := strings.TrimSpace(m.cmd)
		search := m.mode == Search
		m.cmd = ""
		m.mode = Normal
		if search {
			if text == "" {
				return
			}
			m.search = text
			m.findNext(1)
			return
		}
		m.runCommand(text)
		return
	case tea.KeyBackspace:
		r := []rune(m.cmd)
		if len(r) > 0 {
			m.cmd = string(r[:len(r)-1])
		}
		return
	case tea.KeyRunes:
		if len(msg.Runes) > 0 && !containsControl(msg.Runes) {
			m.cmd += string(msg.Runes)
		}
		return
	}
}

func (m *Model) runCommand(cmd string) {
	if cmd == "" {
		return
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return
	}
	op := fields[0]
	force := strings.HasSuffix(op, "!")
	op = strings.TrimSuffix(op, "!")
	arg := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
	switch op {
	case "w", "write":
		if arg != "" {
			m.path = config.ExpandPath(arg)
			if abs, err := filepath.Abs(m.path); err == nil {
				m.path = abs
			}
		}
		if err := m.save(); err != nil {
			m.message = "E: " + err.Error()
		}
	case "q", "quit":
		if m.dirty && !force {
			m.message = "E: unsaved changes (use :q! to discard)"
			return
		}
		m.quitRequest = true
	case "wq", "x":
		if err := m.save(); err != nil {
			m.message = "E: " + err.Error()
			return
		}
		m.quitRequest = true
	case "e", "edit":
		if arg == "" {
			if m.path == "" {
				m.message = "E: no file name"
				return
			}
			arg = m.path
		}
		if m.dirty && !force {
			m.message = "E: unsaved changes (use :e! file to discard)"
			return
		}
		if err := m.Load(arg); err != nil {
			m.message = "E: " + err.Error()
		}
	case "set":
		m.message = "settings are managed from the Themes tab"
	case "help":
		m.message = "i insert · esc normal · :w save · :q quit · / search · dd delete line · yy yank · p paste · u undo"
	default:
		m.message = "E: unknown command :" + op
	}
}

func (m *Model) save() error {
	if m.path == "" {
		return fmt.Errorf("no file name (use :w <path>)")
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	eol := "\n"
	if m.eolCRLF {
		eol = "\r\n"
	}
	for i, line := range m.lines {
		if i > 0 {
			b.WriteString(eol)
		}
		b.WriteString(string(line))
	}
	if m.trailing || len(m.lines) > 1 || m.path != "" {
		b.WriteString(eol)
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(m.path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(m.path, []byte(b.String()), mode); err != nil {
		return err
	}
	m.dirty = false
	m.newFile = false
	m.message = "written " + m.path
	return nil
}

func (m *Model) insertText(runes []rune) {
	if len(runes) == 0 {
		return
	}
	// Bracketed paste can contain newlines: split across lines.
	parts := strings.Split(string(runes), "\n")
	if len(parts) == 1 {
		line := m.lines[m.cy]
		next := make([]rune, 0, len(line)+len(runes))
		next = append(next, line[:m.cx]...)
		next = append(next, runes...)
		next = append(next, line[m.cx:]...)
		m.lines[m.cy] = next
		m.cx += len(runes)
		return
	}
	for i, p := range parts {
		if i == 0 {
			m.insertText([]rune(p))
			continue
		}
		m.splitLine()
		m.insertText([]rune(p))
	}
}

func (m *Model) splitLine() {
	line := m.lines[m.cy]
	left := append([]rune{}, line[:m.cx]...)
	right := append([]rune{}, line[m.cx:]...)
	m.lines[m.cy] = left
	m.lines = append(m.lines, nil)
	copy(m.lines[m.cy+2:], m.lines[m.cy+1:])
	m.lines[m.cy+1] = right
	m.cy++
	m.cx = 0
	m.clamp()
}

func (m *Model) backspace() {
	if m.cx > 0 {
		line := m.lines[m.cy]
		m.lines[m.cy] = append(line[:m.cx-1], line[m.cx:]...)
		m.cx--
		return
	}
	if m.cy == 0 {
		return
	}
	prev := m.lines[m.cy-1]
	m.cx = len(prev)
	m.lines[m.cy-1] = append(prev, m.lines[m.cy]...)
	m.lines = append(m.lines[:m.cy], m.lines[m.cy+1:]...)
	m.cy--
	m.clamp()
}

func (m *Model) deleteForward() {
	line := m.lines[m.cy]
	if m.cx < len(line) {
		m.lines[m.cy] = append(line[:m.cx], line[m.cx+1:]...)
		return
	}
	if m.cy < len(m.lines)-1 {
		m.lines[m.cy] = append(line, m.lines[m.cy+1]...)
		m.lines = append(m.lines[:m.cy+1], m.lines[m.cy+2:]...)
	}
}

func (m *Model) deleteLine() {
	m.pushUndo()
	if len(m.lines) == 1 {
		m.lines[0] = []rune{}
		m.cx, m.cy = 0, 0
		m.dirty = true
		return
	}
	m.clip = [][]rune{append([]rune{}, m.lines[m.cy]...)}
	m.lines = append(m.lines[:m.cy], m.lines[m.cy+1:]...)
	if m.cy >= len(m.lines) {
		m.cy = len(m.lines) - 1
	}
	m.cx = 0
	m.dirty = true
}

func (m *Model) yankLine() {
	m.clip = [][]rune{append([]rune{}, m.lines[m.cy]...)}
	m.message = "yanked 1 line"
}

func (m *Model) paste(after bool) {
	if len(m.clip) == 0 {
		m.message = "E: clipboard is empty"
		return
	}
	m.pushUndo()
	at := m.cy
	if after {
		at++
	}
	for _, line := range m.clip {
		m.lines = append(m.lines, nil)
		copy(m.lines[at+1:], m.lines[at:])
		m.lines[at] = append([]rune{}, line...)
		at++
	}
	m.cy = at - 1
	m.cx = 0
	m.dirty = true
	m.message = fmt.Sprintf("pasted %d line(s)", len(m.clip))
}

func (m *Model) pushUndo() {
	if len(m.undo) >= 200 {
		m.undo = m.undo[1:]
	}
	cp := make([][]rune, len(m.lines))
	for i, l := range m.lines {
		cp[i] = append([]rune{}, l...)
	}
	m.undo = append(m.undo, snapshot{lines: cp, cx: m.cx, cy: m.cy})
}

func (m *Model) undoOne() {
	if len(m.undo) == 0 {
		m.message = "already at oldest change"
		return
	}
	s := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	m.lines = s.lines
	m.cx, m.cy = s.cx, s.cy
	m.dirty = true
	m.clamp()
	m.message = "undo"
}

func (m *Model) wordForward() {
	line := m.lines[m.cy]
	for m.cx < len(line) && isWord(line[m.cx]) {
		m.cx++
	}
	for m.cx < len(line) && !isWord(line[m.cx]) {
		m.cx++
	}
	if m.cx >= len(line) && m.cy < len(m.lines)-1 {
		m.cy++
		m.cx = 0
	}
	m.clamp()
}

func (m *Model) wordBack() {
	for m.cx > 0 {
		m.cx--
		if isWord(m.lines[m.cy][m.cx]) {
			for m.cx > 0 && isWord(m.lines[m.cy][m.cx-1]) {
				m.cx--
			}
			return
		}
	}
	if m.cx == 0 && m.cy > 0 {
		m.cy--
		m.cx = m.maxXValue()
	}
}

func (m *Model) wordEnd() {
	line := m.lines[m.cy]
	for m.cx < len(line)-1 && !isWord(line[m.cx]) {
		m.cx++
	}
	for m.cx < len(line)-1 && isWord(line[m.cx+1]) {
		m.cx++
	}
	m.clamp()
}

func isWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func firstNonSpace(line []rune) int {
	for i, r := range line {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return 0
}

func (m *Model) findNext(dir int) {
	if m.search == "" {
		m.message = "no previous search"
		return
	}
	startLine := m.cy
	startCol := m.cx + 1
	if dir < 0 {
		startCol = m.cx - 1
	}
	for n := 0; n < len(m.lines); n++ {
		lineIdx := startLine + dir*n
		if lineIdx < 0 || lineIdx >= len(m.lines) {
			break
		}
		line := m.lines[lineIdx]
		if lineIdx != startLine {
			if dir > 0 {
				startCol = 0
			} else {
				startCol = len(line)
			}
		}
		hay := strings.ToLower(string(line))
		needle := strings.ToLower(m.search)
		idx := -1
		if dir > 0 {
			idx = strings.Index(hay[min(startCol, len(hay)):], needle)
			if idx >= 0 {
				idx += min(startCol, len(hay))
			}
		} else {
			limit := max(0, startCol)
			if limit > len(hay) {
				limit = len(hay)
			}
			idx = strings.LastIndex(hay[:limit], needle)
			if idx < 0 && limit == len(hay) {
				idx = strings.LastIndex(hay, needle)
			}
		}
		if idx >= 0 {
			m.cy = lineIdx
			m.cx = utf8.RuneCountInString(hay[:idx])
			m.clamp()
			m.message = fmt.Sprintf("/%s", m.search)
			return
		}
		startCol = 0
	}
	m.message = "E: pattern not found: " + m.search
}

func (m *Model) maxXValue() int {
	n := len(m.lines[m.cy]) - 1
	if n < 0 {
		return 0
	}
	return n
}

func (m *Model) clamp() {
	if m.cy < 0 {
		m.cy = 0
	}
	if m.cy >= len(m.lines) {
		m.cy = len(m.lines) - 1
	}
	max := m.maxXValue()
	if m.cx > max {
		m.cx = max
	}
	if m.cx < 0 {
		m.cx = 0
	}
	m.ensureVisible()
}

func (m *Model) clampInsert() {
	if m.cy < 0 {
		m.cy = 0
	}
	if m.cy >= len(m.lines) {
		m.cy = len(m.lines) - 1
	}
	if m.cx > len(m.lines[m.cy]) {
		m.cx = len(m.lines[m.cy])
	}
	if m.cx < 0 {
		m.cx = 0
	}
	m.ensureVisible()
}

func (m *Model) contentHeight() int {
	h := m.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) ensureVisible() {
	h := m.contentHeight()
	if m.cy < m.offY {
		m.offY = m.cy
	}
	if m.cy >= m.offY+h {
		m.offY = m.cy - h + 1
	}
	if m.offY < 0 {
		m.offY = 0
	}
}

// View renders the editor.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "editor not sized"
	}
	m.clamp()
	contentH := m.contentHeight()
	digits := len(strconv.Itoa(len(m.lines)))
	if digits < 3 {
		digits = 3
	}
	gutter := digits + 1
	contentW := m.width - gutter
	if contentW < 8 {
		contentW = 8
	}
	lines := make([]string, 0, contentH+3)
	for row := 0; row < contentH; row++ {
		idx := m.offY + row
		if idx >= len(m.lines) {
			lines = append(lines, strings.Repeat(" ", m.width))
			continue
		}
		lines = append(lines, m.renderLine(idx, gutter, contentW))
	}
	status := m.renderStatus()
	msg := m.renderMessage()
	lines = append(lines, status, msg)
	// Exactly height rows keeps the host layout stable.
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderLine(idx, gutter, contentW int) string {
	p := m.palette
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted))
	num := fmt.Sprintf("%*d ", gutter-1, idx+1)
	numStr := muted.Render(num)
	display := displayRunes(m.lines[idx])
	// Keep horizontal scroll around the cursor.
	if idx == m.cy {
		curVis := visualCursor(m.lines[idx], m.cx)
		if curVis < m.offX {
			m.offX = curVis
		}
		if curVis >= m.offX+contentW {
			m.offX = curVis - contentW + 1
		}
	}
	if m.offX > len(display) {
		m.offX = len(display)
	}
	if m.offX < 0 {
		m.offX = 0
	}
	vis := display
	if m.offX > 0 && m.offX < len(vis) {
		vis = vis[m.offX:]
	} else if m.offX >= len(vis) {
		vis = nil
	}
	if len(vis) > contentW {
		vis = vis[:contentW]
	}
	if idx != m.cy {
		text := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text)).Render(string(vis))
		pad := contentW - runewidth.StringWidth(string(vis))
		if pad > 0 {
			text += strings.Repeat(" ", pad)
		}
		return numStr + text
	}
	// Current line: highlight background and show a block cursor.
	bg := lipgloss.NewStyle().Background(lipgloss.Color(p.Selection)).Foreground(lipgloss.Color(p.Text))
	cursorPos := visualCursor(m.lines[idx], m.cx) - m.offX
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > contentW-1 {
		cursorPos = contentW - 1
	}
	var before, under, after string
	if cursorPos < len(vis) {
		before = string(vis[:cursorPos])
		under = string(vis[cursorPos])
		after = string(vis[cursorPos+1:])
	} else {
		before = string(vis)
		under = " "
	}
	row := bg.Render(before) +
		lipgloss.NewStyle().Background(lipgloss.Color(p.Primary)).Foreground(lipgloss.Color("#001b0a")).Bold(true).Render(under) +
		bg.Render(after)
	// Fill to width for a clean highlight band.
	pad := contentW - lipgloss.Width(row)
	if pad > 0 {
		row += bg.Render(strings.Repeat(" ", pad))
	}
	return numStr + row
}

func (m *Model) renderStatus() string {
	p := m.palette
	mode := m.ModeLabel()
	modeColor := p.Primary
	if m.mode == Insert {
		modeColor = p.Success
	} else if m.mode == Command || m.mode == Search {
		modeColor = p.Warning
	}
	badge := lipgloss.NewStyle().Background(lipgloss.Color(modeColor)).Foreground(lipgloss.Color("#001008")).Bold(true).Render(" " + mode + " ")
	file := m.Filename()
	if m.dirty {
		file += " [+]"
	}
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.Selection)).Render(" " + file + " ")
	left := badge + fileStyle
	pos := fmt.Sprintf("Ln %d, Col %d  %d%% ", m.cy+1, m.cx+1, percent(m.cy, len(m.lines)))
	right := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)).Render(pos)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) renderMessage() string {
	p := m.palette
	if m.mode == Command {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)).Render(":"+m.cmd) + "▌"
	}
	if m.mode == Search {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)).Render("/"+m.cmd) + "▌"
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted))
	if strings.HasPrefix(m.message, "E:") {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Danger))
	}
	msg := style.Render(m.message)
	if m.pending != "" {
		msg += lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Render("  [" + m.pending + "]")
	}
	return msg
}

func percent(cy, n int) int {
	if n <= 1 {
		return 100
	}
	return int(float64(cy) / float64(n-1) * 100)
}

func displayRunes(line []rune) []rune {
	out := make([]rune, 0, len(line))
	for _, r := range line {
		switch {
		case r == '\t':
			out = append(out, ' ', ' ', ' ', ' ')
		case r < 32 || r == 127:
			out = append(out, '·')
		default:
			out = append(out, r)
		}
	}
	return out
}

func visualCursor(line []rune, cx int) int {
	if cx > len(line) {
		cx = len(line)
	}
	out := 0
	for i := 0; i < cx; i++ {
		if line[i] == '\t' {
			out += 4
		} else {
			out++
		}
	}
	return out
}

func containsControl(runes []rune) bool {
	for _, r := range runes {
		if r < 32 {
			return true
		}
	}
	return false
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
