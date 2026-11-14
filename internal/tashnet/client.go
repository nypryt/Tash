package tashnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tailscale"
)

// ErrRevoked means the main device no longer accepts this device's token.
var ErrRevoked = errors.New("Tashnet token revoked: the secret key was regenerated or this device was removed")

// Client talks to a main device's control API.
type Client struct {
	BaseURL string
	Token   string
	Version string
	HTTP    *http.Client
}

// NewClient validates a base URL and returns an API client.
func NewClient(baseURL, token, version string) (*Client, error) {
	u, err := NormalizeURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: u, Token: token, Version: version, HTTP: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any, auth bool) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Tash/"+c.Version)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach main device at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if resp.StatusCode == http.StatusUnauthorized && auth {
			return ErrRevoked
		}
		return fmt.Errorf("%s (%s)", msg, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("bad response from main device: %w", err)
		}
	}
	return nil
}

// Hello reads the main device's public hello banner.
func (c *Client) Hello(ctx context.Context) (HelloResponse, error) {
	var out HelloResponse
	err := c.do(ctx, http.MethodGet, "/v1/hello", nil, &out, false)
	return out, err
}

// Heartbeat authenticates this device and updates its last-seen time.
func (c *Client) Heartbeat(ctx context.Context) (HeartbeatResponse, error) {
	var out HeartbeatResponse
	err := c.do(ctx, http.MethodPost, "/v1/heartbeat", map[string]any{}, &out, true)
	return out, err
}

// Network returns approved devices.
func (c *Client) Network(ctx context.Context) (NetworkResponse, error) {
	var out NetworkResponse
	err := c.do(ctx, http.MethodGet, "/v1/network", nil, &out, true)
	return out, err
}

// Join performs the HMAC join handshake. The secret is never sent.
func (c *Client) Join(ctx context.Context, secret, deviceID, deviceName string) (JoinResponse, error) {
	norm, err := NormalizeSecret(secret)
	if err != nil {
		return JoinResponse{}, err
	}
	now := time.Now().Unix()
	req := JoinRequest{
		DeviceID: deviceID, DeviceName: deviceName, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Timestamp: now, Proof: JoinProof(norm, deviceID, now),
	}
	var out JoinResponse
	err = c.do(ctx, http.MethodPost, "/v1/join", req, &out, false)
	return out, err
}

// Poll checks a pending join request.
func (c *Client) Poll(ctx context.Context, pendingID, pollToken, deviceID string) (PollResponse, error) {
	var out PollResponse
	err := c.do(ctx, http.MethodPost, "/v1/poll", PollRequest{PendingID: pendingID, PollToken: pollToken, DeviceID: deviceID}, &out, false)
	return out, err
}

// CompleteTransfer tells an old main device to demote itself.
func (c *Client) CompleteTransfer(ctx context.Context, req TransferCompleteRequest) error {
	return c.do(ctx, http.MethodPost, "/v1/transfer/complete", req, nil, false)
}

// Discovered is a possible main device found on the tailnet.
type Discovered struct {
	URL   string        `json:"url"`
	Hello HelloResponse `json:"hello"`
}

// Discover probes localhost and online Tailscale peers for Tash main
// devices. It works without root and without a Tailscale CLI when a URL is
// supplied manually; automatic discovery needs the CLI.
func Discover(ctx context.Context, port int, timeout time.Duration) []Discovered {
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	st := tailscale.ReadStatus(ctx)
	hosts := map[string]bool{"127.0.0.1": true, "localhost": true}
	if st.Self.IP != "" {
		hosts[st.Self.IP] = true
	}
	for _, p := range st.OnlinePeers() {
		if p.IP != "" {
			hosts[p.IP] = true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+1500*time.Millisecond)
	defer cancel()
	type result struct {
		d  Discovered
		ok bool
	}
	ch := make(chan result, len(hosts))
	var wg sync.WaitGroup
	for host := range hosts {
		url := fmt.Sprintf("http://%s:%d", host, port)
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			c := &http.Client{Timeout: timeout}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/hello", nil)
			if err != nil {
				ch <- result{}
				return
			}
			resp, err := c.Do(req)
			if err != nil {
				ch <- result{}
				return
			}
			defer resp.Body.Close()
			var hello HelloResponse
			if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&hello) != nil || hello.App != "tash" {
				ch <- result{}
				return
			}
			ch <- result{d: Discovered{URL: url, Hello: hello}, ok: true}
		}(url)
	}
	wg.Wait()
	close(ch)
	seen := map[string]bool{}
	var out []Discovered
	for r := range ch {
		if !r.ok || seen[r.d.Hello.MainDeviceID] {
			continue
		}
		seen[r.d.Hello.MainDeviceID] = true
		out = append(out, r.d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// Probe checks one URL for a Tash main device.
func Probe(ctx context.Context, baseURL string, timeout time.Duration) (HelloResponse, error) {
	u, err := NormalizeURL(baseURL)
	if err != nil {
		return HelloResponse{}, err
	}
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"/v1/hello", nil)
	if err != nil {
		return HelloResponse{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return HelloResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HelloResponse{}, fmt.Errorf("main device returned %s", resp.Status)
	}
	var hello HelloResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&hello); err != nil {
		return HelloResponse{}, err
	}
	if hello.App != "tash" {
		return HelloResponse{}, fmt.Errorf("not a Tash main device")
	}
	return hello, nil
}

// GetConfig fetches the current config from the main device.
func (c *Client) GetConfig(ctx context.Context) (*config.Config, error) {
	var resp ConfigSyncResponse
	err := c.do(ctx, http.MethodGet, "/v1/config/get", nil, &resp, true)
	if err != nil {
		return nil, err
	}
	if resp.Config == nil {
		return nil, fmt.Errorf("no config returned")
	}
	return resp.Config, nil
}

// PushConfig pushes a config to the main device (for main device to broadcast).
func (c *Client) PushConfig(ctx context.Context, cfg *config.Config, targetDeviceID string) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	req := struct {
		TargetDeviceID string          `json:"target_device_id,omitempty"`
		Config         json.RawMessage `json:"config"`
	}{
		TargetDeviceID: targetDeviceID,
		Config:         data,
	}
	var resp ConfigSyncResponse
	return c.do(ctx, http.MethodPost, "/v1/config/push", req, &resp, true)
}
