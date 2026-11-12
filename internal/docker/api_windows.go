//go:build windows

package docker

import (
	"context"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

// npipeDialer returns a Docker named-pipe dialer.
func npipeDialer(path string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	path = strings.TrimPrefix(path, "//")
	if !strings.HasPrefix(path, "./pipe/") && !strings.HasPrefix(path, "//./pipe/") {
		path = "//./pipe/" + strings.TrimPrefix(path, "/")
	}
	if !strings.HasPrefix(path, "//./pipe/") {
		path = "//./pipe/" + strings.TrimPrefix(strings.TrimPrefix(path, "//"), "./pipe/")
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return winio.DialPipeContext(ctx, path)
	}
}
