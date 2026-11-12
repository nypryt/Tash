// Package theme defines Tash's terminal colour themes.
//
// Themes are plain JSON so they can be edited by hand or with the built-in
// vim-style editor. Only hex colours are accepted; anything invalid falls back
// to the Matrix theme.
package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Theme is a complete terminal palette.
type Theme struct {
	Name      string `json:"name"`
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Accent    string `json:"accent"`
	Success   string `json:"success"`
	Warning   string `json:"warning"`
	Danger    string `json:"danger"`
	Muted     string `json:"muted"`
	Text      string `json:"text"`
	Border    string `json:"border"`
	Selection string `json:"selection"`
}

// Builtins returns the bundled themes keyed by lowercase name.
func Builtins() map[string]Theme {
	list := []Theme{
		{
			Name: "matrix", Primary: "#00ff66", Secondary: "#00c853", Accent: "#b6ff00",
			Success: "#33ff99", Warning: "#ffd166", Danger: "#ff3b5c", Muted: "#3f6b50",
			Text: "#c8ffd6", Border: "#00a844", Selection: "#063b1f",
		},
		{
			Name: "cyberpink", Primary: "#ff2a6d", Secondary: "#05d9e8", Accent: "#d1f7ff",
			Success: "#00ffa3", Warning: "#f9c80e", Danger: "#ff3864", Muted: "#5c3a54",
			Text: "#f8f8f2", Border: "#7b2d8b", Selection: "#3a0d2a",
		},
		{
			Name: "amber-crt", Primary: "#ffb000", Secondary: "#ff8c00", Accent: "#ffe08a",
			Success: "#b6ff00", Warning: "#ffcc00", Danger: "#ff4d00", Muted: "#6b4a10",
			Text: "#ffe7bd", Border: "#a86b00", Selection: "#3a2600",
		},
		{
			Name: "dracula", Primary: "#bd93f9", Secondary: "#8be9fd", Accent: "#ff79c6",
			Success: "#50fa7b", Warning: "#f1fa8c", Danger: "#ff5555", Muted: "#6272a4",
			Text: "#f8f8f2", Border: "#44475a", Selection: "#343746",
		},
		{
			Name: "nord", Primary: "#88c0d0", Secondary: "#81a1c1", Accent: "#ebcb8b",
			Success: "#a3be8c", Warning: "#ebcb8b", Danger: "#bf616a", Muted: "#4c566a",
			Text: "#eceff4", Border: "#434c5e", Selection: "#2e3440",
		},
		{
			Name: "gruvbox", Primary: "#b8bb26", Secondary: "#83a598", Accent: "#fabd2f",
			Success: "#b8bb26", Warning: "#fabd2f", Danger: "#fb4934", Muted: "#665c54",
			Text: "#ebdbb2", Border: "#504945", Selection: "#3c3836",
		},
		{
			Name: "synthwave", Primary: "#f92aad", Secondary: "#36f9f6", Accent: "#fede5d",
			Success: "#72f1b8", Warning: "#fede5d", Danger: "#fe4450", Muted: "#5c4b8a",
			Text: "#f0e6ff", Border: "#6d3fa8", Selection: "#2b1055",
		},
		{
			Name: "ocean", Primary: "#56d4dd", Secondary: "#4f9cf9", Accent: "#f7d794",
			Success: "#6ee7b7", Warning: "#fbbf24", Danger: "#fb7185", Muted: "#36546e",
			Text: "#dff6ff", Border: "#2d6a8a", Selection: "#10354a",
		},
		{
			Name: "mono", Primary: "#e6e6e6", Secondary: "#a8a8a8", Accent: "#ffffff",
			Success: "#d4d4d4", Warning: "#bfbfbf", Danger: "#ffffff", Muted: "#6e6e6e",
			Text: "#f2f2f2", Border: "#555555", Selection: "#2d2d2d",
		},
	}
	m := make(map[string]Theme, len(list))
	for _, t := range list {
		m[t.Name] = t
	}
	return m
}

// Default returns the Matrix theme.
func Default() Theme { return Builtins()["matrix"] }

// Normalize cleans a theme loaded from disk.
func Normalize(t Theme) Theme {
	if strings.TrimSpace(t.Name) == "" {
		t.Name = "custom"
	}
	t.Name = strings.ToLower(strings.TrimSpace(t.Name))
	def := Default()
	for _, pair := range []struct {
		value    *string
		fallback string
	}{
		{&t.Primary, def.Primary}, {&t.Secondary, def.Secondary}, {&t.Accent, def.Accent},
		{&t.Success, def.Success}, {&t.Warning, def.Warning}, {&t.Danger, def.Danger},
		{&t.Muted, def.Muted}, {&t.Text, def.Text}, {&t.Border, def.Border}, {&t.Selection, def.Selection},
	} {
		if !IsHex(*pair.value) {
			*pair.value = pair.fallback
		}
	}
	return t
}

// IsHex reports whether s is a #rrggbb colour.
func IsHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// Registry holds built-in and custom themes.
type Registry struct {
	dir    string
	themes map[string]Theme
}

// NewRegistry loads bundled themes plus every *.json file in dir.
func NewRegistry(dir string) *Registry {
	r := &Registry{dir: dir, themes: Builtins()}
	r.Reload()
	return r
}

// Dir returns the custom theme directory.
func (r *Registry) Dir() string { return r.dir }

// Reload re-reads the custom theme directory.
func (r *Registry) Reload() {
	for name, t := range Builtins() {
		r.themes[name] = t
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		var t Theme
		if json.Unmarshal(b, &t) != nil {
			continue
		}
		t = Normalize(t)
		if _, isBuiltin := Builtins()[t.Name]; isBuiltin {
			t.Name += "-custom"
		}
		r.themes[t.Name] = t
	}
}

// Get resolves a theme by name, falling back to Matrix.
func (r *Registry) Get(name string) Theme {
	if t, ok := r.themes[strings.ToLower(name)]; ok {
		return t
	}
	return Default()
}

// Names returns all theme names, built-ins first.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.themes))
	for n := range r.themes {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		bi, bj := Builtins()[names[i]].Name != "", Builtins()[names[j]].Name != ""
		if bi != bj {
			return bi
		}
		return names[i] < names[j]
	})
	return names
}

// IsBuiltin reports whether a theme is shipped with Tash.
func IsBuiltin(name string) bool {
	_, ok := Builtins()[strings.ToLower(name)]
	return ok
}

// Save writes a custom theme to the registry directory.
func (r *Registry) Save(t Theme) (string, error) {
	t = Normalize(t)
	if t.Name == "" {
		return "", fmt.Errorf("theme name is required")
	}
	if err := validateName(t.Name); err != nil {
		return "", err
	}
	if IsBuiltin(t.Name) {
		return "", fmt.Errorf("%q is a built-in theme; choose another name", t.Name)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	path := filepath.Join(r.dir, t.Name+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	r.themes[t.Name] = t
	return path, nil
}

func validateName(name string) error {
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid theme name %q", name)
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("theme name may only contain a-z, 0-9, dash, and underscore")
	}
	return nil
}

// Template returns a custom-theme template based on an existing theme.
func Template(base Theme, name string) Theme {
	base.Name = strings.ToLower(strings.TrimSpace(name))
	return base
}
