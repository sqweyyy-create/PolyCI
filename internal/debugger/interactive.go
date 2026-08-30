// Package debugger implements the pause/resume layer on top of the Docker
// executor: after every step it shows the step's result and asks the user
// whether the pipeline should continue or abort, and on failure it can
// drop the user into an interactive shell in the same container.
package debugger

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sqweyyy-create/PolyCI/internal/executor"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// Interactive is an executor.StepController that prompts a human on in/out
// after every step.
type Interactive struct {
	in  *bufio.Reader
	out io.Writer

	// StepByStep pauses after every step (not just failed ones) and asks
	// the user to continue or abort.
	StepByStep bool
	// ShellOnFail automatically drops the user into an interactive shell
	// in the step's container whenever the step fails.
	ShellOnFail bool
}

// NewInteractive builds an Interactive controller reading prompts from in
// and writing them to out — typically os.Stdin and os.Stdout. Both
// StepByStep and ShellOnFail default to false; set them on the returned
// value before use.
func NewInteractive(in io.Reader, out io.Writer) *Interactive {
	return &Interactive{in: bufio.NewReader(in), out: out}
}

// AfterStep implements executor.StepController. The result of the step was
// already logged by the executor's Logger just before this is called; here
// we only need to react to a failure and/or prompt for the next move.
func (i *Interactive) AfterStep(ctx context.Context, jobName string, step pipeline.Step, exitCode int64, stepErr error, shell executor.ShellFunc) executor.Decision {
	failed := stepErr != nil || exitCode != 0

	if failed && i.ShellOnFail {
		fmt.Fprintf(i.out, "[%s] step %q failed — dropping into a shell in its container (exit the shell to continue)\n", jobName, step.Name)
		if err := shell(ctx); err != nil {
			fmt.Fprintf(i.out, "[%s] shell session ended: %v\n", jobName, err)
		}
	}

	if !i.StepByStep {
		if failed {
			return executor.Abort
		}
		return executor.Continue
	}

	for {
		prompt := "[Y/n/a=abort/s=shell] "
		if failed {
			prompt = "[y=continue past failure/n/a=abort/s=shell] "
		}
		fmt.Fprintf(i.out, "[%s] step %q done — continue? %s", jobName, step.Name, prompt)

		line, err := i.in.ReadString('\n')
		if err != nil {
			fmt.Fprintln(i.out, "\nno input available, aborting")
			return executor.Abort
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return executor.Continue
		case "n", "a", "abort":
			return executor.Abort
		case "s", "shell":
			if err := shell(ctx); err != nil {
				fmt.Fprintf(i.out, "[%s] shell session ended: %v\n", jobName, err)
			}
		default:
			fmt.Fprintln(i.out, "please answer y, a, or s")
		}
	}
}
