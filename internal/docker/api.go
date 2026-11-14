package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tash/tash/internal/version"
)

type apiClient struct {
	base string
	http *http.Client
}

func newAPIClient(host string) (Client, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("Docker host is empty")
	}
	var base string
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	lower := strings.ToLower(host)
	switch {
	case strings.HasPrefix(lower, "unix://"):
		path := strings.TrimPrefix(host, "unix://")
		base = "http://docker"
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 8 * time.Second}
			return d.DialContext(ctx, "unix", path)
		}
	case strings.HasPrefix(lower, "npipe://"):
		path := strings.TrimPrefix(host, "npipe://")
		base = "http://docker"
		dial = npipeDialer(path)
		if dial == nil {
			return nil, fmt.Errorf("named-pipe Docker connections are only supported on Windows; use tcp:// or unix://")
		}
	case strings.HasPrefix(lower, "tcp://"):
		u, err := url.Parse(host)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid Docker host %q", host)
		}
		base = "http://" + u.Host
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		base = strings.TrimRight(host, "/")
	default:
		// Treat a bare path as a Unix socket, a bare host:port as TCP.
		if strings.HasPrefix(host, "/") {
			base = "http://docker"
			dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 8 * time.Second}
				return d.DialContext(ctx, "unix", host)
			}
		} else {
			base = "http://" + host
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if dial != nil {
		tr.DialContext = dial
	}
	return &apiClient{base: base, http: &http.Client{Transport: tr, Timeout: 20 * time.Second}}, nil
}

func (c *apiClient) Mode() string { return "socket" }

func (c *apiClient) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Docker API unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return fmt.Errorf("Docker API: %s (%s)", msg, resp.Status)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("Docker API returned invalid JSON: %w", err)
		}
	}
	return nil
}

func (c *apiClient) Fetch(ctx context.Context) Snapshot {
	s := Snapshot{Mode: "socket", FetchedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var ver struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
		OS         string `json:"Os"`
		Arch       string `json:"Arch"`
	}
	if err := c.do(ctx, http.MethodGet, "/version", &ver); err != nil {
		s.Error = err.Error()
		return s
	}
	s.Available = true
	s.ServerVer = ver.Version
	s.OS = ver.OS
	s.Arch = ver.Arch
	var info struct {
		Name            string `json:"Name"`
		ServerVersion   string `json:"ServerVersion"`
		NCPU            int    `json:"NCPU"`
		MemTotal        int64  `json:"MemTotal"`
		OperatingSystem string `json:"OperatingSystem"`
	}
	if err := c.do(ctx, http.MethodGet, "/info", &info); err == nil {
		s.Engine = info.Name
		s.ServerVer = firstNonEmpty(info.ServerVersion, s.ServerVer)
		s.CPUs = info.NCPU
		s.Memory = info.MemTotal
		s.OS = firstNonEmpty(s.OS, info.OperatingSystem)
	}
	var rows []struct {
		ID     string   `json:"Id"`
		Names  []string `json:"Names"`
		Image  string   `json:"Image"`
		State  string   `json:"State"`
		Status string   `json:"Status"`
		Ports  []struct {
			IP          string `json:"IP"`
			PrivatePort int    `json:"PrivatePort"`
			PublicPort  int    `json:"PublicPort"`
			Type        string `json:"Type"`
		} `json:"Ports"`
		Created int64 `json:"Created"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json?all=1", &rows); err != nil {
		s.Error = err.Error()
		return s
	}
	for _, row := range rows {
		name := row.ID
		if len(row.Names) > 0 {
			name = strings.TrimPrefix(row.Names[0], "/")
		}
		var ports []string
		for _, p := range row.Ports {
			if p.PublicPort > 0 {
				ports = append(ports, fmt.Sprintf("%s:%d→%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type))
			} else {
				ports = append(ports, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
			}
		}
		s.Containers = append(s.Containers, Container{
			ID: row.ID, Name: name, Image: row.Image, State: row.State,
			Status: row.Status, Ports: strings.Join(ports, ", "),
			Created: time.Unix(row.Created, 0).Format("2006-01-02 15:04"),
			Running: strings.EqualFold(row.State, "running"),
		})
	}
	for _, ct := range s.Containers {
		if ct.Running {
			s.Running++
		}
	}
	return s
}

func (c *apiClient) Action(ctx context.Context, id, action string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	switch action {
	case "start", "stop", "restart", "pause", "unpause":
		return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/"+action, nil)
	case "remove", "rm", "delete":
		return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1", nil)
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
}

func (c *apiClient) Logs(ctx context.Context, id string, tail int) (string, error) {
	if tail <= 0 {
		tail = 120
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := "/containers/" + url.PathEscape(id) + "/logs?stdout=1&stderr=1&timestamps=1&tail=" + strconv.Itoa(tail)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if resp.StatusCode != http.StatusOK {
		return string(data), fmt.Errorf("docker logs: %s", resp.Status)
	}
	return demuxLogs(data), nil
}

func (c *apiClient) LogsFollow(ctx context.Context, id string, tail int) (<-chan string, error) {
	if tail <= 0 {
		tail = 120
	}
	ch := make(chan string, 32)
	path := "/containers/" + url.PathEscape(id) + "/logs?stdout=1&stderr=1&timestamps=1&follow=1&tail=" + strconv.Itoa(tail)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("docker logs: %s", string(data))
	}
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		buf := make([]byte, 8192)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, err := resp.Body.Read(buf)
				if err != nil {
					if err != io.EOF {
						select {
						case ch <- "error: " + err.Error():
						case <-ctx.Done():
						}
					}
					return
				}
				if n > 0 {
					lines := demuxLogs(buf[:n])
					if lines != "" {
						for _, line := range strings.Split(lines, "\n") {
							if line != "" {
								select {
								case ch <- line:
								case <-ctx.Done():
									return
								}
							}
						}
					}
				}
			}
		}
	}()
	return ch, nil
}

func (c *apiClient) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	execConfig := map[string]interface{}{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
		"Tty":          false,
	}
	body, _ := json.Marshal(execConfig)
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/containers/"+url.PathEscape(id)+"/exec", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.http.Do(createReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var execResp struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return "", err
	}
	if execResp.ID == "" {
		return "", fmt.Errorf("failed to create exec")
	}
	startBody, _ := json.Marshal(map[string]bool{"Detach": false, "Tty": false})
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/exec/"+execResp.ID+"/start", bytes.NewReader(startBody))
	if err != nil {
		return "", err
	}
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("User-Agent", version.UserAgent())
	startResp, err := c.http.Do(startReq)
	if err != nil {
		return "", err
	}
	defer startResp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(startResp.Body, 256<<10))
	return demuxLogs(data), nil
}

// demuxLogs handles Docker's 8-byte multiplexed stream headers. TTY-enabled
// containers return plain text, which is also handled.
func demuxLogs(data []byte) string {
	var out bytes.Buffer
	for len(data) >= 8 {
		if data[0] > 2 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
			out.Write(data)
			break
		}
		size := int(binary.BigEndian.Uint32(data[4:8]))
		if size < 0 || 8+size > len(data) {
			out.Write(data)
			break
		}
		out.Write(data[8 : 8+size])
		data = data[8+size:]
	}
	if len(data) > 0 && out.Len() == 0 {
		out.Write(data)
	}
	return out.String()
}
