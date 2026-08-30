//go:build windows

package executor

import (
	"context"
	"errors"
	"time"

	dockerclient "github.com/docker/docker/client"
)

// makeRawAndResize is a no-op on Windows: raw-mode TTY passthrough and
// SIGWINCH-based resize aren't implemented for this platform yet.
func makeRawAndResize(ctx context.Context, cli *dockerclient.Client, execID string) func() {
	return func() {}
}

// waitStdinReadable is not implemented on Windows; Shell falls back to
// reading os.Stdin directly without the ability to cleanly cancel that
// read.
func waitStdinReadable(timeout time.Duration) (ready bool, err error) {
	return false, errors.New("waitStdinReadable not supported on windows")
}
