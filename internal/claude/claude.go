// Package claude integrates AI providers into Tash: detection, model fetching,
// one-shot prompts, in-app chat sessions, and interactive launches.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
)

type Project struct {
	Path       string    `json:"path"`
	LastActive time.Time `json:"last_active"`
}

type Status struct {
	Enabled      bool      `json:"enabled"`
	Available    bool      `json:"available"`
	Provider     string    `json:"provider"`
	Command      string    `json:"command,omitempty"`
	Path         string    `json:"path,omitempty"`
	Version      string    `json:"version,omitempty"`
	ConfigDir    string    `json:"config_dir,omitempty"`
	ConfigFile   string    `json:"config_file,omitempty"`
	ConfigFound  bool      `json:"config_found"`
	AuthMode     string    `json:"auth_mode"`
	Projects     []Project `json:"projects"`
	Models       []string  `json:"models,omitempty"`
	Error        string    `json:"error,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
}

func Detect(ctx context.Context, cfg config.AIConfig) Status {
	s := Status{
		Enabled:  cfg.Enabled,
		Provider: cfg.Provider,
		AuthMode: "not detected",
		FetchedAt: time.Now(),
	}
	if !cfg.Enabled {
		s.Error = "AI integration is disabled in Settings"
		return s
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "claude"
	}
	s.Provider = provider

	switch provider {
	case "claude":
		return detectClaude(ctx, cfg, s)
	case "openai", "anthropic", "custom":
		return detectAPIProvider(ctx, cfg, s)
	default:
		s.Error = "unknown provider: " + provider
		return s
	}
}

func detectClaude(ctx context.Context, cfg config.AIConfig, s Status) Status {
	s.Command = firstNonEmpty(strings.TrimSpace(cfg.Command), "claude")

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

func detectAPIProvider(ctx context.Context, cfg config.AIConfig, s Status) Status {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if apiKey == "" && cfg.Provider == "anthropic" {
		apiKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}

	if apiKey == "" {
		s.Error = "API key not configured"
		return s
	}

	s.Available = true
	s.AuthMode = "API key"
	s.Models = fetchModels(ctx, baseURL, apiKey, cfg.Provider)

	if len(s.Models) > 0 {
		s.Version = "models: " + strings.Join(s.Models[:min(3, len(s.Models))], ", ")
		if len(s.Models) > 3 {
			s.Version += fmt.Sprintf(" (+%d more)", len(s.Models)-3)
		}
	} else {
		s.Version = "API reachable"
	}
	return s
}

func fetchModels(ctx context.Context, baseURL, apiKey, provider string) []string {
	url := strings.TrimRight(baseURL, "/")
	if url == "" {
		switch provider {
		case "openai":
			url = "https://api.openai.com/v1"
		case "anthropic":
			url = "https://api.anthropic.com/v1"
		default:
			return nil
		}
	}

	var modelsURL string
	var req *http.Request
	var err error

	switch provider {
	case "openai":
		modelsURL = url + "/models"
		req, err = http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
	case "anthropic":
		modelsURL = url + "/models"
		req, err = http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		modelsURL = url + "/models"
		req, err = http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
		if err != nil {
			return nil
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	var models []string
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	for _, m := range result.Models {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}

	sort.Strings(models)
	return models
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

type ChatResult struct {
	Text      string `json:"text"`
	SessionID string `json:"session_id,omitempty"`
}

func Prompt(ctx context.Context, cfg config.AIConfig, prompt, dir string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "claude"
	}

	switch provider {
	case "claude":
		return promptClaude(ctx, cfg, prompt, dir)
	case "openai", "anthropic", "custom":
		return promptAPI(ctx, cfg, prompt, dir)
	default:
		return "", fmt.Errorf("unknown provider: %s", provider)
	}
}

func promptClaude(ctx context.Context, cfg config.AIConfig, prompt, dir string) (string, error) {
	bin, err := resolveCommand(firstNonEmpty(strings.TrimSpace(cfg.Command), "claude"))
	if err != nil {
		return "", err
	}
	args := []string{"-p", "--output-format", "text"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if mode := strings.TrimSpace(cfg.PermissionMode); mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := execContext(runCtx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
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
		text = text[:256<<10] + "\n... output truncated"
	}
	return strings.TrimRight(text, "\n"), nil
}

func promptAPI(ctx context.Context, cfg config.AIConfig, prompt, dir string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if apiKey == "" && cfg.Provider == "anthropic" {
		apiKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if apiKey == "" {
		return "", fmt.Errorf("API key not configured")
	}
	if baseURL == "" {
		switch cfg.Provider {
		case "openai":
			baseURL = "https://api.openai.com/v1"
		case "anthropic":
			baseURL = "https://api.anthropic.com/v1"
		default:
			return "", fmt.Errorf("base URL not configured for custom provider")
		}
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		switch cfg.Provider {
		case "openai":
			model = "gpt-4o-mini"
		case "anthropic":
			model = "claude-3-5-sonnet-20241022"
		default:
			return "", fmt.Errorf("model not configured")
		}
	}

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	body := map[string]any{
		"model":    model,
		"messages": messages,
	}

	jsonBody, _ := json.Marshal(body)

	var req *http.Request
	var err error
	switch cfg.Provider {
	case "openai", "custom":
		req, err = http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(jsonBody))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
	case "anthropic":
		anthropicBody := map[string]any{
			"model":     model,
			"max_tokens": 4096,
			"messages":  messages,
		}
		jsonBody, _ = json.Marshal(anthropicBody)
		req, err = http.NewRequestWithContext(ctx, "POST", baseURL+"/messages", bytes.NewReader(jsonBody))
		if err != nil {
			return "", err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error: %s", string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}

	var text string
	if len(result.Choices) > 0 {
		text = result.Choices[0].Message.Content
	} else if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	return strings.TrimRight(text, "\n"), nil
}

func Chat(ctx context.Context, cfg config.AIConfig, prompt, dir, sessionID string) (ChatResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ChatResult{}, fmt.Errorf("prompt is empty")
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "claude"
	}

	switch provider {
	case "claude":
		return chatClaude(ctx, cfg, prompt, dir, sessionID)
	case "openai", "anthropic", "custom":
		return chatAPI(ctx, cfg, prompt, dir, sessionID)
	default:
		return ChatResult{}, fmt.Errorf("unknown provider: %s", provider)
	}
}

func chatClaude(ctx context.Context, cfg config.AIConfig, prompt, dir, sessionID string) (ChatResult, error) {
	bin, err := resolveCommand(firstNonEmpty(strings.TrimSpace(cfg.Command), "claude"))
	if err != nil {
		return ChatResult{}, err
	}
	args := []string{"-p", "--output-format", "json"}
	if session := strings.TrimSpace(sessionID); session != "" {
		args = append(args, "--resume", session)
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if mode := strings.TrimSpace(cfg.PermissionMode); mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := execContext(runCtx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
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
		if runCtx.Err() != nil {
			msg = "timed out waiting for Claude"
		}
		return ChatResult{}, fmt.Errorf("claude: %s", msg)
	}
	return parseChatOutput(out)
}

func chatAPI(ctx context.Context, cfg config.AIConfig, prompt, dir, sessionID string) (ChatResult, error) {
	return ChatResult{Text: "API chat not yet implemented for session continuity", SessionID: sessionID}, fmt.Errorf("API chat sessions not yet supported; use one-shot prompt instead")
}

func parseChatOutput(out []byte) (ChatResult, error) {
	var raw struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		Result    string `json:"result"`
		IsError   bool   `json:"is_error"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || raw.Type != "result" {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return ChatResult{}, fmt.Errorf("claude returned no output")
		}
		return ChatResult{Text: text}, nil
	}
	if raw.IsError || strings.EqualFold(raw.Subtype, "error") {
		msg := strings.TrimSpace(raw.Result)
		if msg == "" {
			msg = "Claude reported an error"
		}
		return ChatResult{SessionID: raw.SessionID}, fmt.Errorf("%s", msg)
	}
	text := strings.TrimRight(raw.Result, "\n")
	if len(text) > 256<<10 {
		text = text[:256<<10] + "\n... output truncated"
	}
	if strings.TrimSpace(text) == "" {
		text = "(Claude returned an empty response)"
	}
	return ChatResult{Text: text, SessionID: raw.SessionID}, nil
}

func Command(cfg config.AIConfig, dir string) (*exec.Cmd, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "claude"
	}

	if provider != "claude" {
		return nil, fmt.Errorf("interactive command only supported for claude provider")
	}

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

func WorkDir(cfg config.AIConfig) string {
	if dir := strings.TrimSpace(cfg.WorkDir); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

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
		return "", fmt.Errorf("command not found at %s", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("command not found in PATH (install it or set the command in Settings): %s", name)
	}
	return path, nil
}

func withAPIKey(cfg config.AIConfig) []string {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	key := strings.TrimSpace(cfg.APIKey)

	var envKey string
	switch provider {
	case "claude":
		envKey = "ANTHROPIC_API_KEY"
	case "openai", "custom":
		envKey = "OPENAI_API_KEY"
	case "anthropic":
		envKey = "ANTHROPIC_API_KEY"
	default:
		envKey = "ANTHROPIC_API_KEY"
	}

	if key == "" {
		key = strings.TrimSpace(os.Getenv(envKey))
	}
	if key == "" {
		return os.Environ()
	}

	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, envKey+"=") {
			continue
		}
		env = append(env, item)
	}
	return append(env, envKey+"="+key)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}