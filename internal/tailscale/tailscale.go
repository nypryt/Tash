// Package tailscale reads the local Tailscale daemon and runs a peer ping loop.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Peer is a compact view of a tailnet node.
type Peer struct {
	Name          string `json:"name"`
	TailscaleName string `json:"tailscale_name,omitempty"`
	DNSName       string `json:"dns_name"`
	OS            string `json:"os"`
	IP            string `json:"ip"`
	Online        bool   `json:"online"`
	LastSeen      string `json:"last_seen"`
	ExitNode      bool   `json:"exit_node"`
}

// DisplayName renders "tailscale-name (real-hostname)" so peers are easy to
// recognize even when the machine name and the Tailscale machine name differ.
func (p Peer) DisplayName() string {
	ts := strings.TrimSpace(p.TailscaleName)
	real := strings.TrimSpace(p.Name)
	switch {
	case ts == "":
		return real
	case real == "" || strings.EqualFold(ts, real):
		return ts
	default:
		return ts + " (" + real + ")"
	}
}

// Status is a snapshot of `tailscale status`.
type Status struct {
	Available    bool      `json:"available"`
	Running      bool      `json:"running"`
	Error        string    `json:"error,omitempty"`
	Version      string    `json:"version,omitempty"`
	BackendState string    `json:"backend_state,omitempty"`
	Self         Peer      `json:"self"`
	Peers        []Peer    `json:"peers"`
	FetchedAt    time.Time `json:"fetched_at"`
}

type rawStatus struct {
	Version      string             `json:"Version"`
	BackendState string             `json:"BackendState"`
	Self         rawPeer            `json:"Self"`
	Peer         map[string]rawPeer `json:"Peer"`
}

type rawPeer struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	OS           string   `json:"OS"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	LastSeen     string   `json:"LastSeen"`
	ExitNode     bool     `json:"ExitNode"`
	Active       bool     `json:"Active"`
}

// FindBinary locates the Tailscale CLI. Termux normally does not expose it.
func FindBinary() (string, bool) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, true
	}
	candidates := []string{
		"/usr/local/bin/tailscale",
		"/usr/bin/tailscale",
		"/opt/homebrew/bin/tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	}
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		candidates = append(candidates,
			filepath.Join(pf, "Tailscale", "tailscale.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Tailscale", "tailscale.exe"),
		)
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// ReadStatus runs `tailscale status --json`.
func ReadStatus(ctx context.Context) Status {
	now := time.Now()
	bin, ok := FindBinary()
	if !ok {
		return Status{
			Available: false,
			Error:     "tailscale CLI not found; on Android/Termux use the Tailscale Android app (status is only visible in that app)",
			FetchedAt: now,
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return Status{Available: true, Error: msg, FetchedAt: now}
	}
	var raw rawStatus
	if err := json.Unmarshal(out, &raw); err != nil {
		return Status{Available: true, Error: "could not parse tailscale status: " + err.Error(), FetchedAt: now}
	}
	st := Status{
		Available:    true,
		Running:      raw.BackendState == "Running",
		Version:      raw.Version,
		BackendState: raw.BackendState,
		Self:         convertPeer(raw.Self),
		FetchedAt:    now,
	}
	for _, p := range raw.Peer {
		st.Peers = append(st.Peers, convertPeer(p))
	}
	sort.SliceStable(st.Peers, func(i, j int) bool {
		if st.Peers[i].Online != st.Peers[j].Online {
			return st.Peers[i].Online
		}
		return strings.ToLower(st.Peers[i].DisplayName()) < strings.ToLower(st.Peers[j].DisplayName())
	})
	if st.Self.IP == "" {
		st.Error = "tailscale is not connected"
	}
	return st
}

func convertPeer(p rawPeer) Peer {
	ip := ""
	for _, candidate := range p.TailscaleIPs {
		if strings.Contains(candidate, ".") {
			ip = candidate
			break
		}
	}
	if ip == "" && len(p.TailscaleIPs) > 0 {
		ip = p.TailscaleIPs[0]
	}
	dns := strings.TrimSuffix(strings.TrimSpace(p.DNSName), ".")
	name := strings.TrimSpace(p.HostName)
	if name == "" {
		name = dns
	}
	tailscaleName := ""
	if dns != "" {
		tailscaleName = strings.SplitN(dns, ".", 2)[0]
	}
	if tailscaleName == "" {
		tailscaleName = name
	}
	return Peer{
		Name: name, TailscaleName: tailscaleName, DNSName: dns, OS: p.OS,
		IP: ip, Online: p.Online, LastSeen: p.LastSeen, ExitNode: p.ExitNode,
	}
}

// OnlinePeers returns connected peers with a usable IP.
func (s Status) OnlinePeers() []Peer {
	out := make([]Peer, 0, len(s.Peers))
	for _, p := range s.Peers {
		if p.Online && p.IP != "" {
			out = append(out, p)
		}
	}
	return out
}

// PingResult is one latency sample.
type PingResult struct {
	Peer    Peer          `json:"peer"`
	Latency time.Duration `json:"latency"`
	Via     string        `json:"via"`
	Err     string        `json:"error,omitempty"`
	At      time.Time     `json:"at"`
}

var latencyRE = regexp.MustCompile(`in\s+([0-9.]+)(ms|s|µs|us)`)

// Ping sends a single Tailscale-layer ping and parses the latency.
func Ping(ctx context.Context, peer Peer, timeout time.Duration) PingResult {
	res := PingResult{Peer: peer, At: time.Now()}
	bin, ok := FindBinary()
	if !ok {
		res.Err = "tailscale CLI not found"
		return res
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "ping", "--c", "1", "--timeout", fmt.Sprintf("%.0fs", timeout.Seconds()), "--until-direct=false", peer.IP)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil && text == "" {
		res.Err = err.Error()
		return res
	}
	if m := latencyRE.FindStringSubmatch(text); len(m) == 3 {
		v, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "s":
			res.Latency = time.Duration(v * float64(time.Second))
		case "µs", "us":
			res.Latency = time.Duration(v * float64(time.Microsecond))
		default:
			res.Latency = time.Duration(v * float64(time.Millisecond))
		}
		if idx := strings.Index(text, " via "); idx >= 0 {
			via := text[idx+5:]
			if in := strings.LastIndex(via, " in "); in > 0 {
				via = via[:in]
			}
			res.Via = strings.TrimSpace(via)
		}
		res.Err = ""
		return res
	}
	res.Err = text
	if res.Err == "" {
		res.Err = "no reply"
	}
	return res
}

// LocalCGNATIPv4 returns the first 100.64.0.0/10 Tailscale-style address.
func LocalCGNATIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		ip4 := ip.To4()
		if ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return ip4.String()
		}
	}
	return ""
}

// LocalIPv4 returns a non-loopback IPv4 address, preferring Tailscale.
func LocalIPv4() string {
	if ip := LocalCGNATIPv4(); ip != "" {
		return ip
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		ip4 := ip.To4()
		if ip4 != nil && !ip4.IsLoopback() && ip4.IsPrivate() {
			return ip4.String()
		}
	}
	return ""
}
