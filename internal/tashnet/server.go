package tashnet

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tailscale"
)

// Protocol payloads. These are deliberately small and versioned by URL.

type HelloResponse struct {
	App            string `json:"app"`
	Version        string `json:"version"`
	Network        string `json:"network"`
	MainDeviceID   string `json:"main_device_id"`
	MainDeviceName string `json:"main_device_name"`
	SecretVersion  int    `json:"secret_version"`
	ControlPort    int    `json:"control_port"`
}

type JoinRequest struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Timestamp  int64  `json:"timestamp"`
	Proof      string `json:"proof"`
}

type JoinResponse struct {
	Status         string `json:"status"`
	PendingID      string `json:"pending_id,omitempty"`
	PollToken      string `json:"poll_token,omitempty"`
	Token          string `json:"token,omitempty"`
	SecretVersion  int    `json:"secret_version"`
	MainURL        string `json:"main_url,omitempty"`
	MainDeviceID   string `json:"main_device_id,omitempty"`
	MainDeviceName string `json:"main_device_name,omitempty"`
	Message        string `json:"message,omitempty"`
}

type PollRequest struct {
	PendingID string `json:"pending_id"`
	PollToken string `json:"poll_token"`
	DeviceID  string `json:"device_id"`
}

type PollResponse struct {
	Status         string `json:"status"` // pending, approved, denied
	Token          string `json:"token,omitempty"`
	MainURL        string `json:"main_url,omitempty"`
	MainDeviceName string `json:"main_device_name,omitempty"`
	SecretVersion  int    `json:"secret_version"`
	Message        string `json:"message,omitempty"`
}

