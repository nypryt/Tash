// Package docker provides Docker CLI and Engine-API integrations.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
)

// Container is a compact container row.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
	Running bool   `json:"running"`
}

// Snapshot is a Docker engine overview.
type Snapshot struct {
	Mode       string      `json:"mode"`
	Available  bool        `json:"available"`
	Engine     string      `json:"engine,omitempty"`
	ServerVer  string      `json:"server_version,omitempty"`
	ClientVer  string      `json:"client_version,omitempty"`
	OS         string      `json:"os,omitempty"`
	Arch       string      `json:"arch,omitempty"`
	CPUs       int         `json:"cpus,omitempty"`
	Memory     int64       `json:"memory,omitempty"`
	Containers []Container `json:"containers"`
	Running    int         `json:"running"`
	Error      string      `json:"error,omitempty"`
	FetchedAt  time.Time   `json:"fetched_at"`
}

// Client is the common Docker interface.
type Client interface {
	Fetch(ctx context.Context) Snapshot
	Action(ctx context.Context, id, action string) error
	Logs(ctx context.Context, id string, tail int) (string, error)
	LogsFollow(ctx context.Context, id string, tail int) (<-chan string, error)
	Exec(ctx context.Context, id string, cmd []string) (string, error)
	Mode() string
}

// New returns a Docker client for cfg.Mode.
func New(cfg config.DockerConfig) (Client, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "", "off", "none", "skip":
		return nil, nil
	case "cli":
		return &cliClient{}, nil
	case "socket", "api", "container":
		return newAPIClient(cfg.Host)
	default:
		return nil, fmt.Errorf("unknown Docker mode %q", cfg.Mode)
	}
}

// ModeInfo explains onboarding choices.
type ModeInfo struct {
	Name        string
	BestFor     string
	Tradeoffs   string
	Recommended bool
}

// Modes returns the setup copy for Docker mode selection.
func Modes() []ModeInfo {
	return []ModeInfo{
		{
			Name: "CLI mode", Recommended: true,
			BestFor:   "laptops and desktops with Docker Desktop or Docker Engine already installed",
			Tradeoffs: "uses your docker command and current context, so it also follows remote contexts you configure; full features, easiest setup",
		},
		{
			Name:      "Socket / API mode",
			BestFor:   "headless servers, Tash running inside a container, or machines without the docker CLI",
			Tradeoffs: "connects directly to the engine socket or TCP API; local socket access is effectively root-equivalent, and remote TCP should be protected by a firewall/VPN",
		},
		{
			Name:      "Skip Docker",
			BestFor:   "you don't want Docker data in the dashboard",
			Tradeoffs: "can be enabled later with `tash docker use cli|socket`",
		},
	}
}

func jsonUnmarshal(b []byte, v any) error {
	var err error
	json.Unmarshal(b, v)
	return err
}

type cliClient struct{}

func (c *cliClient) Mode() string { return "cli" }

func dockerBin() (string, error) {
	if p, err := exec.LookPath("docker"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("docker CLI not found in PATH")
}

func (c *cliClient) Fetch(ctx context.Context) Snapshot {
	s := Snapshot{Mode: "cli", FetchedAt: time.Now()}
	bin, err := dockerBin()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	var ver struct {
		Client struct {
			Version string `json:"Version"`
		} `json:"Client"`
		Server struct {
			Version string `json:"Version"`
			Os      string `json:"Os"`
			Arch    string `json:"Arch"`
			Cpus    int    `json:"Cpus"`
		} `json:"Server"`
	}
	cmd := exec.CommandContext(ctx, bin, "version", "--format", "{{json .}}")
	if out, err := cmd.Output(); err == nil {
		if jsonUnmarshal(out, &ver) == nil {
			s.ClientVer = ver.Client.Version
			s.ServerVer = ver.Server.Version
			s.OS = ver.Server.Os
			s.Arch = ver.Server.Arch
			s.CPUs = ver.Server.Cpus
			s.Available = true
		}
	} else {
		s.Error = cliError(err)
	}
	cmd = exec.CommandContext(ctx, bin, "info", "--format", "{{json .}}")
	if out, err := cmd.Output(); err == nil {
		var info struct {
			Name            string `json:"Name"`
			ServerVersion   string `json:"ServerVersion"`
			NCPU            int    `json:"NCPU"`
			MemTotal        int64  `json:"MemTotal"`
			OperatingSystem string `json:"OperatingSystem"`
		}
		if jsonUnmarshal(out, &info) == nil {
			s.Available = true
			s.Engine = info.Name
			s.ServerVer = firstNonEmpty(info.ServerVersion, s.ServerVer)
			s.CPUs = info.NCPU
			s.Memory = info.MemTotal
			s.OS = firstNonEmpty(s.OS, info.OperatingSystem)
		}
	}
	if s.Available {
		cmd = exec.CommandContext(ctx, bin, "ps", "-a", "--no-trunc", "--format", "{{json .}}")
		out, err := cmd.Output()
		if err != nil {
			s.Error = cliError(err)
			return s
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row struct {
				ID        string `json:"ID"`
				Names     string `json:"Names"`
				Image     string `json:"Image"`
				State     string `json:"State"`
				Status    string `json:"Status"`
				Ports     string `json:"Ports"`
				CreatedAt string `json:"CreatedAt"`
			}
			if jsonUnmarshal([]byte(line), &row) != nil {
				continue
			}
			name := row.Names
			if i := strings.Index(name, ","); i >= 0 {
				name = name[:i]
			}
			name = strings.TrimPrefix(name, "/")
			s.Containers = append(s.Containers, Container{
				ID: row.ID, Name: name, Image: row.Image, State: row.State,
				Status: row.Status, Ports: row.Ports, Created: row.CreatedAt,
				Running: strings.EqualFold(row.State, "running"),
			})
		}
	}
	for _, ct := range s.Containers {
		if ct.Running {
			s.Running++
		}
	}
	if s.ServerVer == "" && s.Error == "" {
		s.Error = "Docker daemon is not reachable"
	}
	return s
}

func (c *cliClient) Action(ctx context.Context, id, action string) error {
	bin, err := dockerBin()
	if err != nil {
		return err
	}
	var args []string
	switch action {
	case "start", "stop", "restart", "pause", "unpause":
		args = []string{action, id}
	case "remove", "rm", "delete":
		args = []string{"rm", "-f", id}
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %s", action, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *cliClient) Logs(ctx context.Context, id string, tail int) (string, error) {
	bin, err := dockerBin()
	if err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = 120
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "logs", "--tail", strconv.Itoa(tail), id).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker logs: %w", err)
	}
	return string(out), nil
}

func (c *cliClient) LogsFollow(ctx context.Context, id string, tail int) (<-chan string, error) {
	bin, err := dockerBin()
	if err != nil {
		return nil, err
	}
	if tail <= 0 {
		tail = 120
	}
	ch := make(chan string, 32)
	cmd := exec.CommandContext(ctx, bin, "logs", "--tail", strconv.Itoa(tail), "-f", id)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		for scanner.Scan() {
			select {
			case ch <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		close(ch)
		cmd.Wait()
	}()
	return ch, nil
}

func (c *cliClient) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	bin, err := dockerBin()
	if err != nil {
		return "", err
	}
	args := append([]string{"exec", "-i", id}, cmd...)
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker exec: %w", err)
	}
	return string(out), nil
}

func cliError(err error) string {
	var ee *exec.ExitError
	if ok := asExit(err, &ee); ok && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

func asExit(err error, target **exec.ExitError) bool {
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
