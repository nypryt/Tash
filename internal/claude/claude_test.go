package claude

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tash/tash/internal/config"
)

func TestReadProjectsFiltersAndSorts(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
		"projects": {
			"` + filepath.ToSlash(dir) + `": {"history": []},
			"relative/path": {"history": []}
		}
	}`)
	projects := readProjects(data)
	if len(projects) != 1 {
		t.Fatalf("projects = %#v", projects)
	}
	if projects[0].Path != dir {
		t.Fatalf("path = %q, want %q", projects[0].Path, dir)
	}
}

func TestWorkDirPrefersConfig(t *testing.T) {
	want := filepath.Join(t.TempDir(), "project")
	if got := WorkDir(config.ClaudeConfig{WorkDir: want}); got != want {
		t.Fatalf("WorkDir = %q, want %q", got, want)
	}
	if got := WorkDir(config.ClaudeConfig{}); got == "" {
		t.Fatal("WorkDir fell back to an empty string")
	}
}

func TestDetectDisabled(t *testing.T) {
	st := Detect(context.Background(), config.ClaudeConfig{Enabled: false})
	if st.Enabled || st.Available {
		t.Fatalf("disabled status = %+v", st)
	}
	if st.Error == "" {
		t.Fatal("disabled status has no explanation")
	}
}

func TestIsBatch(t *testing.T) {
	if !isBatch("claude.CMD") || !isBatch("claude.bat") {
		t.Fatal("batch shims were not detected")
	}
	if isBatch("claude.exe") || isBatch("claude") {
		t.Fatal("non-batch executables were misdetected")
	}
	if runtime.GOOS == "windows" {
		cmd := execCommand("claude.cmd", "--version")
		if cmd.Path == "" {
			t.Fatal("execCommand returned an empty path")
		}
	}
}
