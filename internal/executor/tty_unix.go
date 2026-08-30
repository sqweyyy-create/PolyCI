//go:build !windows

package executor

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/term"
)

// makeRawAndResize puts the local terminal into raw mode (so keystrokes
// like Ctrl+C pass through to the remote shell instead of killing polyci)
// and keeps the exec's TTY size in sync with the local terminal, including
// on resize. It returns a function that undoes both, to be called once the
// shell session ends.
func makeRawAndResize(ctx context.Context, cli *dockerclient.Client, execID string) func() {
	fd := int(os.Stdin.Fd())

	var oldState *term.State
	if term.IsTerminal(fd) {
		oldState, _ = term.MakeRaw(fd)
	}

	resize := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			_ = cli.ContainerExecResize(ctx, execID, container.ResizeOptions{Width: uint(w), Height: uint(h)})
		}
	}
	resize()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigCh:
				resize()
			case <-stop:
				signal.Stop(sigCh)
				return
			}
		}
	}()

	return func() {
		close(stop)
		if oldState != nil {
			_ = term.Restore(fd, oldState)
		}
	}
}
