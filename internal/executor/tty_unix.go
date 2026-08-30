//go:build !windows

package executor

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/sys/unix"
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

// waitStdinReadable polls stdin for up to timeout, returning ready=true as
// soon as there's data to read (a subsequent Read won't block). Shell uses
// this so its stdin-forwarding goroutine can be reliably stopped once the
// shell session ends, instead of leaking a goroutine blocked in a Read
// that would otherwise race whatever the caller reads from stdin next.
//
// This deliberately avoids fcntl(O_NONBLOCK) on a duplicated fd, which
// seems like the obvious approach but is wrong: dup'd file descriptors
// share the same underlying open file description as the original in
// POSIX, including status flags — so calling SetNonblock on a dup of
// stdin also makes the real os.Stdin non-blocking (verified against a
// real pty), silently breaking every later blocking read of it, including
// the debugger's own prompts. poll(2) has no such side effect: it only
// queries readiness and never touches the fd's flags, so it's safe to use
// here without disturbing os.Stdin for later readers.
func waitStdinReadable(timeout time.Duration) (ready bool, err error) {
	fds := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if err != nil {
		if err == unix.EINTR {
			return false, nil
		}
		return false, err
	}
	return n > 0, nil
}
