// Package version holds build-time metadata for Tash.
package version

import "runtime"

var (
	// Version is overwritten by -ldflags at build time.
	Version = "1.0.0"
	// Commit is overwritten by -ldflags at build time.
	Commit = "local"
	// BuildDate is overwritten by -ldflags at build time.
	BuildDate = "unknown"
)

// UserAgent is used for outbound HTTP requests.
func UserAgent() string {
	return "Tash/" + Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
