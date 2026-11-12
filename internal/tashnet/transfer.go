package tashnet

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/version"
)

const transferPrefix = "TASHXL1-"

// TransferPayload is the one-time main-role handover package. It contains the
// Tashnet secret and device tokens, so it must be treated like the secret key.
type TransferPayload struct {
	Version        int                      `json:"version"`
	Network        string                   `json:"network"`
	Secret         string                   `json:"secret"`
	SecretVersion  int                      `json:"secret_version"`
	ControlPort    int                      `json:"control_port"`
	FromURL        string                   `json:"from_url"`
	FromDeviceID   string                   `json:"from_device_id"`
	FromDeviceName string                   `json:"from_device_name"`
	Nonce          string                   `json:"nonce"`
	ExpiresAt      time.Time                `json:"expires_at"`
	Devices        map[string]config.Device `json:"devices"`
}

// AdvertiseURL returns the best URL other tailnet devices can use.
func AdvertiseURL(port int) string {
	return fmt.Sprintf("http://%s:%d", advertiseHost(), port)
}

// ExportTransfer creates a transfer code and arms the old main for handover.
func ExportTransfer(store *config.Store) (string, time.Time, error) {
	cfg := store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		return "", time.Time{}, fmt.Errorf("only the main device can create a transfer code")
	}
	secret := store.Secret()
	if secret == "" {
		return "", time.Time{}, fmt.Errorf("Tashnet secret key is missing")
	}
	nonce := NewNonce()
	expires := time.Now().UTC().Add(10 * time.Minute)
	err := store.Update(func(c *config.Config) error {
		self, ok := c.Tashnet.Devices[c.Tashnet.DeviceID]
		if !ok {
			self = config.Device{
				ID: c.Tashnet.DeviceID, Name: c.Tashnet.DeviceName,
				Token: NewToken(), ApprovedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
			}
		}
		if self.Token == "" {
			self.Token = NewToken()
		}
		c.Tashnet.Devices[c.Tashnet.DeviceID] = self
		c.Tashnet.TransferNonce = nonce
		c.Tashnet.TransferExpiry = expires
		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	cfg = store.Snapshot()
	devices := make(map[string]config.Device, len(cfg.Tashnet.Devices))
	for k, v := range cfg.Tashnet.Devices {
		devices[k] = v
	}
	payload := TransferPayload{
		Version: 1, Network: cfg.Tashnet.Name, Secret: secret,
		SecretVersion: cfg.Tashnet.SecretVersion, ControlPort: cfg.Tashnet.ControlPort,
		FromURL: AdvertiseURL(cfg.Tashnet.ControlPort), FromDeviceID: cfg.Tashnet.DeviceID,
		FromDeviceName: cfg.Tashnet.DeviceName, Nonce: nonce, ExpiresAt: expires, Devices: devices,
	}
	code, err := encodeTransfer(payload)
	return code, expires, err
}

func encodeTransfer(p TransferPayload) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(p); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return transferPrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// ParseTransfer decodes and validates a transfer code.
func ParseTransfer(code string) (TransferPayload, error) {
	var out TransferPayload
	code = strings.TrimSpace(code)
	code = strings.ReplaceAll(code, "\n", "")
	code = strings.ReplaceAll(code, "\r", "")
	code = strings.ReplaceAll(code, " ", "")
	if !strings.HasPrefix(code, transferPrefix) {
		return out, fmt.Errorf("not a Tash transfer code")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, transferPrefix))
	if err != nil {
		return out, fmt.Errorf("corrupt transfer code: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return out, fmt.Errorf("corrupt transfer code: %w", err)
	}
	defer gz.Close()
	data, err := io.ReadAll(io.LimitReader(gz, 4<<20))
	if err != nil {
		return out, fmt.Errorf("corrupt transfer code: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("corrupt transfer code: %w", err)
	}
	if out.Version != 1 || out.Secret == "" || out.Nonce == "" || out.FromURL == "" || len(out.Devices) == 0 {
		return out, fmt.Errorf("incomplete transfer code")
	}
	if time.Now().After(out.ExpiresAt) {
		return out, fmt.Errorf("transfer code expired; generate a fresh one on the main device")
	}
	if _, err := NormalizeSecret(out.Secret); err != nil {
		return out, fmt.Errorf("transfer code has an invalid secret")
	}
	return out, nil
}

// ClaimResult is the new main device state derived from a transfer code.
type ClaimResult struct {
	Tashnet config.TashnetConfig
	Secret  string
	OldURL  string
	Nonce   string
	NewSelf config.Device
}

// BuildClaim converts a payload into a main-device Tashnet configuration for
// this device. It keeps all approved device tokens.
func BuildClaim(p TransferPayload, deviceID, deviceName string) (ClaimResult, error) {
	secret, err := NormalizeSecret(p.Secret)
	if err != nil {
		return ClaimResult{}, err
	}
	devices := make(map[string]config.Device, len(p.Devices)+1)
	for k, v := range p.Devices {
		devices[k] = v
	}
	self, ok := devices[deviceID]
	if !ok {
		self = config.Device{ID: deviceID, Name: deviceName, Token: NewToken(), ApprovedAt: time.Now().UTC()}
	}
	self.Name = deviceName
	self.LastSeen = time.Now().UTC()
	if self.Token == "" {
		self.Token = NewToken()
	}
	devices[deviceID] = self
	t := config.TashnetConfig{
		Name: p.Network, Role: "main", MainDeviceID: deviceID, MainDeviceName: deviceName,
		DeviceID: deviceID, DeviceName: deviceName, ControlPort: p.ControlPort,
		SecretVersion: p.SecretVersion, Devices: devices,
		Pending: map[string]config.Pending{},
	}
	if t.ControlPort < 1024 || t.ControlPort > 65535 {
		t.ControlPort = 8787
	}
	return ClaimResult{Tashnet: t, Secret: secret, OldURL: p.FromURL, Nonce: p.Nonce, NewSelf: self}, nil
}

// NotifyTransferComplete asks the old main to demote itself. Failure is not
// fatal for the new main, but split-brain protection depends on it.
func NotifyTransferComplete(ctx context.Context, oldURL, secret, nonce, newURL, newDeviceID, newName string) error {
	oldURL, err := NormalizeURL(oldURL)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	reqBody := TransferCompleteRequest{
		Nonce: nonce, NewURL: newURL, NewDeviceID: newDeviceID, NewName: newName,
		Proof: TransferProof(secret, nonce, newURL, newDeviceID),
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oldURL+"/v1/transfer/complete", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("old main refused handover: %s", strings.TrimSpace(string(body)))
	}
	return nil
}
