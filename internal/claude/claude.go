// Package claude integrates the Claude Code CLI into Tash: detection, recent
// projects, one-shot prompts, and interactive launches.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
)

// Project is a recent Claude Code project directory.
type Project struct {
	Path       string    `json:"path"`
	LastActive time.Time `json:"last_active"`
}

// Status is a snapshot of the local Claude Code installation.
type Status struct {
	Enabled     bool      `json:"enabled"`
	Available   bool      `json:"available"`
	Command     string    `json:"command"`
	Path        string    `json:"path,omitempty"`
	Version     string    `json:"version,omitempty"`
	ConfigDir   string    `json:"config_dir,omitempty"`
	ConfigFile  string    `json:"config_file,omitempty"`
	ConfigFound bool      `json:"config_found"`
	AuthMode    string    `json:"auth_mode"`
	Projects    []Project `json:"projects"`
	Error       string    `json:"error,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// Detect inspects the Claude Code CLI and its local configuration.
func Detect(ctx context.Context, cfg config.ClaudeConfig) Status {
	s := Status{
		Enabled:   cfg.Enabled,
		Command:   firstNonEmpty(strings.TrimSpace(cfg.Command), "claude"),
		AuthMode:  "not detected",
		FetchedAt: time.Now(),
	}
	if !cfg.Enabled {
		s.Error = "Claude integration is disabled in Settings"
		return s
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		s.ConfigDir = filepath.Join(home, ".claude")
		if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
			s.ConfigDir = v
		}
		s.ConfigFile = filepath.Join(home, ".claude.json")
	}

	bin, err := resolveCommand(s.Command)
	if err != nil {
		s.Error = err.Error()
	} else {
		s.Available = true
		s.Path = bin
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, verr := execContext(runCtx, bin, "--version").Output()
		cancel()
		if verr == nil {
			s.Version = firstLine(string(out))
		}
	}

	if cfg.APIKey != "" || strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		s.AuthMode = "API key"
	}

	if s.ConfigFile != "" {
		if data, err := os.ReadFile(s.ConfigFile); err == nil {
			s.ConfigFound = true
			s.Projects = readProjects(data)
			if s.AuthMode == "not detected" && bytes.Contains(data, []byte(`"oauthAccount"`)) {
				s.AuthMode = "Claude account"
			}
		}
	}
	if s.AuthMode == "not detected" {
		if _, err := os.Stat(s.ConfigDir); err == nil {
			s.AuthMode = "local config"
		}
	}
	if s.Error == "" && s.AuthMode == "not detected" {
		s.Error = "Claude Code is installed but no login or API key was detected"
	}
	return s
}

func readProjects(data []byte) []Project {
	var raw struct {
		Projects map[string]struct {
			History []struct {
				Display string `json:"display"`
			} `json:"history"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	out := make([]Project, 0, len(raw.Projects))
	for path := range raw.Projects {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(filepath.FromSlash(path))
		if !filepath.IsAbs(path) {
			continue
		}
		p := Project{Path: path}
		if info, err := os.Stat(path); err == nil {
			p.LastActive = info.ModTime()
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActive.Equal(out[j].LastActive) {
			return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
		}
		return out[i].LastActive.After(out[j].LastActive)
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// Prompt runs `claude -p` non-interactively and returns its text output.
func Prompt(ctx context.Context, cfg config.ClaudeConfig, prompt, dir string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	bin, err := resolveCommand(firstNonEmpty(strings.TrimSpace(cfg.Command), "claude"))
	if err != nil {
		return "", err
	}
	args := []string{"-p", prompt, "--output-format", "text"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if mode := strings.TrimSpace(cfg.PermissionMode); mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := execContext(runCtx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = withAPIKey(cfg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("claude: %s", msg)
	}
	text := string(out)
	if len(text) > 256<<10 {
		text = text[:256<<10] + "\n… output truncated"
	}
	return strings.TrimRight(text, "\n"), nil
}

// Command returns an interactive `claude` command for tea.ExecProcess.
func Command(cfg config.ClaudeConfig, dir string) (*exec.Cmd, error) {
	bin, err := resolveCommand(firstNonEmpty(strings.TrimSpace(cfg.Command), "claude"))
	if err != nil {
		return nil, err
	}
	args := []string{}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	cmd := execCommand(bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = withAPIKey(cfg)
	return cmd, nil
}

// WorkDir resolves the configured project directory, defaulting to the current
// working directory.
func WorkDir(cfg config.ClaudeConfig) string {
	if dir := strings.TrimSpace(cfg.WorkDir); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// execContext builds a command, wrapping .cmd/.bat shims on Windows because
// CreateProcess cannot launch batch files directly.
func execContext(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" && isBatch(bin) {
		all := append([]string{"/c", bin}, args...)
		return exec.CommandContext(ctx, "cmd.exe", all...)
	}
	return exec.CommandContext(ctx, bin, args...)
}

func execCommand(bin string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" && isBatch(bin) {
		all := append([]string{"/c", bin}, args...)
		return exec.Command("cmd.exe", all...)
	}
	return exec.Command(bin, args...)
}

func isBatch(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}

func resolveCommand(name string) (string, error) {
	if name == "" {
		name = "claude"
	}
	if strings.ContainsAny(name, `/\`) {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("Claude command not found at %s", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("Claude Code CLI not found in PATH (install it or set the command in Settings)")
	}
	return path, nil
}

func withAPIKey(cfg config.ClaudeConfig) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "ANTHROPIC_API_KEY=") {
			continue
		}
		env = append(env, item)
	}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		env = append(env, "ANTHROPIC_API_KEY="+key)
	}
	return env
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