type DeviceView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	OS       string    `json:"os"`
	Arch     string    `json:"arch"`
	IP       string    `json:"ip,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

type NetworkResponse struct {
	Network        string       `json:"network"`
	MainDeviceID   string       `json:"main_device_id"`
	MainDeviceName string       `json:"main_device_name"`
	SecretVersion  int          `json:"secret_version"`
	Devices        []DeviceView `json:"devices"`
}

type HeartbeatResponse struct {
	OK             bool      `json:"ok"`
	Network        string    `json:"network"`
	MainURL        string    `json:"main_url"`
	MainDeviceID   string    `json:"main_device_id"`
	MainDeviceName string    `json:"main_device_name"`
	SecretVersion  int       `json:"secret_version"`
	DeviceID       string    `json:"device_id"`
	ServerTime     time.Time `json:"server_time"`
}

type TransferCompleteRequest struct {
	Nonce       string `json:"nonce"`
	NewURL      string `json:"new_url"`
	NewDeviceID string `json:"new_device_id"`
	NewName     string `json:"new_name"`
	Proof       string `json:"proof"`
}

// Event is a small audit record shown in the Network tab.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// Server hosts the authenticated Tashnet control API on the main device.
type Server struct {
	store   *config.Store
	version string

	mu     sync.RWMutex
	server *http.Server
	ln     net.Listener
	url    string
	err    string
	events []Event

	limiter *joinLimiter
}

// NewServer builds a control server for store.
func NewServer(store *config.Store, version string) *Server {
	return &Server{store: store, version: version, limiter: newJoinLimiter(12, time.Minute)}
}

// Start binds the control port. It is safe to call once.
func (s *Server) Start() error {
	cfg := s.store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		return fmt.Errorf("this device is not the Tashnet main device")
	}
	if s.store.Secret() == "" {
		return fmt.Errorf("Tashnet secret key is missing")
	}
	addr := fmt.Sprintf(":%d", cfg.Tashnet.ControlPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.setErr(err.Error())
		return fmt.Errorf("control port %d: %w", cfg.Tashnet.ControlPort, err)
	}
	url := fmt.Sprintf("http://%s:%d", advertiseHost(), cfg.Tashnet.ControlPort)
	s.mu.Lock()
	s.ln = ln
	s.url = url
	s.server = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       12 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	srv := s.server
	s.mu.Unlock()
	s.AddEvent("server", "Tashnet control uplink listening on "+url)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.setErr(err.Error())
		}
	}()
	return nil
}

// Stop gracefully shuts down the control server.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.server
	s.server = nil
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// URL returns the advertised control URL.
func (s *Server) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// Err returns the asynchronous server error, if any.
func (s *Server) Err() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

func (s *Server) setErr(v string) {
	s.mu.Lock()
	s.err = v
	s.mu.Unlock()
}

// AddEvent records an audit line for the UI.
func (s *Server) AddEvent(kind, msg string) {
	s.mu.Lock()
	s.events = append(s.events, Event{At: time.Now(), Kind: kind, Message: msg})
	if len(s.events) > 80 {
		s.events = s.events[len(s.events)-80:]
	}
	s.mu.Unlock()
}

// Events returns a copy of recent audit lines.
func (s *Server) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "app": "tash"})
	})
	mux.HandleFunc("/v1/hello", s.handleHello)
	mux.HandleFunc("/v1/join", s.handleJoin)
	mux.HandleFunc("/v1/poll", s.handlePoll)
	mux.HandleFunc("/v1/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/v1/network", s.handleNetwork)
	mux.HandleFunc("/v1/whoami", s.handleWhoami)
	mux.HandleFunc("/v1/transfer/complete", s.handleTransferComplete)
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Server", "Tash/"+s.version)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := s.store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		writeError(w, http.StatusServiceUnavailable, "not a Tashnet main device")
		return
	}
	writeJSON(w, http.StatusOK, HelloResponse{
		App: "tash", Version: s.version, Network: cfg.Tashnet.Name,
		MainDeviceID: cfg.Tashnet.DeviceID, MainDeviceName: cfg.Tashnet.DeviceName,
		SecretVersion: cfg.Tashnet.SecretVersion, ControlPort: cfg.Tashnet.ControlPort,
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many join attempts; wait a minute")
		return
	}
	var req JoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DeviceID == "" || len(req.DeviceID) > 80 || req.DeviceName == "" || len(req.DeviceName) > 80 {
		writeError(w, http.StatusBadRequest, "device_id and device_name are required")
		return
	}
	secret := s.store.Secret()
	if err := VerifyJoinProof(secret, req.DeviceID, req.Timestamp, req.Proof); err != nil {
		s.AddEvent("denied", fmt.Sprintf("join proof rejected from %s (%s)", req.DeviceName, ip))
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	cfg := s.store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		writeError(w, http.StatusServiceUnavailable, "this Tashnet node is not the main device")
		return
	}
	pendingID := NewPendingID()
	pollToken := NewToken()
	var mainName string
	var secretVersion int
	err := s.store.Update(func(c *config.Config) error {
		if len(c.Tashnet.Pending) >= 32 {
			return fmt.Errorf("too many pending requests; approve or deny some first")
		}
		// Re-requesting replaces the old pairing attempt for this device.
		for id, p := range c.Tashnet.Pending {
			if p.DeviceID == req.DeviceID {
				delete(c.Tashnet.Pending, id)
			}
		}
		c.Tashnet.Pending[pendingID] = config.Pending{
			ID: pendingID, DeviceID: req.DeviceID, Name: req.DeviceName,
			OS: req.OS, Arch: req.Arch, IP: ip, PollHash: HashToken(pollToken),
			RequestedAt: time.Now().UTC(),
		}
		mainName = c.Tashnet.DeviceName
		secretVersion = c.Tashnet.SecretVersion
		return nil
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.AddEvent("pending", fmt.Sprintf("%s (%s) requested to join from %s", req.DeviceName, req.OS, ip))
	writeJSON(w, http.StatusOK, JoinResponse{
		Status: "pending", PendingID: pendingID, PollToken: pollToken,
		SecretVersion: secretVersion, MainURL: s.URL(),
		MainDeviceID: cfg.Tashnet.DeviceID, MainDeviceName: mainName,
		Message: "Approve this device on the main device's Tashnet tab.",
	})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req PollRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := s.store.Snapshot()
	pending, ok := cfg.Tashnet.Pending[req.PendingID]
	if !ok {
		// If approval raced the poll, the device record is authoritative.
		// The poll token still has to match the hash stored at approval time,
		// otherwise a device ID alone would reveal the bearer token.
		if dev, found := cfg.Tashnet.Devices[req.DeviceID]; found && req.DeviceID != "" {
			if dev.PollHash == "" || subtle.ConstantTimeCompare([]byte(HashToken(req.PollToken)), []byte(dev.PollHash)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid pairing token")
				return
			}
			writeJSON(w, http.StatusOK, PollResponse{
				Status: "approved", Token: dev.Token, MainURL: s.URL(),
				MainDeviceName: cfg.Tashnet.DeviceName, SecretVersion: cfg.Tashnet.SecretVersion,
			})
			return
		}
		writeJSON(w, http.StatusOK, PollResponse{Status: "denied", SecretVersion: cfg.Tashnet.SecretVersion, Message: "request is no longer pending"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(HashToken(req.PollToken)), []byte(pending.PollHash)) != 1 || pending.DeviceID != req.DeviceID {
		writeError(w, http.StatusUnauthorized, "invalid pairing token")
		return
	}
	if time.Since(pending.RequestedAt) > 20*time.Minute {
		_ = s.store.Update(func(c *config.Config) error {
			delete(c.Tashnet.Pending, req.PendingID)
			return nil
		})
		writeJSON(w, http.StatusOK, PollResponse{Status: "denied", SecretVersion: cfg.Tashnet.SecretVersion, Message: "request expired"})
		return
	}
	writeJSON(w, http.StatusOK, PollResponse{
		Status: "pending", MainURL: s.URL(), MainDeviceName: cfg.Tashnet.DeviceName,
		SecretVersion: cfg.Tashnet.SecretVersion,
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dev, ok := s.auth(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "token revoked or invalid")
		return
	}
	cfg := s.store.Snapshot()
	now := time.Now().UTC()
	if now.Sub(dev.LastSeen) > 25*time.Second {
		_ = s.store.Update(func(c *config.Config) error {
			if d, exists := c.Tashnet.Devices[dev.ID]; exists {
				d.LastSeen = now
				c.Tashnet.Devices[dev.ID] = d
			}
			return nil
		})
	}
	writeJSON(w, http.StatusOK, HeartbeatResponse{
		OK: true, Network: cfg.Tashnet.Name, MainURL: s.URL(),
		MainDeviceID: cfg.Tashnet.DeviceID, MainDeviceName: cfg.Tashnet.DeviceName,
		SecretVersion: cfg.Tashnet.SecretVersion, DeviceID: dev.ID, ServerTime: now,
	})
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.auth(r); !ok {
		writeError(w, http.StatusUnauthorized, "token revoked or invalid")
		return
	}
	cfg := s.store.Snapshot()
	resp := NetworkResponse{
		Network: cfg.Tashnet.Name, MainDeviceID: cfg.Tashnet.DeviceID,
		MainDeviceName: cfg.Tashnet.DeviceName, SecretVersion: cfg.Tashnet.SecretVersion,
	}
	for _, d := range cfg.Tashnet.Devices {
		resp.Devices = append(resp.Devices, DeviceView{ID: d.ID, Name: d.Name, OS: d.OS, Arch: d.Arch, IP: d.IP, LastSeen: d.LastSeen})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dev, ok := s.auth(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "token revoked or invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": dev, "server_time": time.Now().UTC()})
}

func (s *Server) handleTransferComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req TransferCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := s.store.Snapshot()
	if cfg.Tashnet.Role != "main" {
		writeError(w, http.StatusForbidden, "this device is no longer main")
		return
	}
	if cfg.Tashnet.TransferNonce == "" || cfg.Tashnet.TransferNonce != req.Nonce || time.Now().After(cfg.Tashnet.TransferExpiry) {
		writeError(w, http.StatusForbidden, "transfer code expired or was not generated here")
		return
	}
	if !VerifyTransferProof(s.store.Secret(), req.Nonce, req.NewURL, req.NewDeviceID, req.Proof) {
		writeError(w, http.StatusUnauthorized, "invalid transfer proof")
		return
	}
	self, ok := cfg.Tashnet.Devices[cfg.Tashnet.DeviceID]
	if !ok || self.Token == "" {
		writeError(w, http.StatusConflict, "old main is missing its device token")
		return
	}
	newURL, err := NormalizeURL(req.NewURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err = s.store.Update(func(c *config.Config) error {
		c.Tashnet.Role = "client"
		c.Tashnet.MainURL = newURL
		c.Tashnet.MainDeviceID = req.NewDeviceID
		c.Tashnet.MainDeviceName = req.NewName
		c.Tashnet.Token = self.Token
		c.Tashnet.Devices = map[string]config.Device{}
		c.Tashnet.Pending = map[string]config.Pending{}
		c.Tashnet.TransferNonce = ""
		c.Tashnet.TransferExpiry = time.Time{}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.ClearSecret(); err != nil {
		writeError(w, http.StatusInternalServerError, "configuration saved but secret removal failed: "+err.Error())
		return
	}
	s.AddEvent("transfer", "main role transferred to "+req.NewName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "this device is now a Tashnet client"})
	go func() {
		time.Sleep(400 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	}()
}

func (s *Server) auth(r *http.Request) (config.Device, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return config.Device{}, false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if tok == "" {
		return config.Device{}, false
	}
	cfg := s.store.Snapshot()
	var found config.Device
	ok := false
	for _, d := range cfg.Tashnet.Devices {
		if d.Token != "" && subtle.ConstantTimeCompare([]byte(d.Token), []byte(tok)) == 1 {
			found = d
			ok = true
		}
	}
	return found, ok
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func advertiseHost() string {
	if ip := tailscale.LocalIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// joinLimiter is a tiny fixed-window per-IP rate limiter.
type joinLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newJoinLimiter(limit int, window time.Duration) *joinLimiter {
	return &joinLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *joinLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	list := l.hits[ip]
	keep := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	l.hits[ip] = keep
	if len(keep) >= l.limit {
		return false
	}
	l.hits[ip] = append(l.hits[ip], now)
	return true
}

// ParsePort is a helper for CLI flags and config forms.
func ParsePort(s string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || v < 1024 || v > 65535 {
		return 0, fmt.Errorf("port must be between 1024 and 65535")
	}
	return v, nil
}
