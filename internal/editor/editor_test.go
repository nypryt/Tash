package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tash/tash/internal/theme"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m *Model, msgs ...tea.KeyMsg) {
	for _, msg := range msgs {
		m.HandleKey(msg)
	}
}

func TestInsertSaveAndQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(PaletteFromTheme(theme.Default()))
	m.SetSize(80, 20)
	if err := m.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	press(m,
		keyRunes("i"),
		keyRunes("hello "),
		tea.KeyMsg{Type: tea.KeyEsc},
		keyRunes(":"),
		keyRunes("wq"),
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if !m.CloseRequested() {
		t.Fatal(":wq did not request close")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "hello first\nsecond\n" {
		t.Fatalf("saved content = %q", got)
	}
}

func TestUndoRestoresOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "undo.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(PaletteFromTheme(theme.Default()))
	m.SetSize(80, 20)
	if err := m.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	press(m,
		keyRunes("i"),
		keyRunes("broken "),
		tea.KeyMsg{Type: tea.KeyEsc},
		keyRunes("u"),
		keyRunes(":"),
		keyRunes("wq"),
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "original\n" {
		t.Fatalf("undo saved content = %q", got)
	}
}

func TestSearchFindsNextMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\nalpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(PaletteFromTheme(theme.Default()))
	m.SetSize(80, 20)
	if err := m.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	press(m, keyRunes("/"), keyRunes("alpha"), tea.KeyMsg{Type: tea.KeyEnter})
	view := m.View()
	if !strings.Contains(view, "alpha") {
		t.Fatalf("search view missing match:\n%s", view)
	}
}
