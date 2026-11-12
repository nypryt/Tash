//go:build !windows

package docker

import (
	"context"
	"fmt"
	"net"
)

// npipeDialer is unavailable off Windows.
func npipeDialer(path string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil
}

var _ = fmt.Sprintf
