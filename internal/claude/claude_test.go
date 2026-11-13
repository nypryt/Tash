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
	if got := WorkDir(config.AIConfig{WorkDir: want}); got != want {
		t.Fatalf("WorkDir = %q, want %q", got, want)
	}
	if got := WorkDir(config.AIConfig{}); got == "" {
		t.Fatal("WorkDir fell back to an empty string")
	}
}

func TestDetectDisabled(t *testing.T) {
	st := Detect(context.Background(), config.AIConfig{Enabled: false})
	if st.Enabled || st.Available {
		t.Fatalf("disabled status = %+v", st)
	}
	if st.Error == "" {
		t.Fatal("disabled status has no explanation")
	}
}

func TestParseChatOutput(t *testing.T) {
	res, err := parseChatOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"hello\n","session_id":"abc"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "hello" || res.SessionID != "abc" {
		t.Fatalf("res = %+v", res)
	}

	if _, err := parseChatOutput([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in"}`)); err == nil {
		t.Fatal("error output was treated as success")
	}

	res, err = parseChatOutput([]byte("plain text"))
	if err != nil || res.Text != "plain text" {
		t.Fatalf("fallback = %+v, %v", res, err)
	}

	if _, err := parseChatOutput(nil); err == nil {
		t.Fatal("empty output did not error")
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
