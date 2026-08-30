//go:build windows

package executor

import (
	"context"

	dockerclient "github.com/docker/docker/client"
)

// makeRawAndResize is a no-op on Windows: raw-mode TTY passthrough and
// SIGWINCH-based resize aren't implemented for this platform yet.
func makeRawAndResize(ctx context.Context, cli *dockerclient.Client, execID string) func() {
	return func() {}
}
