package theme

import (
	"strings"
	"testing"
)

func TestNormalizeFallsBack(t *testing.T) {
	got := Normalize(Theme{Name: "  Custom  ", Primary: "not-a-colour", Accent: "#00FF00"})
	if got.Name != "custom" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Primary != Default().Primary {
		t.Fatalf("invalid primary was not replaced: %q", got.Primary)
	}
	if got.Accent != "#00FF00" {
		t.Fatalf("valid accent changed: %q", got.Accent)
	}
}

func TestRegistrySaveLoadAndValidation(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	base := r.Get("nord")
	custom := Template(base, "My-Theme")
	custom.Primary = "#123456"
	path, err := r.Save(custom)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasSuffix(path, "my-theme.json") {
		t.Fatalf("unexpected path: %s", path)
	}
	r2 := NewRegistry(dir)
	loaded := r2.Get("my-theme")
	if loaded.Primary != "#123456" {
		t.Fatalf("custom theme not reloaded: %+v", loaded)
	}
	if !IsBuiltin("matrix") {
		t.Fatal("matrix should be built in")
	}
	if IsBuiltin("my-theme") {
		t.Fatal("custom theme reported as built-in")
	}

	if _, err := r.Save(Template(base, "matrix")); err == nil {
		t.Fatal("expected built-in name to be rejected")
	}
	if _, err := r.Save(Template(base, "../escape")); err == nil {
		t.Fatal("expected path traversal name to be rejected")
	}
	if _, err := r.Save(Template(base, "space name")); err == nil {
		t.Fatal("expected space in theme name to be rejected")
	}
}

func TestIsHex(t *testing.T) {
	for _, ok := range []string{"#000000", "#abcdef", "#ABCDEF"} {
		if !IsHex(ok) {
			t.Errorf("IsHex(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "fff", "#fff", "#gggggg", " #ffffff", "#ffffff "} {
		if IsHex(bad) {
			t.Errorf("IsHex(%q) = true", bad)
		}
	}
}
