// Package sysinfo collects small, dependency-free host facts for the UI.
package sysinfo

import (
	"net"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tailscale"
	"github.com/tash/tash/internal/version"
)

// Info is a host snapshot used by the dashboard.
type Info struct {
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	User        string    `json:"user"`
	LocalIP     string    `json:"local_ip"`
	TailscaleIP string    `json:"tailscale_ip"`
	CPUs        int       `json:"cpus"`
	GoVersion   string    `json:"go_version"`
	TashVersion string    `json:"tash_version"`
	ConfigDir   string    `json:"config_dir"`
	Termux      bool      `json:"termux"`
	StartedAt   time.Time `json:"started_at"`
}

// Collect gathers host details.
func Collect() Info {
	host, _ := os.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
		if i := strings.LastIndexAny(username, `\/`); i >= 0 {
			username = username[i+1:]
		}
	}
	if username == "" {
		username = os.Getenv("USER")
	}
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	return Info{
		Hostname:    host,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		User:        username,
		LocalIP:     localIPv4(),
		TailscaleIP: tailscale.LocalCGNATIPv4(),
		CPUs:        runtime.NumCPU(),
		GoVersion:   runtime.Version(),
		TashVersion: version.Version,
		ConfigDir:   config.Dir(),
		Termux:      config.IsTermux(),
		StartedAt:   time.Now(),
	}
}

// OSName is a friendlier name than the GOOS token.
func (i Info) OSName() string {
	switch i.OS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		if i.Termux {
			return "Termux"
		}
		return "Linux"
	case "android":
		return "Android"
	default:
		return i.OS
	}
}

func localIPv4() string {
	if ip := tailscale.LocalCGNATIPv4(); ip != "" {
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
		if ip4 != nil && !ip4.IsLoopback() {
			return ip4.String()
		}
	}
	return "127.0.0.1"
}
