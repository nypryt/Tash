package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dir returns the platform-appropriate Tash directory.
// TASH_HOME overrides it for tests and portable installs.
func Dir() string {
	if v := strings.TrimSpace(os.Getenv("TASH_HOME")); v != "" {
		return ExpandPath(v)
	}
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			return filepath.Join(d, "Tash")
		}
		return filepath.Join(d, "tash")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tash")
}

// DefaultNASRoot is the folder exposed by the built-in NAS.
func DefaultNASRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Tash", "NAS")
}

// ExpandPath expands a leading ~ and environment variables.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			p = filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

// IsTermux reports whether Tash is running inside unrooted Termux.
func IsTermux() bool {
	if os.Getenv("TERMUX_VERSION") != "" {
		return true
	}
	prefix := os.Getenv("PREFIX")
	return strings.Contains(prefix, "com.termux") || strings.Contains(strings.ToLower(prefix), "termux")
}

// IsMobile reports whether the terminal is probably a phone.
func IsMobile() bool { return IsTermux() }
